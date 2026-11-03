package httpapi

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const airPayBaseURL = "https://airpaygateway.site"

type airPayData struct {
	Success       bool   `json:"success"`
	Message       string `json:"message"`
	TransactionID string `json:"transaction_id"`
	ExternalID    string `json:"external_id"`
	QRURL         string `json:"qr_url"`
	BaseAmount    int64  `json:"base_amount"`
	UniqueCode    int64  `json:"unique_code"`
	TotalAmount   int64  `json:"total_amount"`
	FeeAmount     int64  `json:"fee_amount"`
	NetAmount     int64  `json:"net_amount"`
	Status        string `json:"status"`
	PaidAt        string `json:"paid_at"`
	ExpiryAt      string `json:"expiry_at"`
	PaymentMethod string `json:"payment_method"`
}

type airPayReply struct {
	Success bool       `json:"success"`
	Message string     `json:"message"`
	Data    airPayData `json:"data"`
	// The API examples expose data fields at the top level for some responses.
	airPayData
}

func (r airPayReply) result() airPayData {
	if r.Data.TransactionID != "" || r.Data.ExternalID != "" || r.Data.Status != "" {
		return r.Data
	}
	return r.airPayData
}

func (api paymentAPI) airPayRequest(ctx context.Context, path string, body any) (airPayReply, int, error) {
	var result airPayReply
	if api.airPayAPIKey == "" {
		return result, 0, errors.New("AirPay is not configured")
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return result, 0, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, airPayBaseURL+path, strings.NewReader(string(raw)))
	if err != nil {
		return result, 0, err
	}
	req.Header.Set("X-API-KEY", api.airPayAPIKey)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	resp, err := (&http.Client{Timeout: 25 * time.Second}).Do(req)
	if err != nil {
		return result, 0, err
	}
	defer resp.Body.Close()
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&result); err != nil {
		return result, resp.StatusCode, err
	}
	return result, resp.StatusCode, nil
}

func (api paymentAPI) airPayQRURL(externalID string) string {
	values := url.Values{}
	values.Set("tid", externalID)
	values.Set("mid", api.airPayMerchantUID)
	return airPayBaseURL + "/api/qris/image?" + values.Encode()
}

