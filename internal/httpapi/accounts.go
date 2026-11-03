package httpapi

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	dbstore "github.com/rnpproject/algentra-backend/internal/database"
	"io"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/rnpproject/algentra-backend/internal/ea/snapshot"
)

type clientMT5API struct {
	pool             *dbstore.Pool
	state            *snapshot.Store
	pricing          ServicePricing
	workerConfigured bool
	copierAPIKey     string
	appSecret        string
	updates          *mt5UpdateHub
}

func (api clientMT5API) register(mux *http.ServeMux) {
	for _, prefix := range []string{"/api/mt5", "/v1/mt5"} {
		mux.HandleFunc("GET "+prefix+"/accounts", api.listAccounts)
		mux.HandleFunc("GET "+prefix+"/accounts/{account_id}/details", api.accountDetails)
		mux.HandleFunc("GET "+prefix+"/stream", api.accountUpdatesStream)
		mux.HandleFunc("GET "+prefix+"/plans", api.listPlans)
		mux.HandleFunc("GET "+prefix+"/pricing", api.servicePricing)
		mux.HandleFunc("POST "+prefix+"/accounts", api.createAccount)
		mux.HandleFunc("PUT "+prefix+"/accounts/{account_id}/ea-choice", api.setEAChoice)
		mux.HandleFunc("POST "+prefix+"/accounts/{account_id}/custom-ea", api.uploadCustomEA)
		mux.HandleFunc("POST "+prefix+"/accounts/{account_id}/rotate-token", api.rotateToken)
		mux.HandleFunc("PUT "+prefix+"/accounts/{account_id}/active", api.setAccountActive)
		mux.HandleFunc("PUT "+prefix+"/accounts/{account_id}", api.updateAccount)
		mux.HandleFunc("PUT "+prefix+"/accounts/{account_id}/managed-copy", api.configureManagedCopy)
		mux.HandleFunc("PUT "+prefix+"/accounts/{account_id}/managed-copy/active", api.setManagedCopyActive)
		mux.HandleFunc("GET "+prefix+"/performance", api.performance)
		mux.HandleFunc("DELETE "+prefix+"/accounts/{account_id}", api.deleteAccount)
		mux.HandleFunc("GET "+prefix+"/follower/positions", api.followerPositions)
		mux.HandleFunc("POST "+prefix+"/follower/report", api.reportFollower)
	}
}

func (api clientMT5API) listPlans(w http.ResponseWriter, r *http.Request) {
	if !api.authorizeRead(w, r) {
		return
	}
	plans := map[string]map[string]any{
		"RENTAL":  {"name": "Sewa server", "vcpu": "MT5 server", "ram": "—", "disk": "—", "price_idr": 2000},
		"ZERO":    {"name": "ZERO", "vcpu": "1 core", "ram": "1 GB", "disk": "20 GB", "price_idr": 150000},
		"PRO":     {"name": "PRO", "vcpu": "2 core", "ram": "4 GB", "disk": "40 GB", "price_idr": 200000},
		"EXPERT":  {"name": "EXPERT", "vcpu": "4 core", "ram": "8 GB", "disk": "80 GB", "price_idr": 300000},
		"MANAGED": {"name": "Dikelola server", "vcpu": "—", "ram": "—", "disk": "—", "price_idr": api.pricing.ManagementFeeIDR},
		"EA":      {"name": "EA sendiri", "vcpu": "—", "ram": "—", "disk": "—", "price_idr": api.pricing.ManagementFeeIDR / 2},
	}
	for _, vps := range api.pricing.VPSOptions {
		plans["EA_"+vps.Code] = map[string]any{
			"name": "EA sendiri + " + vps.Name, "vcpu": vps.VCPU, "ram": vps.RAM, "disk": vps.Disk,
			"price_idr": api.pricing.ManagementFeeIDR/2 + vps.PriceIDR,
		}
	}
	writeJSON(w, http.StatusOK, plans)
}

