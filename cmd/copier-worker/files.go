package main

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"unicode/utf16"
)

func isTerminalFileBusy(err error) bool {
	if os.IsPermission(err) {
		return true
	}
	if runtime.GOOS != "windows" {
		return false
	}
	var code syscall.Errno
	if !errors.As(err, &code) {
		return false
	}
	// Windows reports sharing and lock violations separately from access denied.
	return code == syscall.Errno(32) || code == syscall.Errno(33)
}

// clearSavedCharts removes charts MT5 restored from the saved profile. Each
// launch with a startup Symbol adds a chart to that profile, so stale ones
// (often for symbols the broker does not offer) pile up as extra windows.
func clearSavedCharts(terminalDir string) {
	dir := filepath.Join(terminalDir, "MQL5", "Profiles", "Charts", "Default")
	files, _ := filepath.Glob(filepath.Join(dir, "*.chr"))
	for _, f := range files {
		_ = os.Remove(f)
	}
}

func enableAutoTrading(terminalDir string) error {
	path := filepath.Join(terminalDir, "Config", "common.ini")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	raw, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	encoding, bom, text := "utf-8", []byte{}, string(raw)
	if bytes.HasPrefix(raw, []byte{0xef, 0xbb, 0xbf}) {
		bom = raw[:3]
	}
	if len(raw) >= 2 && raw[0] == 0xff && raw[1] == 0xfe {
		encoding, bom, text = "utf-16le", raw[:2], decodeUTF16(raw[2:], binary.LittleEndian)
	}
	if len(raw) >= 2 && raw[0] == 0xfe && raw[1] == 0xff {
		encoding, bom, text = "utf-16be", raw[:2], decodeUTF16(raw[2:], binary.BigEndian)
	}
	if strings.HasPrefix(text, "\ufeff") {
		text = strings.TrimPrefix(text, "\ufeff")
	}
	newline := "\n"
	if strings.Contains(text, "\r\n") {
		newline = "\r\n"
	}
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	keys := []struct{ key, value string }{{"allowlivetrading", "AllowLiveTrading=1"}, {"enabled", "Enabled=1"}, {"account", "Account=0"}, {"profile", "Profile=0"}}
	var output []string
	present := map[string]bool{}
	inExperts, foundExperts := false, false
	flush := func() {
		for _, item := range keys {
			if !present[item.key] {
				output = append(output, item.value)
			}
		}
	}
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "[") && strings.HasSuffix(trimmed, "]") {
			if inExperts {
				flush()
			}
			inExperts = strings.EqualFold(strings.TrimSpace(trimmed[1:len(trimmed)-1]), "experts")
			foundExperts = foundExperts || inExperts
			present = map[string]bool{}
			output = append(output, line)
			continue
		}
		if inExperts && strings.Contains(trimmed, "=") && !strings.HasPrefix(trimmed, ";") && !strings.HasPrefix(trimmed, "#") {
			key, _, _ := strings.Cut(trimmed, "=")
			key = strings.ToLower(strings.TrimSpace(key))
			for _, item := range keys {
				if key == item.key {
					output = append(output, item.value)
					present[key] = true
					goto nextLine
				}
			}
		}
		output = append(output, line)
	nextLine:
	}
	if inExperts {
		flush()
	}
	if !foundExperts {
		if len(output) > 0 && strings.TrimSpace(output[len(output)-1]) != "" {
			output = append(output, "")
		}
		output = append(output, "[Experts]")
		for _, item := range keys {
			output = append(output, item.value)
		}
	}
	rendered := strings.Join(output, newline) + newline
	var encoded []byte
	switch encoding {
	case "utf-16le":
		encoded = encodeUTF16(rendered, binary.LittleEndian)
	case "utf-16be":
		encoded = encodeUTF16(rendered, binary.BigEndian)
	default:
		encoded = []byte(rendered)
	}
	encoded = append(bom, encoded...)
	if bytes.Equal(raw, encoded) {
		return nil
	}
	temp := path + ".algentra.tmp"
	if err := os.WriteFile(temp, encoded, 0o600); err != nil {
		return err
	}
	if err := os.Rename(temp, path); err != nil {
		_ = os.Remove(temp)
		return err
	}
	return nil
}

func decodeUTF16(raw []byte, order binary.ByteOrder) string {
	if len(raw)%2 != 0 {
		raw = raw[:len(raw)-1]
	}
	units := make([]uint16, len(raw)/2)
	for i := range units {
		units[i] = order.Uint16(raw[i*2:])
	}
	return string(utf16.Decode(units))
}
func encodeUTF16(value string, order binary.ByteOrder) []byte {
	units := utf16.Encode([]rune(value))
	out := make([]byte, len(units)*2)
	for i, unit := range units {
		order.PutUint16(out[i*2:], unit)
	}
	return out
}

func copyTree(source, destination string) error {
	info, err := os.Stat(source)
	if err != nil || !info.IsDir() {
		return errors.New("template MT5 tidak dapat dibaca")
	}
	if err := os.MkdirAll(destination, 0o700); err != nil {
		return err
	}
	entries, err := os.ReadDir(source)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		src := filepath.Join(source, entry.Name())
		dst := filepath.Join(destination, entry.Name())
		info, err := os.Stat(src)
		if err != nil {
			return err
		}
		if info.IsDir() {
			if err := copyTree(src, dst); err != nil {
				return err
			}
			continue
		}
		if info.Mode().IsRegular() {
			if err := copyFile(src, dst); err != nil {
				return err
			}
		}
	}
	return nil
}

func copyFile(source, destination string) error {
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
		return err
	}
	info, err := input.Stat()
	if err != nil {
		return err
	}
	mode := info.Mode().Perm()
	if mode == 0 {
		mode = 0o600
	}
	output, err := os.OpenFile(destination, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(output, input); err != nil {
		output.Close()
		return err
	}
	return output.Close()
}

func removeWithin(root, target string) error {
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	targetAbs, err := filepath.Abs(target)
	if err != nil {
		return err
	}
	rel, err := filepath.Rel(rootAbs, targetAbs)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("path cleanup berada di luar runtime")
	}
	if resolvedRoot, e := filepath.EvalSymlinks(rootAbs); e == nil {
		rootAbs = resolvedRoot
	}
	if resolvedTarget, e := filepath.EvalSymlinks(targetAbs); e == nil {
		rel, err = filepath.Rel(rootAbs, resolvedTarget)
		if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return fmt.Errorf("path cleanup symlink berada di luar runtime")
		}
	}
	return nil
}
