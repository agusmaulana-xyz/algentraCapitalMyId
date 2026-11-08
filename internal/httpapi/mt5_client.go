package httpapi

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/rnpproject/algentra-backend/internal/security"
	"github.com/rnpproject/algentra-backend/internal/textutil"
)

var followerSymbolInput = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

var (
	errAccountTokenInvalid  = errors.New("account token invalid")
	errAccountTokenManaged  = errors.New("account managed")
	errAccountTokenIdentity = errors.New("account identity mismatch")
)

type managedCopySetupRequest struct {
	BrokerPassword   requestField[string]  `json:"broker_password"`
	FollowerSymbol   requestField[string]  `json:"follower_symbol"`
	ModeLot          requestField[string]  `json:"mode_lot"`
	RatioLot         requestField[float64] `json:"ratio_lot"`
	FixedLot         requestField[float64] `json:"lot_tetap"`
	MaxLotPerOrder   requestField[float64] `json:"max_lot_per_order"`
	MaxLotTotal      requestField[float64] `json:"max_lot_total"`
	MaxOpenPositions requestField[int]     `json:"max_open_positions"`
}

type requestField[T any] struct {
	Value   T
	Present bool
	Null    bool
}

func (field *requestField[T]) UnmarshalJSON(data []byte) error {
	field.Present = true
	if strings.TrimSpace(string(data)) == "null" {
		field.Null = true
		return nil
	}
	return json.Unmarshal(data, &field.Value)
}

func (api clientMT5API) configureManagedCopy(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 12*time.Second)
	defer cancel()
	ownerID, ok := requireClient(ctx, api.pool, w, r)
	if !ok {
		return
	}
	var p managedCopySetupRequest
	if err := decodeJSON(w, r, &p, 32<<10); err != nil {
		writeError(w, 422, "Pengaturan managed copy tidak valid")
		return
	}
	if !p.BrokerPassword.Present || p.BrokerPassword.Null || p.FollowerSymbol.Null || p.ModeLot.Null || p.RatioLot.Null || p.FixedLot.Null || p.MaxLotPerOrder.Null || p.MaxLotTotal.Null || p.MaxOpenPositions.Null {
		writeError(w, 422, "Pengaturan managed copy tidak valid")
		return
	}
	password, symbol, mode := p.BrokerPassword.Value, "", "rasio"
	ratio, fixedLot, orderLot, totalLot, positions := 1.0, 0.01, 0.05, 0.10, 3
	if p.FollowerSymbol.Present {
		symbol = p.FollowerSymbol.Value
	}
	if p.ModeLot.Present {
		mode = p.ModeLot.Value
	}
	if p.RatioLot.Present {
		ratio = p.RatioLot.Value
	}
	if p.FixedLot.Present {
		fixedLot = p.FixedLot.Value
	}
	if p.MaxLotPerOrder.Present {
		orderLot = p.MaxLotPerOrder.Value
	}
	if p.MaxLotTotal.Present {
		totalLot = p.MaxLotTotal.Value
	}
	if p.MaxOpenPositions.Present {
		positions = p.MaxOpenPositions.Value
	}
	if strings.HasPrefix(strings.ToUpper(strings.TrimSpace(symbol)), "XAUUSD247") {
		writeError(w, 422, "Simbol XAUUSD247 tidak didukung. Kosongkan simbol agar sistem memilih simbol akun yang tersedia.")
		return
	}
	if !validLength(password, 1, 128) || len(symbol) > 64 || (symbol != "" && !followerSymbolInput.MatchString(symbol)) || (mode != "tetap" && mode != "rasio") || !finite(ratio) || ratio <= 0 || ratio > 5 || !finite(fixedLot) || fixedLot <= 0 || !finite(orderLot) || orderLot <= 0 || orderLot > 5 || !finite(totalLot) || totalLot <= 0 || totalLot > 10 || orderLot > totalLot || positions < 1 || positions > 50 {
		writeError(w, 422, "Pengaturan lot atau kredensial broker tidak valid")
		return
	}
	accountID, ok := pathAccountID(r)
	if !ok {
		writeError(w, 404, "Akun MT5 tidak ditemukan")
		return
	}
	tx, err := api.pool.Begin(ctx)
	if err != nil {
		writeError(w, 503, "Akun MT5 tidak tersedia")
		return
	}
	defer tx.Rollback(ctx)
	var executionMode string
	if err = tx.QueryRow(ctx, `SELECT execution_mode FROM mt5_accounts WHERE id=$1 AND owner_id=$2 FOR UPDATE`, accountID, ownerID).Scan(&executionMode); errors.Is(err, sql.ErrNoRows) {
		writeError(w, 404, "Akun MT5 tidak ditemukan")
		return
	} else if err != nil {
		writeError(w, 503, "Akun MT5 tidak tersedia")
		return
	}
	if executionMode != "MANAGED" {
		writeError(w, 409, "Akun ini dibuat untuk pemasangan EA dari MT5")
		return
	}
	var active bool
	err = tx.QueryRow(ctx, `SELECT active FROM managed_copy_profiles WHERE account_id=$1 FOR UPDATE`, accountID).Scan(&active)
	exists := err == nil
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		writeError(w, 503, "Profil managed copy tidak tersedia")
		return
	}
	if exists && active {
		writeError(w, 409, "Jeda copy trading sebelum mengubah pengaturan akun")
		return
	}
	ciphertext, err := security.EncryptManagedCopyPassword(api.appSecret, password)
	if err != nil {
		writeError(w, 500, "Kredensial broker tidak dapat dienkripsi")
		return
	}
	now := time.Now().UTC()
	_, err = tx.Exec(ctx, `INSERT INTO managed_copy_profiles(account_id,password_ciphertext,follower_symbol,mode_lot,ratio_lot,lot_tetap,max_lot_per_order,max_lot_total,max_open_positions,login_status,login_message,active,status,status_message,last_seen_at,updated_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,'CHECKING','Memeriksa login dan kesiapan terminal MT5.',FALSE,'PAUSED',NULL,NULL,$10) ON CONFLICT(account_id) DO UPDATE SET password_ciphertext=EXCLUDED.password_ciphertext,follower_symbol=EXCLUDED.follower_symbol,mode_lot=EXCLUDED.mode_lot,ratio_lot=EXCLUDED.ratio_lot,lot_tetap=EXCLUDED.lot_tetap,max_lot_per_order=EXCLUDED.max_lot_per_order,max_lot_total=EXCLUDED.max_lot_total,max_open_positions=EXCLUDED.max_open_positions,login_status='CHECKING',login_message='Memeriksa login dan kesiapan terminal MT5.',active=FALSE,status='PAUSED',status_message=NULL,updated_at=EXCLUDED.updated_at`, accountID, ciphertext, symbol, mode, ratio, fixedLot, orderLot, totalLot, positions, now)
	if err != nil {
		writeError(w, 503, "Pengaturan managed copy tidak dapat disimpan")
		return
	}
	if err = tx.Commit(ctx); err != nil {
		writeError(w, 503, "Pengaturan managed copy tidak dapat disimpan")
		return
	}
	api.writeManagedProfile(ctx, w, accountID)
}