func (api clientMT5API) servicePricing(w http.ResponseWriter, r *http.Request) {
	if !api.authorizeRead(w, r) {
		return
	}
	vps := make([]map[string]any, 0, len(api.pricing.VPSOptions))
	for _, item := range api.pricing.VPSOptions {
		vps = append(vps, map[string]any{"code": item.Code, "name": item.Name, "vcpu": item.VCPU, "ram": item.RAM, "disk": item.Disk, "price_idr": item.PriceIDR})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"daily_rate_idr": 2000, "minimum_rental_days": 10,
		"management_fee_idr":   api.pricing.ManagementFeeIDR,
		"copy_trading_fee_idr": api.pricing.ManagementFeeIDR / 2,
		"vps_options":          vps,
	})
}

func (api clientMT5API) authorizeRead(w http.ResponseWriter, r *http.Request) bool {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	if _, ok := requireClient(ctx, api.pool, w, r); !ok {
		return false
	}
	state := SessionFromRequest(r)
	if _, err := state.CSRFToken(); err != nil {
		writeError(w, http.StatusInternalServerError, "Token keamanan tidak dapat dibuat")
		return false
	}
	return true
}

func (api clientMT5API) deleteAccount(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	ownerID, ok := requireClient(ctx, api.pool, w, r)
	if !ok {
		return
	}
	accountID, err := strconv.ParseInt(r.PathValue("account_id"), 10, 64)
	if err != nil || accountID <= 0 {
		writeError(w, http.StatusNotFound, "Akun MT5 tidak ditemukan")
		return
	}
	tx, err := api.pool.Begin(ctx)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "Akun MT5 tidak tersedia")
		return
	}
	defer tx.Rollback(ctx)
	var mode string
	err = tx.QueryRow(ctx, `SELECT execution_mode FROM mt5_accounts WHERE id = $1 AND owner_id = $2 FOR UPDATE`, accountID, ownerID).Scan(&mode)
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "Akun MT5 tidak ditemukan")
		return
	}
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "Akun MT5 tidak tersedia")
		return
	}
	if mode == "MANAGED" {
		var active bool
		err = tx.QueryRow(ctx, `SELECT active FROM managed_copy_profiles WHERE account_id = $1`, accountID).Scan(&active)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			writeError(w, http.StatusServiceUnavailable, "Status managed copy tidak tersedia")
			return
		}
		if active {
			writeError(w, http.StatusConflict, "Jeda copy trading sebelum menghapus akun")
			return
		}
	}
	if _, err = tx.Exec(ctx, `DELETE FROM mt5_history_deals WHERE account_id = $1`, accountID); err != nil {
		writeError(w, http.StatusServiceUnavailable, "Riwayat akun tidak dapat dihapus")
		return
	}
	if _, err = tx.Exec(ctx, `DELETE FROM mt5_account_state WHERE account_id = $1`, accountID); err != nil {
		writeError(w, http.StatusServiceUnavailable, "Status akun tidak dapat dihapus")
		return
	}
	if _, err = tx.Exec(ctx, `DELETE FROM mt5_accounts WHERE id = $1 AND owner_id = $2`, accountID, ownerID); err != nil {
		writeError(w, http.StatusServiceUnavailable, "Akun MT5 tidak dapat dihapus")
		return
	}
	if err := tx.Commit(ctx); err != nil {
		writeError(w, http.StatusServiceUnavailable, "Akun MT5 tidak dapat dihapus")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "deleted", "id": accountID})
}