func (api paymentAPI) createQRIS(w http.ResponseWriter, r *http.Request) {
	ownerID, ok := api.client(w, r)
	if !ok {
		return
	}
	id, ok := parsePaymentOrderID(r)
	if !ok {
		writeError(w, 404, "Pesanan tidak ditemukan")
		return
	}
	if api.airPayAPIKey == "" || api.airPayMerchantUID == "" {
		writeError(w, 503, "Pembayaran QRIS belum tersedia.")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 35*time.Second)
	defer cancel()
	tx, err := api.pool.Begin(ctx)
	if err != nil {
		writeError(w, 503, "Pesanan QRIS tidak tersedia")
		return
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `INSERT INTO auth_locks(lock_key) VALUES('writer') ON CONFLICT(lock_key) DO NOTHING`); err != nil {
		writeError(w, 503, "Pesanan QRIS tidak tersedia")
		return
	}
	var owner int64
	var status, plan, method string
	var amount int64
	var externalID, transactionID sql.NullString
	var qrisTotal int64
	err = tx.QueryRow(ctx, `SELECT owner_id,status,plan,total_idr,payment_method,midtrans_order_id,midtrans_transaction_id,qris_total_idr FROM payment_orders WHERE id=$1 FOR UPDATE`, id).Scan(&owner, &status, &plan, &amount, &method, &externalID, &transactionID, &qrisTotal)
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, 404, "Pesanan tidak ditemukan")
		return
	}
	if err != nil {
		writeError(w, 503, "Pesanan QRIS tidak tersedia")
		return
	}
	if owner != ownerID {
		writeError(w, 404, "Pesanan tidak ditemukan")
		return
	}
	if status != "PENDING_PAYMENT" {
		writeError(w, 409, "Pesanan ini tidak sedang menunggu pembayaran.")
		return
	}
	if method == "QRIS" && externalID.Valid {
		returnQRIS(w, 200, id, externalID.String, qrisTotal, api.airPayQRURL(externalID.String), transactionID.String)
		return
	}
	invoice := fmt.Sprintf("INV-AC%04d", id)
	response, code, err := api.airPayRequest(ctx, "/api/qris/create", map[string]any{"amount": amount, "external_id": invoice})
	if err != nil {
		slog.Error("AirPay QRIS request failed", "order_id", id, "error", err)
		writeError(w, 502, "Tidak dapat menghubungi AirPay. Coba lagi sebentar.")
		return
	}
	data := response.result()
	// Recover an existing provider-side invoice if the first create response was
	// lost and AirPay reports a duplicate external_id on retry.
	if code == http.StatusConflict {
		response, code, err = api.airPayRequest(ctx, "/api/qris/status", map[string]any{"transaction_id": invoice})
		if err != nil {
			writeError(w, 502, "QRIS AirPay sebelumnya belum dapat dipulihkan. Coba lagi sebentar.")
			return
		}
		data = response.result()
	}
	if code < 200 || code >= 300 || (!response.Success && !data.Success) || data.ExternalID == "" {
		slog.Error("AirPay rejected QRIS request", "order_id", id, "http_status", code, "provider_message", response.Message)
		writeError(w, 502, "AirPay menolak pembuatan QRIS. Coba lagi sebentar.")
		return
	}
	if data.ExternalID != invoice || data.BaseAmount != amount || data.TotalAmount < amount {
		slog.Error("AirPay returned mismatched QRIS amount or invoice", "order_id", id, "external_id", data.ExternalID, "base_amount", data.BaseAmount, "total_amount", data.TotalAmount)
		writeError(w, 502, "Data QRIS dari AirPay tidak cocok dengan invoice.")
		return
	}
	qrURL := strings.TrimSpace(data.QRURL)
	if qrURL == "" {
		qrURL = api.airPayQRURL(invoice)
	}
	if _, err = tx.Exec(ctx, `UPDATE payment_orders SET payment_method='QRIS',midtrans_order_id=$2,midtrans_transaction_id=NULLIF($3,''),midtrans_status=COALESCE(NULLIF($4,''),'Pending'),qris_total_idr=$5,updated_at=$6 WHERE id=$1`, id, invoice, data.TransactionID, data.Status, data.TotalAmount, time.Now().UTC()); err != nil {
		writeError(w, 503, "Status QRIS tidak dapat disimpan")
		return
	}
	if err = tx.Commit(ctx); err != nil {
		writeError(w, 503, "Status QRIS tidak dapat disimpan")
		return
	}
	writeJSON(w, 200, map[string]any{"order_id": id, "airpay_external_id": invoice, "qr_code_url": qrURL, "gross_amount": data.TotalAmount, "base_amount": data.BaseAmount, "unique_code": data.UniqueCode, "fee_amount": data.FeeAmount, "expiry_at": data.ExpiryAt})
}

func returnQRIS(w http.ResponseWriter, status int, id int64, externalID string, baseAmount int64, qrURL, transactionID string) {
	writeJSON(w, status, map[string]any{"order_id": id, "airpay_external_id": externalID, "transaction_id": transactionID, "qr_code_url": qrURL, "gross_amount": baseAmount})
}

func (api paymentAPI) qrisState(w http.ResponseWriter, r *http.Request) {
	ownerID, ok := api.client(w, r)
	if !ok {
		return
	}
	id, ok := parsePaymentOrderID(r)
	if !ok {
		writeError(w, 404, "Status QRIS tidak ditemukan")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	var status, qrisStatus string
	var externalID, transactionID sql.NullString
	var amount int64
	err := api.pool.QueryRow(ctx, `SELECT status,midtrans_status,midtrans_order_id,midtrans_transaction_id,qris_total_idr FROM payment_orders WHERE id=$1 AND owner_id=$2 AND payment_method='QRIS' AND midtrans_order_id IS NOT NULL`, id, ownerID).Scan(&status, &qrisStatus, &externalID, &transactionID, &amount)
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, 404, "Status QRIS tidak ditemukan")
		return
	}
	if err != nil {
		writeError(w, 503, "Status QRIS tidak tersedia")
		return
	}
	writeJSON(w, 200, map[string]any{"order_id": id, "status": status, "qris_status": qrisStatus, "airpay_external_id": nullableString(externalID), "transaction_id": nullableString(transactionID), "qr_code_url": api.airPayQRURL(externalID.String), "gross_amount": amount})
}