func (api clientMT5API) writeManagedProfile(ctx context.Context, w http.ResponseWriter, accountID int64) {
	var loginStatus, symbol, status string
	var loginMessage, statusMessage sql.NullString
	var active bool
	var lastSeen sql.NullTime
	var mode string
	var ratio, fixedLot, orderLot, totalLot float64
	var positions int
	err := api.pool.QueryRow(ctx, `SELECT login_status,login_message,follower_symbol,active,status,status_message,last_seen_at,mode_lot,ratio_lot,lot_tetap,max_lot_per_order,max_lot_total,max_open_positions FROM managed_copy_profiles WHERE account_id=$1`, accountID).Scan(&loginStatus, &loginMessage, &symbol, &active, &status, &statusMessage, &lastSeen, &mode, &ratio, &fixedLot, &orderLot, &totalLot, &positions)
	if err != nil {
		writeError(w, 503, "Profil managed copy tidak tersedia")
		return
	}
	writeJSON(w, 200, map[string]any{"configured": true, "login_status": loginStatus, "login_message": nullableString(loginMessage), "follower_symbol": symbol, "active": active, "status": status, "status_message": nullableString(statusMessage), "last_seen_at": nullableWIB(lastSeen, time.FixedZone("WIB", 7*60*60)), "mode_lot": mode, "ratio_lot": ratio, "lot_tetap": fixedLot, "max_lot_per_order": orderLot, "max_lot_total": totalLot, "max_open_positions": positions})
}