func (api clientMT5API) rotateToken(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	ownerID, ok := requireClient(ctx, api.pool, w, r)
	if !ok {
		return
	}
	accountID, err := strconv.ParseInt(r.PathValue("account_id"), 10, 64)
	if err != nil || accountID <= 0 {
		writeError(w, http.StatusNotFound, "Akun MT5 tidak ditemukan")
		return
	}
	var mode string
	err = api.pool.QueryRow(ctx, `SELECT execution_mode FROM mt5_accounts WHERE id = $1 AND owner_id = $2`, accountID, ownerID).Scan(&mode)
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "Akun MT5 tidak ditemukan")
		return
	}
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "Akun MT5 tidak tersedia")
		return
	}
	if mode == "MANAGED" {
		writeError(w, http.StatusConflict, "Akun managed copy tidak memakai token EA")
		return
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		writeError(w, http.StatusInternalServerError, "Token akun tidak dapat dibuat")
		return
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	hash := sha256.Sum256([]byte(token))
	if _, err := api.pool.Exec(ctx, `UPDATE mt5_accounts SET token_hash = $3, last_seen_at = NULL WHERE id = $1 AND owner_id = $2`, accountID, ownerID, hex.EncodeToString(hash[:])); err != nil {
		writeError(w, http.StatusServiceUnavailable, "Token akun tidak dapat diperbarui")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"token": token})
}

