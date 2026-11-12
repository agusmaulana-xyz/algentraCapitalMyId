package main

// Login probe: membuka terminal MT5 follower dengan konfigurasi startup yang
// ditulis langsung oleh worker, lalu menunggu laporan dari Expert
// MT5LoginProbe dan membaca log terminal untuk alasan penolakan broker.

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"
)

const (
	probeTimeout    = 150 * time.Second
	probeStatusFile = "algentra-login-probe.json"
)

type probeStatus struct {
	Ready   bool   `json:"ready"`
	Valid   bool   `json:"valid"`
	Message string `json:"message"`
	Login   int64  `json:"login"`
	Server  string `json:"server"`
	Symbol  string `json:"symbol"`
}

func (w *worker) validateLogin(check loginCheck) (bool, string) {
	valid, message := w.probeLogin(check)
	return valid, limitRunes(message, 240)
}

func (w *worker) probeLogin(check loginCheck) (bool, string) {
	login, err := strconv.ParseInt(strings.TrimSpace(check.Login), 10, 64)
	if err != nil || login <= 0 {
		return false, "Nomor login MT5 harus berupa angka."
	}
	server := strings.TrimSpace(check.Server)
	if server == "" || check.BrokerPassword == "" || strings.ContainsAny(server+check.BrokerPassword, "\r\n\x00") {
		return false, "Server atau password broker kosong atau mengandung karakter yang tidak valid."
	}
	accountDir := filepath.Join(w.cfg.runtimeDir, "terminals", fmt.Sprint(check.AccountID))
	terminal := filepath.Join(accountDir, "terminal64.exe")
	// Terminal sisa dari pemeriksaan sebelumnya (timeout) masih mengunci file,
	// folder data, dan port MCP 22346. Tutup dulu sebelum memulai yang baru.
	w.killTerminalsIn(accountDir)

	if _, err := os.Stat(terminal); err != nil {
		if err := copyTree(w.cfg.templateDir, accountDir); err != nil {
			if isTerminalFileBusy(err) {
				return false, terminalInUseMessage
			}
			return false, "Terminal follower tidak tersedia pada server."
		}
	}
	if _, err := os.Stat(terminal); err != nil {
		return false, "Terminal follower tidak tersedia pada server."
	}
	if err := ensureNativeBrokerServers(accountDir, w.cfg.templateDir, w.cfg.masterTerminalPath, server); err != nil {
		return false, "Daftar server broker MT5 tidak tersedia. Pastikan terminal MT5 worker sudah memuat perusahaan/server broker ini, lalu coba lagi."
	}
	probeSource := filepath.Join(w.cfg.projectRoot, "mt5", "experts", "Algentra", "MT5LoginProbe.mq5")
	probeTarget := filepath.Join(accountDir, "MQL5", "Experts", "Algentra", "MT5LoginProbe.mq5")
	if err := copyFile(probeSource, probeTarget); err != nil {
		if isTerminalFileBusy(err) {
			return false, terminalInUseMessage
		}
		return false, "Source Expert MT5LoginProbe.mq5 tidak dapat dipasang pada terminal follower."
	}
	// Compile the exact source installed with this worker update inside the
	// account terminal. The template's .ex5 can be stale after an update, and
	// .ex5 files are intentionally not tracked in the repository.
	if err := compileCustomEA(accountDir, probeTarget); err != nil {
		if isTerminalFileBusy(err) {
			return false, terminalInUseMessage
		}
		return false, limitRunes("Expert MT5LoginProbe gagal dikompilasi pada terminal follower: "+err.Error(), 240)
	}
	probeBinary := strings.TrimSuffix(probeTarget, filepath.Ext(probeTarget)) + ".ex5"
	if info, err := os.Stat(probeBinary); err != nil || info.Size() == 0 {
		return false, "Kompilasi Expert MT5LoginProbe tidak menghasilkan file .ex5 yang valid."
	}
	clearSavedCharts(accountDir)
	if err := enableAutoTrading(accountDir); err != nil {
		if isTerminalFileBusy(err) {
			return false, terminalInUseMessage
		}
		return false, "Worker tidak dapat menyiapkan izin trading terminal MT5."
	}

	filesDir := filepath.Join(accountDir, "MQL5", "Files")
	legacyStatusPath := filepath.Join(filesDir, probeStatusFile)
	if err := os.MkdirAll(filesDir, 0o700); err != nil {
		return false, "Worker tidak dapat menyiapkan folder status terminal MT5."
	}
	appData := os.Getenv("APPDATA")
	if appData == "" {
		appData, _ = os.UserConfigDir()
	}
	if appData == "" {
		return false, "Worker tidak dapat menemukan folder data bersama MT5 untuk laporan probe."
	}
	commonFilesDir := filepath.Join(appData, "MetaQuotes", "Terminal", "Common", "Files")
	if err := os.MkdirAll(commonFilesDir, 0o700); err != nil {
		return false, "Worker tidak dapat menyiapkan folder laporan bersama MT5."
	}
	statusName := fmt.Sprintf("algentra-login-probe-%d.json", check.AccountID)
	statusPath := filepath.Join(commonFilesDir, statusName)
	portableStatusPath := filepath.Join(filesDir, statusName)
	for _, oldPath := range []string{statusPath, portableStatusPath, legacyStatusPath} {
		if err := os.Remove(oldPath); err != nil && !os.IsNotExist(err) {
			return false, terminalInUseMessage
		}
	}
	presetName := fmt.Sprintf("algentra-login-probe-%d.set", check.AccountID)
	presetPath := filepath.Join(accountDir, "MQL5", "Presets", presetName)
	if err := os.MkdirAll(filepath.Dir(presetPath), 0o700); err != nil {
		return false, "Worker tidak dapat menyiapkan preset Expert login probe."
	}
	if err := os.WriteFile(presetPath, []byte("ProbeStatusFileName="+statusName+"\r\n"), 0o600); err != nil {
		return false, "Worker tidak dapat menulis preset Expert login probe."
	}
	defer os.Remove(presetPath)

	probeDir, err := os.MkdirTemp("", "algentra-mt5-login-")
	if err != nil {
		return false, "Worker tidak dapat menyiapkan folder sementara pemeriksaan login."
	}
	defer os.RemoveAll(probeDir)
	_ = os.Chmod(probeDir, 0o700)
	iniPath := filepath.Join(probeDir, "login-probe.ini")
	// A server no terminal here knows yet is reached through its access point.
	loginServer := w.loginServerFor(accountDir, server)
	startupSymbol := w.knownAccountSymbol(check.AccountID, login, accountDir)
	if startupSymbol == "" {
		// New account: let the broker sync once so the chart can open on a
		// symbol the account really has, instead of guessing XAUUSD.
		bootstrapINI := filepath.Join(probeDir, "login-bootstrap.ini")
		bootstrap := strings.Join([]string{
			"[Common]",
			"Login=" + strconv.FormatInt(login, 10),
			"Password=" + check.BrokerPassword,
			"Server=" + loginServer,
			"KeepPrivate=1",
			"NewsEnable=0",
			"ProxyEnable=0",
			"",
		}, "\r\n")
		if err := os.WriteFile(bootstrapINI, []byte(bootstrap), 0o600); err != nil {
			return false, "Worker tidak dapat menulis konfigurasi startup terminal MT5."
		}
		startupSymbol = w.bootstrapAccountSymbols(terminal, accountDir, bootstrapINI, login)
		_ = os.Remove(bootstrapINI)
		clearSavedCharts(accountDir)
		if startupSymbol == "" {
			return false, "Login broker berhasil, tetapi MT5 belum menemukan simbol Market Watch yang tersedia. Coba lagi setelah terminal selesai menyinkronkan simbol."
		}
	}
	ini := strings.Join([]string{
		"[Common]",
		"Login=" + strconv.FormatInt(login, 10),
		"Password=" + check.BrokerPassword,
		"Server=" + loginServer,
		"KeepPrivate=1",
		"NewsEnable=0",
		"ProxyEnable=0",
		"",
		"[Experts]",
		"AllowLiveTrading=1",
		"AllowDllImport=0",
		"Enabled=1",
		"Account=0",
		"Profile=0",
		"",
		"[StartUp]",
		`Expert=Algentra\MT5LoginProbe`,
		"ExpertParameters=" + presetName,
		"Symbol=" + startupSymbol,
		"Period=M1",
		"",
	}, "\r\n")
	if err := os.WriteFile(iniPath, []byte(ini), 0o600); err != nil {
		return false, "Worker tidak dapat menulis konfigurasi startup terminal MT5."
	}
	// File ini berisi password; hapus secepat mungkin setelah terminal membacanya.
	removeINI := time.AfterFunc(20*time.Second, func() { _ = os.Remove(iniPath) })
	defer removeINI.Stop()
	defer os.Remove(iniPath)

	offsets := logOffsets(accountDir)
	cmd := exec.Command(terminal, "/portable", "/config:"+iniPath)
	cmd.Dir = accountDir
	cmd.Env = w.cleanEnvironment(false)
	prepareChild(cmd)
	if err := cmd.Start(); err != nil {
		return false, "Terminal MT5 tidak dapat dijalankan oleh worker."
	}
	chartMonitorStop := make(chan struct{})
	go manageMT5Charts(uint32(cmd.Process.Pid), chartMonitorStop, true)
	defer close(chartMonitorStop)
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	exited := false
	defer func() {
		if !exited {
			_ = stopChildProcess(cmd, done, 10*time.Second)
		}
		w.killTerminalsIn(accountDir)
	}()

	deadline := time.NewTimer(probeTimeout)
	defer deadline.Stop()
	tick := time.NewTicker(time.Second)
	defer tick.Stop()

	var last probeStatus
	seen := false
	for n := 0; ; n++ {
		select {
		case <-w.stop:
			return false, "Pemeriksaan dibatalkan karena worker dihentikan."
		case waitErr := <-done:
			exited = true
			status, ok := readAnyProbeStatus(portableStatusPath, statusPath, legacyStatusPath)
			if !ok {
				status, ok = readProbeStatusFromLogs(accountDir, offsets)
			}
			if ok && status.Ready {
				return w.validateProbeResult(status, login, server, loginServer, check.AccountID)
			}
			authorized, reason := scanAuthLog(accountDir, offsets, login)
			if reason != "" {
				message, _ := authFailureMessageForServer(reason, server)
				return false, message
			}
			if authorized {
				return false, "Login broker berhasil, tetapi terminal tidak menjalankan MT5LoginProbe yang sudah dikompilasi. Periksa log Experts/Journals dan izin Algo Trading."
			}
			if waitErr != nil {
				return false, "Terminal MT5 berhenti sebelum laporan login diterima. Periksa log terminal dan konfigurasi startup."
			}
			return false, "Terminal MT5 berhenti sebelum login selesai."
		case <-deadline.C:
			authorized, reason := scanAuthLog(accountDir, offsets, login)
			switch {
			case authorized && !seen:
				return false, "Login broker berhasil, tetapi terminal tidak menjalankan MT5LoginProbe yang sudah dikompilasi. Periksa log Experts/Journals dan izin Algo Trading."
			case reason != "":
				message, _ := authFailureMessageForServer(reason, server)
				return false, message
			case seen && last.Message != "" && !strings.Contains(strings.ToLower(last.Message), "menunggu koneksi broker"):
				return false, last.Message
			default:
				return false, "Terminal MT5 terbuka tetapi tidak pernah mencoba login ke broker dalam 150 detik. Periksa nama server dan konfigurasi startup terminal."
			}
		case <-tick.C:
		}
		status, ok := readAnyProbeStatus(portableStatusPath, statusPath, legacyStatusPath)
		if !ok {
			status, ok = readProbeStatusFromLogs(accountDir, offsets)
		}
		if ok {
			last, seen = status, true
			if status.Ready {
				return w.validateProbeResult(status, login, server, loginServer, check.AccountID)
			}
		}
		if n%3 == 2 {
			if _, reason := scanAuthLog(accountDir, offsets, login); reason != "" {
				if message, definitive := authFailureMessageForServer(reason, server); definitive {
					return false, message
				}
			}
		}
	}
}