func (api clientMT5API) setManagedCopyActive(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	ownerID, ok := requireClient(ctx, api.pool, w, r)
	if !ok {
		return
	}
	accountID, ok := pathAccountID(r)
	if !ok {
		writeError(w, 404, "Akun MT5 tidak ditemukan")
		return
	}
	var p struct {
		Active      *bool `json:"active"`
		Acknowledge bool  `json:"acknowledge_live_risk"`
	}
	if err := decodeJSON(w, r, &p, 16<<10); err != nil || p.Active == nil {
		writeError(w, 422, "Status copy trading tidak valid")
		return
	}
	tx, err := api.pool.Begin(ctx)
	if err != nil {
		writeError(w, 503, "Status copy trading tidak tersedia")
		return
	}
	defer tx.Rollback(ctx)
	var mode string
	if err = tx.QueryRow(ctx, `SELECT execution_mode FROM mt5_accounts WHERE id=$1 AND owner_id=$2 FOR UPDATE`, accountID, ownerID).Scan(&mode); errors.Is(err, sql.ErrNoRows) {
		writeError(w, 404, "Akun MT5 tidak ditemukan")
		return
	} else if err != nil {
		writeError(w, 503, "Akun MT5 tidak tersedia")
		return
	}
	if mode != "MANAGED" {
		writeError(w, 409, "Akun ini tidak memakai layanan managed copy")
		return
	}
	var loginStatus string
	err = tx.QueryRow(ctx, `SELECT login_status FROM managed_copy_profiles WHERE account_id=$1 FOR UPDATE`, accountID).Scan(&loginStatus)
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, 409, "Simpan pengaturan dan kredensial broker terlebih dahulu")
		return
	}
	if err != nil {
		writeError(w, 503, "Profil managed copy tidak tersedia")
		return
	}
	if *p.Active && loginStatus != "VALID" {
		writeError(w, 409, "Login broker belum lolos pemeriksaan. Simpan kredensial untuk memeriksa login terlebih dahulu.")
		return
	}
	if *p.Active && !api.managedWorkerOnline(ctx) {
		writeError(w, 409, "Worker copier sedang offline. Coba lagi setelah layanan worker tersambung.")
		return
	}
	if *p.Active && !p.Acknowledge {
		writeError(w, 422, "Konfirmasi risiko diperlukan untuk mulai menyalin")
		return
	}
	now := time.Now().UTC()
	status := "PAUSED"
	if *p.Active {
		status = "STARTING"
	}
	if *p.Active {
		_, err = tx.Exec(ctx, `UPDATE managed_copy_profiles SET active=TRUE,status=$2,status_message=NULL,updated_at=$3 WHERE account_id=$1`, accountID, status, now)
	} else {
		_, err = tx.Exec(ctx, `UPDATE managed_copy_profiles SET active=FALSE,status=$2,status_message=NULL,last_seen_at=NULL,updated_at=$3 WHERE account_id=$1`, accountID, status, now)
	}
	if err != nil {
		writeError(w, 503, "Status copy trading tidak dapat disimpan")
		return
	}
	if err = tx.Commit(ctx); err != nil {
		writeError(w, 503, "Status copy trading tidak dapat disimpan")
		return
	}
	api.writeManagedProfile(ctx, w, accountID)
}

func (api clientMT5API) managedWorkerOnline(ctx context.Context) bool {
	if !api.workerConfigured {
		return false
	}
	var raw string
	if err := api.pool.QueryRow(ctx, `SELECT value FROM settings WHERE key='managed_copy_worker_seen_at'`).Scan(&raw); err != nil {
		return false
	}
	seen, err := time.Parse(time.RFC3339Nano, raw)
	return err == nil && !seen.After(time.Now().Add(time.Minute)) && time.Since(seen) <= 30*time.Second
}

func (api clientMT5API) setAccountActive(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	ownerID, ok := requireClient(ctx, api.pool, w, r)
	if !ok {
		return
	}
	id, ok := pathAccountID(r)
	if !ok {
		writeError(w, 404, "Akun MT5 tidak ditemukan")
		return
	}
	var p struct {
		Active *bool `json:"active"`
	}
	if decodeJSON(w, r, &p, 16<<10) != nil || p.Active == nil {
		writeError(w, 422, "Status akun tidak valid")
		return
	}
	var mode string
	err := api.pool.QueryRow(ctx, `SELECT execution_mode FROM mt5_accounts WHERE id=$1 AND owner_id=$2`, id, ownerID).Scan(&mode)
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, 404, "Akun MT5 tidak ditemukan")
		return
	}
	if err != nil {
		writeError(w, 503, "Akun MT5 tidak tersedia")
		return
	}
	if mode == "MANAGED" {
		writeError(w, 409, "Kelola status managed copy dari kontrol copy trading")
		return
	}
	if _, err = api.pool.Exec(ctx, `UPDATE mt5_accounts SET active=$3,last_seen_at=CASE WHEN active<>$3 THEN NULL ELSE last_seen_at END WHERE id=$1 AND owner_id=$2`, id, ownerID, *p.Active); err != nil {
		writeError(w, 503, "Status akun tidak dapat diperbarui")
		return
	}
	api.writeAccountPayload(ctx, w, ownerID, id)
}