func (api clientMT5API) listAccounts(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	ownerID, ok := requireClient(ctx, api.pool, w, r)
	if !ok {
		return
	}
	state := SessionFromRequest(r)
	if _, err := state.CSRFToken(); err != nil {
		writeError(w, http.StatusInternalServerError, "Token keamanan tidak dapat dibuat")
		return
	}
	now := time.Now().UTC()
	latest, hasSnapshot := api.state.Current()
	masterOnline := hasSnapshot && latest.Account != nil && now.Sub(latest.ObservedAt) <= 20*time.Second && now.Sub(latest.Account.ObservedAt) <= 30*time.Second
	rows, err := api.pool.Query(ctx, `
		SELECT a.id, a.label, a.server, a.login, a.plan, a.execution_mode, a.active, a.last_seen_at, a.created_at,
		       a.rental_expires_at, a.ea_mode, a.custom_ea_name,
		       s.balance, s.equity, s.floating_profit, s.currency, s.trade_mode, s.observed_at,
		       s.terminal_trade_allowed, s.expert_trade_allowed,
	       p.account_id, p.login_status, p.login_message, p.follower_symbol, p.active, p.status,
		       p.status_message, p.last_seen_at, p.updated_at, p.mode_lot, p.ratio_lot,
		       p.lot_tetap, p.max_lot_per_order, p.max_lot_total, p.max_open_positions
		FROM mt5_accounts a
		LEFT JOIN mt5_account_state s ON s.account_id = a.id
		LEFT JOIN managed_copy_profiles p ON p.account_id = a.id
		WHERE a.owner_id = $1 ORDER BY a.id ASC
	`, ownerID)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "Daftar akun MT5 tidak tersedia")
		return
	}
	defer rows.Close()
	result := make([]map[string]any, 0)
	wib := time.FixedZone("WIB", 7*60*60)
	for rows.Next() {
		var id int64
		var label, server, login, plan, mode string
		var active bool
		var lastSeen, createdAt, stateObserved sql.NullTime
		var rentalExpires sql.NullTime
		var eaMode, customEAName sql.NullString
		var balance, equity, floating sql.NullFloat64
		var currency, tradeMode sql.NullString
		var terminalAllowed, expertAllowed sql.NullBool
		var profileID sql.NullInt64
		var loginStatus, loginMessage, followerSymbol, profileStatus, statusMessage sql.NullString
		var profileActive sql.NullBool
		var profileSeen, profileUpdated sql.NullTime
		var lotMode sql.NullString
		var ratio, fixedLot, maxOrder, maxTotal sql.NullFloat64
		var maxPositions sql.NullInt64
		if err := rows.Scan(&id, &label, &server, &login, &plan, &mode, &active, &lastSeen, &createdAt, &rentalExpires, &eaMode, &customEAName,
			&balance, &equity, &floating, &currency, &tradeMode, &stateObserved, &terminalAllowed, &expertAllowed,
			&profileID, &loginStatus, &loginMessage, &followerSymbol, &profileActive, &profileStatus,
			&statusMessage, &profileSeen, &profileUpdated, &lotMode, &ratio, &fixedLot, &maxOrder, &maxTotal, &maxPositions); err != nil {
			writeError(w, http.StatusServiceUnavailable, "Daftar akun MT5 tidak dapat dibaca")
			return
		}
		if rentalExpires.Valid && !rentalExpires.Time.After(now) {
			active = false
		}
		followerOnline := active && lastSeen.Valid && now.Sub(lastSeen.Time) <= 30*time.Second
		stateOnline := stateObserved.Valid && now.Sub(stateObserved.Time) <= 30*time.Second
		connection := "ready"
		switch {
		case !active:
			connection = "disabled"
		case !followerOnline || !stateOnline:
			connection = "offline"
		case !terminalAllowed.Bool || !expertAllowed.Bool:
			connection = "trading_disabled"
		case !masterOnline:
			connection = "waiting_for_source"
		}
		payload := map[string]any{
			"id": id, "label": label, "server": server, "login": login, "plan": plan,
			"execution_mode": mode, "role": "follower", "active": active,
			"last_seen_at": nullableWIB(lastSeen, wib), "connection_status": connection,
			"rental_expires_at": nullableWIB(rentalExpires, wib), "ea_mode": nullableString(eaMode), "custom_ea_name": nullableString(customEAName),
			"balance": nullableFloat(balance), "equity": nullableFloat(equity), "floating_profit": nullableFloat(floating),
			"currency": nullableString(currency), "trade_mode": nullableString(tradeMode),
			"state_updated_at": nullableWIB(stateObserved, wib), "created_at": nullableWIB(createdAt, wib),
		}
		if !profileID.Valid {
			payload["managed_copy"] = map[string]any{"configured": false, "active": false, "status": "SETUP_REQUIRED"}
		} else {
			managedStatus := profileStatus.String
			activeReference := profileUpdated.Time
			if profileSeen.Valid {
				activeReference = profileSeen.Time
			}
			if profileActive.Bool && now.Sub(activeReference) > 30*time.Second {
				managedStatus = "OFFLINE"
			}
			payload["managed_copy"] = map[string]any{
				"configured": true, "login_status": loginStatus.String, "login_message": nullableString(loginMessage),
				"follower_symbol": followerSymbol.String, "active": profileActive.Bool, "status": managedStatus,
				"status_message": nullableString(statusMessage), "last_seen_at": nullableWIB(profileSeen, wib),
				"mode_lot": lotMode.String, "ratio_lot": ratio.Float64, "lot_tetap": fixedLot.Float64,
				"max_lot_per_order": maxOrder.Float64, "max_lot_total": maxTotal.Float64,
				"max_open_positions": maxPositions.Int64,
			}
		}
		result = append(result, payload)
	}
	if err := rows.Err(); err != nil {
		writeError(w, http.StatusServiceUnavailable, "Daftar akun MT5 tidak dapat dibaca")
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (api clientMT5API) createAccount(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	if _, ok := requireClient(ctx, api.pool, w, r); !ok {
		return
	}
	writeError(w, http.StatusConflict, "Akun dibuat setelah admin menyetujui pembayaran. Buat pesanan melalui alur pembayaran.")
}

func (api clientMT5API) setEAChoice(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	ownerID, ok := requireClient(ctx, api.pool, w, r)
	if !ok {
		return
	}
	accountID, err := strconv.ParseInt(r.PathValue("account_id"), 10, 64)
	if err != nil || accountID <= 0 {
		writeError(w, http.StatusNotFound, "Akun MT5 tidak ditemukan")
		return
	}
	var payload struct {
		Mode string `json:"mode"`
	}
	if err = decodeJSON(w, r, &payload, 8<<10); err != nil || payload.Mode != "SERVER" {
		writeError(w, http.StatusUnprocessableEntity, "Pilih EA yang disediakan server atau unggah EA sendiri.")
		return
	}
	result, err := api.pool.Exec(ctx, `UPDATE mt5_accounts SET ea_mode='SERVER',custom_ea_name=NULL,custom_ea_data=NULL,custom_ea_updated_at=NULL WHERE id=$1 AND owner_id=$2`, accountID, ownerID)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "Pilihan EA tidak dapat disimpan")
		return
	}
	if result.RowsAffected() == 0 {
		writeError(w, http.StatusNotFound, "Akun MT5 tidak ditemukan")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "saved", "ea_mode": "SERVER"})
}