// ensureNativeBrokerServers keeps an existing terminal catalog, or seeds an
// isolated terminal from the user's installed MT5 before using its logical
// company/server name for account login.
func ensureNativeBrokerServers(accountDir, templateDir, masterTerminalPath, server string) error {
	path := filepath.Join(accountDir, "Config", "servers.dat")
	if info, err := os.Stat(path); err == nil && !info.IsDir() && info.Size() > 0 {
		if strings.TrimSpace(server) == "" || dirExists(filepath.Join(accountDir, "bases", server)) {
			return nil
		}
		// MT5 never tries to log in to a server missing from its catalog (it only
		// shows a default "Netting" account). Copy the catalog from a terminal
		// that has really connected to this server.
		if source := terminalCatalogWithServer(server, masterTerminalPath); source != "" &&
			filepath.Clean(source) != filepath.Clean(path) {
			return copyFile(source, path)
		}
		return nil
	}
	return seedBrokerServers(accountDir, templateDir, masterTerminalPath)
}

func dirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

// terminalCatalogWithServer returns the newest servers.dat of a terminal on this
// machine whose data folder contains bases\<server>, i.e. one that knows it.
func terminalCatalogWithServer(server, masterTerminalPath string) string {
	var roots []string
	if strings.TrimSpace(masterTerminalPath) != "" {
		roots = append(roots, filepath.Dir(masterTerminalPath))
	}
	if appData := os.Getenv("APPDATA"); appData != "" {
		terminalDataRoot := filepath.Join(appData, "MetaQuotes", "Terminal")
		entries, _ := os.ReadDir(terminalDataRoot)
		for _, entry := range entries {
			if entry.IsDir() {
				roots = append(roots, filepath.Join(terminalDataRoot, entry.Name()))
			}
		}
	}
	var best string
	var bestAt time.Time
	for _, root := range roots {
		if !dirExists(filepath.Join(root, "bases", server)) {
			continue
		}
		for _, name := range []string{"Config", "config"} {
			candidate := filepath.Join(root, name, "servers.dat")
			if info, err := os.Stat(candidate); err == nil && !info.IsDir() && info.Size() > 0 && info.ModTime().After(bestAt) {
				best, bestAt = candidate, info.ModTime()
			}
		}
	}
	return best
}