func (api clientMT5API) updateAccount(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	ownerID, ok := requireClient(ctx, api.pool, w, r)
	if !ok {
		return
	}
	id, ok := pathAccountID(r)
	if !ok {
		writeError(w, 404, "Akun MT5 tidak ditemukan")
		return
	}
	var p struct {
		Label  string `json:"label"`
		Server string `json:"server"`
		Login  string `json:"login"`
	}
	if err := decodeJSON(w, r, &p, 16<<10); err != nil || !validLength(strings.TrimSpace(p.Label), 1, 80) || !validLength(strings.TrimSpace(p.Server), 1, 128) || !validLength(strings.TrimSpace(p.Login), 1, 32) {
		writeError(w, 422, "Nama, server, dan nomor login wajib diisi")
		return
	}
	p.Label = strings.TrimSpace(p.Label)
	p.Server = strings.TrimSpace(p.Server)
	p.Login = strings.TrimSpace(p.Login)
	tx, err := api.pool.Begin(ctx)
	if err != nil {
		writeError(w, 503, "Akun MT5 tidak tersedia")
		return
	}
	defer tx.Rollback(ctx)
	var mode, oldServer, oldLogin string
	err = tx.QueryRow(ctx, `SELECT execution_mode,server,login FROM mt5_accounts WHERE id=$1 AND owner_id=$2 FOR UPDATE`, id, ownerID).Scan(&mode, &oldServer, &oldLogin)
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, 404, "Akun MT5 tidak ditemukan")
		return
	}
	if err != nil {
		writeError(w, 503, "Akun MT5 tidak tersedia")
		return
	}
	if mode == "MANAGED" {
		var active bool
		err = tx.QueryRow(ctx, `SELECT active FROM managed_copy_profiles WHERE account_id=$1 FOR UPDATE`, id).Scan(&active)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			writeError(w, 503, "Status managed copy tidak tersedia")
			return
		}
		if active {
			writeError(w, 409, "Jeda copy trading sebelum mengubah detail akun")
			return
		}
	}
	if !textutil.EqualFold(oldServer, p.Server) || oldLogin != p.Login {
		if _, err = tx.Exec(ctx, `DELETE FROM mt5_history_deals WHERE account_id=$1`, id); err == nil {
			_, err = tx.Exec(ctx, `DELETE FROM mt5_account_state WHERE account_id=$1`, id)
		}
		if err != nil {
			writeError(w, 503, "Riwayat akun tidak dapat dihapus")
			return
		}
		if mode == "MANAGED" {
			if _, err = tx.Exec(ctx, `UPDATE managed_copy_profiles SET password_ciphertext='',login_status='UNCHECKED',login_message='Detail akun MT5 berubah. Masukkan ulang password broker untuk memeriksa login.',active=FALSE,status='PAUSED',status_message=NULL,last_seen_at=NULL,updated_at=$2 WHERE account_id=$1`, id, time.Now().UTC()); err != nil {
				writeError(w, 503, "Kredensial dan status login akun tidak dapat diatur ulang")
				return
			}
		}
	}
	_, err = tx.Exec(ctx, `UPDATE mt5_accounts SET label=$3,server=$4,login=$5,last_seen_at=CASE WHEN lower(server)<>lower($4) OR login<>$5 THEN NULL ELSE last_seen_at END WHERE id=$1 AND owner_id=$2`, id, ownerID, p.Label, p.Server, p.Login)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			writeError(w, 409, "Server dan nomor akun tersebut sudah terdaftar")
		} else {
			writeError(w, 503, "Akun MT5 tidak dapat diperbarui")
		}
		return
	}
	if err = tx.Commit(ctx); err != nil {
		writeError(w, 503, "Akun MT5 tidak dapat diperbarui")
		return
	}
	api.writeAccountPayload(ctx, w, ownerID, id)
}

func (api clientMT5API) writeAccountPayload(ctx context.Context, w http.ResponseWriter, ownerID, id int64) {
	now := time.Now().UTC()
	var label, server, login, plan, mode string
	var active bool
	var lastSeen, created sql.NullTime
	var balance, equity, floating sql.NullFloat64
	var currency, tradeMode sql.NullString
	var observed sql.NullTime
	var terminalAllowed, expertAllowed sql.NullBool
	err := api.pool.QueryRow(ctx, `SELECT a.label,a.server,a.login,a.plan,a.execution_mode,a.active,a.last_seen_at,a.created_at,s.balance,s.equity,s.floating_profit,s.currency,s.trade_mode,s.observed_at,s.terminal_trade_allowed,s.expert_trade_allowed FROM mt5_accounts a LEFT JOIN mt5_account_state s ON s.account_id=a.id WHERE a.id=$1 AND a.owner_id=$2`, id, ownerID).Scan(&label, &server, &login, &plan, &mode, &active, &lastSeen, &created, &balance, &equity, &floating, &currency, &tradeMode, &observed, &terminalAllowed, &expertAllowed)
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, 404, "Akun MT5 tidak ditemukan")
		return
	}
	if err != nil {
		writeError(w, 503, "Akun MT5 tidak tersedia")
		return
	}
	master, has := api.state.Current()
	masterOnline := has && !now.Before(master.ObservedAt) && now.Sub(master.ObservedAt) <= 20*time.Second
	followerOnline := active && lastSeen.Valid && now.Sub(lastSeen.Time) <= 30*time.Second
	stateOnline := observed.Valid && now.Sub(observed.Time) <= 30*time.Second
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
	writeJSON(w, 200, map[string]any{"id": id, "label": label, "server": server, "login": login, "plan": plan, "execution_mode": mode, "role": "follower", "active": active, "last_seen_at": nullableWIB(lastSeen, time.FixedZone("WIB", 7*60*60)), "connection_status": connection, "balance": nullableFloat(balance), "equity": nullableFloat(equity), "floating_profit": nullableFloat(floating), "currency": nullableString(currency), "trade_mode": nullableString(tradeMode), "state_updated_at": nullableWIB(observed, time.FixedZone("WIB", 7*60*60)), "created_at": nullableWIB(created, time.FixedZone("WIB", 7*60*60))})
}

