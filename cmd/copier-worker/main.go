// copier-worker supervises managed-copy follower processes on Windows.
// Managed job polling, MT5 login checks, EA startup, lifecycle and API
// reporting run through this Go process.
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/rnpproject/algentra-backend/internal/copier/managed"
	"github.com/rnpproject/algentra-backend/internal/textutil"
)

type config struct {
	APIBaseURL             string `json:"api_base_url"`
	WorkerAPIKeyEnv        string `json:"worker_api_key_env"`
	MasterConfig           string `json:"master_config"`
	MasterSource           string `json:"master_source"`
	TerminalTemplateDir    string `json:"terminal_template_dir"`
	RuntimeDir             string `json:"runtime_dir"`
	ExecutionMode          string `json:"execution_mode"`
	FeedAddress            string `json:"feed_address"`
	PollSeconds            int    `json:"poll_seconds"`
	HeartbeatTimeoutSecond int    `json:"heartbeat_timeout_seconds"`
	ProjectRoot            string `json:"project_root"`

	configDir, apiBase, masterConfig, masterTerminalPath, templateDir, runtimeDir string
	projectRoot, apiKey, masterToken                                              string
	signalDir                                                                     string
	masterHealthURL                                                               string
	masterSnapshotURL                                                             string
}

type job struct {
	AccountID         int64             `json:"account_id"`
	Label             string            `json:"label"`
	Login             string            `json:"login"`
	Server            string            `json:"server"`
	BrokerPassword    string            `json:"broker_password"`
	FollowerSymbol    string            `json:"follower_symbol"`
	ModeLot           string            `json:"mode_lot"`
	RatioLot          float64           `json:"ratio_lot"`
	FixedLot          float64           `json:"lot_tetap"`
	MaxLotPerOrder    float64           `json:"max_lot_per_order"`
	MaxLotTotal       float64           `json:"max_lot_total"`
	MaxOpenPositions  int               `json:"max_open_positions"`
	EAMode            string            `json:"ea_mode"`
	CustomEAName      string            `json:"custom_ea_name"`
	CustomEAUpdatedAt string            `json:"custom_ea_updated_at"`
	CustomEAParams    map[string]string `json:"custom_ea_params"`
	CustomEAPaused    bool              `json:"custom_ea_paused"`
	CustomEAInputsKnown bool            `json:"custom_ea_inputs_known"`
}

type loginCheck struct {
	AccountID      int64  `json:"account_id"`
	Login          string `json:"login"`
	Server         string `json:"server"`
	BrokerPassword string `json:"broker_password"`
}

type child struct {
	accountID    int64
	fingerprint  string
	cmd          *exec.Cmd
	done         chan error
	stopped      chan struct{}
	log          *os.File
	heartbeat    string
	startedAt    time.Time
	executor     string
	feedToken    string
	startupINI   string
	parameterSet string
}

type worker struct {
	cfg           config
	client        *http.Client
	children      map[int64]*child
	reporters     map[int64]*child
	reporterRetry map[int64]time.Time
	discoverRetry map[int64]time.Time
	captureAttempts map[int64]int
	retryAt       map[int64]time.Time
	failures      map[int64]int
	master        *exec.Cmd
	masterDone    chan error
	masterLog     *os.File
	masterRetryAt time.Time
	stop          chan struct{}
	stopOnce      sync.Once
	masterBinary  string
	feed          *managed.Feed
	checkMu       sync.Mutex
	checking      map[int64]bool
	checks        sync.WaitGroup
}

func main() {
	flag.Usage = func() { fmt.Fprintln(flag.CommandLine.Output(), "Usage: copier-worker [managed-worker.json]") }
	flag.Parse()
	if runtime.GOOS != "windows" {
		log.Fatal("managed copier worker hanya berjalan pada Windows")
	}
	path := "apps/copier-worker/config/managed-worker.json"
	if flag.NArg() > 1 {
		log.Fatal("copier-worker hanya menerima satu path konfigurasi")
	}
	if flag.NArg() == 1 {
		path = flag.Arg(0)
	}
	cfg, err := loadConfig(path)
	if err != nil {
		log.Fatal(err)
	}
	w, err := newWorker(cfg)
	if err != nil {
		log.Fatal(err)
	}
	interrupt := make(chan os.Signal, 1)
	signal.Notify(interrupt, os.Interrupt)
	defer signal.Stop(interrupt)
	go func() { <-interrupt; w.stopOnce.Do(func() { close(w.stop) }) }()
	if err := w.run(); err != nil {
		log.Fatal(err)
	}
}