const maxUploadedEABytes = 10 << 20

func (api clientMT5API) uploadCustomEA(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	ownerID, ok := requireClient(ctx, api.pool, w, r)
	if !ok {
		return
	}
	accountID, err := strconv.ParseInt(r.PathValue("account_id"), 10, 64)
	if err != nil || accountID <= 0 {
		writeError(w, http.StatusNotFound, "Akun MT5 tidak ditemukan")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxUploadedEABytes+(1<<20))
	if err := r.ParseMultipartForm(maxUploadedEABytes); err != nil {
		writeError(w, http.StatusBadRequest, "File EA tidak valid atau melebihi batas 10 MB")
		return
	}
	file, header, err := r.FormFile("ea")
	if err != nil {
		writeError(w, http.StatusBadRequest, "Pilih file EA .mq5 atau .ex5")
		return
	}
	defer file.Close()
	name := filepath.Base(strings.ReplaceAll(header.Filename, `\`, "/"))
	ext := strings.ToLower(filepath.Ext(name))
	if len(name) < 5 || (ext != ".ex5" && ext != ".mq5") || strings.ContainsAny(name, "\r\n") {
		writeError(w, http.StatusUnprocessableEntity, "Format EA harus berupa .mq5 atau .ex5")
		return
	}
	data, err := io.ReadAll(io.LimitReader(file, maxUploadedEABytes+1))
	if err != nil || len(data) == 0 || len(data) > maxUploadedEABytes {
		writeError(w, http.StatusBadRequest, "File EA kosong, tidak dapat dibaca, atau melebihi batas 10 MB")
		return
	}
	result, err := api.pool.Exec(ctx, `UPDATE mt5_accounts SET ea_mode='CUSTOM',custom_ea_name=$3,custom_ea_data=$4,custom_ea_updated_at=$5 WHERE id=$1 AND owner_id=$2`, accountID, ownerID, name, data, time.Now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "File EA tidak dapat disimpan")
		return
	}
	if result.RowsAffected() == 0 {
		writeError(w, http.StatusNotFound, "Akun MT5 tidak ditemukan")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "uploaded", "ea_mode": "CUSTOM", "custom_ea_name": name})
}

func requireClient(ctx context.Context, pool *dbstore.Pool, w http.ResponseWriter, r *http.Request) (int64, bool) {
	ownerID, ok := ClientIDFromRequest(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "Login pengguna diperlukan")
		return 0, false
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead && r.Method != http.MethodOptions {
		if state := SessionFromRequest(r); state == nil || !state.RequireCSRF(r) {
			writeError(w, http.StatusForbidden, "Token CSRF tidak valid atau kedaluwarsa")
			return 0, false
		}
	}
	var exists bool
	if err := pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM client_users WHERE id = $1)`, ownerID).Scan(&exists); err != nil {
		writeError(w, http.StatusServiceUnavailable, "Autentikasi tidak tersedia")
		return 0, false
	}
	if !exists {
		writeError(w, http.StatusUnauthorized, "Login pengguna diperlukan")
		return 0, false
	}
	return ownerID, true
}

func nullableString(value sql.NullString) any {
	if !value.Valid {
		return nil
	}
	return value.String
}

func nullableFloat(value sql.NullFloat64) any {
	if !value.Valid {
		return nil
	}
	return value.Float64
}

func nullableWIB(value sql.NullTime, zone *time.Location) any {
	if !value.Valid {
		return nil
	}
	return value.Time.In(zone).Format(time.RFC3339Nano)
}
