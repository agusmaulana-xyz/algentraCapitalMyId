package httpapi

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	dbstore "github.com/rnpproject/algentra-backend/internal/database"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

type telegramPaymentBot struct {
	pool          *dbstore.Pool
	token, chatID string
	pricing       ServicePricing
	projectRoot   string
	client        *http.Client
}

type telegramEnvelope struct {
	OK     bool            `json:"ok"`
	Result json.RawMessage `json:"result"`
}
type telegramUpdate struct {
	UpdateID int64             `json:"update_id"`
	Callback *telegramCallback `json:"callback_query"`
}
type telegramCallback struct {
	ID      string          `json:"id"`
	Data    string          `json:"data"`
	Message telegramMessage `json:"message"`
}
type telegramMessage struct {
	MessageID int64  `json:"message_id"`
	Caption   string `json:"caption"`
	Chat      struct {
		ID json.RawMessage `json:"id"`
	} `json:"chat"`
}

func (bot *telegramPaymentBot) httpClient() *http.Client {
	if bot.client == nil {
		bot.client = &http.Client{Timeout: 45 * time.Second}
	}
	return bot.client
}
func (bot *telegramPaymentBot) endpoint(method string) string {
	return "https://api.telegram.org/bot" + bot.token + "/" + method
}

func (bot *telegramPaymentBot) call(ctx context.Context, method string, values url.Values) (json.RawMessage, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, bot.endpoint(method), strings.NewReader(values.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := bot.httpClient().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var envelope telegramEnvelope
	if err := json.NewDecoder(io.LimitReader(resp.Body, 2<<20)).Decode(&envelope); err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 || !envelope.OK {
		return nil, fmt.Errorf("telegram api rejected %s", method)
	}
	return envelope.Result, nil
}

func (bot *telegramPaymentBot) notifyOrder(ctx context.Context, orderID int64) bool {
	if bot == nil || bot.token == "" || bot.chatID == "" {
		return false
	}
	var email, label, server, login, plan string
	var amount int64
	var proof sql.NullString
	var status string
	var notified sql.NullTime
	err := bot.pool.QueryRow(ctx, `SELECT email,label,server,login,plan,total_idr,proof_filename,status,admin_notified_at FROM payment_orders WHERE id=$1`, orderID).Scan(&email, &label, &server, &login, &plan, &amount, &proof, &status, &notified)
	if err != nil || status != "PAYMENT_SUBMITTED" || !proof.Valid {
		return false
	}
	if notified.Valid {
		return true
	}
	name := filepath.Base(proof.String)
	if name != proof.String {
		return false
	}
	root := bot.projectRoot
	if root == "" {
		root = os.Getenv("PROJECT_ROOT")
	}
	if root == "" {
		exe, _ := os.Executable()
		root = filepath.Dir(exe)
	}
	path := filepath.Join(root, "backend", "data", "payment_proofs", name)
	file, err := os.Open(path)
	if err != nil {
		return false
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return false
	}
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	_ = writer.WriteField("chat_id", bot.chatID)
	caption := fmt.Sprintf("Pembayaran pesanan #%d\nClient: %s\nAkun: %s · %s · %s\nPaket: %s\nTotal: %s\n\nPeriksa bukti transfer, lalu pilih keputusan di bawah.", orderID, email, label, login, server, plan, formatRupiah(amount))
	_ = writer.WriteField("caption", caption)
	keyboard, _ := json.Marshal(map[string]any{"inline_keyboard": [][]map[string]string{{{"text": "Transaksi diterima", "callback_data": fmt.Sprintf("pay:accept:%d", orderID)}, {"text": "Transaksi tidak valid", "callback_data": fmt.Sprintf("pay:invalid:%d", orderID)}}}})
	_ = writer.WriteField("reply_markup", string(keyboard))
	photoHeader := make(textproto.MIMEHeader)
	photoHeader.Set("Content-Disposition", fmt.Sprintf(`form-data; name="photo"; filename="%s"`, info.Name()))
	photoType := map[string]string{".jpg": "image/jpeg", ".png": "image/png", ".webp": "image/webp"}[strings.ToLower(filepath.Ext(info.Name()))]
	if photoType == "" {
		photoType = "application/octet-stream"
	}
	photoHeader.Set("Content-Type", photoType)
	part, err := writer.CreatePart(photoHeader)
	if err != nil {
		return false
	}
	if _, err = io.Copy(part, file); err != nil {
		return false
	}
	if err = writer.Close(); err != nil {
		return false
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, bot.endpoint("sendPhoto"), &body)
	if err != nil {
		return false
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	resp, err := bot.httpClient().Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	var envelope telegramEnvelope
	if json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&envelope) != nil || resp.StatusCode < 200 || resp.StatusCode >= 300 || !envelope.OK {
		return false
	}
	_, err = bot.pool.Exec(ctx, `UPDATE payment_orders SET admin_notified_at=$2,updated_at=$2 WHERE id=$1 AND status='PAYMENT_SUBMITTED' AND admin_notified_at IS NULL`, orderID, time.Now().UTC())
	if err != nil {
		return false
	}
	return true
}

func formatRupiah(value int64) string {
	raw := strconv.FormatInt(value, 10)
	if value < 0 {
		return "Rp " + raw
	}
	var chunks []string
	for len(raw) > 3 {
		chunks = append([]string{raw[len(raw)-3:]}, chunks...)
		raw = raw[:len(raw)-3]
	}
	chunks = append([]string{raw}, chunks...)
	return "Rp " + strings.Join(chunks, ".")
}

func (api paymentAPI) retryNotification(w http.ResponseWriter, r *http.Request) {
	owner, ok := api.client(w, r)
	if !ok {
		return
	}
	id, ok := parsePaymentOrderID(r)
	if !ok {
		writeError(w, 404, "Pesanan tidak ditemukan")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	var status string
	var notified sql.NullTime
	err := api.pool.QueryRow(ctx, `SELECT status,admin_notified_at FROM payment_orders WHERE id=$1 AND owner_id=$2`, id, owner).Scan(&status, &notified)
	cancel()
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, 404, "Pesanan tidak ditemukan")
		return
	}
	if err != nil {
		writeError(w, 503, "Pesanan pembayaran tidak tersedia")
		return
	}
	if status != "PAYMENT_SUBMITTED" {
		writeError(w, 409, "Pesanan tidak sedang menunggu pemeriksaan pembayaran")
		return
	}
	if notified.Valid {
		writeJSON(w, 200, map[string]bool{"notification_sent": true})
		return
	}
	notifyCtx, notifyCancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer notifyCancel()
	sent := api.bot != nil && api.bot.notifyOrder(notifyCtx, id)
	writeJSON(w, 200, map[string]bool{"notification_sent": sent})
}