func loadConfig(path string) (config, error) {
	var cfg config
	absolute, err := filepath.Abs(path)
	if err != nil {
		return cfg, err
	}
	data, err := os.ReadFile(absolute)
	if err != nil {
		return cfg, fmt.Errorf("config worker tidak dapat dibaca: %w", err)
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return cfg, fmt.Errorf("config worker bukan JSON valid: %w", err)
	}
	if cfg.WorkerAPIKeyEnv == "" || cfg.MasterConfig == "" || cfg.TerminalTemplateDir == "" || cfg.RuntimeDir == "" {
		return cfg, errors.New("config worker wajib memiliki worker_api_key_env, master_config, terminal_template_dir, dan runtime_dir")
	}
	if cfg.ExecutionMode == "" || cfg.ExecutionMode == "auto" {
		cfg.ExecutionMode = "mt5_ea"
	}
	if cfg.ExecutionMode != "mt5_ea" {
		return cfg, errors.New("execution_mode managed worker Go harus mt5_ea")
	}
	if cfg.ExecutionMode == "auto" {
		cfg.ExecutionMode = "mt5_ea"
	}
	if cfg.FeedAddress == "" {
		cfg.FeedAddress = "127.0.0.1:8766"
	}
	feedHost, _, err := net.SplitHostPort(cfg.FeedAddress)
	parsedFeedHost := net.ParseIP(feedHost)
	if err != nil || (feedHost != "localhost" && (parsedFeedHost == nil || !parsedFeedHost.IsLoopback())) {
		return cfg, errors.New("feed_address harus memakai alamat loopback dan port yang valid")
	}
	u, err := url.Parse(cfg.APIBaseURL)
	if err != nil || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "https" && !(u.Scheme == "http" && isLocal(u.Hostname()))) {
		return cfg, errors.New("api_base_url wajib HTTPS (HTTP hanya localhost atau 127.0.0.1)")
	}
	cfg.configDir = filepath.Dir(absolute)
	cfg.apiBase = strings.TrimRight(cfg.APIBaseURL, "/")
	cfg.masterConfig = resolve(cfg.configDir, cfg.MasterConfig)
	cfg.templateDir = resolve(cfg.configDir, cfg.TerminalTemplateDir)
	cfg.runtimeDir = resolve(cfg.configDir, cfg.RuntimeDir)
	cfg.PollSeconds = clamp(cfg.PollSeconds, 1, 60, 5)
	cfg.HeartbeatTimeoutSecond = clamp(cfg.HeartbeatTimeoutSecond, 15, 180, 45)
	cfg.projectRoot = findProjectRoot(absolute)
	if strings.TrimSpace(cfg.ProjectRoot) != "" {
		cfg.projectRoot = resolve(cfg.configDir, cfg.ProjectRoot)
	}
	cfg.apiKey = strings.TrimSpace(os.Getenv(cfg.WorkerAPIKeyEnv))
	cfg.masterToken = os.Getenv("MASTER_PUBLISHER_TOKEN")
	if len(cfg.apiKey) < 32 {
		return cfg, fmt.Errorf("environment variable %s wajib berisi worker key minimal 32 karakter", cfg.WorkerAPIKeyEnv)
	}
	if len(cfg.masterToken) < 32 {
		return cfg, errors.New("MASTER_PUBLISHER_TOKEN wajib tersedia minimal 32 karakter")
	}
	if _, err := os.Stat(filepath.Join(cfg.templateDir, "terminal64.exe")); err != nil {
		return cfg, errors.New("terminal_template_dir harus berisi terminal64.exe")
	}
	var master struct {
		TerminalPath  string `json:"terminal_path"`
		SignalDir     string `json:"folder_sinyal"`
		ListenAddress string `json:"listen_addr"`
	}
	masterData, err := os.ReadFile(cfg.masterConfig)
	if err != nil || json.Unmarshal(masterData, &master) != nil || master.SignalDir == "" {
		return cfg, errors.New("master_config wajib memiliki folder_sinyal")
	}
	if strings.TrimSpace(master.TerminalPath) != "" {
		cfg.masterTerminalPath = resolve(cfg.projectRoot, master.TerminalPath)
	}
	if cfg.MasterSource == "" {
		cfg.MasterSource = "publisher_ea"
	}
	if cfg.MasterSource != "publisher_ea" && cfg.MasterSource != "telegram_signal" {
		return cfg, errors.New("master_source harus publisher_ea atau telegram_signal")
	}
	if cfg.MasterSource == "publisher_ea" && master.TerminalPath == "" {
		return cfg, errors.New("mode publisher_ea memerlukan terminal_path pada master_config")
	}
	if master.ListenAddress == "" {
		master.ListenAddress = "127.0.0.1:8765"
	}
	masterHost, _, err := net.SplitHostPort(master.ListenAddress)
	masterIP := net.ParseIP(masterHost)
	if err != nil || masterIP == nil || !masterIP.IsLoopback() {
		return cfg, errors.New("listen_addr pada master_config wajib menggunakan IP loopback")
	}
	cfg.signalDir = resolve(cfg.projectRoot, master.SignalDir)
	cfg.masterHealthURL = "http://" + master.ListenAddress + "/health"
	cfg.masterSnapshotURL = "http://" + master.ListenAddress + "/snapshot"
	return cfg, nil
}

func newWorker(cfg config) (*worker, error) {
	for _, directory := range []string{
		cfg.signalDir,
		filepath.Join(cfg.runtimeDir, "logs"),
		filepath.Join(cfg.runtimeDir, "states"),
		filepath.Join(cfg.runtimeDir, "reports"),
		filepath.Join(cfg.runtimeDir, "heartbeats"),
		filepath.Join(cfg.runtimeDir, "terminals"),
	} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			return nil, fmt.Errorf("buat direktori runtime %s: %w", directory, err)
		}
	}
	store, err := managed.OpenSnapshotStore(cfg.signalDir, filepath.Join(cfg.runtimeDir, "states", "master-snapshot.json"), 3*time.Second)
	if err != nil {
		return nil, err
	}
	feed, err := managed.NewFeed(store, filepath.Join(cfg.runtimeDir, "reports"), filepath.Join(cfg.runtimeDir, "heartbeats"), cfg.masterHealthURL)
	if err != nil {
		return nil, err
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.MaxIdleConns = 256
	transport.MaxIdleConnsPerHost = 128
	transport.MaxConnsPerHost = 128
	return &worker{
		cfg: cfg, client: &http.Client{Timeout: 20 * time.Second, Transport: transport}, children: map[int64]*child{},
		reporters: map[int64]*child{}, reporterRetry: map[int64]time.Time{}, discoverRetry: map[int64]time.Time{}, captureAttempts: map[int64]int{},
		retryAt: map[int64]time.Time{}, failures: map[int64]int{}, stop: make(chan struct{}), checking: map[int64]bool{},
		masterBinary: findMasterPublisher(cfg.projectRoot),
		feed:         feed,
	}, nil
}