// seedBrokerServers copies MT5's broker directory into the portable follower.
// A clean MetaQuotes terminal can have no broker-specific server entries yet,
// even when the requested server name is correct. Broker discovery from the
// master terminal or another profile on this Windows account seeds that list.
func seedBrokerServers(accountDir, templateDir, masterTerminalPath string) error {
	target := filepath.Join(accountDir, "Config", "servers.dat")
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return err
	}

	var candidates []string
	if strings.TrimSpace(masterTerminalPath) != "" {
		masterDir := filepath.Dir(masterTerminalPath)
		candidates = append(candidates,
			filepath.Join(masterDir, "Config", "servers.dat"),
			filepath.Join(masterDir, "config", "servers.dat"),
		)
		if appData := os.Getenv("APPDATA"); appData != "" {
			terminalDataRoot := filepath.Join(appData, "MetaQuotes", "Terminal")
			entries, _ := os.ReadDir(terminalDataRoot)
			want := normalizedWindowsPath(masterTerminalPath)
			for _, entry := range entries {
				if !entry.IsDir() {
					continue
				}
				dataDir := filepath.Join(terminalDataRoot, entry.Name())
				origin, _ := os.ReadFile(filepath.Join(dataDir, "origin.txt"))
				if strings.Contains(normalizedWindowsPath(string(origin)), want) {
					candidates = append(candidates, filepath.Join(dataDir, "config", "servers.dat"))
				}
			}
		}
	}

	// Scheduled workers commonly use TelegramSignalEA and therefore have no
	// master terminal path in master.json. Reuse the newest terminal profile in
	// the worker's own Windows account; this is where the broker was discovered.
	if appData := os.Getenv("APPDATA"); appData != "" {
		terminalDataRoot := filepath.Join(appData, "MetaQuotes", "Terminal")
		entries, _ := os.ReadDir(terminalDataRoot)
		var newest string
		var newestAt time.Time
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			candidate := filepath.Join(terminalDataRoot, entry.Name(), "config", "servers.dat")
			info, err := os.Stat(candidate)
			if err == nil && info.ModTime().After(newestAt) {
				newest, newestAt = candidate, info.ModTime()
			}
		}
		if newest != "" {
			candidates = append(candidates, newest)
		}
	}
	candidates = append(candidates,
		filepath.Join(templateDir, "Config", "servers.dat"),
		filepath.Join(templateDir, "config", "servers.dat"),
	)

	for _, source := range candidates {
		info, err := os.Stat(source)
		if err != nil || info.IsDir() || info.Size() == 0 {
			continue
		}
		if filepath.Clean(source) == filepath.Clean(target) {
			return nil
		}
		return copyFile(source, target)
	}
	return os.ErrNotExist
}