func (bot *telegramPaymentBot) run(ctx context.Context) {
	var offset int64
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		form := url.Values{"timeout": {"25"}, "allowed_updates": {"[\"callback_query\"]"}}
		if offset > 0 {
			form.Set("offset", strconv.FormatInt(offset, 10))
		}
		callCtx, cancel := context.WithTimeout(ctx, 35*time.Second)
		raw, err := bot.call(callCtx, "getUpdates", form)
		cancel()
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			waitContext(ctx, 5*time.Second)
			continue
		}
		var updates []telegramUpdate
		if json.Unmarshal(raw, &updates) != nil {
			waitContext(ctx, 5*time.Second)
			continue
		}
		for _, update := range updates {
			if update.UpdateID >= offset {
				offset = update.UpdateID + 1
			}
			if update.Callback != nil {
				bot.handleCallback(ctx, *update.Callback)
			}
		}
	}
}

func waitContext(ctx context.Context, d time.Duration) {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
	case <-timer.C:
	}
}

func (bot *telegramPaymentBot) handleCallback(parent context.Context, q telegramCallback) {
	chatID := string(q.Message.Chat.ID)
	if len(chatID) > 1 && chatID[0] == '"' {
		var decoded string
		if json.Unmarshal(q.Message.Chat.ID, &decoded) == nil {
			chatID = decoded
		}
	}
	if chatID != bot.chatID {
		return
	}
	parts := strings.SplitN(q.Data, ":", 3)
	action := ""
	orderID := int64(0)
	if len(parts) == 3 && parts[0] == "pay" && (parts[1] == "accept" || parts[1] == "invalid") {
		action = parts[1]
		orderID, _ = strconv.ParseInt(parts[2], 10, 64)
	}
	if action == "" || orderID < 1 {
		bot.answerCallback(parent, q.ID, "Tombol pesanan tidak valid.", true)
		return
	}
	status, accountID, err := bot.reviewOrder(parent, orderID, action)
	if err != nil {
		bot.answerCallback(parent, q.ID, "Gagal memproses keputusan. Coba lagi.", true)
		return
	}
	answer := ""
	captionStatus := ""
	switch status {
	case "APPROVED":
		answer = fmt.Sprintf("Transaksi diterima. Akun #%d dibuat.", accountID)
		captionStatus = fmt.Sprintf("\n\nSTATUS: TRANSAKSI DITERIMA · akun #%d dibuat", accountID)
	case "REJECTED":
		answer = "Transaksi ditandai tidak valid."
		captionStatus = "\n\nSTATUS: TRANSAKSI TIDAK VALID"
	case "ACCOUNT_EXISTS":
		answer = "Akun dengan server/login ini sudah terdaftar. Perlu pemeriksaan manual."
	case "PAYMENT_SUBMITTED":
		answer = "Bukti masih menunggu keputusan admin."
	default:
		answer = "Pesanan sudah diproses atau berstatus " + strings.ToLower(status) + "."
	}
	bot.answerCallback(parent, q.ID, answer, status != "APPROVED" && status != "REJECTED")
	if captionStatus != "" && q.Message.MessageID > 0 {
		markup, _ := json.Marshal(map[string]any{"inline_keyboard": []any{}})
		form := url.Values{"chat_id": {bot.chatID}, "message_id": {strconv.FormatInt(q.Message.MessageID, 10)}, "caption": {truncateRunes(q.Message.Caption+captionStatus, 1024)}, "reply_markup": {string(markup)}}
		ctx, cancel := context.WithTimeout(parent, 10*time.Second)
		_, _ = bot.call(ctx, "editMessageCaption", form)
		cancel()
	}
}