type tokenAccount struct {
	id                  int64
	ownerID             int64
	login, server, mode string
}

func (api clientMT5API) accountByToken(ctx context.Context, r *http.Request) (tokenAccount, error) {
	token := r.Header.Get("X-Account-Token")
	login := strings.TrimSpace(r.Header.Get("X-MT5-Login"))
	server := strings.TrimSpace(r.Header.Get("X-MT5-Server"))
	if len(token) < 32 {
		return tokenAccount{}, errAccountTokenInvalid
	}
	hash := sha256.Sum256([]byte(token))
	var account tokenAccount
	err := api.pool.QueryRow(ctx, `SELECT id,owner_id,login,server,execution_mode FROM mt5_accounts WHERE token_hash=$1 AND active=TRUE`, hex.EncodeToString(hash[:])).Scan(&account.id, &account.ownerID, &account.login, &account.server, &account.mode)
	if err != nil {
		return tokenAccount{}, err
	}
	if account.mode == "MANAGED" {
		return tokenAccount{}, errAccountTokenManaged
	}
	if login != account.login || !textutil.EqualFold(server, account.server) {
		return tokenAccount{}, errAccountTokenIdentity
	}
	_, err = api.pool.Exec(ctx, `UPDATE mt5_accounts SET last_seen_at=$2 WHERE id=$1`, account.id, time.Now().UTC())
	return account, err
}

func (api clientMT5API) followerPositions(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	account, err := api.accountByToken(ctx, r)
	if errors.Is(err, errAccountTokenInvalid) || errors.Is(err, sql.ErrNoRows) {
		status := 401
		message := "Token akun tidak valid atau dinonaktifkan"
		if errors.Is(err, errAccountTokenManaged) {
			status = 403
			message = "Akun ini dikelola worker copier dan tidak memakai EA token"
		} else if errors.Is(err, errAccountTokenIdentity) {
			status = 403
			message = "Server atau nomor login terminal tidak sesuai dengan akun terdaftar"
		}
		writeError(w, status, message)
		return
	}
	if err != nil {
		writeError(w, 503, "Status akun MT5 tidak tersedia")
		return
	}
	now := time.Now().UTC()
	snap, has := api.state.Current()
	fresh := has && !now.Before(snap.ObservedAt) && now.Sub(snap.ObservedAt) <= 20*time.Second
	positions := make([]map[string]any, 0)
	if fresh {
		for _, p := range snap.Positions {
			positions = append(positions, map[string]any{"ticket": p.Ticket, "symbol": p.Symbol, "action": p.Action, "lots": p.Lots, "entry_price": p.EntryPrice, "sl": p.SL, "tp": p.TP})
		}
	}
	var cursor string
	var cursorMSC int64
	err = api.pool.QueryRow(ctx, `SELECT history_cursor,history_cursor_msc FROM mt5_account_state WHERE account_id=$1`, account.id).Scan(&cursor, &cursorMSC)
	if errors.Is(err, sql.ErrNoRows) {
		cursor = "0"
		cursorMSC = 0
	} else if err != nil {
		writeError(w, 503, "Status akun MT5 tidak tersedia")
		return
	}
	var masterLabel any
	if fresh {
		masterLabel = "EA utama Algentra"
	}
	writeJSON(w, 200, map[string]any{"active": fresh, "master_label": masterLabel, "positions": positions, "history_cursor": cursor, "history_cursor_msc": cursorMSC, "server_time": now.In(time.FixedZone("WIB", 7*60*60)).Format(time.RFC3339Nano)})
}