func (w *worker) run() error {
	if err := os.MkdirAll(w.cfg.runtimeDir, 0o700); err != nil {
		return err
	}
	lock, err := lockWorker(w.cfg.runtimeDir)
	if err != nil {
		return err
	}
	defer unlockWorker(lock)
	for _, dir := range []string{"logs", "configs", "terminals", "heartbeats", "reports", "states"} {
		if err := os.MkdirAll(filepath.Join(w.cfg.runtimeDir, dir), 0o700); err != nil {
			return err
		}
	}
	logFile, err := os.OpenFile(filepath.Join(w.cfg.runtimeDir, "logs", "managed_worker.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer logFile.Close()
	log.SetOutput(io.MultiWriter(os.Stdout, logFile))
	listener, err := net.Listen("tcp", w.cfg.FeedAddress)
	if err != nil {
		return fmt.Errorf("listener managed MT5 gagal dibuka pada %s: %w", w.cfg.FeedAddress, err)
	}
	feedServer := &http.Server{Handler: w.feed, ReadHeaderTimeout: 3 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 10 * time.Second}
	feedDone := make(chan error, 1)
	go func() { feedDone <- feedServer.Serve(listener) }()
	defer func() {
		_ = feedServer.Close()
		<-feedDone
	}()
	log.Printf("managed copier Go supervisor dimulai")
	ticker := time.NewTicker(time.Duration(w.cfg.PollSeconds) * time.Second)
	defer ticker.Stop()
	if err := w.syncOnce(); err != nil {
		log.Printf("sinkronisasi awal gagal: %v", err)
	}
	for {
		select {
		case <-w.stop:
			w.shutdown()
			return nil
		case <-ticker.C:
			if err := w.syncOnce(); err != nil {
				log.Printf("sinkronisasi worker gagal: %v", err)
			}
		}
	}
}

func (w *worker) request(endpoint string, payload any, out any) error {
	var body io.Reader
	if payload != nil {
		data, err := json.Marshal(payload)
		if err != nil {
			return errors.New("payload worker tidak valid")
		}
		body = strings.NewReader(string(data))
	}
	method := http.MethodGet
	if payload != nil {
		method = http.MethodPost
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, w.cfg.apiBase+endpoint, body)
	if err != nil {
		return errors.New("URL API worker tidak valid")
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Copier-Worker-Key", w.cfg.apiKey)
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := w.client.Do(req)
	if err != nil {
		return fmt.Errorf("API worker tidak dapat dijangkau (%T)", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("API worker gagal (HTTP %d pada %s)", resp.StatusCode, endpoint)
	}
	if out == nil {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
		return nil
	}
	decoder := json.NewDecoder(io.LimitReader(resp.Body, 4<<20))
	if err := decoder.Decode(out); err != nil {
		return errors.New("respons API worker tidak valid")
	}
	return nil
}

func (w *worker) startMaster() error {
	if w.master != nil {
		select {
		case err := <-w.masterDone:
			log.Printf("publisher master berhenti: %v", err)
			if w.masterLog != nil {
				_ = w.masterLog.Close()
			}
			w.master, w.masterDone = nil, nil
		default:
			return nil
		}
	}
	if time.Now().Before(w.masterRetryAt) {
		return nil
	}
	if _, err := os.Stat(w.masterBinary); err != nil {
		return errors.New("binary publisher master Go tidak ditemukan")
	}
	logFile, err := os.OpenFile(filepath.Join(w.cfg.runtimeDir, "logs", "master-publisher.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	cmd := exec.Command(w.masterBinary, w.cfg.masterConfig)
	cmd.Dir = w.cfg.projectRoot
	cmd.Env = w.cleanEnvironment(true)
	prepareChild(cmd)
	cmd.Stdout, cmd.Stderr = logFile, logFile
	if err := cmd.Start(); err != nil {
		logFile.Close()
		return err
	}
	w.master, w.masterLog = cmd, logFile
	w.masterDone = make(chan error, 1)
	w.masterRetryAt = time.Now().Add(5 * time.Second)
	go func() { w.masterDone <- cmd.Wait() }()
	log.Printf("publisher master Go dimulai pid=%d", cmd.Process.Pid)
	return nil
}

func (w *worker) cleanEnvironment(includeMasterToken bool) []string {
	allowed := make([]string, 0, len(os.Environ()))
	for _, pair := range os.Environ() {
		key, _, _ := strings.Cut(pair, "=")
		upper := strings.ToUpper(key)
		if key == w.cfg.WorkerAPIKeyEnv || strings.Contains(upper, "PASSWORD") || strings.Contains(upper, "PASS_") || strings.Contains(upper, "SECRET") || strings.Contains(upper, "TOKEN") || strings.Contains(upper, "API_KEY") || strings.Contains(upper, "APIKEY") {
			continue
		}
		allowed = append(allowed, pair)
	}
	if includeMasterToken {
		allowed = append(allowed, "MASTER_PUBLISHER_TOKEN="+w.cfg.masterToken)
	}
	return allowed
}

// terminalDirName is the runtime terminal folder for an account. Custom-EA
// accounts get a second, report-only terminal so balance and history still
// reach the portal while the uploaded EA owns the main terminal.
func terminalDirName(id int64, reporter bool) string {
	if reporter {
		return fmt.Sprintf("%d%s", id, reporterDirSuffix)
	}
	return fmt.Sprint(id)
}

func (w *worker) startJob(j job, now time.Time) error {
	return w.launch(j, now, false)
}

func (w *worker) startReporter(j job, now time.Time) error {
	return w.launch(j, now, true)
}

func (w *worker) launch(j job, now time.Time, reporter bool) error {
	configPath, heartbeat, token, err := w.makeJobConfig(j, reporter)
	if err != nil {
		return err
	}
	logName := fmt.Sprintf("managed_%d.log", j.AccountID)
	if reporter {
		logName = fmt.Sprintf("managed_%d_report.log", j.AccountID)
	}
	logFile, err := os.OpenFile(filepath.Join(w.cfg.runtimeDir, "logs", logName), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		w.feed.Unregister(j.AccountID)
		return err
	}
	cmd := exec.Command(filepath.Join(w.cfg.runtimeDir, "terminals", terminalDirName(j.AccountID, reporter), "terminal64.exe"), "/portable", "/config:"+configPath)
	cmd.Dir = filepath.Dir(cmd.Path)
	cmd.Env = w.cleanEnvironment(false)
	prepareChild(cmd)
	cmd.Stdout, cmd.Stderr = logFile, logFile
	if err := cmd.Start(); err != nil {
		logFile.Close()
		w.feed.Unregister(j.AccountID)
		return err
	}
	executor := "mt5_ea"
	if reporter {
		executor = "mt5_reporter"
	} else if strings.EqualFold(j.EAMode, "CUSTOM") {
		executor = "custom_ea"
	}
	c := &child{accountID: j.AccountID, fingerprint: jobFingerprint(j), cmd: cmd, done: make(chan error, 1), stopped: make(chan struct{}), log: logFile, heartbeat: heartbeat, startedAt: now, executor: executor, feedToken: token, startupINI: configPath}
	if reporter {
		c.fingerprint = reporterFingerprint(j)
		w.reporters[j.AccountID] = c
	} else {
		w.children[j.AccountID] = c
	}
	go func() {
		c.done <- cmd.Wait()
		close(c.stopped)
	}()
	go manageMT5Charts(uint32(cmd.Process.Pid), c.stopped, true)
	if c.executor == "mt5_ea" || reporter {
		go w.serveManagedFileBridge(c)
		go w.watchReport(c)
	}
	if !reporter {
		_ = w.report(j.AccountID, "STARTING", "")
	}
	log.Printf("follower managed akun %d (%s) dimulai pid=%d", j.AccountID, executor, cmd.Process.Pid)
	go w.removeStartupConfigAfterLaunch(c)
	return nil
}

func (w *worker) makeJobConfig(j job, reporter bool) (string, string, string, error) {
	if j.AccountID <= 0 || j.BrokerPassword == "" || j.Login == "" || j.Server == "" {
		return "", "", "", errors.New("data akun follower tidak lengkap")
	}
	var token string
	var err error
	customEA := strings.EqualFold(j.EAMode, "CUSTOM") && !reporter
	if !customEA {
		token, err = newFeedToken()
		if err != nil {
			return "", "", "", err
		}
	}
	name := fmt.Sprintf("managed_%d", j.AccountID)
	if reporter {
		name += "_report"
	}
	accountDir := filepath.Join(w.cfg.runtimeDir, "terminals", terminalDirName(j.AccountID, reporter))
	terminal := filepath.Join(accountDir, "terminal64.exe")
	w.killTerminalsIn(accountDir)
	if _, err := os.Stat(terminal); err != nil {
		if err := copyTree(w.cfg.templateDir, accountDir); err != nil {
			w.feed.Unregister(j.AccountID)
			return "", "", "", fmt.Errorf("terminal follower tidak dapat disalin: %w", err)
		}
	}
	if _, err := os.Stat(terminal); err != nil {
		w.feed.Unregister(j.AccountID)
		return "", "", "", errors.New("salinan terminal follower tidak lengkap")
	}
	clearSavedCharts(accountDir)
	if err := enableAutoTrading(accountDir); err != nil {
		w.feed.Unregister(j.AccountID)
		return "", "", "", fmt.Errorf("auto trading terminal gagal disiapkan: %w", err)
	}
	if err := ensureNativeBrokerServers(accountDir, w.cfg.templateDir, w.cfg.masterTerminalPath, j.Server); err != nil {
		w.feed.Unregister(j.AccountID)
		return "", "", "", errors.New("daftar server broker MT5 tidak tersedia pada terminal")
	}
	startupServer := w.loginServerFor(accountDir, j.Server)
	expertName, presetName := "MT5FollowerCopyEA", name+".set"
	if customEA {
		customData, err := w.download(fmt.Sprintf("/api/mt5/copier/accounts/%d/custom-ea", j.AccountID))
		if err != nil {
			return "", "", "", fmt.Errorf("EA custom akun %d tidak dapat diunduh: %w", j.AccountID, err)
		}
		if len(customData) == 0 || len(customData) > 10<<20 {
			return "", "", "", errors.New("ukuran file EA custom tidak valid")
		}
		expertsDir := filepath.Join(accountDir, "MQL5", "Experts", "AlgentraCustom")
		if err := os.MkdirAll(expertsDir, 0o700); err != nil {
			return "", "", "", err
		}
		switch strings.ToLower(filepath.Ext(j.CustomEAName)) {
		case ".mq5":
			sourcePath := filepath.Join(expertsDir, "CustomEA.mq5")
			if err := writeAtomic(sourcePath, customData); err != nil {
				return "", "", "", fmt.Errorf("source EA custom akun %d tidak dapat dipasang: %w", j.AccountID, err)
			}
			if err := compileCustomEA(accountDir, sourcePath); err != nil {
				return "", "", "", fmt.Errorf("EA custom akun %d gagal dikompilasi: %w", j.AccountID, err)
			}
		case ".ex5":
			if err := writeAtomic(filepath.Join(expertsDir, "CustomEA.ex5"), customData); err != nil {
				return "", "", "", fmt.Errorf("EA custom akun %d tidak dapat dipasang: %w", j.AccountID, err)
			}
		default:
			return "", "", "", errors.New("format EA custom harus .mq5 atau .ex5")
		}
		expertName, presetName = "AlgentraCustom\\CustomEA", ""
		if len(j.CustomEAParams) > 0 {
			presetName = name + ".set"
			if err := writeCustomPreset(filepath.Join(accountDir, "MQL5", "Presets", presetName), j.CustomEAParams); err != nil {
				return "", "", "", fmt.Errorf("parameter EA custom akun %d tidak dapat ditulis: %w", j.AccountID, err)
			}
		}
	} else if err := w.prepareManagedEA(j, token, accountDir, name, reporter); err != nil {
		w.feed.Unregister(j.AccountID)
		return "", "", "", err
	}
	startupConfig := filepath.Join(accountDir, "managed-startup.ini")
	startupSymbol := w.knownAccountSymbol(j.AccountID, mustInt(j.Login), accountDir)
	if startupSymbol == "" {
		startupSymbol = "EURUSD"
	}
	if err := writeManagedStartup(startupConfig, j, expertName, presetName, startupServer, startupSymbol); err != nil {
		w.feed.Unregister(j.AccountID)
		return "", "", "", err
	}
	return startupConfig, filepath.Join(w.cfg.runtimeDir, "heartbeats", "heartbeat_"+name+".txt"), token, nil
}

func (w *worker) download(endpoint string) ([]byte, error) {
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, w.cfg.apiBase+endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-Copier-Worker-Key", w.cfg.apiKey)
	resp, err := w.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("worker API HTTP %d", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, (10<<20)+1))
}

const terminalInUseMessage = "File terminal MT5 sedang dipakai proses lain. Tutup terminal MT5 untuk akun ini lalu coba simpan kredensial lagi."

const reporterDirSuffix = "-report"

func (w *worker) syncOnce() error {
	if err := w.startMaster(); err != nil {
		log.Printf("publisher master start gagal: %v", err)
	}
	var response struct {
		Jobs        []job        `json:"jobs"`
		LoginChecks []loginCheck `json:"login_checks"`
		ManagedIDs  []int64      `json:"managed_account_ids"`
	}
	if err := w.request("/api/mt5/copier/jobs", nil, &response); err != nil {
		return err
	}
	if response.Jobs == nil {
		return errors.New("API worker tidak mengembalikan daftar jobs")
	}
	if response.ManagedIDs == nil {
		return errors.New("API worker tidak mengembalikan daftar akun managed yang valid")
	}
	if w.cfg.MasterSource == "telegram_signal" {
		if err := w.bridgeTelegramMasterSnapshot(); err != nil {
			log.Printf("snapshot TelegramSignalEA belum diteruskan ke copier: %v", err)
		}
	}
	for _, id := range response.ManagedIDs {
		if id <= 0 {
			return errors.New("API worker tidak mengembalikan daftar akun managed yang valid")
		}
	}
	for _, check := range response.LoginChecks {
		if check.AccountID <= 0 || !w.beginCheck(check.AccountID) {
			continue
		}
		w.stopChild(check.AccountID, false)
		w.checks.Add(1)
		go w.runLoginCheck(check)
	}
	desired := make(map[int64]job)
	usedBroker := make(map[string]struct{})
	for _, j := range response.Jobs {
		if j.AccountID <= 0 {
			continue
		}
		broker := textutil.Fold(j.Server) + "\x00" + j.Login
		if _, exists := usedBroker[broker]; exists {
			_ = w.report(j.AccountID, "ERROR", "Akun broker duplikat di daftar worker.")
			continue
		}
		usedBroker[broker] = struct{}{}
		desired[j.AccountID] = j
	}
	for id := range w.children {
		if _, ok := desired[id]; !ok {
			w.stopChild(id, true)
		}
	}
	managed := make(map[int64]struct{}, len(response.ManagedIDs))
	for _, id := range response.ManagedIDs {
		managed[id] = struct{}{}
	}
	if entries, err := os.ReadDir(filepath.Join(w.cfg.runtimeDir, "terminals")); err == nil {
		for _, entry := range entries {
			id, parseErr := parsePositive(strings.TrimSuffix(entry.Name(), reporterDirSuffix))
			if entry.IsDir() && parseErr == nil {
				if _, ok := managed[id]; !ok && !w.isChecking(id) {
					if err := w.removeOrphan(id); err != nil {
						log.Printf("runtime orphan akun %d tidak dapat dibersihkan: %v", id, err)
					}
				}
			}
		}
	}
	for id, j := range desired {
		if w.isChecking(id) {
			continue
		}
		current := w.children[id]
		if strings.EqualFold(j.EAMode, "CUSTOM") && j.CustomEAPaused {
			// Paused custom EA: only the report-only terminal keeps running.
			w.stopMainChild(id, false)
			if !j.CustomEAInputsKnown {
				w.startInputDiscovery(j)
			}
			w.ensureReporter(j)
			if w.reporters[id] != nil {
				_ = w.report(id, "RUNNING", "EA kustom dijeda. Atur parameter lalu jalankan dari portal.")
			} else {
				_ = w.report(id, "STARTING", "")
			}
			continue
		}
		sum := jobFingerprint(j)
		if !strings.EqualFold(j.EAMode, "CUSTOM") {
			w.stopReporter(id)
		}
		if current != nil && current.fingerprint != sum {
			w.stopMainChild(id, false)
			current = nil
		}
		if current != nil && current.executor == "custom_ea" && !j.CustomEAInputsKnown && w.captureRunningInputs(j, current) {
			continue
		}
		if current == nil {
			if time.Now().Before(w.retryAt[id]) {
				_ = w.report(id, "ERROR", "Follower gagal mulai; worker sedang menunggu sebelum mencoba lagi.")
				continue
			}
			if err := w.startJob(j, time.Now()); err != nil {
				log.Printf("follower managed akun %d gagal disiapkan: %v", id, err)
				w.retryAt[id] = time.Now().Add(10 * time.Second)
				_ = w.report(id, "ERROR", "Persiapan follower gagal.")
				continue
			}
			current = w.children[id]
		}
		select {
		case err := <-current.done:
			delete(w.children, id)
			if current.executor != "custom_ea" {
				w.feed.Unregister(id)
			}
			w.failures[id]++
			backoff := time.Duration(min(60, 1<<min(w.failures[id], 6))) * time.Second
			w.retryAt[id] = time.Now().Add(backoff)
			current.log.Close()
			_ = w.report(id, "ERROR", "Proses follower berhenti; worker akan mencoba lagi.")
			log.Printf("follower akun %d berhenti: %v", id, err)
			continue
		default:
		}
		if current.executor == "custom_ea" {
			if err := os.WriteFile(current.heartbeat, []byte(time.Now().UTC().Format(time.RFC3339Nano)), 0o600); err != nil {
				_ = w.report(id, "ERROR", "Worker tidak dapat memperbarui status EA kustom.")
				continue
			}
			w.failures[id] = 0
			_ = w.report(id, "RUNNING", "EA kustom berjalan pada terminal sewa.")
			w.ensureReporter(j)
			continue
		}
		info, err := os.Stat(current.heartbeat)
		heartbeatAge := time.Duration(1<<63 - 1)
		if err == nil {
			heartbeatAge = time.Since(info.ModTime())
		}
		if heartbeatAge <= time.Duration(w.cfg.HeartbeatTimeoutSecond)*time.Second {
			w.failures[id] = 0
			_ = w.report(id, "RUNNING", "")
		} else if time.Since(current.startedAt) < time.Duration(w.cfg.HeartbeatTimeoutSecond)*time.Second {
			_ = w.report(id, "STARTING", "")
		} else {
			_ = w.report(id, "ERROR", "Follower belum terhubung ke terminal MT5.")
		}
	}
	return nil
}

func (w *worker) bridgeTelegramMasterSnapshot() error {
	var current struct {
		Active    bool `json:"active"`
		Positions []struct {
			Identifier int64   `json:"identifier"`
			Ticket     int64   `json:"ticket"`
			Symbol     string  `json:"symbol"`
			Type       int     `json:"type"`
			Volume     float64 `json:"volume"`
			OpenPrice  float64 `json:"price_open"`
			StopLoss   float64 `json:"sl"`
			TakeProfit float64 `json:"tp"`
			Comment    string  `json:"comment"`
		} `json:"positions"`
	}
	if err := w.request("/api/mt5/copier/master/snapshot", nil, &current); err != nil {
		return err
	}
	// A stale/disconnected master must never look like a flat account, which
	// would otherwise generate close signals for every follower position.
	if !current.Active {
		return errors.New("snapshot master tidak aktif atau kedaluwarsa")
	}
	payload, err := json.Marshal(map[string]any{"positions": current.Positions})
	if err != nil {
		return errors.New("snapshot master tidak dapat dienkode")
	}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, w.cfg.masterSnapshotURL, bytes.NewReader(payload))
	if err != nil {
		return errors.New("request snapshot lokal tidak valid")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+w.cfg.masterToken)
	resp, err := w.client.Do(req)
	if err != nil {
		return fmt.Errorf("publisher lokal tidak terjangkau: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("publisher lokal menolak snapshot (HTTP %d)", resp.StatusCode)
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	return nil
}

func (w *worker) watchReport(c *child) {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	reportPath := filepath.Join(w.cfg.runtimeDir, "reports", fmt.Sprintf("%d.json", c.accountID))
	lastUploaded := ""
	retryAt := time.Time{}
	// Reports change on every signal/tick; cap uploads so the API is not flooded.
	const minUploadGap = 2 * time.Second
	for {
		select {
		case <-c.stopped:
			return
		case <-ticker.C:
			info, err := os.Stat(reportPath)
			if err != nil || time.Since(info.ModTime()) > time.Duration(w.cfg.HeartbeatTimeoutSecond)*time.Second {
				continue
			}
			version := fmt.Sprintf("%d:%d", info.ModTime().UnixNano(), info.Size())
			if version == lastUploaded || time.Now().Before(retryAt) {
				continue
			}
			if w.syncReport(c.accountID) {
				lastUploaded = version
				retryAt = time.Now().Add(minUploadGap)
			} else {
				retryAt = time.Now().Add(5 * time.Second)
			}
		}
	}
}

func (w *worker) syncReport(id int64) bool {
	path := filepath.Join(w.cfg.runtimeDir, "reports", fmt.Sprintf("%d.json", id))
	info, err := os.Stat(path)
	if err != nil || time.Since(info.ModTime()) > time.Duration(w.cfg.HeartbeatTimeoutSecond)*time.Second {
		return false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	var report map[string]any
	if json.Unmarshal(data, &report) != nil {
		return false
	}
	scan, _ := report["history_scan_msc"]
	delete(report, "history_scan_msc")
	var response map[string]any
	if err := w.request(fmt.Sprintf("/api/mt5/copier/jobs/%d/report", id), report, &response); err != nil {
		log.Printf("laporan akun %d tidak tersinkron: %v", id, err)
		return false
	}
	cursor := map[string]any{"history_cursor": response["history_cursor"], "history_cursor_msc": response["history_cursor_msc"], "scan_msc": scan}
	data, _ = json.Marshal(cursor)
	path = filepath.Join(w.cfg.runtimeDir, "reports", fmt.Sprintf("%d.cursor.json", id))
	if writeAtomic(path, data) != nil {
		log.Printf("cursor histori akun %d tidak dapat disimpan", id)
	}
	return true
}

func (w *worker) report(id int64, status, message string) error {
	var body any = map[string]any{"status": status}
	if message != "" {
		body = map[string]any{"status": status, "message": message}
	}
	return w.request(fmt.Sprintf("/api/mt5/copier/jobs/%d/status", id), body, nil)
}

func (w *worker) stopChild(id int64, report bool) {
	w.stopReporter(id)
	w.stopMainChild(id, report)
}

func (w *worker) stopMainChild(id int64, report bool) {
	c := w.children[id]
	if c == nil {
		return
	}
	delete(w.children, id)
	// A custom EA never registers with the feed; the id may belong to its reporter.
	if c.executor != "custom_ea" {
		w.feed.Unregister(id)
	}
	if c.startupINI != "" {
		defer os.Remove(c.startupINI)
	}
	select {
	case <-c.done:
	default:
		_ = stopChildProcess(c.cmd, c.done, 10*time.Second)
	}
	if c.log != nil {
		_ = c.log.Close()
	}
	if report {
		_ = w.report(id, "STOPPED", "")
	}
}

func (w *worker) stopMaster() {
	if w.master == nil {
		return
	}
	_ = stopChildProcess(w.master, w.masterDone, 5*time.Second)
	if w.masterLog != nil {
		_ = w.masterLog.Close()
	}
	w.master, w.masterDone = nil, nil
}

func (w *worker) shutdown() {
	for id := range w.children {
		w.stopChild(id, true)
	}
	w.stopMaster()
	waited := make(chan struct{})
	go func() { w.checks.Wait(); close(waited) }()
	select {
	case <-waited:
	case <-time.After(30 * time.Second):
	}
	log.Printf("managed copier Go supervisor berhenti")
}

// ensureReporter keeps a report-only terminal (DryRun follower EA, never
// trades) alive next to a custom-EA terminal, so the portal keeps receiving
// balance, equity and deal history for the account.
func (w *worker) ensureReporter(j job) {
	sum := reporterFingerprint(j)
	if c := w.reporters[j.AccountID]; c != nil && c.fingerprint != sum {
		w.stopReporter(j.AccountID)
	}
	if c := w.reporters[j.AccountID]; c != nil {
		select {
		case err := <-c.done:
			delete(w.reporters, j.AccountID)
			w.feed.Unregister(j.AccountID)
			if c.log != nil {
				_ = c.log.Close()
			}
			w.reporterRetry[j.AccountID] = time.Now().Add(15 * time.Second)
			log.Printf("terminal laporan akun %d berhenti: %v", j.AccountID, err)
		default:
			return
		}
	}
	if time.Now().Before(w.reporterRetry[j.AccountID]) {
		return
	}
	if err := w.startReporter(j, time.Now()); err != nil {
		w.reporterRetry[j.AccountID] = time.Now().Add(30 * time.Second)
		log.Printf("terminal laporan akun %d gagal disiapkan: %v", j.AccountID, err)
	}
}

func (w *worker) stopReporter(id int64) {
	c := w.reporters[id]
	if c == nil {
		return
	}
	delete(w.reporters, id)
	delete(w.reporterRetry, id)
	w.feed.Unregister(id)
	if c.startupINI != "" {
		defer os.Remove(c.startupINI)
	}
	select {
	case <-c.done:
	default:
		_ = stopChildProcess(c.cmd, c.done, 10*time.Second)
	}
	if c.log != nil {
		_ = c.log.Close()
	}
}

func (w *worker) removeTerminalDir(name string) error {
	root := filepath.Join(w.cfg.runtimeDir, "terminals")
	dir := filepath.Join(root, name)
	if err := removeWithin(root, dir); err != nil {
		return err
	}
	return os.RemoveAll(dir)
}

func (w *worker) removeOrphan(id int64) error {
	_ = w.removeTerminalDir(terminalDirName(id, true))
	dir := filepath.Join(w.cfg.runtimeDir, "terminals", fmt.Sprint(id))
	if err := removeWithin(filepath.Join(w.cfg.runtimeDir, "terminals"), dir); err != nil {
		return err
	}
	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	for _, item := range [][2]string{
		{"configs", fmt.Sprintf("managed_%d.json", id)}, {"states", fmt.Sprintf("%d.json", id)},
		{"states", fmt.Sprintf("%d.copy-after", id)},
		{"heartbeats", fmt.Sprintf("heartbeat_managed_%d.txt", id)}, {"reports", fmt.Sprintf("%d.json", id)},
		{"reports", fmt.Sprintf("%d.cursor.json", id)}, {"logs", fmt.Sprintf("managed_%d.log", id)},
	} {
		_ = os.Remove(filepath.Join(w.cfg.runtimeDir, item[0], item[1]))
	}
	return nil
}

// jobFingerprint ignores discovery bookkeeping so learning an EA's inputs does
// not restart a running terminal.
func jobFingerprint(j job) string {
	j.CustomEAInputsKnown = false
	return fingerprint(j)
}

// reporterFingerprint only covers what the report-only terminal depends on, so
// EA parameter or pause changes do not restart it.
func reporterFingerprint(j job) string {
	return fingerprint([]any{j.AccountID, j.Login, j.Server, j.BrokerPassword, j.FollowerSymbol})
}

func fingerprint(value any) string {
	data, _ := json.Marshal(value)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
func resolve(base, value string) string {
	if filepath.IsAbs(value) {
		return filepath.Clean(value)
	}
	return filepath.Clean(filepath.Join(base, value))
}
func isLocal(host string) bool {
	return strings.EqualFold(host, "localhost") || host == "127.0.0.1" || host == "::1"
}
func clamp(value, low, high, fallback int) int {
	if value == 0 {
		return fallback
	}
	if value < low {
		return low
	}
	if value > high {
		return high
	}
	return value
}
func findProjectRoot(path string) string {
	dir := filepath.Dir(path)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return filepath.Dir(path)
		}
		dir = parent
	}
}

func findMasterPublisher(root string) string {
	for _, candidate := range []string{
		filepath.Join(root, "algentra-copier-master.exe"),
		filepath.Join(root, "copier-master.exe"),
		filepath.Join(root, "bin", "copier-master.exe"),
	} {
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate
		}
	}
	return filepath.Join(root, "algentra-copier-master.exe")
}
func parsePositive(value string) (int64, error) {
	var id int64
	_, err := fmt.Sscan(value, &id)
	if err != nil || id <= 0 || fmt.Sprint(id) != value {
		return 0, errors.New("ID tidak valid")
	}
	return id, nil
}
func mustInt(value string) int64 { var n int64; _, _ = fmt.Sscan(value, &n); return n }
func limitRunes(value string, limit int) string {
	r := []rune(value)
	if len(r) > limit {
		return string(r[:limit])
	}
	return value
}
func writeAtomic(path string, data []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
func signalDir(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	var value struct {
		Dir string `json:"folder_sinyal"`
	}
	_ = json.Unmarshal(data, &value)
	return value.Dir
}

func (w *worker) beginCheck(id int64) bool {
	w.checkMu.Lock()
	defer w.checkMu.Unlock()
	if w.checking[id] {
		return false
	}
	w.checking[id] = true
	return true
}

func (w *worker) isChecking(id int64) bool {
	w.checkMu.Lock()
	defer w.checkMu.Unlock()
	return w.checking[id]
}

// runLoginCheck berjalan di goroutine sendiri supaya polling job dan heartbeat
// worker tetap jalan selama terminal MT5 diperiksa (bisa sampai 150 detik).
func (w *worker) runLoginCheck(check loginCheck) {
	defer w.checks.Done()
	defer func() {
		w.checkMu.Lock()
		delete(w.checking, check.AccountID)
		w.checkMu.Unlock()
	}()
	valid, message := w.validateLogin(check)
	log.Printf("probe login akun %d selesai: valid=%t pesan=%q", check.AccountID, valid, message)
	for attempt := 0; attempt < 3; attempt++ {
		var ignored any
		err := w.request(fmt.Sprintf("/api/mt5/copier/jobs/%d/login-check", check.AccountID), map[string]any{"valid": valid, "message": message}, &ignored)
		if err == nil {
			return
		}
		log.Printf("hasil pemeriksaan login akun %d tidak terkirim (percobaan %d): %v", check.AccountID, attempt+1, err)
		select {
		case <-w.stop:
			return
		case <-time.After(2 * time.Second):
		}
	}
}
