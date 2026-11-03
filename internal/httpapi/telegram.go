package httpapi

import (
	"context"
	"errors"
	"fmt"
	dbstore "github.com/rnpproject/algentra-backend/internal/database"
	"net/http"
	"strings"
	"time"

	gotdAuth "github.com/gotd/td/telegram/auth"
	"github.com/gotd/td/tgerr"
	telegramclient "github.com/rnpproject/algentra-backend/internal/telegram"
)

type telegramAPI struct {
	pool    *dbstore.Pool
	manager *telegramclient.Manager
	parser  parserAPI
}

func (api telegramAPI) register(mux *http.ServeMux) {
	if api.manager != nil {
		api.manager.SetMessageHandler(api.processMessage)
	}
	for _, prefix := range []string{"/api/telegram", "/v1/telegram"} {
		mux.HandleFunc("GET "+prefix+"/status", api.status)
		mux.HandleFunc("POST "+prefix+"/send-code", api.sendCode)
		mux.HandleFunc("POST "+prefix+"/verify-code", api.verifyCode)
		mux.HandleFunc("POST "+prefix+"/verify-2fa", api.verify2FA)
		mux.HandleFunc("POST "+prefix+"/logout", api.logout)
		mux.HandleFunc("POST "+prefix+"/reconnect", api.reconnect)
		mux.HandleFunc("GET "+prefix+"/groups", api.groups)
		mux.HandleFunc("POST "+prefix+"/groups/select", api.selectGroups)
	}
}

func (api telegramAPI) authorize(w http.ResponseWriter, r *http.Request) bool {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	_, ok := requireAdmin(ctx, api.pool, w, r)
	return ok
}

func (api telegramAPI) status(w http.ResponseWriter, r *http.Request) {
	if !api.authorize(w, r) {
		return
	}
	writeJSON(w, http.StatusOK, api.manager.Status(r.Context()))
}