func (api paymentAPI) syncQRIS(w http.ResponseWriter, r *http.Request) {
	ownerID, ok := api.client(w, r)
	if !ok {
		return
	}
	id, ok := parsePaymentOrderID(r)
	if !ok {
		writeError(w, 404, "Pesanan tidak ditemukan")
		return
	}
	if api.airPayAPIKey == "" {
		writeError(w, 503, "Pembayaran QRIS belum tersedia.")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 35*time.Second)
	defer cancel()
	var invoice, currentStatus, qrisStatus string
	var account sql.NullInt64
	err := api.pool.QueryRow(ctx, `SELECT midtrans_order_id,status,midtrans_status,account_id FROM payment_orders WHERE id=$1 AND owner_id=$2 AND payment_method='QRIS'`, id, ownerID).Scan(&invoice, &currentStatus, &qrisStatus, &account)
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, 404, "Pesanan tidak ditemukan")
		return
	}
	if err != nil {
		writeError(w, 503, "Status QRIS tidak tersedia")
		return
	}
	if currentStatus != "PENDING_PAYMENT" {
		writeJSON(w, 200, map[string]any{"order_id": id, "order_status": currentStatus, "qris_status": qrisStatus, "result": currentStatus, "account_id": nullableInt(account)})
		return
	}
	response, code, err := api.airPayRequest(ctx, "/api/qris/status", map[string]any{"transaction_id": invoice})
	if err != nil {
		writeError(w, 502, "Tidak dapat menghubungi AirPay. Coba lagi sebentar.")
		return
	}
	data := response.result()
	if code < 200 || code >= 300 || (!response.Success && !data.Success) {
		writeError(w, 502, "AirPay belum dapat memeriksa status QRIS.")
		return
	}
	state, result, accountID, applyErr := api.applyAirPayStatus(ctx, id, data)
	if applyErr != nil {
		writeError(w, 503, "Status pembayaran tidak dapat disimpan")
		return
	}
	writeJSON(w, 200, map[string]any{"order_id": id, "order_status": state, "qris_status": data.Status, "result": result, "account_id": accountID})
}

