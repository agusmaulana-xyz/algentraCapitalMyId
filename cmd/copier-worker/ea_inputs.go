package main

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const discoverRetryDelay = 10 * time.Minute

const maxCaptureAttempts = 2

type discoveredInput struct {
	Name    string `json:"name"`
	Type    string `json:"type"`
	Default string `json:"default"`
	Comment string `json:"comment"`
}

var chartInputName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,63}$`)

// startInputDiscovery learns the input list of a compiled (.ex5) custom EA.
// The list cannot be read from the file, so the EA is attached once with live
// trading disabled and the inputs MT5 saves into the chart profile on close
// are read back.
func (w *worker) startInputDiscovery(j job) {
	w.checkMu.Lock()
	retryAt := w.discoverRetry[j.AccountID]
	w.checkMu.Unlock()
	if time.Now().Before(retryAt) || !w.beginCheck(j.AccountID) {
		return
	}
	w.checks.Add(1)
	go w.discoverCustomInputs(j)
}

func (w *worker) discoverCustomInputs(j job) {
	defer w.checks.Done()
	defer func() {
		w.checkMu.Lock()
		delete(w.checking, j.AccountID)
		w.checkMu.Unlock()
	}()
	retryLater := func(reason string) {
		w.checkMu.Lock()
		w.discoverRetry[j.AccountID] = time.Now().Add(discoverRetryDelay)
		w.checkMu.Unlock()
		log.Printf("daftar parameter EA akun %d belum terbaca: %s", j.AccountID, reason)
	}
	j.CustomEAParams = nil
	configPath, _, _, err := w.makeJobConfig(j, false)
	if err != nil {
		retryLater(err.Error())
		return
	}
	defer os.Remove(configPath)
	raw, err := os.ReadFile(configPath)
	if err != nil {
		retryLater("startup config tidak terbaca")
		return
	}
	if err := os.WriteFile(configPath, bytes.Replace(raw, []byte("AllowLiveTrading=1"), []byte("AllowLiveTrading=0"), 1), 0o600); err != nil {
		retryLater("startup config tidak dapat diubah")
		return
	}
	accountDir := filepath.Join(w.cfg.runtimeDir, "terminals", fmt.Sprint(j.AccountID))
	login := mustInt(j.Login)
	offsets := logOffsets(accountDir)
	clearSavedCharts(accountDir)
	cmd, done := execTerminal(filepath.Join(accountDir, "terminal64.exe"), accountDir, configPath, w.cleanEnvironment(false))
	if cmd == nil {
		retryLater("terminal tidak dapat dijalankan")
		return
	}
	defer w.killTerminalsIn(accountDir)
	deadline := time.After(90 * time.Second)
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
wait:
	for {
		select {
		case <-w.stop:
			return
		case <-done:
			break wait
		case <-deadline:
			break wait
		case <-tick.C:
			authorized, failure := scanAuthLog(accountDir, offsets, login)
			if failure != "" {
				requestMT5Close(uint32(cmd.Process.Pid))
				retryLater("login broker gagal")
				select {
				case <-done:
				case <-time.After(15 * time.Second):
				}
				return
			}
			if authorized {
				time.Sleep(8 * time.Second)
				break wait
			}
		}
	}
	select {
	case <-done:
	default:
		// MT5 writes the chart profile only on a normal close.
		requestMT5Close(uint32(cmd.Process.Pid))
		select {
		case <-done:
		case <-time.After(20 * time.Second):
		}
	}
	inputs, ok := readChartInputs(accountDir)
	clearSavedCharts(accountDir)
	if !ok {
		retryLater("profil chart tidak berisi EA")
		return
	}
	var ignored any
	if err := w.request(fmt.Sprintf("/api/mt5/copier/accounts/%d/custom-ea/inputs", j.AccountID), map[string]any{"inputs": inputs}, &ignored); err != nil {
		retryLater("gagal mengirim daftar parameter: " + err.Error())
		return
	}
	log.Printf("daftar parameter EA akun %d terbaca: %d input", j.AccountID, len(inputs))
}

// captureRunningInputs reads the input list of a running custom EA. MT5 only
// writes the chart profile on a normal close, so the terminal is closed once;
// the supervisor loop starts it again on the next sync. It reports whether
// the terminal was stopped. Attempts are capped because every attempt costs a
// restart of the client's EA.
func (w *worker) captureRunningInputs(j job, c *child) bool {
	w.checkMu.Lock()
	attempts := w.captureAttempts[j.AccountID]
	w.checkMu.Unlock()
	if attempts >= maxCaptureAttempts || time.Since(c.startedAt) < 90*time.Second {
		return false
	}
	w.checkMu.Lock()
	w.captureAttempts[j.AccountID] = attempts + 1
	w.checkMu.Unlock()

	requestMT5Close(uint32(c.cmd.Process.Pid))
	select {
	case <-c.stopped:
	case <-time.After(25 * time.Second):
		_ = stopChildProcess(c.cmd, c.done, 10*time.Second)
	}
	delete(w.children, j.AccountID)
	if c.log != nil {
		_ = c.log.Close()
	}
	accountDir := filepath.Join(w.cfg.runtimeDir, "terminals", fmt.Sprint(j.AccountID))
	inputs, ok := readChartInputs(accountDir)
	clearSavedCharts(accountDir)
	if !ok {
		log.Printf("daftar parameter EA akun %d belum terbaca dari terminal yang berjalan (percobaan %d)", j.AccountID, attempts+1)
		return true
	}
	var ignored any
	if err := w.request(fmt.Sprintf("/api/mt5/copier/accounts/%d/custom-ea/inputs", j.AccountID), map[string]any{"inputs": inputs}, &ignored); err != nil {
		log.Printf("daftar parameter EA akun %d gagal dikirim: %v", j.AccountID, err)
		return true
	}
	log.Printf("daftar parameter EA akun %d terbaca dari terminal yang berjalan: %d input", j.AccountID, len(inputs))
	return true
}

// readChartInputs parses the saved chart profile written by MT5 and returns
// the inputs of the attached expert.
func readChartInputs(accountDir string) ([]discoveredInput, bool) {
	files, _ := filepath.Glob(filepath.Join(accountDir, "MQL5", "Profiles", "Charts", "Default", "*.chr"))
	for _, file := range files {
		data, err := os.ReadFile(file)
		if err != nil {
			continue
		}
		if inputs, ok := parseChartInputs(decodeChartFile(data)); ok {
			return inputs, true
		}
	}
	return nil, false
}

func decodeChartFile(data []byte) string {
	switch {
	case len(data) >= 2 && data[0] == 0xff && data[1] == 0xfe:
		return decodeUTF16(data[2:], binary.LittleEndian)
	case len(data) >= 2 && data[0] == 0xfe && data[1] == 0xff:
		return decodeUTF16(data[2:], binary.BigEndian)
	}
	return strings.TrimPrefix(string(data), "\xef\xbb\xbf")
}

func parseChartInputs(text string) ([]discoveredInput, bool) {
	inExpert, inInputs, found, sawInputs := false, false, false, false
	inputs := make([]discoveredInput, 0)
	// Some builds store expert inputs directly after the header keys, as
	// indicators do, instead of inside an <inputs> section.
	loose := make([]discoveredInput, 0)
	seen := map[string]struct{}{}
	add := func(list *[]discoveredInput, line string) {
		name, value, ok := strings.Cut(line, "=")
		// Lines such as "Lots,F=0" hold optimizer metadata, not an input.
		if !ok || !chartInputName.MatchString(name) || len(*list) >= 200 || len(value) > 256 {
			return
		}
		if _, duplicate := seen[name]; duplicate {
			return
		}
		seen[name] = struct{}{}
		*list = append(*list, discoveredInput{Name: name, Type: guessInputType(value), Default: value})
	}
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		switch line {
		case "<expert>":
			inExpert, found = true, true
			continue
		case "</expert>":
			if sawInputs {
				return inputs, found
			}
			return loose, found
		case "<inputs>":
			inInputs, sawInputs = inExpert, inExpert
			continue
		case "</inputs>":
			inInputs = false
			continue
		}
		switch {
		case inInputs:
			add(&inputs, line)
		case inExpert && !strings.HasPrefix(line, "<"):
			if name, _, ok := strings.Cut(line, "="); ok && !chartHeaderKeys[name] {
				add(&loose, line)
			}
		}
	}
	return inputs, found
}

var chartHeaderKeys = map[string]bool{
	"name": true, "path": true, "expertmode": true, "fixed_height": true, "apply": true, "show_data": true,
	"scale_inherit": true, "scale_line": true, "scale_line_percent": true, "scale_line_value": true,
	"scale_fix_min": true, "scale_fix_min_val": true, "scale_fix_max": true, "scale_fix_max_val": true,
}

func guessInputType(value string) string {
	if value == "true" || value == "false" {
		return "bool"
	}
	if _, err := strconv.ParseInt(value, 10, 64); err == nil {
		return "int"
	}
	if _, err := strconv.ParseFloat(value, 64); err == nil {
		return "double"
	}
	return "string"
}