func normalizedWindowsPath(value string) string {
	value = strings.TrimSpace(strings.Trim(string(value), "\x00"))
	value = strings.ReplaceAll(value, "/", "\\")
	return strings.ToLower(filepath.Clean(value))
}

func (w *worker) validateProbeResult(status probeStatus, login int64, expectedServer, loginServer string, accountID int64) (bool, string) {
	if status.Login != login {
		return false, "Terminal login ke akun lain dari yang didaftarkan."
	}
	reported := strings.TrimSpace(status.Server)
	if !strings.EqualFold(reported, strings.TrimSpace(expectedServer)) && !strings.EqualFold(reported, strings.TrimSpace(loginServer)) {
		return false, fmt.Sprintf("Server broker tidak cocok (diminta: %s; terminal: %s).", expectedServer, status.Server)
	}
	if status.Valid && status.Symbol != "" {
		symbol := strings.TrimSpace(status.Symbol)
		if strings.ContainsAny(symbol, "\r\n,=") {
			return false, "Nama simbol gold dari terminal MT5 tidak valid."
		}
		path := filepath.Join(w.cfg.runtimeDir, "states", fmt.Sprintf("%d.gold-symbol", accountID))
		if err := writeAtomic(path, []byte(symbol+"\n")); err != nil {
			return false, "Login broker berhasil, tetapi simbol gold akun tidak dapat disimpan untuk copier."
		}
	}
	return status.Valid, status.Message
}

