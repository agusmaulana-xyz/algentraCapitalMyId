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
	inExpert, inInputs, found := false, false, false
	inputs := make([]discoveredInput, 0)
	seen := map[string]struct{}{}
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		switch line {
		case "<expert>":
			inExpert, found = true, true
			continue
		case "</expert>":
			return inputs, found
		case "<inputs>":
			inInputs = inExpert
			continue
		case "</inputs>":
			inInputs = false
			continue
		}
		if !inInputs {
			continue
		}
		name, value, ok := strings.Cut(line, "=")
		// Lines such as "Lots,F=0" hold optimizer metadata, not an input.
		if !ok || !chartInputName.MatchString(name) || len(inputs) >= 200 || len(value) > 256 {
			continue
		}
		if _, duplicate := seen[name]; duplicate {
			continue
		}
		seen[name] = struct{}{}
		inputs = append(inputs, discoveredInput{Name: name, Type: guessInputType(value), Default: value})
	}
	return inputs, found
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
