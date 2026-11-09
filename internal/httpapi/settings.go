package httpapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	dbstore "github.com/rnpproject/algentra-backend/internal/database"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

type settingsAPI struct {
	pool         *dbstore.Pool
	appEnv       string
	eaAPIKey     string
	geminiAPIKey string
	geminiModel  string
}

var symbolNamePattern = regexp.MustCompile(`^[A-Za-z0-9._]{1,32}$`)
var geminiModelPattern = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

func (api settingsAPI) register(mux *http.ServeMux) {
	for _, prefix := range []string{"/api/settings", "/v1/settings"} {
		mux.HandleFunc("GET "+prefix, api.read)
		mux.HandleFunc("GET "+prefix+"/ea-api-key", api.readEAAPIKey)
		mux.HandleFunc("PUT "+prefix, api.update)
		mux.HandleFunc("PUT "+prefix+"/gemini-config", api.updateGemini)
	}
}

func (api settingsAPI) readEAAPIKey(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	if _, ok := requireAdmin(ctx, api.pool, w, r); !ok {
		return
	}
	w.Header().Set("Cache-Control", "private, no-store")
	writeJSON(w, http.StatusOK, map[string]any{
		"configured": strings.TrimSpace(api.eaAPIKey) != "",
		"api_key":    api.eaAPIKey,
	})
}

func (api settingsAPI) read(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	if _, ok := requireAdmin(ctx, api.pool, w, r); !ok {
		return
	}
	rows, err := api.pool.Query(ctx, `SELECT key, value FROM settings ORDER BY key`)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "Settings tidak tersedia")
		return
	}
	defer rows.Close()
	values := make(map[string]any)
	for rows.Next() {
		var key, raw string
		if err := rows.Scan(&key, &raw); err != nil {
			writeError(w, http.StatusServiceUnavailable, "Settings tidak dapat dibaca")
			return
		}
		if key == "registration_auto_open" || hiddenApplicationSetting(key) {
			continue
		}
		var decoded any
		if json.Unmarshal([]byte(raw), &decoded) == nil {
			values[key] = decoded
		}
	}
	if err := rows.Err(); err != nil {
		writeError(w, http.StatusServiceUnavailable, "Settings tidak dapat dibaca")
		return
	}
	open, err := registrationIsOpen(ctx, api.pool)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "Settings tidak tersedia")
		return
	}
	values["entry_mode"] = defaultValue(values["entry_mode"], "PARTIAL")
	values["rr_mode"] = defaultValue(values["rr_mode"], "SIGNAL")
	values["rr_fixed_sl_pips"] = defaultValue(values["rr_fixed_sl_pips"], 200.0)
	values["rr_fixed_tp_pips"] = defaultValue(values["rr_fixed_tp_pips"], 400.0)
	values["rr_reward_multiple"] = defaultValue(values["rr_reward_multiple"], 2.0)
	values["registration_open"] = open
	values["gemini_configured"] = api.geminiAPIKey != ""
	values["gemini_model"] = api.currentGeminiModel()
	writeJSON(w, http.StatusOK, values)
}

func hiddenApplicationSetting(key string) bool {
	switch key {
	case "default_symbol", "max_daily_loss_money", "max_lot", "max_open_trades", "max_signal_age_seconds", "allowed_symbols", "demo_mode":
		return true
	default:
		return false
	}
}

func defaultValue(value any, fallback any) any {
	if value == nil {
		return fallback
	}
	return value
}

