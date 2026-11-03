package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode/utf16"
)

var symbolTokenPattern = regexp.MustCompile(`[A-Za-z0-9._#@+\-]{3,32}`)

// knownAccountSymbol returns a symbol that the broker has already served to
// this account. A chart opened on a symbol the account does not have is dead
// and never runs the attached Expert, so the startup symbol must come from the
// account itself: the gold symbol confirmed by an earlier login, else the
// terminal's saved Market Watch list for this login.
func (w *worker) knownAccountSymbol(accountID, login int64, accountDir string) string {
	path := filepath.Join(w.cfg.runtimeDir, "states", fmt.Sprintf("%d.gold-symbol", accountID))
	if raw, err := os.ReadFile(path); err == nil {
		if symbol := strings.TrimSpace(string(raw)); symbol != "" && !strings.ContainsAny(symbol, "\r\n,=") {
			return symbol
		}
	}
	return marketWatchSymbol(accountDir, login)
}

// marketWatchSymbol returns a symbol the broker really serves to this account.
// MT5 creates bases\<server>\ticks\<symbol> as soon as quotes for a Market Watch
// symbol arrive, which is more reliable than selected-<login>.dat (empty until
// the terminal saves its state). Gold is preferred, then any quoted symbol.
func marketWatchSymbol(accountDir string, login int64) string {
	var gold, other string
	tickDirs, _ := filepath.Glob(filepath.Join(accountDir, "bases", "*", "ticks", "*"))
	for _, dir := range tickDirs {
		info, err := os.Stat(dir)
		if err != nil || !info.IsDir() {
			continue
		}
		name := filepath.Base(dir)
		if !symbolTokenPattern.MatchString(name) || symbolTokenPattern.FindString(name) != name {
			continue
		}
		if strings.HasPrefix(strings.ToUpper(name), "XAUUSD") {
			if gold == "" || len(name) < len(gold) {
				gold = name
			}
		} else if other == "" {
			other = name
		}
	}
	if gold != "" {
		return gold
	}
	if symbol := selectedSymbol(accountDir, login); symbol != "" {
		return symbol
	}
	return other
}

func selectedSymbol(accountDir string, login int64) string {
	files, _ := filepath.Glob(filepath.Join(accountDir, "bases", "*", "symbols", fmt.Sprintf("selected-%d.dat", login)))
	var fallback string
	for _, file := range files {
		data, err := os.ReadFile(file)
		if err != nil || len(data) < 6 {
			continue
		}
		units := make([]uint16, 0, len(data)/2)
		for i := 4; i+1 < len(data); i += 2 {
			units = append(units, uint16(data[i])|uint16(data[i+1])<<8)
		}
		serverDir := filepath.Dir(filepath.Dir(file))
		for _, token := range symbolTokenPattern.FindAllString(string(utf16.Decode(units)), -1) {
			if strings.HasPrefix(strings.ToUpper(token), "XAUUSD") {
				return token
			}
			if fallback == "" {
				if info, err := os.Stat(filepath.Join(serverDir, "ticks", token)); err == nil && info.IsDir() {
					fallback = token
				}
			}
		}
	}
	return fallback
}

func execTerminal(terminal, dir, iniPath string, env []string) (*exec.Cmd, <-chan struct{}) {
	cmd := exec.Command(terminal, "/portable", "/config:"+iniPath)
	cmd.Dir = dir
	cmd.Env = env
	prepareChild(cmd)
	if err := cmd.Start(); err != nil {
		return nil, nil
	}
	done := make(chan struct{})
	go func() { _ = cmd.Wait(); close(done) }()
	return cmd, done
}

// bootstrapAccountSymbols starts the terminal with only a login (no Expert and
// no chart symbol), waits for the broker to authorize and sync, then closes it
// normally. MT5 only saves the Market Watch list (selected-<login>.dat) and the
// quoted symbol folders on a normal close, never when the process is killed.
func (w *worker) bootstrapAccountSymbols(terminal, accountDir, iniPath string, login int64) string {
	offsets := logOffsets(accountDir)
	cmd, done := execTerminal(terminal, accountDir, iniPath, w.cleanEnvironment(false))
	if cmd == nil {
		return ""
	}
	defer w.killTerminalsIn(accountDir)
	deadline := time.After(60 * time.Second)
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		select {
		case <-w.stop:
			return ""
		case <-done:
			return marketWatchSymbol(accountDir, login)
		case <-deadline:
			return marketWatchSymbol(accountDir, login)
		case <-tick.C:
			authorized, failure := scanAuthLog(accountDir, offsets, login)
			if failure != "" {
				return ""
			}
			if !authorized {
				continue
			}
			// Let the server finish syncing symbols and start quotes, then close.
			time.Sleep(6 * time.Second)
			requestMT5Close(uint32(cmd.Process.Pid))
			select {
			case <-done:
			case <-time.After(15 * time.Second):
			}
			return marketWatchSymbol(accountDir, login)
		}
	}
}
