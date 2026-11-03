package httpapi

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	dbstore "github.com/rnpproject/algentra-backend/internal/database"
	"math"
	"math/big"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/rnpproject/algentra-backend/internal/ea/snapshot"
	"github.com/rnpproject/algentra-backend/internal/security"
)

type copierAPI struct {
	pool    *dbstore.Pool
	apiKey  string
	secret  string
	master  *snapshot.Store
	updates *mt5UpdateHub
}

type copierJob struct {
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
}

type loginCheckJob struct {
	AccountID      int64  `json:"account_id"`
	Login          string `json:"login"`
	Server         string `json:"server"`
	BrokerPassword string `json:"broker_password"`
}

type followerReport struct {
	Balance              float64        `json:"balance"`
	Equity               float64        `json:"equity"`
	FloatingProfit       float64        `json:"floating_profit"`
	Margin               float64        `json:"margin"`
	FreeMargin           float64        `json:"free_margin"`
	Currency             string         `json:"currency"`
	TradeMode            string         `json:"trade_mode"`
	AllowLiveTrading     *bool          `json:"allow_live_trading"`
	TerminalTradeAllowed *bool          `json:"terminal_trade_allowed"`
	ExpertTradeAllowed   *bool          `json:"expert_trade_allowed"`
	OpenPositionIDs      []string       `json:"open_position_ids"`
	Deals                []followerDeal `json:"deals"`
}

type followerDeal struct {
	DealTicket string  `json:"deal_ticket"`
	PositionID string  `json:"position_id"`
	TimeMSC    int64   `json:"time_msc"`
	Symbol     string  `json:"symbol"`
	Action     string  `json:"action"`
	Entry      string  `json:"entry"`
	Volume     float64 `json:"volume"`
	Price      float64 `json:"price"`
	Profit     float64 `json:"profit"`
	Commission float64 `json:"commission"`
	Swap       float64 `json:"swap"`
	Fee        float64 `json:"fee"`
}

func (p *followerReport) UnmarshalJSON(data []byte) error {
	if err := requireJSONFields(data,
		"balance", "equity", "floating_profit", "margin", "free_margin", "currency", "trade_mode",
		"allow_live_trading", "terminal_trade_allowed", "expert_trade_allowed", "open_position_ids", "deals",
	); err != nil {
		return err
	}
	type reportAlias followerReport
	var decoded reportAlias
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	*p = followerReport(decoded)
	return nil
}

func (d *followerDeal) UnmarshalJSON(data []byte) error {
	if err := requireJSONFields(data,
		"deal_ticket", "position_id", "time_msc", "symbol", "action", "entry", "volume", "price", "profit", "commission", "swap", "fee",
	); err != nil {
		return err
	}
	type dealAlias followerDeal
	var decoded dealAlias
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	*d = followerDeal(decoded)
	return nil
}

func requireJSONFields(data []byte, names ...string) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	for _, name := range names {
		value, exists := fields[name]
		if !exists || strings.TrimSpace(string(value)) == "null" {
			return fmt.Errorf("field %s wajib diisi", name)
		}
	}
	return nil
}

func (api copierAPI) register(mux *http.ServeMux, prefix string) {
	mux.HandleFunc("GET "+prefix+"/copier/health", api.health)
	mux.HandleFunc("GET "+prefix+"/copier/status", api.serviceStatus)
	mux.HandleFunc("GET "+prefix+"/copier/jobs", api.jobs)
	mux.HandleFunc("GET "+prefix+"/copier/master/snapshot", api.masterSnapshot)
	mux.HandleFunc("POST "+prefix+"/copier/jobs/{account_id}/login-check", api.reportLoginCheck)
	mux.HandleFunc("POST "+prefix+"/copier/jobs/{account_id}/status", api.reportStatus)
	mux.HandleFunc("POST "+prefix+"/copier/jobs/{account_id}/report", api.reportAccount)
	mux.HandleFunc("GET "+prefix+"/copier/accounts/{account_id}/custom-ea", api.customEA)
}

