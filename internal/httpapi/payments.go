package httpapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	dbstore "github.com/rnpproject/algentra-backend/internal/database"
	"net/http"
	"regexp"
	"strings"
	"time"
)

type paymentAPI struct {
	pool                                 *dbstore.Pool
	pricing                              ServicePricing
	workerConfigured                     bool
	bankName, paymentNumber, paymentName string
	qrisAvailable                        bool
	projectRoot                          string
	airPayAPIKey                         string
	airPayMerchantUID                    string
	bot                                  *telegramPaymentBot
}

type paymentOrderCreateRequest struct {
	Label         string          `json:"label"`
	Server        string          `json:"server"`
	Login         string          `json:"login"`
	Plan          string          `json:"plan"`
	RentalDays    int             `json:"rental_days"`
	ExecutionMode json.RawMessage `json:"execution_mode"`
}

var paymentLoginPattern = regexp.MustCompile(`^\d+$`)
var paymentPlanPattern = regexp.MustCompile(`^RENTAL$`)

func (api paymentAPI) register(mux *http.ServeMux) {
	for _, prefix := range []string{"/api/payments", "/v1/payments"} {
		mux.HandleFunc("GET "+prefix+"/orders", api.listOrders)
		mux.HandleFunc("POST "+prefix+"/orders", api.createOrder)
		mux.HandleFunc("POST "+prefix+"/orders/{order_id}/cancel", api.cancelOrder)
		mux.HandleFunc("GET "+prefix+"/orders/{order_id}/proof", api.getProof)
		mux.HandleFunc("POST "+prefix+"/orders/{order_id}/proof", api.submitProof)
		mux.HandleFunc("POST "+prefix+"/orders/{order_id}/qris", api.createQRIS)
		mux.HandleFunc("GET "+prefix+"/orders/{order_id}/qris", api.qrisState)
		mux.HandleFunc("POST "+prefix+"/orders/{order_id}/qris/sync", api.syncQRIS)
		mux.HandleFunc("POST "+prefix+"/orders/{order_id}/notify", api.retryNotification)
		mux.HandleFunc("GET "+prefix+"/manual-instructions", api.manualInstructions)
	}
	mux.HandleFunc("GET /api/payments/qris.jpeg", qrisUnavailable)
	mux.HandleFunc("GET /v1/payments/qris.jpeg", qrisUnavailable)
}

func qrisUnavailable(w http.ResponseWriter, r *http.Request) {
	writeError(w, http.StatusGone, "Pembayaran QRIS belum tersedia. Gunakan instruksi pembayaran manual pada pesanan.")
}

func (api paymentAPI) client(w http.ResponseWriter, r *http.Request) (int64, bool) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	id, ok := requireClient(ctx, api.pool, w, r)
	if !ok {
		return 0, false
	}
	if r.Method == http.MethodGet {
		if state := SessionFromRequest(r); state != nil {
			if _, err := state.CSRFToken(); err != nil {
				writeError(w, 500, "Token keamanan tidak dapat dibuat")
				return 0, false
			}
		}
	}
	return id, true
}

func (api paymentAPI) manualInstructions(w http.ResponseWriter, r *http.Request) {
	if _, ok := api.client(w, r); !ok {
		return
	}
	writeJSON(w, 200, map[string]any{"bank_name": api.bankName, "payment_number": api.paymentNumber, "payment_name": api.paymentName, "qris_available": api.qrisAvailable})
}

