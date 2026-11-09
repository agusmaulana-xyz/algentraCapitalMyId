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

func forbiddenStartupSymbol(symbol string) bool {
	return strings.HasPrefix(strings.ToUpper(strings.TrimSpace(symbol)), "XAUUSD247")
}

// knownAccountSymbol returns a symbol that the broker has already served to
// this account. A chart opened on a symbol the account does not have is dead
// and never runs the attached Expert, so the startup symbol must come from the
// account itself: the gold symbol confirmed by an earlier login, else the
// terminal's saved Market Watch list for this login.
func (w *worker) knownAccountSymbol(accountID, login int64, accountDir string) string {
	path := filepath.Join(w.cfg.runtimeDir, "states", fmt.Sprintf("%d.gold-symbol", accountID))
	if raw, err := os.ReadFile(path); err == nil {
		if symbol := strings.TrimSpace(string(raw)); symbol != "" && !forbiddenStartupSymbol(symbol) && !strings.ContainsAny(symbol, "\r\n,=") {
			return symbol
		}
	}
	return marketWatchSymbol(accountDir, login)
}

// marketWatchSymbol chooses a symbol from the account's saved watch list or
// broker symbol catalog when quotes are unavailable (for example, market close).
func marketWatchSymbol(accountDir string, login int64) string {
	var gold, other string
	tickDirs, _ := filepath.Glob(filepath.Join(accountDir, "bases", "*", "ticks", "*"))
	for _, dir := range tickDirs {
		info, err := os.Stat(dir)
		if err != nil || !info.IsDir() {
			continue
		}
		name := filepath.Base(dir)
		if !symbolTokenPattern.MatchString(name) || symbolTokenPattern.FindString(name) != name || forbiddenStartupSymbol(name) {
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
	if symbol := historicalSymbol(accountDir); symbol != "" {
		return symbol
	}
	if symbol := brokerCatalogSymbol(accountDir); symbol != "" {
		return symbol
	}
	return other
}

// history contains per-symbol directories even while the broker sends no live
// ticks, so it is another valid source for the chart symbol at market close.
func historicalSymbol(accountDir string) string {
	dirs, _ := filepath.Glob(filepath.Join(accountDir, "bases", "*", "history", "*"))
	var gold, preferred, other string
	for _, dir := range dirs {
		info, err := os.Stat(dir)
		if err != nil || !info.IsDir() {
			continue
		}
		name := filepath.Base(dir)
		if symbolTokenPattern.FindString(name) != name || forbiddenStartupSymbol(name) {
			continue
		}
		upper := strings.ToUpper(name)
		if strings.HasPrefix(upper, "XAUUSD") {
			if gold == "" || len(name) < len(gold) {
				gold = name
			}
		} else if isCommonStartupSymbol(name) {
			if preferred == "" {
				preferred = name
			}
		} else if other == "" {
			other = name
		}
	}
	if gold != "" {
		return gold
	}
	if preferred != "" {
		return preferred
	}
	return other
}

func symbolNames(data []byte) (gold, other string) {
	if len(data) < 6 {
		return "", ""
	}
	units := make([]uint16, 0, len(data)/2)
	for i := 4; i+1 < len(data); i += 2 {
		units = append(units, uint16(data[i])|uint16(data[i+1])<<8)
	}
	preferred := ""
	for _, token := range symbolTokenPattern.FindAllString(string(utf16.Decode(units)), -1) {
		if forbiddenStartupSymbol(token) {
			continue
		}
		if !symbolTokenPattern.MatchString(token) || symbolTokenPattern.FindString(token) != token {
			continue
		}
		if strings.HasPrefix(strings.ToUpper(token), "XAUUSD") {
			if gold == "" || len(token) < len(gold) {
				gold = token
			}
		} else if isCommonStartupSymbol(token) {
			if preferred == "" {
				preferred = token
			}
		} else if other == "" {
			other = token
		}
	}
	if preferred != "" {
		return gold, preferred
	}
	return gold, other
}

func isCommonStartupSymbol(symbol string) bool {
	upper := strings.ToUpper(symbol)
	for _, base := range []string{
		"EURUSD", "GBPUSD", "USDJPY", "USDCHF", "AUDUSD", "NZDUSD", "USDCAD",
		"EURJPY", "GBPJPY", "XAGUSD", "BTCUSD", "ETHUSD", "US30", "US500",
		"NAS100", "GER40", "UK100", "JP225",
	} {
		if strings.HasPrefix(upper, base) {
			return true
		}
	}
	return false
}

func selectedSymbol(accountDir string, login int64) string {
	files, _ := filepath.Glob(filepath.Join(accountDir, "bases", "*", "symbols", fmt.Sprintf("selected-%d.dat", login)))
	var fallback string
	for _, file := range files {
		data, err := os.ReadFile(file)
		if err != nil {
			continue
		}
		gold, other := symbolNames(data)
		if gold != "" {
			return gold
		}
		if fallback == "" {
			fallback = other
		}
	}
	return fallback
}

// symbols-*.dat is MT5's broker symbol catalog. Unlike tick folders, it is
// populated when the terminal syncs the broker even if the market is closed.
func brokerCatalogSymbol(accountDir string) string {
	files, _ := filepath.Glob(filepath.Join(accountDir, "bases", "*", "symbols", "symbols-*.dat"))
	var gold, other string
	for _, file := range files {
		data, err := os.ReadFile(file)
		if err != nil {
			continue
		}
		candidateGold, candidateOther := symbolNames(data)
		if candidateGold != "" && (gold == "" || len(candidateGold) < len(gold)) {
			gold = candidateGold
		}
		if other == "" && candidateOther != "" {
			other = candidateOther
		}
	}
	if gold != "" {
		return gold
	}
	return other
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
			// Wait for the broker symbol catalog; closed markets may never create
			// tick folders, so a quote is not the completion signal.
			syncDeadline := time.Now().Add(20 * time.Second)
			for time.Now().Before(syncDeadline) && marketWatchSymbol(accountDir, login) == "" {
				select {
				case <-w.stop:
					return ""
				case <-done:
					return marketWatchSymbol(accountDir, login)
				case <-time.After(time.Second):
				}
			}
			requestMT5Close(uint32(cmd.Process.Pid))
			select {
			case <-done:
			case <-time.After(15 * time.Second):
			}
			return marketWatchSymbol(accountDir, login)
		}
	}
}