func (api copierAPI) customEA(w http.ResponseWriter, r *http.Request) {
	if !api.requireWorker(w, r) {
		return
	}
	noStore(w)
	accountID, ok := pathAccountID(r)
	if !ok {
		writeError(w, http.StatusNotFound, "EA akun tidak ditemukan")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	var name string
	var data []byte
	err := api.pool.QueryRow(ctx, `SELECT custom_ea_name,custom_ea_data FROM mt5_accounts WHERE id=$1 AND ea_mode='CUSTOM'`, accountID).Scan(&name, &data)
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "EA kustom tidak ditemukan")
		return
	}
	if err != nil || len(data) == 0 {
		writeError(w, http.StatusServiceUnavailable, "EA kustom tidak dapat dibaca")
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", `attachment; filename="CustomEA.ex5"`)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

// masterSnapshot bridges the TelegramSignalEA snapshot to the local Go
// publisher used by managed followers. Only the authenticated worker can read it.
func (api copierAPI) masterSnapshot(w http.ResponseWriter, r *http.Request) {
	if !api.requireWorker(w, r) {
		return
	}
	noStore(w)
	if api.master == nil {
		writeJSON(w, http.StatusOK, map[string]any{"active": false, "positions": []any{}})
		return
	}
	current, ok := api.master.Current()
	active := ok && time.Since(current.ObservedAt) >= -time.Second && time.Since(current.ObservedAt) <= 15*time.Second
	positions := make([]map[string]any, 0, len(current.Positions))
	if active {
		for _, p := range current.Positions {
			ticket, err := strconv.ParseInt(p.Ticket, 10, 64)
			if err != nil || ticket <= 0 {
				writeError(w, http.StatusUnprocessableEntity, "Ticket posisi master tidak valid")
				return
			}
			typeCode := 0
			if p.Action == "SELL" {
				typeCode = 1
			}
			position := map[string]any{
				"identifier": ticket, "ticket": ticket, "symbol": p.Symbol,
				"type": typeCode, "volume": p.Lots, "price_open": p.EntryPrice,
				"comment": "",
			}
			if p.SL != nil {
				position["sl"] = *p.SL
			}
			if p.TP != nil {
				position["tp"] = *p.TP
			}
			positions = append(positions, position)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"active": active, "positions": positions, "observed_at": current.ObservedAt.UTC().Format(time.RFC3339Nano)})
}

func (api copierAPI) serviceStatus(w http.ResponseWriter, r *http.Request) {
	noStore(w)
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	if _, ok := requireClient(ctx, api.pool, w, r); !ok {
		return
	}
	if state := SessionFromRequest(r); state != nil {
		if _, err := state.CSRFToken(); err != nil {
			writeError(w, http.StatusInternalServerError, "Token keamanan tidak dapat dibuat")
			return
		}
	}
	var raw string
	err := api.pool.QueryRow(ctx, `SELECT value FROM settings WHERE key = 'managed_copy_worker_seen_at'`).Scan(&raw)
	var lastSeen any
	available := false
	if err == nil {
		parsed, parseErr := time.Parse(time.RFC3339Nano, raw)
		if parseErr == nil {
			parsed = parsed.UTC()
			lastSeen = parsed.In(time.FixedZone("WIB", 7*60*60)).Format(time.RFC3339Nano)
			available = api.apiKey != "" && time.Since(parsed) <= 30*time.Second
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusServiceUnavailable, "Status copier tidak tersedia")
		return
	}
	status := "OFFLINE"
	if available {
		status = "ONLINE"
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"configured": api.apiKey != "", "available": available, "status": status, "last_seen_at": lastSeen,
	})
}

func (api copierAPI) authenticate(r *http.Request) bool {
	if api.apiKey == "" {
		return false
	}
	supplied := r.Header.Get("X-Copier-Worker-Key")
	return len(supplied) == len(api.apiKey) && subtle.ConstantTimeCompare([]byte(supplied), []byte(api.apiKey)) == 1
}