func (api clientMT5API) reportFollower(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	account, err := api.accountByToken(ctx, r)
	if err != nil {
		if !errors.Is(err, sql.ErrNoRows) && !errors.Is(err, errAccountTokenInvalid) && !errors.Is(err, errAccountTokenManaged) && !errors.Is(err, errAccountTokenIdentity) {
			writeError(w, 503, "Autentikasi akun MT5 tidak tersedia")
			return
		}
		status := 401
		message := "Token akun tidak valid atau dinonaktifkan"
		if errors.Is(err, errAccountTokenManaged) {
			status = 403
			message = "Akun ini dikelola worker copier dan tidak memakai EA token"
		} else if errors.Is(err, errAccountTokenIdentity) {
			status = 403
			message = "Server atau nomor login terminal tidak sesuai dengan akun terdaftar"
		}
		writeError(w, status, message)
		return
	}
	var p followerReport
	if decodeJSON(w, r, &p, 2<<20) != nil || !validFollowerReport(p) {
		writeError(w, 422, "Laporan akun MT5 tidak valid")
		return
	}
	now := time.Now().UTC()
	tx, err := api.pool.Begin(ctx)
	if err != nil {
		writeError(w, 503, "Laporan akun MT5 tidak dapat disimpan")
		return
	}
	defer tx.Rollback(ctx)
	positions, _ := json.Marshal(sortedUnique(p.OpenPositionIDs))
	cursor, cursorMSC := "0", int64(0)
	err = tx.QueryRow(ctx, `SELECT history_cursor,history_cursor_msc FROM mt5_account_state WHERE account_id=$1 FOR UPDATE`, account.id).Scan(&cursor, &cursorMSC)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		writeError(w, 503, "Laporan akun MT5 tidak dapat disimpan")
		return
	}
	for _, d := range p.Deals {
		cursor = maxDecimalString(cursor, d.DealTicket)
		if d.TimeMSC > cursorMSC {
			cursorMSC = d.TimeMSC
		}
	}
	_, err = tx.Exec(ctx, `INSERT INTO mt5_account_state(account_id,balance,equity,floating_profit,margin,free_margin,currency,trade_mode,allow_live_trading,terminal_trade_allowed,expert_trade_allowed,open_position_ids_json,history_cursor,history_cursor_msc,observed_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15) ON CONFLICT(account_id) DO UPDATE SET balance=EXCLUDED.balance,equity=EXCLUDED.equity,floating_profit=EXCLUDED.floating_profit,margin=EXCLUDED.margin,free_margin=EXCLUDED.free_margin,currency=EXCLUDED.currency,trade_mode=EXCLUDED.trade_mode,allow_live_trading=EXCLUDED.allow_live_trading,terminal_trade_allowed=EXCLUDED.terminal_trade_allowed,expert_trade_allowed=EXCLUDED.expert_trade_allowed,open_position_ids_json=EXCLUDED.open_position_ids_json,history_cursor=EXCLUDED.history_cursor,history_cursor_msc=EXCLUDED.history_cursor_msc,observed_at=EXCLUDED.observed_at`, account.id, p.Balance, p.Equity, p.FloatingProfit, p.Margin, p.FreeMargin, strings.ToUpper(p.Currency), p.TradeMode, *p.AllowLiveTrading, *p.TerminalTradeAllowed, *p.ExpertTradeAllowed, string(positions), cursor, cursorMSC, now)
	if err != nil {
		writeError(w, 503, "Laporan akun MT5 tidak dapat disimpan")
		return
	}
	for _, d := range p.Deals {
		_, err = tx.Exec(ctx, `INSERT INTO mt5_history_deals(account_id,deal_ticket,position_id,deal_time,deal_time_msc,symbol,action,entry,volume,price,profit,commission,swap,fee) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14) ON CONFLICT(account_id,deal_ticket) DO UPDATE SET position_id=EXCLUDED.position_id,deal_time=EXCLUDED.deal_time,deal_time_msc=EXCLUDED.deal_time_msc,symbol=EXCLUDED.symbol,action=EXCLUDED.action,entry=EXCLUDED.entry,volume=EXCLUDED.volume,price=EXCLUDED.price,profit=EXCLUDED.profit,commission=EXCLUDED.commission,swap=EXCLUDED.swap,fee=EXCLUDED.fee`, account.id, d.DealTicket, d.PositionID, time.UnixMilli(d.TimeMSC).UTC(), d.TimeMSC, d.Symbol, d.Action, d.Entry, d.Volume, d.Price, d.Profit, d.Commission, d.Swap, d.Fee)
		if err != nil {
			writeError(w, 503, "Riwayat transaksi MT5 tidak dapat disimpan")
			return
		}
	}
	if err = refreshAccountDailyProfit(ctx, tx, account.id, p.Deals, strings.ToUpper(p.Currency), now); err != nil {
		writeError(w, 503, "Profit harian akun MT5 tidak dapat disimpan")
		return
	}
	if _, err = tx.Exec(ctx, `UPDATE mt5_accounts SET last_seen_at=$2 WHERE id=$1`, account.id, now); err != nil {
		writeError(w, 503, "Laporan akun MT5 tidak dapat disimpan")
		return
	}
	if err = tx.Commit(ctx); err != nil {
		writeError(w, 503, "Laporan akun MT5 tidak dapat disimpan")
		return
	}
	api.updates.publish(account.ownerID, account.id)
	writeJSON(w, 200, map[string]any{"status": "ok", "history_cursor": cursor, "history_cursor_msc": cursorMSC, "received_deals": len(p.Deals), "observed_at": now.In(time.FixedZone("WIB", 7*60*60)).Format(time.RFC3339Nano)})
}