func (api paymentAPI) applyAirPayStatus(ctx context.Context, id int64, data airPayData) (string, string, any, error) {
	tx, err := api.pool.Begin(ctx)
	if err != nil {
		return "", "", nil, err
	}
	defer tx.Rollback(ctx)
	var status, plan, invoice string
	var expected, storedTotal int64
	var rentalDays int
	var owner int64
	var server, login, mode string
	var existingAccount sql.NullInt64
	err = tx.QueryRow(ctx, `SELECT status,total_idr,plan,owner_id,server,login,execution_mode,account_id,midtrans_order_id,qris_total_idr,rental_days FROM payment_orders WHERE id=$1 FOR UPDATE`, id).Scan(&status, &expected, &plan, &owner, &server, &login, &mode, &existingAccount, &invoice, &storedTotal, &rentalDays)
	if err != nil {
		return "", "", nil, err
	}
	if (data.ExternalID != "" && data.ExternalID != invoice) || data.BaseAmount != expected || data.TotalAmount != storedTotal || data.TotalAmount != data.BaseAmount+data.UniqueCode {
		return status, "AMOUNT_MISMATCH", nullableInt(existingAccount), nil
	}
	if _, err = tx.Exec(ctx, `UPDATE payment_orders SET midtrans_status=$2,midtrans_transaction_id=COALESCE(NULLIF($3,''),midtrans_transaction_id),updated_at=$4 WHERE id=$1`, id, data.Status, data.TransactionID, time.Now().UTC()); err != nil {
		return "", "", nil, err
	}
	result := "PENDING"
	accountID := nullableInt(existingAccount)
	if strings.EqualFold(data.Status, "Success") {
		if status != "PENDING_PAYMENT" {
			result = status
		} else {
			price, valid := api.resolvePlan(plan, rentalDays)
			if !valid || price != expected {
				result = "INVALID_ORDER"
			} else {
				managed := mode == "MANAGED"
				var duplicateID sql.NullInt64
				var duplicateOwner int64
				var duplicateExpiry sql.NullTime
				err = tx.QueryRow(ctx, `SELECT id,owner_id,rental_expires_at FROM mt5_accounts WHERE lower(server)=lower($1) AND login=$2 AND ($4::boolean OR owner_id=$3) ORDER BY id LIMIT 1`, server, login, owner, managed).Scan(&duplicateID, &duplicateOwner, &duplicateExpiry)
				if err == nil {
					if plan == "RENTAL" && duplicateOwner == owner {
						now := time.Now().UTC()
						if duplicateExpiry.Valid && duplicateExpiry.Time.After(now) {
							now = duplicateExpiry.Time
						}
						if _, err = tx.Exec(ctx, `UPDATE mt5_accounts SET rental_expires_at=$2,active=true WHERE id=$1`, duplicateID.Int64, now.Add(time.Duration(rentalDays)*24*time.Hour).Format(time.RFC3339Nano)); err != nil {
							return "", "", nil, err
						}
						if _, err = tx.Exec(ctx, `UPDATE payment_orders SET status='APPROVED',account_id=$2,updated_at=$3 WHERE id=$1`, id, duplicateID.Int64, time.Now().UTC()); err != nil {
							return "", "", nil, err
						}
						result = "APPROVED"
						accountID = duplicateID.Int64
					} else {
						result = "ACCOUNT_EXISTS"
						accountID = duplicateID.Int64
					}
				} else if !errors.Is(err, sql.ErrNoRows) {
					return "", "", nil, err
				} else {
					raw := make([]byte, 32)
					if _, err = rand.Read(raw); err != nil {
						return "", "", nil, err
					}
					token := base64.RawURLEncoding.EncodeToString(raw)
					hash := sha256.Sum256([]byte(token))
					var createdID int64
					now := time.Now().UTC()
					var rentalExpires any
					if plan == "RENTAL" {
						rentalExpires = now.Add(time.Duration(rentalDays) * 24 * time.Hour).Format(time.RFC3339Nano)
					}
					err = tx.QueryRow(ctx, `INSERT INTO mt5_accounts(owner_id,label,server,login,plan,role,execution_mode,token_hash,active,created_at,rental_expires_at,ea_mode) SELECT owner_id,label,server,login,plan,'follower',execution_mode,$2,true,$3,$4,'SERVER' FROM payment_orders WHERE id=$1 ON CONFLICT DO NOTHING RETURNING id`, id, hex.EncodeToString(hash[:]), now, rentalExpires).Scan(&createdID)
					if errors.Is(err, sql.ErrNoRows) {
						result = "ACCOUNT_EXISTS"
					} else if err != nil {
						return "", "", nil, err
					} else {
						if _, err = tx.Exec(ctx, `UPDATE payment_orders SET status='APPROVED',account_id=$2,updated_at=$3 WHERE id=$1`, id, createdID, time.Now().UTC()); err != nil {
							return "", "", nil, err
						}
						result = "APPROVED"
						accountID = createdID
					}
				}
			}
		}
	} else if strings.EqualFold(data.Status, "Failed") || strings.EqualFold(data.Status, "Expired") || strings.EqualFold(data.Status, "Cancelled") {
		if status == "PENDING_PAYMENT" {
			if _, err = tx.Exec(ctx, `UPDATE payment_orders SET payment_method='MANUAL',midtrans_order_id=NULL,midtrans_transaction_id=NULL WHERE id=$1`, id); err != nil {
				return "", "", nil, err
			}
		}
		result = "PAYMENT_FAILED"
	}
	if err = tx.Commit(ctx); err != nil {
		return "", "", nil, err
	}
	var finalStatus string
	_ = api.pool.QueryRow(ctx, `SELECT status FROM payment_orders WHERE id=$1`, id).Scan(&finalStatus)
	return finalStatus, result, accountID, nil
}