func readProbeStatus(path string) (probeStatus, bool) {
	data, err := os.ReadFile(path)
	if err != nil || len(data) == 0 {
		return probeStatus{}, false
	}
	var status probeStatus
	if json.Unmarshal(data, &status) != nil {
		return probeStatus{}, false // file sedang ditulis EA; coba lagi
	}
	return status, true
}

func readAnyProbeStatus(paths ...string) (probeStatus, bool) {
	for _, path := range paths {
		if status, ok := readProbeStatus(path); ok {
			return status, true
		}
	}
	return probeStatus{}, false
}

// readProbeStatusFromLogs is a fallback for terminal environments where MQL5
// cannot write into either the portable Files folder or FILE_COMMON. The EA
// emits a single STATUS JSON log line whenever its report changes.
func readProbeStatusFromLogs(dir string, offsets map[string]int64) (probeStatus, bool) {
	patterns := []string{
		filepath.Join(dir, "logs", "*.log"),
		filepath.Join(dir, "MQL5", "Logs", "*.log"),
	}
	var latest probeStatus
	found := false
	const marker = "[MT5LoginProbe] STATUS "
	for _, pattern := range patterns {
		files, _ := filepath.Glob(pattern)
		for _, path := range files {
			data, err := os.ReadFile(path)
			if err != nil {
				continue
			}
			start := offsets[path]
			if start < 0 || start > int64(len(data)) {
				start = 0
			}
			data = data[start&^1:]
			units := make([]uint16, 0, len(data)/2)
			for i := 0; i+1 < len(data); i += 2 {
				units = append(units, uint16(data[i])|uint16(data[i+1])<<8)
			}
			for _, line := range strings.Split(string(utf16.Decode(units)), "\n") {
				index := strings.Index(line, marker)
				if index < 0 {
					continue
				}
				var status probeStatus
				if json.Unmarshal([]byte(strings.TrimSpace(line[index+len(marker):])), &status) == nil {
					latest, found = status, true
				}
			}
		}
	}
	return latest, found
}

func logOffsets(dir string) map[string]int64 {
	offsets := map[string]int64{}
	for _, pattern := range []string{
		filepath.Join(dir, "logs", "*.log"),
		filepath.Join(dir, "MQL5", "Logs", "*.log"),
	} {
		files, _ := filepath.Glob(pattern)
		for _, f := range files {
			if info, err := os.Stat(f); err == nil {
				offsets[f] = info.Size()
			}
		}
	}
	return offsets
}