func (api clientMT5API) performance(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 12*time.Second)
	defer cancel()
	owner, ok := requireClient(ctx, api.pool, w, r)
	if !ok {
		return
	}
	rows, err := api.pool.Query(ctx, `SELECT id FROM mt5_accounts WHERE owner_id=$1 ORDER BY id`, owner)
	if err != nil {
		writeError(w, 503, "Performa akun tidak tersedia")
		return
	}
	ids := []int64{}
	for rows.Next() {
		var id int64
		if rows.Scan(&id) != nil {
			rows.Close()
			writeError(w, 503, "Performa akun tidak tersedia")
			return
		}
		ids = append(ids, id)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		writeError(w, 503, "Performa akun tidak tersedia")
		return
	}
	rows.Close()
	payload, err := api.performancePayload(ctx, ids)
	if err != nil {
		writeError(w, 503, "Performa akun tidak tersedia")
		return
	}
	writeJSON(w, 200, payload)
}

type perfState struct {
	mode, currency            string
	balance, equity, floating float64
	observed, lastSeen        sql.NullTime
	active                    bool
	open                      map[string]bool
}
type perfDeal struct {
	account, position, entry, symbol, action     string
	timeMS                                       int64
	time                                         time.Time
	volume, price, profit, commission, swap, fee float64
}