func (api copierAPI) requireWorker(w http.ResponseWriter, r *http.Request) bool {
	if api.apiKey == "" {
		writeError(w, http.StatusServiceUnavailable, "Worker copier belum dikonfigurasi")
		return false
	}
	if !api.authenticate(r) {
		writeError(w, http.StatusUnauthorized, "Worker tidak terautentikasi")
		return false
	}
	return true
}

func (api copierAPI) health(w http.ResponseWriter, r *http.Request) {
	if !api.requireWorker(w, r) {
		return
	}
	noStore(w)
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (api copierAPI) jobs(w http.ResponseWriter, r *http.Request) {
	if !api.requireWorker(w, r) {
		return
	}
	noStore(w)
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	if _, err := api.pool.Exec(ctx, `
		INSERT INTO settings (key, value) VALUES ('managed_copy_worker_seen_at', $1)
		ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value
	`, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		writeError(w, http.StatusServiceUnavailable, "Worker tidak dapat memperbarui heartbeat")
		return
	}
	jobs := make([]copierJob, 0)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	rows, err := api.pool.Query(ctx, `
		SELECT a.id, a.label, a.login, a.server, p.password_ciphertext, p.follower_symbol,
		       p.mode_lot, p.ratio_lot, p.lot_tetap, p.max_lot_per_order, p.max_lot_total,
	       p.max_open_positions, COALESCE(a.ea_mode, 'SERVER'), COALESCE(a.custom_ea_name, ''),
	       COALESCE(a.custom_ea_updated_at, ''), a.custom_ea_params, a.custom_ea_paused		FROM managed_copy_profiles AS p
		JOIN mt5_accounts AS a ON a.id = p.account_id
		WHERE (p.active = TRUE OR (a.ea_mode = 'CUSTOM' AND p.login_status = 'VALID'))
		  AND a.execution_mode = 'MANAGED'
		  AND (a.rental_expires_at IS NULL OR a.rental_expires_at > $1)
		ORDER BY a.id ASC
	`, now)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "Worker gagal mengambil job")
		return
	}
	for rows.Next() {
		var job copierJob
		var ciphertext string
		var rawParams sql.NullString
		if err := rows.Scan(&job.AccountID, &job.Label, &job.Login, &job.Server, &ciphertext,
			&job.FollowerSymbol, &job.ModeLot, &job.RatioLot, &job.FixedLot, &job.MaxLotPerOrder,
			&job.MaxLotTotal, &job.MaxOpenPositions, &job.EAMode, &job.CustomEAName, &job.CustomEAUpdatedAt, &rawParams, &job.CustomEAPaused); err != nil {
			rows.Close()
			writeError(w, http.StatusServiceUnavailable, "Worker gagal membaca job")
			return
		}
		if job.EAMode == "CUSTOM" {
			job.CustomEAParams = decodeStoredParams(rawParams)
		}
		password, err := security.DecryptManagedCopyPassword(api.secret, ciphertext)
		if err != nil {
			_, _ = api.pool.Exec(ctx, `
				UPDATE managed_copy_profiles
				SET status = 'ERROR', status_message = 'Kredensial tersimpan tidak dapat dibuka; simpan ulang password broker.'
				WHERE account_id = $1
			`, job.AccountID)
			continue
		}
		job.BrokerPassword = password
		jobs = append(jobs, job)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		writeError(w, http.StatusServiceUnavailable, "Worker gagal membaca job")
		return
	}
	rows.Close()

	loginChecks := make([]loginCheckJob, 0)
	checkRows, err := api.pool.Query(ctx, `
		SELECT a.id, a.login, a.server, p.password_ciphertext
		FROM managed_copy_profiles AS p
		JOIN mt5_accounts AS a ON a.id = p.account_id
		WHERE p.login_status = 'CHECKING' AND a.execution_mode = 'MANAGED'
		ORDER BY a.id ASC
	`)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "Worker gagal mengambil pemeriksaan login")
		return
	}
	for checkRows.Next() {
		var check loginCheckJob
		var ciphertext string
		if err := checkRows.Scan(&check.AccountID, &check.Login, &check.Server, &ciphertext); err != nil {
			checkRows.Close()
			writeError(w, http.StatusServiceUnavailable, "Worker gagal membaca pemeriksaan login")
			return
		}
		password, err := security.DecryptManagedCopyPassword(api.secret, ciphertext)
		if err != nil {
			_, _ = api.pool.Exec(ctx, `
				UPDATE managed_copy_profiles
				SET login_status = 'ERROR', login_message = 'Kredensial tidak dapat dibuka. Masukkan ulang password broker.'
				WHERE account_id = $1 AND login_status = 'CHECKING'
			`, check.AccountID)
			continue
		}
		check.BrokerPassword = password
		loginChecks = append(loginChecks, check)
	}
	if err := checkRows.Err(); err != nil {
		checkRows.Close()
		writeError(w, http.StatusServiceUnavailable, "Worker gagal membaca pemeriksaan login")
		return
	}
	checkRows.Close()

	managedIDs := make([]int64, 0)
	idRows, err := api.pool.Query(ctx, `SELECT id FROM mt5_accounts WHERE execution_mode = 'MANAGED' AND (rental_expires_at IS NULL OR rental_expires_at > $1) ORDER BY id ASC`, now)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "Worker gagal mengambil daftar akun")
		return
	}
	for idRows.Next() {
		var id int64
		if err := idRows.Scan(&id); err != nil {
			idRows.Close()
			writeError(w, http.StatusServiceUnavailable, "Worker gagal membaca daftar akun")
			return
		}
		managedIDs = append(managedIDs, id)
	}
	if err := idRows.Err(); err != nil {
		idRows.Close()
		writeError(w, http.StatusServiceUnavailable, "Worker gagal membaca daftar akun")
		return
	}
	idRows.Close()
	writeJSON(w, http.StatusOK, map[string]any{
		"jobs": jobs, "login_checks": loginChecks, "managed_account_ids": managedIDs,
	})
}