func (bot *telegramPaymentBot) answerCallback(parent context.Context, id, message string, alert bool) {
	ctx, cancel := context.WithTimeout(parent, 10*time.Second)
	defer cancel()
	form := url.Values{"callback_query_id": {id}, "text": {truncateRunes(message, 200)}, "show_alert": {strconv.FormatBool(alert)}}
	_, _ = bot.call(ctx, "answerCallbackQuery", form)
}

func (bot *telegramPaymentBot) reviewOrder(parent context.Context, id int64, decision string) (string, int64, error) {
	ctx, cancel := context.WithTimeout(parent, 10*time.Second)
	defer cancel()
	tx, err := bot.pool.Begin(ctx)
	if err != nil {
		return "", 0, err
	}
	defer tx.Rollback(ctx)
	var owner int64
	var label, server, login, plan, mode, status string
	var amount int64
	var rentalDays int
	var existing sql.NullInt64
	err = tx.QueryRow(ctx, `SELECT owner_id,label,server,login,plan,execution_mode,total_idr,status,account_id,rental_days FROM payment_orders WHERE id=$1 FOR UPDATE`, id).Scan(&owner, &label, &server, &login, &plan, &mode, &amount, &status, &existing, &rentalDays)
	if errors.Is(err, sql.ErrNoRows) {
		return "MISSING", 0, nil
	}
	if err != nil {
		return "", 0, err
	}
	if status != "PAYMENT_SUBMITTED" {
		return status, nullableInt64(existing), nil
	}
	if decision == "invalid" {
		_, err = tx.Exec(ctx, `UPDATE payment_orders SET status='REJECTED',updated_at=$2 WHERE id=$1`, id, time.Now().UTC())
		if err != nil {
			return "", 0, err
		}
		if err = tx.Commit(ctx); err != nil {
			return "", 0, err
		}
		return "REJECTED", 0, nil
	}
	api := paymentAPI{pool: bot.pool, pricing: bot.pricing}
	price, valid := api.resolvePlan(plan, rentalDays)
	if !valid || price != amount {
		return "INVALID_ORDER", 0, nil
	}
	managed := mode == "MANAGED"
	var duplicate sql.NullInt64
	var duplicateOwner int64
	var duplicateExpiry sql.NullTime
	err = tx.QueryRow(ctx, `SELECT id,owner_id,rental_expires_at FROM mt5_accounts WHERE lower(server)=lower($1) AND login=$2 AND ($4::boolean OR owner_id=$3) ORDER BY id LIMIT 1`, server, login, owner, managed).Scan(&duplicate, &duplicateOwner, &duplicateExpiry)
	if err == nil {
		if plan == "RENTAL" && duplicateOwner == owner {
			expiry := time.Now().UTC()
			if duplicateExpiry.Valid && duplicateExpiry.Time.After(expiry) {
				expiry = duplicateExpiry.Time
			}
			if _, err = tx.Exec(ctx, `UPDATE mt5_accounts SET rental_expires_at=$2,active=true WHERE id=$1`, duplicate.Int64, expiry.Add(time.Duration(rentalDays)*24*time.Hour).Format(time.RFC3339Nano)); err != nil {
				return "", 0, err
			}
			if _, err = tx.Exec(ctx, `UPDATE payment_orders SET status='APPROVED',account_id=$2,updated_at=$3 WHERE id=$1`, id, duplicate.Int64, time.Now().UTC()); err != nil {
				return "", 0, err
			}
			if err = tx.Commit(ctx); err != nil {
				return "", 0, err
			}
			return "APPROVED", duplicate.Int64, nil
		}
		return "ACCOUNT_EXISTS", duplicate.Int64, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", 0, err
	}
	random := make([]byte, 32)
	if _, err = rand.Read(random); err != nil {
		return "", 0, err
	}
	token := base64.RawURLEncoding.EncodeToString(random)
	sum := sha256.Sum256([]byte(token))
	var accountID int64
	now := time.Now().UTC()
	var rentalExpires any
	if plan == "RENTAL" {
		rentalExpires = now.Add(time.Duration(rentalDays) * 24 * time.Hour).Format(time.RFC3339Nano)
	}
	err = tx.QueryRow(ctx, `INSERT INTO mt5_accounts(owner_id,label,server,login,plan,role,execution_mode,token_hash,active,created_at,rental_expires_at,ea_mode) SELECT owner_id,label,server,login,plan,'follower',execution_mode,$2,true,$3,$4,'SERVER' FROM payment_orders WHERE id=$1 ON CONFLICT DO NOTHING RETURNING id`, id, hex.EncodeToString(sum[:]), now, rentalExpires).Scan(&accountID)
	if errors.Is(err, sql.ErrNoRows) {
		return "ACCOUNT_EXISTS", 0, nil
	}
	if err != nil {
		return "", 0, err
	}
	if _, err = tx.Exec(ctx, `UPDATE payment_orders SET status='APPROVED',account_id=$2,updated_at=$3 WHERE id=$1`, id, accountID, time.Now().UTC()); err != nil {
		return "", 0, err
	}
	if err = tx.Commit(ctx); err != nil {
		return "", 0, err
	}
	return "APPROVED", accountID, nil
}

func nullableInt64(value sql.NullInt64) int64 {
	if value.Valid {
		return value.Int64
	}
	return 0
}

func truncateRunes(value string, maximum int) string {
	if utf8.RuneCountInString(value) <= maximum {
		return value
	}
	runes := []rune(value)
	return string(runes[:maximum])
}