func (api clientMT5API) performancePayload(ctx context.Context, ids []int64) (map[string]any, error) {
	if len(ids) == 0 {
		return map[string]any{"available": false, "accounts_total": 0, "balance_groups": []any{}, "performance_groups": []any{}, "updated_at": nil}, nil
	}
	now := time.Now().UTC()
	wib := time.FixedZone("WIB", 7*60*60)
	states := map[string]*perfState{}
	balanceMap := map[string]map[string]any{}
	var latest *time.Time
	stateRows, err := api.pool.Query(ctx, `SELECT a.id,a.active,a.last_seen_at,s.trade_mode,s.currency,s.balance,s.equity,s.floating_profit,s.observed_at,s.open_position_ids_json FROM mt5_accounts a LEFT JOIN mt5_account_state s ON s.account_id=a.id WHERE a.id=ANY($1)`, ids)
	if err != nil {
		return nil, err
	}
	for stateRows.Next() {
		var id int64
		var active bool
		var last, observed sql.NullTime
		var mode, currency, open sql.NullString
		var balance, equity, floating sql.NullFloat64
		if err = stateRows.Scan(&id, &active, &last, &mode, &currency, &balance, &equity, &floating, &observed, &open); err != nil {
			stateRows.Close()
			return nil, err
		}
		if !observed.Valid {
			continue
		}
		st := &perfState{mode: mode.String, currency: strings.ToUpper(currency.String), balance: balance.Float64, equity: equity.Float64, floating: floating.Float64, observed: observed, lastSeen: last, active: active, open: map[string]bool{}}
		var openIDs []string
		if json.Unmarshal([]byte(open.String), &openIDs) == nil {
			for _, v := range openIDs {
				st.open[v] = true
			}
		}
		states[strconv.FormatInt(id, 10)] = st
		key := st.mode + "\x00" + st.currency
		g := balanceMap[key]
		if g == nil {
			g = map[string]any{"trade_mode": st.mode, "currency": st.currency, "balance": 0.0, "equity": 0.0, "floating_profit": 0.0, "reported_accounts": 0, "online_accounts": 0, "accounts_total": 0, "updated_at": nil}
			balanceMap[key] = g
		}
		g["balance"] = g["balance"].(float64) + st.balance
		g["equity"] = g["equity"].(float64) + st.equity
		g["floating_profit"] = g["floating_profit"].(float64) + st.floating
		g["reported_accounts"] = g["reported_accounts"].(int) + 1
		g["accounts_total"] = g["accounts_total"].(int) + 1
		if active && now.Sub(observed.Time) <= 30*time.Second && last.Valid && now.Sub(last.Time) <= 30*time.Second {
			g["online_accounts"] = g["online_accounts"].(int) + 1
		}
		stamp := observed.Time.In(wib)
		if current, ok := g["updated_at"].(time.Time); !ok || stamp.After(current) {
			g["updated_at"] = stamp
		}
		if latest == nil || observed.Time.After(*latest) {
			v := observed.Time
			latest = &v
		}
	}
	if err = stateRows.Err(); err != nil {
		stateRows.Close()
		return nil, err
	}
	stateRows.Close()
	dealRows, err := api.pool.Query(ctx, `SELECT account_id,position_id,entry,deal_time_msc,deal_time,symbol,action,volume,price,profit,commission,swap,fee FROM mt5_history_deals WHERE account_id=ANY($1) ORDER BY deal_time_msc ASC,id ASC`, ids)
	if err != nil {
		return nil, err
	}
	positions := map[string][]perfDeal{}
	positionOrder := make([]string, 0)
	for dealRows.Next() {
		var d perfDeal
		var accountID int64
		if err = dealRows.Scan(&accountID, &d.position, &d.entry, &d.timeMS, &d.time, &d.symbol, &d.action, &d.volume, &d.price, &d.profit, &d.commission, &d.swap, &d.fee); err != nil {
			dealRows.Close()
			return nil, err
		}
		d.account = strconv.FormatInt(accountID, 10)
		key := d.account + "\x00" + d.position
		if _, exists := positions[key]; !exists {
			positionOrder = append(positionOrder, key)
		}
		positions[key] = append(positions[key], d)
	}
	if err = dealRows.Err(); err != nil {
		dealRows.Close()
		return nil, err
	}
	dealRows.Close()
	type daily struct{ pnl float64 }
	type aggregate struct {
		mode, currency           string
		closed, wins, losses, be int
		pnl                      float64
		last                     *time.Time
		daily                    map[string]daily
	}
	aggs := map[string]*aggregate{}
	for _, positionKey := range positionOrder {
		ds := positions[positionKey]
		if len(ds) == 0 {
			continue
		}
		st := states[ds[0].account]
		if st == nil || st.open[ds[0].position] {
			continue
		}
		var closedAt *time.Time
		var latestClosingMS int64
		net := 0.0
		for _, d := range ds {
			net += d.profit + d.commission + d.swap + d.fee
			if d.entry == "OUT" || d.entry == "OUT_BY" || d.entry == "INOUT" {
				if closedAt == nil || d.timeMS > latestClosingMS {
					stamp := d.time
					closedAt = &stamp
					latestClosingMS = d.timeMS
				}
			}
		}
		if closedAt == nil {
			continue
		}
		stamp := closedAt.In(wib)
		key := st.mode + "\x00" + st.currency
		a := aggs[key]
		if a == nil {
			a = &aggregate{mode: st.mode, currency: st.currency, daily: map[string]daily{}}
			aggs[key] = a
		}
		a.closed++
		a.pnl += net
		if net > 0 {
			a.wins++
		} else if net < 0 {
			a.losses++
		} else {
			a.be++
		}
		day := stamp.Format("2006-01-02")
		d := a.daily[day]
		d.pnl += net
		a.daily[day] = d
		if a.last == nil || stamp.After(*a.last) {
			v := stamp
			a.last = &v
		}
	}
	keys := make([]string, 0, len(balanceMap))
	for k := range balanceMap {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	balances := make([]any, 0, len(keys))
	accountsReporting := 0
	for _, k := range keys {
		g := balanceMap[k]
		for _, name := range []string{"balance", "equity", "floating_profit"} {
			g[name] = math.Round(g[name].(float64)*100) / 100
		}
		if t, ok := g["updated_at"].(time.Time); ok {
			g["updated_at"] = t.Format(time.RFC3339Nano)
		}
		accountsReporting += g["reported_accounts"].(int)
		balances = append(balances, g)
	}
	keys = keys[:0]
	for k := range aggs {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	performances := make([]any, 0, len(keys))
	for _, k := range keys {
		a := aggs[k]
		days := make([]string, 0, len(a.daily))
		for day := range a.daily {
			days = append(days, day)
		}
		sort.Strings(days)
		curve := []map[string]any{}
		cumulative := 0.0
		start := max(0, len(days)-180)
		for i, day := range days {
			cumulative += a.daily[day].pnl
			if i >= start {
				curve = append(curve, map[string]any{"date": day, "pnl": math.Round(cumulative*100) / 100})
			}
		}
		winRate := 0.0
		if a.wins+a.losses > 0 {
			winRate = math.Round(float64(a.wins)/float64(a.wins+a.losses)*10000) / 100
		}
		var last any
		if a.last != nil {
			last = a.last.Format(time.RFC3339Nano)
		}
		performances = append(performances, map[string]any{"trade_mode": a.mode, "currency": a.currency, "closed_trades": a.closed, "wins": a.wins, "losses": a.losses, "break_even": a.be, "win_rate": winRate, "realized_pnl": math.Round(a.pnl*100) / 100, "last_closed_at": last, "curve": curve})
	}
	var updated any
	if latest != nil {
		updated = latest.In(wib).Format(time.RFC3339Nano)
	}
	return map[string]any{"available": len(balances) > 0 || len(performances) > 0, "accounts_total": len(ids), "accounts_reporting": accountsReporting, "balance_groups": balances, "performance_groups": performances, "updated_at": updated}, nil
}