func (api copierAPI) reportLoginCheck(w http.ResponseWriter, r *http.Request) {
	if !api.requireWorker(w, r) {
		return
	}
	noStore(w)
	accountID, ok := pathAccountID(r)
	if !ok {
		writeError(w, http.StatusUnprocessableEntity, "ID akun tidak valid")
		return
	}
	var payload struct {
		Valid   *bool   `json:"valid"`
		Message *string `json:"message"`
	}
	if err := decodeJSON(w, r, &payload, 64<<10); err != nil || payload.Valid == nil || payload.Message == nil {
		writeError(w, http.StatusUnprocessableEntity, "Hasil pemeriksaan login tidak valid")
		return
	}
	if strings.TrimSpace(*payload.Message) == "" || utf8.RuneCountInString(*payload.Message) > 240 {
		writeError(w, http.StatusUnprocessableEntity, "Pesan pemeriksaan login harus 1 sampai 240 karakter")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	var status string
	var ownerID int64
	err := api.pool.QueryRow(ctx, `SELECT p.login_status,a.owner_id FROM managed_copy_profiles p JOIN mt5_accounts a ON a.id=p.account_id WHERE p.account_id = $1`, accountID).Scan(&status, &ownerID)
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "Profil managed copy tidak ditemukan")
		return
	}
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "Hasil pemeriksaan login tidak dapat disimpan")
		return
	}
	if status == "CHECKING" {
		status = "ERROR"
		if *payload.Valid {
			status = "VALID"
		}
		if _, err := api.pool.Exec(ctx, `
			UPDATE managed_copy_profiles SET login_status = $2, login_message = $3
			WHERE account_id = $1 AND login_status = 'CHECKING'
		`, accountID, status, *payload.Message); err != nil {
			writeError(w, http.StatusServiceUnavailable, "Hasil pemeriksaan login tidak dapat disimpan")
			return
		}
		api.updates.publish(ownerID, accountID)
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": status})
}