func (api paymentAPI) listOrders(w http.ResponseWriter, r *http.Request) {
	ownerID, ok := api.client(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	rows, err := api.pool.Query(ctx, `SELECT id,email,label,server,login,plan,execution_mode,total_idr,rental_days,status,payment_method,midtrans_status,proof_filename,account_id,admin_notified_at,created_at,updated_at FROM payment_orders WHERE owner_id=$1 ORDER BY id DESC LIMIT 20`, ownerID)
	if err != nil {
		writeError(w, 503, "Pesanan pembayaran tidak tersedia")
		return
	}
	defer rows.Close()
	items := make([]map[string]any, 0)
	for rows.Next() {
		var id int64
		var email, label, server, login, plan, mode, status, method string
		var amount int64
		var rentalDays int
		var midtrans, proof sql.NullString
		var account sql.NullInt64
		var notified sql.NullTime
		var created, updated time.Time
		if err := rows.Scan(&id, &email, &label, &server, &login, &plan, &mode, &amount, &rentalDays, &status, &method, &midtrans, &proof, &account, &notified, &created, &updated); err != nil {
			writeError(w, 503, "Pesanan pembayaran tidak dapat dibaca")
			return
		}
		items = append(items, paymentOrderPayload(id, email, label, server, login, plan, mode, amount, rentalDays, status, method, midtrans, proof, account, notified, created, updated))
	}
	if err := rows.Err(); err != nil {
		writeError(w, 503, "Pesanan pembayaran tidak dapat dibaca")
		return
	}
	writeJSON(w, 200, items)
}

func (api paymentAPI) createOrder(w http.ResponseWriter, r *http.Request) {
	ownerID, ok := api.client(w, r)
	if !ok {
		return
	}
	var p paymentOrderCreateRequest
	if err := decodeJSON(w, r, &p, 64<<10); err != nil {
		writeError(w, 422, "Data pesanan tidak valid")
		return
	}
	p.Label = strings.TrimSpace(p.Label)
	p.Server = strings.TrimSpace(p.Server)
	p.Login = strings.TrimSpace(p.Login)
	executionMode := "MANAGED"
	if p.Plan != "RENTAL" && p.ExecutionMode != nil {
		if err := json.Unmarshal(p.ExecutionMode, &executionMode); err != nil {
			writeError(w, 422, "Data pesanan tidak valid")
			return
		}
	}
	if p.Plan == "RENTAL" {
		if p.RentalDays < 10 || p.RentalDays > 10000 {
			writeError(w, 422, "Masa sewa minimal 10 hari.")
			return
		}
		executionMode = "MANAGED"
	}
	if !validLength(p.Label, 1, 80) || !validLength(p.Server, 1, 128) || !validLength(p.Login, 1, 32) || !paymentLoginPattern.MatchString(p.Login) || !paymentPlanPattern.MatchString(p.Plan) || (executionMode != "EA" && executionMode != "MANAGED") {
		writeError(w, 422, "Data pesanan tidak valid")
		return
	}
	if executionMode == "MANAGED" && !api.workerConfigured {
		writeError(w, 503, "Layanan managed copy belum dikonfigurasi di server.")
		return
	}
	amount, exists := api.resolvePlan(p.Plan, p.RentalDays)
	if !exists {
		writeError(w, 409, "Paket layanan tidak dikenal")
		return
	}
	if executionMode != "MANAGED" {
		writeError(w, 409, "Paket tidak sesuai dengan cara menjalankan copy trading")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	tx, err := api.pool.Begin(ctx)
	if err != nil {
		writeError(w, 503, "Pesanan pembayaran tidak tersedia")
		return
	}
	defer tx.Rollback(ctx)
	// Reserve the SQLite writer transaction before checking account/order limits.
	_, err = tx.Exec(ctx, `INSERT INTO auth_locks(lock_key) VALUES('writer') ON CONFLICT(lock_key) DO NOTHING`)
	if err != nil {
		writeError(w, 503, "Pesanan pembayaran tidak tersedia")
		return
	}
	var email string
	if err = tx.QueryRow(ctx, `SELECT email FROM client_users WHERE id=$1`, ownerID).Scan(&email); errors.Is(err, sql.ErrNoRows) {
		writeError(w, 404, "Akun pengguna tidak ditemukan")
		return
	} else if err != nil {
		writeError(w, 503, "Akun pengguna tidak tersedia")
		return
	}
	var accountCount, reserved int64
	nowText := time.Now().UTC().Format(time.RFC3339Nano)
	if err = tx.QueryRow(ctx, `SELECT count(*) FROM mt5_accounts WHERE owner_id=$1 AND (rental_expires_at IS NULL OR rental_expires_at>$2)`, ownerID, nowText).Scan(&accountCount); err != nil {
		writeError(w, 503, "Akun pengguna tidak tersedia")
		return
	}
	if err = tx.QueryRow(ctx, `SELECT count(*) FROM payment_orders WHERE owner_id=$1 AND status IN ('PENDING_PAYMENT','PAYMENT_SUBMITTED')`, ownerID).Scan(&reserved); err != nil {
		writeError(w, 503, "Pesanan pembayaran tidak tersedia")
		return
	}
	if accountCount+reserved >= 10 {
		writeError(w, 409, "Anda sudah mencapai batas 10 akun Copy Trading.")
		return
	}
	var duplicate bool
	managed := executionMode == "MANAGED"
	query := `SELECT EXISTS(SELECT 1 FROM mt5_accounts WHERE lower(server)=lower($1) AND login=$2 AND (($4::boolean AND (NOT $5::boolean OR owner_id<>$3)) OR (NOT $4::boolean AND owner_id=$3)))`
	if err = tx.QueryRow(ctx, query, p.Server, p.Login, ownerID, managed, p.Plan == "RENTAL").Scan(&duplicate); err != nil {
		writeError(w, 503, "Akun broker tidak dapat diperiksa")
		return
	}
	if duplicate {
		writeError(w, 409, "Akun broker ini sudah terdaftar.")
		return
	}
	query = `SELECT EXISTS(SELECT 1 FROM payment_orders WHERE lower(server)=lower($1) AND login=$2 AND status IN ('PENDING_PAYMENT','PAYMENT_SUBMITTED') AND (($4::boolean AND (owner_id=$3 OR execution_mode='MANAGED')) OR (NOT $4::boolean AND owner_id=$3)))`
	if err = tx.QueryRow(ctx, query, p.Server, p.Login, ownerID, managed).Scan(&duplicate); err != nil {
		writeError(w, 503, "Pesanan aktif tidak dapat diperiksa")
		return
	}
	if duplicate {
		writeError(w, 409, "Masih ada pesanan aktif untuk akun broker ini.")
		return
	}
	var id int64
	now := time.Now().UTC()
	err = tx.QueryRow(ctx, `INSERT INTO payment_orders(owner_id,email,label,server,login,plan,execution_mode,total_idr,rental_days,status,created_at,updated_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,'PENDING_PAYMENT',$10,$10) RETURNING id`, ownerID, email, p.Label, p.Server, p.Login, p.Plan, executionMode, amount, p.RentalDays, now).Scan(&id)
	if err != nil {
		writeError(w, 503, "Pesanan pembayaran gagal disimpan")
		return
	}
	if err = tx.Commit(ctx); err != nil {
		writeError(w, 503, "Pesanan pembayaran gagal disimpan")
		return
	}
	var nullString sql.NullString
	var nullInt sql.NullInt64
	var nullTime sql.NullTime
	writeJSON(w, 200, paymentOrderPayload(id, email, p.Label, p.Server, p.Login, p.Plan, executionMode, amount, p.RentalDays, "PENDING_PAYMENT", "MANUAL", nullString, nullString, nullInt, nullTime, now, now))
}

func (api paymentAPI) resolvePlan(code string, rentalDays ...int) (int64, bool) {
	switch code {
	case "RENTAL":
		if len(rentalDays) != 1 || rentalDays[0] < 10 || rentalDays[0] > 10000 {
			return 0, false
		}
		return int64(rentalDays[0]) * 2000, true
	case "ZERO":
		return 150000, true
	case "PRO":
		return 200000, true
	case "EXPERT":
		return 300000, true
	case "MANAGED":
		return api.pricing.ManagementFeeIDR, true
	case "EA":
		return api.pricing.ManagementFeeIDR / 2, true
	}
	if strings.HasPrefix(code, "EA_VPS") {
		slot := strings.TrimPrefix(code, "EA_VPS")
		for _, plan := range api.pricing.VPSOptions {
			if plan.Code == "VPS"+slot {
				return api.pricing.ManagementFeeIDR/2 + plan.PriceIDR, true
			}
		}
	}
	return 0, false
}

func paymentOrderPayload(id int64, email, label, server, login, plan, mode string, amount int64, rentalDays int, status, method string, midtrans, proof sql.NullString, account sql.NullInt64, notified sql.NullTime, created, updated time.Time) map[string]any {
	return map[string]any{"id": id, "invoice_code": fmt.Sprintf("INV-AC%04d", id), "email": email, "label": label, "server": server, "login": login, "plan": plan, "execution_mode": mode, "total_idr": amount, "rental_days": rentalDays, "status": status, "payment_method": method, "qris_status": nullableString(midtrans), "has_proof": proof.Valid, "account_id": nullableInt(account), "notification_sent": notified.Valid, "created_at": wibTime(created), "updated_at": wibTime(updated)}
}

func nullableInt(v sql.NullInt64) any {
	if !v.Valid {
		return nil
	}
	return v.Int64
}
