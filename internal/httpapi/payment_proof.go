package httpapi

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	dbstore "github.com/rnpproject/algentra-backend/internal/database"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const maxPaymentProofBytes = 5 << 20

type paymentCancelError struct {
	status  int
	message string
}

func (err *paymentCancelError) Error() string { return err.message }

func (api paymentAPI) proofDir() string {
	root := api.projectRoot
	if root == "" {
		exe, _ := os.Executable()
		root = filepath.Dir(exe)
	}
	return filepath.Join(root, "backend", "data", "payment_proofs")
}

func parsePaymentOrderID(r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("order_id"), 10, 64)
	return id, err == nil && id > 0
}

func (api paymentAPI) getProof(w http.ResponseWriter, r *http.Request) {
	ownerID, ok := api.client(w, r)
	if !ok {
		return
	}
	id, ok := parsePaymentOrderID(r)
	if !ok {
		writeError(w, 404, "Nota atau bukti pembayaran tidak ditemukan")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	var name string
	err := api.pool.QueryRow(ctx, `SELECT proof_filename FROM payment_orders WHERE id=$1 AND owner_id=$2`, id, ownerID).Scan(&name)
	if errors.Is(err, sql.ErrNoRows) || name == "" {
		writeError(w, 404, "Nota atau bukti pembayaran tidak ditemukan")
		return
	}
	if err != nil {
		writeError(w, 503, "Bukti pembayaran tidak tersedia")
		return
	}
	if filepath.Base(name) != name {
		writeError(w, 404, "File bukti pembayaran tidak tersedia")
		return
	}
	file, err := os.Open(filepath.Join(api.proofDir(), name))
	if err != nil {
		writeError(w, 404, "File bukti pembayaran tidak tersedia")
		return
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		writeError(w, 404, "File bukti pembayaran tidak tersedia")
		return
	}
	media := map[string]string{".jpg": "image/jpeg", ".png": "image/png", ".webp": "image/webp"}[strings.ToLower(filepath.Ext(name))]
	if media == "" {
		media = "application/octet-stream"
	}
	w.Header().Set("Content-Type", media)
	w.Header().Set("Content-Disposition", "inline")
	w.Header().Set("Cache-Control", "private, no-store")
	http.ServeContent(w, r, name, info.ModTime(), file)
}

func (api paymentAPI) submitProof(w http.ResponseWriter, r *http.Request) {
	ownerID, ok := api.client(w, r)
	if !ok {
		return
	}
	id, ok := parsePaymentOrderID(r)
	if !ok {
		writeError(w, 404, "Pesanan tidak ditemukan")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxPaymentProofBytes+(1<<20))
	if err := r.ParseMultipartForm(maxPaymentProofBytes + (1 << 20)); err != nil {
		writeError(w, 422, "File bukti pembayaran tidak valid")
		return
	}
	defer r.MultipartForm.RemoveAll()
	file, _, err := r.FormFile("file")
	if err != nil {
		writeError(w, 422, "File bukti pembayaran wajib diisi")
		return
	}
	defer file.Close()
	content, err := io.ReadAll(io.LimitReader(file, maxPaymentProofBytes+1))
	if err != nil {
		writeError(w, 422, "File bukti pembayaran tidak valid")
		return
	}
	if len(content) == 0 {
		writeError(w, 409, "File bukti pembayaran kosong.")
		return
	}
	if len(content) > maxPaymentProofBytes {
		writeError(w, 409, "Ukuran bukti pembayaran maksimal 5 MB.")
		return
	}
	ext := ""
	switch {
	case len(content) >= 3 && content[0] == 0xff && content[1] == 0xd8 && content[2] == 0xff:
		ext = ".jpg"
	case len(content) >= 8 && string(content[:8]) == "\x89PNG\r\n\x1a\n":
		ext = ".png"
	case len(content) >= 12 && string(content[:4]) == "RIFF" && string(content[8:12]) == "WEBP":
		ext = ".webp"
	}
	if ext == "" {
		writeError(w, 409, "Bukti pembayaran harus berupa gambar JPG, PNG, atau WebP yang valid.")
		return
	}
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		writeError(w, 500, "Bukti pembayaran tidak dapat disimpan")
		return
	}
	name := hex.EncodeToString(raw) + ext
	dir := api.proofDir()
	if err := os.MkdirAll(dir, 0700); err != nil {
		writeError(w, 503, "Bukti pembayaran tidak dapat disimpan")
		return
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, content, 0600); err != nil {
		writeError(w, 503, "Bukti pembayaran tidak dapat disimpan")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	tx, err := api.pool.Begin(ctx)
	if err != nil {
		_ = os.Remove(path)
		writeError(w, 503, "Bukti pembayaran tidak dapat disimpan")
		return
	}
	defer tx.Rollback(ctx)
	var status string
	err = tx.QueryRow(ctx, `SELECT status FROM payment_orders WHERE id=$1 AND owner_id=$2 FOR UPDATE`, id, ownerID).Scan(&status)
	if errors.Is(err, sql.ErrNoRows) {
		_ = os.Remove(path)
		writeError(w, 404, "Pesanan tidak ditemukan")
		return
	}
	if err != nil {
		_ = os.Remove(path)
		writeError(w, 503, "Pesanan pembayaran tidak tersedia")
		return
	}
	if status != "PENDING_PAYMENT" {
		_ = os.Remove(path)
		writeError(w, 409, "Pesanan ini tidak sedang menunggu bukti pembayaran.")
		return
	}
	now := time.Now().UTC()
	if _, err = tx.Exec(ctx, `UPDATE payment_orders SET proof_filename=$3,status='PAYMENT_SUBMITTED',updated_at=$4 WHERE id=$1 AND owner_id=$2`, id, ownerID, name, now); err != nil {
		_ = os.Remove(path)
		writeError(w, 503, "Bukti pembayaran tidak dapat disimpan")
		return
	}
	order, err := loadPaymentOrder(ctx, tx, id)
	if err != nil {
		_ = os.Remove(path)
		writeError(w, 503, "Pesanan pembayaran tidak tersedia")
		return
	}
	if err = tx.Commit(ctx); err != nil {
		_ = os.Remove(path)
		writeError(w, 503, "Bukti pembayaran tidak dapat disimpan")
		return
	}
	notificationSent := false
	if api.bot != nil {
		notifyCtx, notifyCancel := context.WithTimeout(r.Context(), 45*time.Second)
		notificationSent = api.bot.notifyOrder(notifyCtx, id)
		notifyCancel()
	}
	order["notification_sent"] = notificationSent
	writeJSON(w, 200, order)
}

func (api paymentAPI) cancelOrder(w http.ResponseWriter, r *http.Request) {
	ownerID, ok := api.client(w, r)
	if !ok {
		return
	}
	id, ok := parsePaymentOrderID(r)
	if !ok {
		writeError(w, 404, "Pesanan tidak ditemukan")
		return
	}
	preflightCtx, preflightCancel := context.WithTimeout(r.Context(), 5*time.Second)
	var preStatus, preMethod string
	var preQRISID sql.NullString
	preErr := api.pool.QueryRow(preflightCtx, `SELECT status,payment_method,midtrans_order_id FROM payment_orders WHERE id=$1 AND owner_id=$2`, id, ownerID).Scan(&preStatus, &preMethod, &preQRISID)
	preflightCancel()
	if errors.Is(preErr, sql.ErrNoRows) {
		writeError(w, 404, "Pesanan tidak ditemukan")
		return
	}
	if preErr != nil {
		writeError(w, 503, "Pesanan pembayaran tidak tersedia")
		return
	}
	if preStatus == "PENDING_PAYMENT" && preMethod == "QRIS" && preQRISID.Valid {
		if err := api.cancelQRISCharge(r.Context(), id, preQRISID.String); err != nil {
			var cancelErr *paymentCancelError
			if errors.As(err, &cancelErr) {
				writeError(w, cancelErr.status, cancelErr.message)
			} else {
				writeError(w, 409, err.Error())
			}
			return
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	tx, err := api.pool.Begin(ctx)
	if err != nil {
		writeError(w, 503, "Pesanan pembayaran tidak tersedia")
		return
	}
	defer tx.Rollback(ctx)
	var status, method string
	var midtransID, proof sql.NullString
	err = tx.QueryRow(ctx, `SELECT status,payment_method,midtrans_order_id,proof_filename FROM payment_orders WHERE id=$1 AND owner_id=$2 FOR UPDATE`, id, ownerID).Scan(&status, &method, &midtransID, &proof)
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, 404, "Pesanan tidak ditemukan")
		return
	}
	if err != nil {
		writeError(w, 503, "Pesanan pembayaran tidak tersedia")
		return
	}
	if status != "PENDING_PAYMENT" && status != "PAYMENT_SUBMITTED" {
		writeError(w, 409, "Pesanan ini tidak bisa dibatalkan")
		return
	}
	if method == "QRIS" && status == "PENDING_PAYMENT" {
		writeError(w, 409, "Status pembayaran QRIS belum dapat dipastikan.")
		return
	}
	if _, err = tx.Exec(ctx, `UPDATE payment_orders SET status='CANCELLED',updated_at=$3 WHERE id=$1 AND owner_id=$2`, id, ownerID, time.Now().UTC()); err != nil {
		writeError(w, 503, "Pesanan tidak dapat dibatalkan")
		return
	}
	order, err := loadPaymentOrder(ctx, tx, id)
	if err != nil {
		writeError(w, 503, "Pesanan pembayaran tidak tersedia")
		return
	}
	if err = tx.Commit(ctx); err != nil {
		writeError(w, 503, "Pesanan tidak dapat dibatalkan")
		return
	}
	if proof.Valid && filepath.Base(proof.String) == proof.String {
		_ = os.Remove(filepath.Join(api.proofDir(), proof.String))
	}
	writeJSON(w, 200, order)
}

func (api paymentAPI) cancelQRISCharge(parent context.Context, orderID int64, externalID string) error {
	if api.airPayAPIKey == "" {
		return &paymentCancelError{status: http.StatusConflict, message: "Pembayaran QRIS belum dikonfigurasi."}
	}
	ctx, cancel := context.WithTimeout(parent, 30*time.Second)
	defer cancel()
	response, code, err := api.airPayRequest(ctx, "/api/qris/status", map[string]any{"transaction_id": externalID})
	if err != nil || code < 200 || code >= 300 {
		return &paymentCancelError{status: http.StatusBadGateway, message: "Status QRIS belum dapat dipastikan. Coba batalkan pesanan lagi."}
	}
	data := response.result()
	_, result, _, err := api.applyAirPayStatus(ctx, orderID, data)
	if err != nil {
		return &paymentCancelError{status: http.StatusInternalServerError, message: "Status QRIS tidak dapat diperbarui."}
	}
	if result != "PAYMENT_FAILED" {
		return &paymentCancelError{status: http.StatusConflict, message: "QRIS masih aktif atau pembayaran sudah diproses. Pesanan belum dapat dibatalkan."}
	}
	return nil
}

func loadPaymentOrder(ctx context.Context, tx *dbstore.Tx, id int64) (map[string]any, error) {
	var email, label, server, login, plan, mode, status, method string
	var amount int64
	var rentalDays int
	var midtrans, proof sql.NullString
	var account sql.NullInt64
	var notified sql.NullTime
	var created, updated time.Time
	err := tx.QueryRow(ctx, `SELECT email,label,server,login,plan,execution_mode,total_idr,rental_days,status,payment_method,midtrans_status,proof_filename,account_id,admin_notified_at,created_at,updated_at FROM payment_orders WHERE id=$1`, id).Scan(&email, &label, &server, &login, &plan, &mode, &amount, &rentalDays, &status, &method, &midtrans, &proof, &account, &notified, &created, &updated)
	if err != nil {
		return nil, err
	}
	return paymentOrderPayload(id, email, label, server, login, plan, mode, amount, rentalDays, status, method, midtrans, proof, account, notified, created, updated), nil
}