// scanAuthLog membaca log terminal (UTF-16LE) yang ditulis setelah probe mulai
// dan mengembalikan hasil otorisasi terakhir untuk nomor login tersebut.
func scanAuthLog(dir string, offsets map[string]int64, login int64) (authorized bool, failure string) {
	files, _ := filepath.Glob(filepath.Join(dir, "logs", "*.log"))
	needle := "'" + strconv.FormatInt(login, 10) + "'"
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		start := offsets[f]
		if start < 0 || start > int64(len(data)) {
			start = 0
		}
		data = data[start&^1:]
		units := make([]uint16, 0, len(data)/2)
		for i := 0; i+1 < len(data); i += 2 {
			units = append(units, uint16(data[i])|uint16(data[i+1])<<8)
		}
		for _, line := range strings.Split(string(utf16.Decode(units)), "\n") {
			if !strings.Contains(line, needle) {
				continue
			}
			lower := strings.ToLower(line)
			if strings.Contains(lower, "authorized on") {
				authorized, failure = true, ""
			} else if strings.Contains(lower, "trading has been disabled") && strings.Contains(lower, "disabled on server") {
				failure = "trading disabled on server"
			} else if i := strings.LastIndex(lower, "authorization on"); i >= 0 {
				if j := strings.LastIndex(lower, "failed ("); j > i {
					authorized = false
					failure = strings.TrimSpace(strings.TrimRight(strings.TrimSpace(lower[j+len("failed ("):]), ")"))
				}
			}
		}
	}
	return authorized, failure
}

func authFailureMessage(reason string) (string, bool) {
	r := strings.ToLower(reason)
	switch {
	case strings.Contains(r, "invalid account"):
		return "Login ditolak broker (Invalid account). Periksa nomor login, password, dan nama server.", true
	case strings.Contains(r, "account disabled"):
		return "Akun dinonaktifkan oleh broker.", true
	case strings.Contains(r, "trading disabled on server"):
		return "Login broker berhasil, tetapi server broker menonaktifkan trading untuk akun ini. Minta broker mengaktifkan izin trading akun sebelum copy trading dapat berjalan.", true
	case strings.Contains(r, "no connection"):
		return "Terminal tidak dapat terhubung ke server broker (No connection). Periksa nama server dan akses internet keluar dari server Windows.", false
	default:
		return "Broker menolak login: " + reason + ".", false
	}
}

func authFailureMessageForServer(reason, requestedServer string) (string, bool) {
	message, definitive := authFailureMessage(reason)
	if strings.Contains(strings.ToLower(reason), "invalid account") {
		return fmt.Sprintf("Broker menolak login pada server %q (Invalid account). Di MT5, cari broker melalui Open an Account > Find your company, pilih server akun yang benar, lalu pastikan nomor akun dan password trading (bukan investor password) sesuai.", requestedServer), true
	}
	return message, definitive
}

// killTerminalsIn menghentikan terminal64.exe yang berjalan dari folder akun
// tertentu saja (terminal akun lain dan master tidak tersentuh).
func (w *worker) killTerminalsIn(dir string) {
	if runtime.GOOS != "windows" {
		return
	}
	script := `$dir = $env:ALGENTRA_TERMINAL_DIR.TrimEnd('\') + '\'
Get-CimInstance Win32_Process -Filter "Name='terminal64.exe'" | Where-Object { $_.ExecutablePath -and $_.ExecutablePath.StartsWith($dir, [System.StringComparison]::OrdinalIgnoreCase) } | ForEach-Object { Stop-Process -Id $_.ProcessId -Force -ErrorAction SilentlyContinue }`
	units := utf16.Encode([]rune(script))
	raw := make([]byte, 0, len(units)*2)
	for _, u := range units {
		raw = append(raw, byte(u), byte(u>>8))
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "powershell.exe", "-NoProfile", "-NonInteractive", "-EncodedCommand", base64.StdEncoding.EncodeToString(raw))
	cmd.Env = append(w.cleanEnvironment(false), "ALGENTRA_TERMINAL_DIR="+filepath.Clean(dir))
	_ = cmd.Run()
	time.Sleep(time.Second) // beri waktu Windows melepas handle file
}