func (api settingsAPI) update(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	if _, ok := requireAdmin(ctx, api.pool, w, r); !ok {
		return
	}
	var payload struct {
		Values map[string]any `json:"values"`
	}
	if err := decodeJSON(w, r, &payload, 1<<20); err != nil || payload.Values == nil {
		writeError(w, http.StatusUnprocessableEntity, "Body settings tidak valid")
		return
	}
	if len(payload.Values) > 100 {
		writeError(w, http.StatusUnprocessableEntity, "Maksimal 100 setting per permintaan")
		return
	}
	validated := make(map[string]any, len(payload.Values))
	encoded := make(map[string]string, len(payload.Values))
	for key, value := range payload.Values {
		if strings.TrimSpace(key) == "" || len(key) > 128 {
			writeError(w, http.StatusUnprocessableEntity, "Nama setting harus berisi 1-128 karakter")
			return
		}
		normalized, err := validateSetting(key, value)
		if err != nil {
			writeError(w, http.StatusUnprocessableEntity, err.Error())
			return
		}
		data, err := json.Marshal(normalized)
		if err != nil || len(data) > 10000 {
			writeError(w, http.StatusUnprocessableEntity, fmt.Sprintf("Nilai setting '%s' terlalu besar", key))
			return
		}
		validated[key], encoded[key] = normalized, string(data)
	}
	if err := validateRRConsistency(ctx, api.pool, validated); err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	tx, err := api.pool.Begin(ctx)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "Settings tidak dapat diperbarui")
		return
	}
	defer tx.Rollback(ctx)
	now := time.Now().UTC()
	for key, value := range encoded {
		if _, err := tx.Exec(ctx, `INSERT INTO settings (key,value) VALUES ($1,$2) ON CONFLICT (key) DO UPDATE SET value=EXCLUDED.value`, key, value); err != nil {
			writeError(w, http.StatusServiceUnavailable, "Settings tidak dapat diperbarui")
			return
		}
		if key == "registration_open" {
			autoOpen := !payload.Values[key].(bool) && now.Before(time.Date(2027, time.January, 16, 2, 0, 0, 0, time.UTC))
			autoJSON, _ := json.Marshal(autoOpen)
			if _, err := tx.Exec(ctx, `INSERT INTO settings (key,value) VALUES ('registration_auto_open',$1) ON CONFLICT (key) DO UPDATE SET value=EXCLUDED.value`, string(autoJSON)); err != nil {
				writeError(w, http.StatusServiceUnavailable, "Settings tidak dapat diperbarui")
				return
			}
		}
	}
	if err := tx.Commit(ctx); err != nil {
		writeError(w, http.StatusServiceUnavailable, "Settings tidak dapat diperbarui")
		return
	}
	writeJSON(w, http.StatusOK, validated)
}

func validateSetting(key string, value any) (any, error) {
	switch key {
	case "entry_mode":
		mode, ok := value.(string)
		if !ok || mode != "SINGLE" && mode != "PARTIAL" {
			return nil, errors.New("entry_mode harus SINGLE atau PARTIAL")
		}
		return mode, nil
	case "rr_mode":
		mode, ok := value.(string)
		if !ok || mode != "SIGNAL" && mode != "FIXED" && mode != "BY_SL" {
			return nil, errors.New("rr_mode harus SIGNAL, FIXED, atau BY_SL")
		}
		return mode, nil
	case "allow_updates", "kill_switch", "registration_open":
		boolean, ok := value.(bool)
		if !ok {
			return nil, fmt.Errorf("Setting '%s' harus boolean", key)
		}
		return boolean, nil
	case "confidence_threshold", "max_market_deviation_pct", "rr_fixed_sl_pips", "rr_fixed_tp_pips", "rr_reward_multiple":
		number, ok := value.(float64)
		if !ok || math.IsNaN(number) || math.IsInf(number, 0) {
			return nil, fmt.Errorf("Setting '%s' harus angka valid", key)
		}
		lower, upper := 0.0, 1.0
		switch key {
		case "max_market_deviation_pct":
			lower, upper = 0.1, 100.0
		case "rr_fixed_sl_pips", "rr_fixed_tp_pips":
			lower, upper = 0.1, 100000.0
		case "rr_reward_multiple":
			lower, upper = 1.0, 100.0
		}
		if number < lower || number > upper {
			return nil, fmt.Errorf("Setting '%s' di luar rentang %v sampai %v", key, lower, upper)
		}
		return number, nil
	case "symbol_mapping":
		mapping, ok := value.(map[string]any)
		if !ok || len(mapping) > 100 {
			return nil, errors.New("symbol_mapping harus objek maksimal 100 pasangan")
		}
		normalized := make(map[string]string, len(mapping))
		for from, targetValue := range mapping {
			to, ok := targetValue.(string)
			if !ok || !symbolNamePattern.MatchString(from) || !symbolNamePattern.MatchString(to) {
				return nil, errors.New("Semua symbol mapping harus berupa symbol valid")
			}
			normalized[strings.ToUpper(from)] = strings.ToUpper(to)
		}
		return normalized, nil
	default:
		return nil, fmt.Errorf("Setting '%s' tidak dapat diubah dari dashboard", key)
	}
}

func validateRRConsistency(ctx context.Context, pool *dbstore.Pool, updated map[string]any) error {
	sl, err := currentRRNumber(ctx, pool, "rr_fixed_sl_pips", 200)
	if err != nil {
		return err
	}
	tp, err := currentRRNumber(ctx, pool, "rr_fixed_tp_pips", 400)
	if err != nil {
		return err
	}
	if value, ok := updated["rr_fixed_sl_pips"].(float64); ok {
		sl = value
	}
	if value, ok := updated["rr_fixed_tp_pips"].(float64); ok {
		tp = value
	}
	if tp < sl {
		return errors.New("TP manual harus sama dengan atau lebih jauh dari SL manual")
	}
	return nil
}