func (api telegramAPI) sendCode(w http.ResponseWriter, r *http.Request) {
	if !api.authorize(w, r) {
		return
	}
	var payload struct {
		Phone string `json:"phone"`
	}
	if err := decodeJSON(w, r, &payload, 8<<10); err != nil || len([]rune(payload.Phone)) < 7 || len([]rune(payload.Phone)) > 24 {
		writeError(w, http.StatusUnprocessableEntity, "Data nomor telepon tidak valid")
		return
	}
	result, err := api.manager.SendCode(r.Context(), payload.Phone)
	if err != nil {
		api.writeTelegramError(w, err, "Gagal meminta kode Telegram", http.StatusBadGateway)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (api telegramAPI) verifyCode(w http.ResponseWriter, r *http.Request) {
	if !api.authorize(w, r) {
		return
	}
	var payload struct {
		Code string `json:"code"`
	}
	if err := decodeJSON(w, r, &payload, 8<<10); err != nil || len([]rune(payload.Code)) < 3 || len([]rune(payload.Code)) > 12 {
		writeError(w, http.StatusUnprocessableEntity, "Kode Telegram tidak valid")
		return
	}
	result, err := api.manager.VerifyCode(r.Context(), payload.Code)
	if err != nil {
		if rpc, ok := tgerr.As(err); ok && (rpc.Type == "PHONE_CODE_EXPIRED" || rpc.Type == "PHONE_CODE_INVALID") {
			writeError(w, http.StatusUnprocessableEntity, "Kode Telegram tidak valid atau sudah kedaluwarsa")
			return
		}
		if errors.Is(err, gotdAuth.ErrPasswordAuthNeeded) {
			writeJSON(w, http.StatusOK, map[string]any{"status": "two_factor_required", "requires_2fa": true})
			return
		}
		api.writeTelegramError(w, err, "Verifikasi Telegram gagal", http.StatusBadGateway)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (api telegramAPI) verify2FA(w http.ResponseWriter, r *http.Request) {
	if !api.authorize(w, r) {
		return
	}
	var payload struct {
		Password string `json:"password"`
	}
	if err := decodeJSON(w, r, &payload, 4<<10); err != nil || strings.TrimSpace(payload.Password) == "" || len([]rune(payload.Password)) > 512 {
		writeError(w, http.StatusUnprocessableEntity, "Password 2FA tidak valid")
		return
	}
	result, err := api.manager.Verify2FA(r.Context(), payload.Password)
	if err != nil {
		api.writeTelegramError(w, err, "Password 2FA Telegram gagal", http.StatusBadGateway)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (api telegramAPI) logout(w http.ResponseWriter, r *http.Request) {
	if !api.authorize(w, r) {
		return
	}
	result, err := api.manager.Logout(r.Context())
	if err != nil {
		api.writeTelegramError(w, err, "Logout Telegram gagal", http.StatusBadGateway)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (api telegramAPI) reconnect(w http.ResponseWriter, r *http.Request) {
	if !api.authorize(w, r) {
		return
	}
	result, err := api.manager.Reconnect(r.Context())
	if err != nil {
		api.writeTelegramError(w, err, "Reconnect Telegram gagal", http.StatusBadGateway)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (api telegramAPI) groups(w http.ResponseWriter, r *http.Request) {
	if !api.authorize(w, r) {
		return
	}
	available, err := api.manager.Groups(r.Context())
	if err != nil {
		api.writeTelegramError(w, err, "Telegram belum terhubung", http.StatusConflict)
		return
	}
	rows, err := api.pool.Query(r.Context(), `SELECT chat_id,enabled,alias FROM groups`)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "Pilihan grup Telegram tidak tersedia")
		return
	}
	saved := make(map[string]struct {
		enabled bool
		alias   *string
	})
	for rows.Next() {
		var id string
		var item struct {
			enabled bool
			alias   *string
		}
		if err := rows.Scan(&id, &item.enabled, &item.alias); err != nil {
			rows.Close()
			writeError(w, http.StatusServiceUnavailable, "Pilihan grup Telegram tidak tersedia")
			return
		}
		saved[id] = item
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		writeError(w, http.StatusServiceUnavailable, "Pilihan grup Telegram tidak tersedia")
		return
	}
	rows.Close()
	availableGroupResults := make([]map[string]any, 0, len(available))
	for _, group := range available {
		id := group["chat_id"].(string)
		item := map[string]any{"chat_id": id, "name": group["name"], "username": group["username"], "enabled": false, "alias": nil}
		if value, ok := saved[id]; ok {
			item["enabled"], item["alias"] = value.enabled, value.alias
		}
		availableGroupResults = append(availableGroupResults, item)
	}
	writeJSON(w, http.StatusOK, availableGroupResults)
}

type telegramGroupSelection struct {
	ChatID  string  `json:"chat_id"`
	Enabled bool    `json:"enabled"`
	Alias   *string `json:"alias"`
}

func (api telegramAPI) selectGroups(w http.ResponseWriter, r *http.Request) {
	if !api.authorize(w, r) {
		return
	}
	var payload struct {
		Groups []telegramGroupSelection `json:"groups"`
	}
	if err := decodeJSON(w, r, &payload, 256<<10); err != nil || len(payload.Groups) > 1000 {
		writeError(w, http.StatusUnprocessableEntity, "Pilihan grup Telegram tidak valid")
		return
	}
	available, err := api.manager.Groups(r.Context())
	if err != nil {
		api.writeTelegramError(w, err, "Telegram belum terhubung", http.StatusConflict)
		return
	}
	availableByID := make(map[string]map[string]any, len(available))
	for _, group := range available {
		availableByID[group["chat_id"].(string)] = group
	}
	ids := make([]string, 0, len(payload.Groups))
	seen := make(map[string]struct{}, len(payload.Groups))
	for _, selection := range payload.Groups {
		if selection.ChatID == "" || len(selection.ChatID) > 128 {
			writeError(w, http.StatusUnprocessableEntity, "chat_id tidak valid")
			return
		}
		if _, duplicate := seen[selection.ChatID]; duplicate {
			writeError(w, http.StatusUnprocessableEntity, "chat_id tidak boleh duplikat")
			return
		}
		if _, found := availableByID[selection.ChatID]; !found {
			writeError(w, http.StatusUnprocessableEntity, "Grup harus dipilih dari daftar akun Telegram")
			return
		}
		if selection.Alias != nil && len([]rune(*selection.Alias)) > 255 {
			writeError(w, http.StatusUnprocessableEntity, "alias maksimal 255 karakter")
			return
		}
		seen[selection.ChatID] = struct{}{}
		ids = append(ids, selection.ChatID)
	}
	tx, err := api.pool.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "Pilihan grup Telegram tidak dapat disimpan")
		return
	}
	defer tx.Rollback(r.Context())
	var disabled int64
	if len(ids) == 0 {
		tag, execErr := tx.Exec(r.Context(), `UPDATE groups SET enabled=FALSE WHERE enabled=TRUE`)
		err = execErr
		disabled = tag.RowsAffected()
	} else {
		tag, execErr := tx.Exec(r.Context(), `UPDATE groups SET enabled=FALSE WHERE enabled=TRUE AND NOT (chat_id = ANY($1::text[]))`, ids)
		err = execErr
		disabled = tag.RowsAffected()
	}
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "Pilihan grup Telegram tidak dapat disimpan")
		return
	}
	for _, selection := range payload.Groups {
		group := availableByID[selection.ChatID]
		var alias any
		if selection.Alias != nil && strings.TrimSpace(*selection.Alias) != "" {
			alias = strings.TrimSpace(*selection.Alias)
		}
		_, err = tx.Exec(r.Context(), `INSERT INTO groups(chat_id,name,alias,enabled) VALUES($1,$2,$3,$4) ON CONFLICT(chat_id) DO UPDATE SET name=EXCLUDED.name,alias=EXCLUDED.alias,enabled=EXCLUDED.enabled`, selection.ChatID, group["name"], alias, selection.Enabled)
		if err != nil {
			writeError(w, http.StatusServiceUnavailable, "Pilihan grup Telegram tidak dapat disimpan")
			return
		}
	}
	if err = tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusServiceUnavailable, "Pilihan grup Telegram tidak dapat disimpan")
		return
	}
	writeJSON(w, http.StatusOK, map[string]int64{"saved": int64(len(payload.Groups)), "disabled": disabled})
}

func (api telegramAPI) writeTelegramError(w http.ResponseWriter, err error, message string, fallback int) {
	if errors.Is(err, telegramclient.ErrInvalidPhone) {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	if errors.Is(err, telegramclient.ErrCodeNotRequested) || errors.Is(err, telegramclient.Err2FAState) {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	if errors.Is(err, telegramclient.ErrNotConfigured) {
		writeError(w, http.StatusBadGateway, "TELEGRAM_API_ID dan TELEGRAM_API_HASH belum dikonfigurasi")
		return
	}
	if wait, ok := tgerr.AsFloodWait(err); ok {
		writeError(w, http.StatusTooManyRequests, fmt.Sprintf("Telegram meminta tunggu %d detik", int(wait.Seconds())))
		return
	}
	if errors.Is(err, telegramclient.ErrNotConnected) {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	if fallback == http.StatusUnprocessableEntity || fallback == http.StatusConflict {
		writeError(w, fallback, err.Error())
		return
	}
	writeError(w, fallback, fmt.Sprintf("%s (%T)", message, err))
}