func (api copierAPI) reportStatus(w http.ResponseWriter, r *http.Request) {
	if !api.requireWorker(w, r) {
		return
	}
	noStore(w)
	accountID, ok := pathAccountID(r)
	if !ok {
		writeError(w, http.StatusUnprocessableEntity, "ID akun tidak valid")
		return
	}
	var payload struct {
		Status  string  `json:"status"`
		Message *string `json:"message"`
	}
	if err := decodeJSON(w, r, &payload, 64<<10); err != nil {
		writeError(w, http.StatusUnprocessableEntity, "Status worker tidak valid")
		return
	}
	if payload.Status != "STARTING" && payload.Status != "RUNNING" && payload.Status != "STOPPED" && payload.Status != "ERROR" {
		writeError(w, http.StatusUnprocessableEntity, "Status worker tidak dikenal")
		return
	}
	if payload.Message != nil && utf8.RuneCountInString(*payload.Message) > 240 {
		writeError(w, http.StatusUnprocessableEntity, "Pesan status maksimal 240 karakter")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	var active bool
	var ownerID int64
	err := api.pool.QueryRow(ctx, `SELECT p.active,a.owner_id FROM managed_copy_profiles p JOIN mt5_accounts a ON a.id=p.account_id WHERE p.account_id = $1`, accountID).Scan(&active, &ownerID)
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "Job copy tidak ditemukan")
		return
	}
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "Status worker tidak dapat disimpan")
		return
	}
	status := payload.Status
	var statusMessage any
	if payload.Message != nil {
		statusMessage = *payload.Message
	}
	if status == "STOPPED" && !active {
		status = "PAUSED"
		statusMessage = nil
	}
	if status == "RUNNING" {
		_, err = api.pool.Exec(ctx, `
			UPDATE managed_copy_profiles
			SET status = $2, status_message = $3, login_status = 'VALID',
			    login_message = 'Login broker berhasil dan terminal siap untuk copy trading.', last_seen_at = $4
			WHERE account_id = $1
		`, accountID, status, statusMessage, time.Now().UTC())
	} else {
		_, err = api.pool.Exec(ctx, `
			UPDATE managed_copy_profiles SET status = $2, status_message = $3, last_seen_at = $4
			WHERE account_id = $1
		`, accountID, status, statusMessage, time.Now().UTC())
	}
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "Status worker tidak dapat disimpan")
		return
	}
	api.updates.publish(ownerID, accountID)
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (api copierAPI) reportAccount(w http.ResponseWriter, r *http.Request) {
	if !api.requireWorker(w, r) {
		return
	}
	noStore(w)
	accountID, ok := pathAccountID(r)
	if !ok {
		writeError(w, http.StatusUnprocessableEntity, "ID akun tidak valid")
		return
	}
	var payload followerReport
	if err := decodeJSON(w, r, &payload, 2<<20); err != nil || !validFollowerReport(payload) {
		writeError(w, http.StatusUnprocessableEntity, "Laporan akun MT5 tidak valid")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	tx, err := api.pool.Begin(ctx)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "Laporan akun MT5 tidak dapat disimpan")
		return
	}
	defer tx.Rollback(ctx)
	var active bool
	var executionMode string
	var ownerID int64
	err = tx.QueryRow(ctx, `
		SELECT p.active, a.execution_mode, a.owner_id
		FROM managed_copy_profiles AS p
		JOIN mt5_accounts AS a ON a.id = p.account_id
		WHERE p.account_id = $1
		FOR UPDATE OF p
	`, accountID).Scan(&active, &executionMode, &ownerID)
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "Akun managed copy tidak ditemukan")
		return
	}
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "Laporan akun MT5 tidak dapat disimpan")
		return
	}
	if executionMode != "MANAGED" {
		writeError(w, http.StatusNotFound, "Akun managed copy tidak ditemukan")
		return
	}
	if !active {
		writeError(w, http.StatusConflict, "Copy trading akun ini sedang dijeda")
		return
	}
	now := time.Now().UTC()
	positionsJSON, _ := json.Marshal(sortedUnique(payload.OpenPositionIDs))
	cursor, cursorMSC := "0", int64(0)
	if err := tx.QueryRow(ctx, `
		SELECT history_cursor, history_cursor_msc FROM mt5_account_state WHERE account_id = $1
	`, accountID).Scan(&cursor, &cursorMSC); err != nil && !errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusServiceUnavailable, "Laporan akun MT5 tidak dapat disimpan")
		return
	}
	for _, deal := range payload.Deals {
		cursor = maxDecimalString(cursor, deal.DealTicket)
		if deal.TimeMSC > cursorMSC {
			cursorMSC = deal.TimeMSC
		}
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO mt5_account_state (
			account_id, balance, equity, floating_profit, margin, free_margin, currency, trade_mode,
			allow_live_trading, terminal_trade_allowed, expert_trade_allowed, open_position_ids_json,
			history_cursor, history_cursor_msc, observed_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15)
		ON CONFLICT (account_id) DO UPDATE SET
			balance=EXCLUDED.balance, equity=EXCLUDED.equity, floating_profit=EXCLUDED.floating_profit,
			margin=EXCLUDED.margin, free_margin=EXCLUDED.free_margin, currency=EXCLUDED.currency,
			trade_mode=EXCLUDED.trade_mode, allow_live_trading=EXCLUDED.allow_live_trading,
			terminal_trade_allowed=EXCLUDED.terminal_trade_allowed, expert_trade_allowed=EXCLUDED.expert_trade_allowed,
			open_position_ids_json=EXCLUDED.open_position_ids_json, history_cursor=EXCLUDED.history_cursor,
			history_cursor_msc=EXCLUDED.history_cursor_msc, observed_at=EXCLUDED.observed_at
	`, accountID, payload.Balance, payload.Equity, payload.FloatingProfit, payload.Margin, payload.FreeMargin,
		strings.ToUpper(payload.Currency), payload.TradeMode, *payload.AllowLiveTrading, *payload.TerminalTradeAllowed,
		*payload.ExpertTradeAllowed, string(positionsJSON), cursor, cursorMSC, now)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "Laporan akun MT5 tidak dapat disimpan")
		return
	}
	for _, deal := range payload.Deals {
		_, err = tx.Exec(ctx, `
			INSERT INTO mt5_history_deals (
				account_id, deal_ticket, position_id, deal_time, deal_time_msc, symbol, action, entry,
				volume, price, profit, commission, swap, fee
			) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)
			ON CONFLICT (account_id, deal_ticket) DO UPDATE SET
				position_id=EXCLUDED.position_id, deal_time=EXCLUDED.deal_time,
				deal_time_msc=EXCLUDED.deal_time_msc, symbol=EXCLUDED.symbol, action=EXCLUDED.action,
				entry=EXCLUDED.entry, volume=EXCLUDED.volume, price=EXCLUDED.price,
				profit=EXCLUDED.profit, commission=EXCLUDED.commission, swap=EXCLUDED.swap, fee=EXCLUDED.fee
		`, accountID, deal.DealTicket, deal.PositionID, time.UnixMilli(deal.TimeMSC).UTC(), deal.TimeMSC,
			deal.Symbol, deal.Action, deal.Entry, deal.Volume, deal.Price, deal.Profit, deal.Commission, deal.Swap, deal.Fee)
		if err != nil {
			writeError(w, http.StatusServiceUnavailable, "Riwayat transaksi MT5 tidak dapat disimpan")
			return
		}
	}
	if err = refreshAccountDailyProfit(ctx, tx, accountID, payload.Deals, strings.ToUpper(payload.Currency), now); err != nil {
		writeError(w, http.StatusServiceUnavailable, "Profit harian akun MT5 tidak dapat disimpan")
		return
	}
	if _, err = tx.Exec(ctx, `UPDATE mt5_accounts SET last_seen_at = $2 WHERE id = $1`, accountID, now); err != nil {
		writeError(w, http.StatusServiceUnavailable, "Laporan akun MT5 tidak dapat disimpan")
		return
	}
	if err := tx.Commit(ctx); err != nil {
		writeError(w, http.StatusServiceUnavailable, "Laporan akun MT5 tidak dapat disimpan")
		return
	}
	api.updates.publish(ownerID, accountID)
	wib := time.FixedZone("WIB", 7*60*60)
	writeJSON(w, http.StatusOK, map[string]any{
		"status": "ok", "history_cursor": cursor, "history_cursor_msc": cursorMSC,
		"received_deals": len(payload.Deals), "observed_at": now.In(wib).Format(time.RFC3339Nano),
	})
}

func validFollowerReport(p followerReport) bool {
	if !finiteNonnegative(p.Balance) || !finiteNonnegative(p.Margin) || !finite(p.Equity) || !finite(p.FloatingProfit) || !finite(p.FreeMargin) {
		return false
	}
	if !validAlphaNumeric(p.Currency, 3, 16) || (p.TradeMode != "real" && p.TradeMode != "demo" && p.TradeMode != "contest") {
		return false
	}
	if p.AllowLiveTrading == nil || p.TerminalTradeAllowed == nil || p.ExpertTradeAllowed == nil || p.OpenPositionIDs == nil || p.Deals == nil {
		return false
	}
	if len(p.OpenPositionIDs) > 500 || len(p.Deals) > 100 {
		return false
	}
	for _, id := range p.OpenPositionIDs {
		if len(id) < 1 || len(id) > 64 {
			return false
		}
	}
	for _, deal := range p.Deals {
		if !digits(deal.DealTicket, 1, 64) || !digits(deal.PositionID, 1, 64) || deal.TimeMSC < 1 || deal.TimeMSC > 4102444800000 ||
			!validLength(deal.Symbol, 1, 64) || (deal.Action != "BUY" && deal.Action != "SELL") ||
			(deal.Entry != "IN" && deal.Entry != "OUT" && deal.Entry != "INOUT" && deal.Entry != "OUT_BY") ||
			!finite(deal.Volume) || deal.Volume <= 0 || deal.Volume > 1000 || !finite(deal.Price) || deal.Price <= 0 ||
			!finite(deal.Profit) || !finite(deal.Commission) || !finite(deal.Swap) || !finite(deal.Fee) {
			return false
		}
	}
	return true
}

func finite(value float64) bool            { return !math.IsNaN(value) && !math.IsInf(value, 0) }
func finiteNonnegative(value float64) bool { return finite(value) && value >= 0 }

func validAlphaNumeric(value string, minimum, maximum int) bool {
	if !validLength(value, minimum, maximum) {
		return false
	}
	for _, c := range value {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9') {
			return false
		}
	}
	return true
}

func validLength(value string, minimum, maximum int) bool {
	length := utf8.RuneCountInString(value)
	return length >= minimum && length <= maximum
}

func digits(value string, minimum, maximum int) bool {
	if len(value) < minimum || len(value) > maximum {
		return false
	}
	for _, c := range value {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

func sortedUnique(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		if _, exists := seen[value]; !exists {
			seen[value] = struct{}{}
			result = append(result, value)
		}
	}
	sort.Strings(result)
	return result
}

func maxDecimalString(current, candidate string) string {
	currentInt, currentOK := new(big.Int).SetString(current, 10)
	candidateInt, candidateOK := new(big.Int).SetString(candidate, 10)
	if !currentOK {
		currentInt = big.NewInt(0)
	}
	if candidateOK && candidateInt.Cmp(currentInt) > 0 {
		return candidateInt.String()
	}
	return currentInt.String()
}

func pathAccountID(r *http.Request) (int64, bool) {
	value := r.PathValue("account_id")
	if value == "" {
		return 0, false
	}
	id, err := strconv.ParseInt(value, 10, 64)
	return id, err == nil && id > 0
}

func noStore(w http.ResponseWriter) { w.Header().Set("Cache-Control", "no-store") }