func currentRRNumber(ctx context.Context, pool *dbstore.Pool, key string, fallback float64) (float64, error) {
	var raw string
	err := pool.QueryRow(ctx, `SELECT value FROM settings WHERE key=$1`, key).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return fallback, nil
	}
	if err != nil {
		return 0, err
	}
	var value float64
	if json.Unmarshal([]byte(raw), &value) != nil || math.IsNaN(value) || math.IsInf(value, 0) || value <= 0 {
		return fallback, nil
	}
	return value, nil
}

func requireAdmin(ctx context.Context, pool *dbstore.Pool, w http.ResponseWriter, r *http.Request) (string, bool) {
	state := SessionFromRequest(r)
	if state == nil {
		writeError(w, http.StatusUnauthorized, "Login admin diperlukan")
		return "", false
	}
	username, ok := state.Values["admin_username"].(string)
	if !ok || username == "" {
		writeError(w, http.StatusUnauthorized, "Login admin diperlukan")
		return "", false
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead && r.Method != http.MethodOptions && !state.RequireCSRF(r) {
		writeError(w, http.StatusForbidden, "Token CSRF tidak valid atau kedaluwarsa")
		return "", false
	}
	if r.Method == http.MethodGet || r.Method == http.MethodHead || r.Method == http.MethodOptions {
		if _, err := state.CSRFToken(); err != nil {
			writeError(w, http.StatusInternalServerError, "Token keamanan tidak dapat dibuat")
			return "", false
		}
	}
	return username, true
}

func (api settingsAPI) updateGemini(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	if _, ok := requireAdmin(ctx, api.pool, w, r); !ok {
		return
	}
	var payload struct {
		APIKey *string `json:"api_key"`
		Model  *string `json:"model"`
	}
	if err := decodeJSON(w, r, &payload, 16<<10); err != nil || payload.APIKey == nil && payload.Model == nil {
		writeError(w, http.StatusUnprocessableEntity, "Isi api_key atau model")
		return
	}
	if payload.APIKey != nil && (len([]rune(*payload.APIKey)) < 20 || len([]rune(*payload.APIKey)) > 1024) {
		writeError(w, http.StatusUnprocessableEntity, "API key Gemini tidak valid")
		return
	}
	if payload.Model != nil && (len([]rune(*payload.Model)) < 2 || len([]rune(*payload.Model)) > 100 || !geminiModelPattern.MatchString(*payload.Model)) {
		writeError(w, http.StatusUnprocessableEntity, "Nama model Gemini tidak valid")
		return
	}
	if payload.APIKey != nil {
		writeError(w, http.StatusConflict, "Atur GEMINI_API_KEY di environment layanan, lalu mulai ulang aplikasi.")
		return
	}
	if api.appEnv != "development" {
		writeError(w, http.StatusConflict, "Ubah GEMINI_MODEL pada environment Windows Service, lalu mulai ulang aplikasi.")
		return
	}
	model := *payload.Model
	path := os.Getenv("ALGENTRA_ENV_FILE")
	if path == "" {
		path = ".env.local"
	}
	if _, err := os.Stat(path); err != nil {
		writeError(w, http.StatusConflict, "Model Gemini hanya dapat diubah bila file .env.local tersedia")
		return
	}
	if err := setEnvFileValue(path, "GEMINI_MODEL", model); err != nil {
		writeError(w, http.StatusServiceUnavailable, "Model Gemini tidak dapat disimpan")
		return
	}
	if err := os.Setenv("GEMINI_MODEL", model); err != nil {
		writeError(w, http.StatusServiceUnavailable, "Model Gemini tidak dapat diterapkan")
		return
	}
	resultModel := api.currentGeminiModel()
	writeJSON(w, http.StatusOK, map[string]any{"gemini_configured": api.geminiAPIKey != "", "gemini_model": resultModel})
}

func (api settingsAPI) currentGeminiModel() string {
	if current := strings.TrimSpace(os.Getenv("GEMINI_MODEL")); current != "" {
		return current
	}
	return api.geminiModel
}

func setEnvFileValue(path, key, value string) error {
	content, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	lines := strings.Split(strings.TrimSuffix(string(content), "\n"), "\n")
	updated := false
	for index, line := range lines {
		trimmed := strings.TrimSpace(line)
		name, _, found := strings.Cut(trimmed, "=")
		if found && strings.TrimSpace(strings.TrimPrefix(name, "export ")) == key {
			lines[index] = key + `="` + strings.ReplaceAll(value, `"`, `\"`) + `"`
			updated = true
			break
		}
	}
	if !updated {
		lines = append(lines, key+`="`+strings.ReplaceAll(value, `"`, `\"`)+`"`)
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	name := temporary.Name()
	defer os.Remove(name)
	if _, err := temporary.WriteString(strings.Join(lines, "\n") + "\n"); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}
