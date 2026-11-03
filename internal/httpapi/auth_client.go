package httpapi

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	dbstore "github.com/rnpproject/algentra-backend/internal/database"
	"html"
	"log/slog"
	"math/big"
	"net"
	"net/http"
	"net/mail"
	"net/smtp"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/rnpproject/algentra-backend/internal/auth"
	"golang.org/x/text/cases"
)

const (
	verificationCooldown    = time.Minute
	verificationWindow      = time.Hour
	verificationLifetime    = 10 * time.Minute
	verificationMaxSends    = 3
	verificationMaxAttempts = 3
)

var authEmailPattern = regexp.MustCompile(`^[^\s@]+@[^\s@]+\.[^\s@]+$`)
var authCodePattern = regexp.MustCompile(`^\d{6}$`)

type emailConfig struct {
	configured               bool
	host                     string
	port                     int
	username, password, from string
	startTLS                 bool
	socials                  []emailSocialLink
}

type emailSocialLink struct{ label, url string }

func configuredEmailSocials(contact ContactConfig) []emailSocialLink {
	items := []struct {
		label string
		value string
		hosts []string
	}{
		{"Instagram", contact.Instagram, []string{"instagram.com"}},
		{"Telegram", contact.Telegram, []string{"t.me", "telegram.me"}},
		{"Telegram Admin", contact.TelegramAdmin, []string{"t.me", "telegram.me"}},
		{"Facebook", contact.Facebook, []string{"facebook.com", "fb.com"}},
		{"LinkedIn", contact.LinkedIn, []string{"linkedin.com"}},
		{"X", contact.X, []string{"x.com", "twitter.com"}},
		{"YouTube", contact.YouTube, []string{"youtube.com", "youtu.be"}},
		{"TikTok", contact.TikTok, []string{"tiktok.com"}},
		{"Website", contact.Website, []string{"algentracapital.my.id"}},
	}
	var links []emailSocialLink
	for _, item := range items {
		value := strings.TrimSpace(item.value)
		parsed, err := url.Parse(value)
		if err != nil || !strings.EqualFold(parsed.Scheme, "https") || parsed.Hostname() == "" || parsed.User != nil {
			continue
		}
		host := strings.ToLower(parsed.Hostname())
		validHost := false
		for _, allowed := range item.hosts {
			if host == allowed || strings.HasSuffix(host, "."+allowed) {
				validHost = true
				break
			}
		}
		if validHost {
			links = append(links, emailSocialLink{label: item.label, url: value})
		}
	}
	return links
}

type authClientAPI struct {
	pool      *dbstore.Pool
	appSecret string
	email     emailConfig
}
type emailPasswordRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}
type emailCodeRequest struct {
	Email string `json:"email"`
	Code  string `json:"code"`
}
type resetConfirmRequest struct {
	Email    string `json:"email"`
	Code     string `json:"code"`
	Password string `json:"password"`
}

func (api authClientAPI) register(mux *http.ServeMux) {
	for _, prefix := range []string{"/api/auth", "/v1/auth"} {
		mux.HandleFunc("POST "+prefix+"/register", api.registerClient)
		mux.HandleFunc("POST "+prefix+"/resend-code", api.resendCode)
		mux.HandleFunc("POST "+prefix+"/verify-email", api.verifyEmail)
		mux.HandleFunc("POST "+prefix+"/password-reset/request", api.requestPasswordReset)
		mux.HandleFunc("POST "+prefix+"/password-reset/confirm", api.confirmPasswordReset)
	}
}

func validEmail(raw string) (string, bool) {
	email := cases.Fold().String(strings.TrimSpace(raw))
	for _, char := range email {
		if unicode.IsSpace(char) {
			return email, false
		}
	}
	return email, len([]rune(email)) >= 3 && len([]rune(email)) <= 320 && authEmailPattern.MatchString(email)
}
func emailCode() (string, error) {
	n, err := rand.Int(rand.Reader, big.NewInt(1_000_000))
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%06d", n.Int64()), nil
}
func authHMAC(secret, value string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(value))
	return hex.EncodeToString(mac.Sum(nil))
}
func verificationHash(secret, email, code string) string { return authHMAC(secret, email+":"+code) }
func resetEmailHash(secret, email string) string {
	return authHMAC(secret, "password-reset-email:"+email)
}
func resetCodeHash(secret, email, code string) string {
	return authHMAC(secret, "password-reset:"+email+":"+code)
}
func checkHash(a, b string) bool { return len(a) == len(b) && hmac.Equal([]byte(a), []byte(b)) }

type verificationRecord struct {
	CodeHash, PasswordHash     string
	ExpiresAt, ResendAfter     time.Time
	Attempts, SendCount        int
	WindowStarted, LockedUntil sql.NullTime
}

func authLock(ctx context.Context, tx *dbstore.Tx, key string) error {
	// This first write reserves SQLite's single writer slot for the transaction.
	_, err := tx.Exec(ctx, `INSERT INTO auth_locks(lock_key) VALUES('writer') ON CONFLICT(lock_key) DO NOTHING`)
	return err
}
func secondsUntil(deadline sql.NullTime, now time.Time) int {
	if !deadline.Valid {
		return 0
	}
	n := int(time.Until(deadline.Time).Seconds())
	if deadline.Time.After(now) {
		n = int(deadline.Time.Sub(now).Seconds())
		if deadline.Time.Sub(now)%time.Second != 0 {
			n++
		}
	}
	if n < 0 {
		return 0
	}
	return n
}
func recordWait(locked, resend, window sql.NullTime, sends int, now time.Time) int {
	if seconds := secondsUntil(locked, now); seconds > 0 {
		return seconds
	}
	if seconds := secondsUntil(resend, now); seconds > 0 {
		return seconds
	}
	if sends >= verificationMaxSends && window.Valid {
		deadline := sql.NullTime{Time: window.Time.Add(verificationWindow), Valid: true}
		return secondsUntil(deadline, now)
	}
	return 0
}
func authRetryResponse(w http.ResponseWriter, wait int) {
	if wait > 0 {
		writeJSON(w, http.StatusAccepted, map[string]any{"status": "verification_sent", "retry_after_seconds": wait})
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"status": "verification_sent", "retry_after_seconds": int(verificationCooldown.Seconds())})
}

func (api authClientAPI) registerClient(w http.ResponseWriter, r *http.Request) {
	var p emailPasswordRequest
	if err := decodeJSON(w, r, &p, 64<<10); err != nil || len([]rune(p.Password)) < 8 {
		writeError(w, 422, "Data pendaftaran tidak valid")
		return
	}
	email, ok := validEmail(p.Email)
	if !ok {
		writeError(w, 422, "Format email tidak valid")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	open, err := registrationIsOpen(ctx, api.pool)
	if err != nil {
		writeError(w, 503, "Status pendaftaran tidak tersedia")
		return
	}
	if !open {
		writeError(w, 403, "Pendaftaran client sedang ditutup. Silakan tunggu pengumuman launching.")
		return
	}
	if !api.email.configured {
		writeError(w, 503, "Pendaftaran belum tersedia karena email SMTP belum dikonfigurasi")
		return
	}
	tx, err := api.pool.Begin(ctx)
	if err != nil {
		writeError(w, 503, "Pendaftaran tidak tersedia")
		return
	}
	defer tx.Rollback(ctx)
	if err = authLock(ctx, tx, "email-verification:"+email); err != nil {
		writeError(w, 503, "Pendaftaran tidak tersedia")
		return
	}
	var userExists bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM client_users WHERE email=$1)`, email).Scan(&userExists); err != nil {
		writeError(w, 503, "Pendaftaran tidak tersedia")
		return
	}
	if userExists {
		_ = tx.Rollback(ctx)
		authRetryResponse(w, 60)
		return
	}
	now := time.Now().UTC()
	var pending verificationRecord
	err = tx.QueryRow(ctx, `SELECT code_hash,password_hash,expires_at,resend_after,attempts,send_count,send_window_started_at,locked_until FROM email_verifications WHERE email=$1 FOR UPDATE`, email).Scan(&pending.CodeHash, &pending.PasswordHash, &pending.ExpiresAt, &pending.ResendAfter, &pending.Attempts, &pending.SendCount, &pending.WindowStarted, &pending.LockedUntil)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		slog.Error("registration: read existing verification failed", "error", err)
		writeError(w, 503, "Data verifikasi tidak tersedia")
		return
	}
	exists := err == nil
	if exists {
		if wait := recordWait(pending.LockedUntil, sql.NullTime{Time: pending.ResendAfter, Valid: true}, pending.WindowStarted, pending.SendCount, now); wait > 0 {
			_ = tx.Rollback(ctx)
			authRetryResponse(w, wait)
			return
		}
	}
	code, err := emailCode()
	if err != nil {
		writeError(w, 500, "Kode verifikasi tidak dapat dibuat")
		return
	}
	hash, err := auth.HashClientPassword(p.Password)
	if err != nil {
		writeError(w, 500, "Kata sandi tidak dapat diproses")
		return
	}
	codeHash := verificationHash(api.appSecret, email, code)
	windowStarted := now
	sendCount := 0
	if exists && pending.WindowStarted.Valid && pending.WindowStarted.Time.Add(verificationWindow).After(now) {
		windowStarted = pending.WindowStarted.Time
		sendCount = pending.SendCount
	}
	sendCount++
	retry := recordWait(sql.NullTime{}, sql.NullTime{Time: now.Add(verificationCooldown), Valid: true}, sql.NullTime{Time: windowStarted, Valid: true}, sendCount, now)
	if exists {
		_, err = tx.Exec(ctx, `UPDATE email_verifications SET code_hash=$2,password_hash=$3,expires_at=$4,resend_after=$5,attempts=0,send_count=$6,send_window_started_at=$7,locked_until=NULL WHERE email=$1`, email, codeHash, hash, now.Add(verificationLifetime), now.Add(verificationCooldown), sendCount, windowStarted)
	} else {
		_, err = tx.Exec(ctx, `INSERT INTO email_verifications(email,code_hash,password_hash,expires_at,resend_after,attempts,send_count,send_window_started_at,locked_until,created_at) VALUES($1,$2,$3,$4,$5,0,$6,$7,NULL,$8)`, email, codeHash, hash, now.Add(verificationLifetime), now.Add(verificationCooldown), sendCount, windowStarted, now)
	}
	if err != nil {
		writeError(w, 503, "Kode verifikasi tidak dapat disimpan")
		return
	}
	if err = tx.Commit(ctx); err != nil {
		writeError(w, 503, "Kode verifikasi tidak dapat disimpan")
		return
	}
	if err = sendVerificationEmail(api.email, email, code, false); err != nil {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		_, _ = api.pool.Exec(cleanupCtx, `DELETE FROM email_verifications WHERE email=$1 AND code_hash=$2`, email, codeHash)
		cleanupCancel()
		writeError(w, 503, "Email verifikasi gagal dikirim; coba lagi nanti")
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"status": "verification_sent", "retry_after_seconds": retry})
}

func (api authClientAPI) resendCode(w http.ResponseWriter, r *http.Request) {
	var p emailPasswordRequest
	if err := decodeJSON(w, r, &p, 64<<10); err != nil || len([]rune(p.Password)) < 8 {
		writeError(w, 422, "Data pendaftaran tidak valid")
		return
	}
	email, ok := validEmail(p.Email)
	if !ok {
		writeError(w, 422, "Format email tidak valid")
		return
	}
	if !api.email.configured {
		writeError(w, 503, "Pendaftaran belum tersedia karena email SMTP belum dikonfigurasi")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	tx, err := api.pool.Begin(ctx)
	if err != nil {
		writeError(w, 503, "Pendaftaran tidak tersedia")
		return
	}
	defer tx.Rollback(ctx)
	if err = authLock(ctx, tx, "email-verification:"+email); err != nil {
		writeError(w, 503, "Pendaftaran tidak tersedia")
		return
	}
	var existsUser bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM client_users WHERE email=$1)`, email).Scan(&existsUser); err != nil {
		writeError(w, 503, "Pendaftaran tidak tersedia")
		return
	}
	var pending verificationRecord
	err = tx.QueryRow(ctx, `SELECT code_hash,password_hash,expires_at,resend_after,attempts,send_count,send_window_started_at,locked_until FROM email_verifications WHERE email=$1 FOR UPDATE`, email).Scan(&pending.CodeHash, &pending.PasswordHash, &pending.ExpiresAt, &pending.ResendAfter, &pending.Attempts, &pending.SendCount, &pending.WindowStarted, &pending.LockedUntil)
	if errors.Is(err, sql.ErrNoRows) || existsUser {
		_ = tx.Rollback(ctx)
		authRetryResponse(w, 60)
		return
	}
	if err != nil {
		writeError(w, 503, "Data verifikasi tidak tersedia")
		return
	}
	now := time.Now().UTC()
	if wait := recordWait(pending.LockedUntil, sql.NullTime{Time: pending.ResendAfter, Valid: true}, pending.WindowStarted, pending.SendCount, now); wait > 0 {
		_ = tx.Rollback(ctx)
		authRetryResponse(w, wait)
		return
	}
	code, err := emailCode()
	if err != nil {
		writeError(w, 500, "Kode verifikasi tidak dapat dibuat")
		return
	}
	passwordHash, err := auth.HashClientPassword(p.Password)
	if err != nil {
		writeError(w, 500, "Kata sandi tidak dapat diproses")
		return
	}
	hash := verificationHash(api.appSecret, email, code)
	window := now
	count := 0
	if pending.WindowStarted.Valid && pending.WindowStarted.Time.Add(verificationWindow).After(now) {
		window = pending.WindowStarted.Time
		count = pending.SendCount
	}
	count++
	_, err = tx.Exec(ctx, `UPDATE email_verifications SET code_hash=$2,password_hash=$3,expires_at=$4,resend_after=$5,attempts=0,send_count=$6,send_window_started_at=$7,locked_until=NULL WHERE email=$1`, email, hash, passwordHash, now.Add(verificationLifetime), now.Add(verificationCooldown), count, window)
	if err != nil {
		writeError(w, 503, "Kode verifikasi tidak dapat disimpan")
		return
	}
	if err = tx.Commit(ctx); err != nil {
		writeError(w, 503, "Kode verifikasi tidak dapat disimpan")
		return
	}
	if err = sendVerificationEmail(api.email, email, code, false); err != nil {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		_, _ = api.pool.Exec(cleanupCtx, `DELETE FROM email_verifications WHERE email=$1 AND code_hash=$2`, email, hash)
		cleanupCancel()
		writeError(w, 503, "Email verifikasi gagal dikirim; coba lagi nanti")
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"status": "verification_sent", "retry_after_seconds": 60})
}

func (api authClientAPI) verifyEmail(w http.ResponseWriter, r *http.Request) {
	var p emailCodeRequest
	if err := decodeJSON(w, r, &p, 32<<10); err != nil || !authCodePattern.MatchString(p.Code) {
		writeError(w, 422, "Data verifikasi tidak valid")
		return
	}
	email, ok := validEmail(p.Email)
	if !ok {
		writeError(w, 422, "Format email tidak valid")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	tx, err := api.pool.Begin(ctx)
	if err != nil {
		slog.Error("email verification: begin transaction failed", "error", err)
		writeError(w, 503, "Verifikasi tidak tersedia")
		return
	}
	defer tx.Rollback(ctx)
	if err = authLock(ctx, tx, "email-verification:"+email); err != nil {
		slog.Error("email verification: acquire database lock failed", "error", err)
		writeError(w, 503, "Verifikasi tidak tersedia")
		return
	}
	var v verificationRecord
	err = tx.QueryRow(ctx, `SELECT code_hash,password_hash,expires_at,resend_after,attempts,send_count,send_window_started_at,locked_until FROM email_verifications WHERE email=$1 FOR UPDATE`, email).Scan(&v.CodeHash, &v.PasswordHash, &v.ExpiresAt, &v.ResendAfter, &v.Attempts, &v.SendCount, &v.WindowStarted, &v.LockedUntil)
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, 400, "Kode tidak valid atau kedaluwarsa")
		return
	}
	if err != nil {
		slog.Error("email verification: read code failed", "error", err)
		writeError(w, 503, "Kode verifikasi tidak tersedia")
		return
	}
	now := time.Now().UTC()
	if v.Attempts >= verificationMaxAttempts {
		if wait := secondsUntil(v.LockedUntil, now); wait > 0 {
			_ = tx.Rollback(ctx)
			w.Header().Set("Retry-After", strconv.Itoa(wait))
			writeError(w, 429, "Kesempatan memasukkan kode habis. Coba lagi dalam 1 jam.")
			return
		}
		_, _ = tx.Exec(ctx, `UPDATE email_verifications SET attempts=0,locked_until=NULL WHERE email=$1`, email)
		v.Attempts = 0
		v.LockedUntil = sql.NullTime{}
	}
	if !v.ExpiresAt.After(now) {
		writeError(w, 400, "Kode tidak valid atau kedaluwarsa")
		return
	}
	if !checkHash(v.CodeHash, verificationHash(api.appSecret, email, p.Code)) {
		v.Attempts++
		if v.Attempts >= verificationMaxAttempts {
			lockUntil := now.Add(verificationWindow)
			_, err = tx.Exec(ctx, `UPDATE email_verifications SET attempts=$2,locked_until=$3,resend_after=$3 WHERE email=$1`, email, v.Attempts, lockUntil)
			if err != nil {
				slog.Error("email verification: record attempt limit failed", "error", err)
				writeError(w, 503, "Status verifikasi tidak dapat disimpan")
				return
			}
			if err = tx.Commit(ctx); err != nil {
				slog.Error("email verification: commit attempt limit failed", "error", err)
				writeError(w, 503, "Status verifikasi tidak dapat disimpan")
				return
			}
			w.Header().Set("Retry-After", "3600")
			writeError(w, 429, "Kesempatan memasukkan kode habis. Coba lagi dalam 1 jam.")
			return
		}
		_, err = tx.Exec(ctx, `UPDATE email_verifications SET attempts=$2 WHERE email=$1`, email, v.Attempts)
		if err != nil {
			slog.Error("email verification: record failed attempt failed", "error", err)
			writeError(w, 503, "Status verifikasi tidak dapat disimpan")
			return
		}
		if err = tx.Commit(ctx); err != nil {
			slog.Error("email verification: commit failed attempt failed", "error", err)
			writeError(w, 503, "Status verifikasi tidak dapat disimpan")
			return
		}
		writeError(w, 400, "Kode tidak valid atau kedaluwarsa")
		return
	}
	var userID int64
	err = tx.QueryRow(ctx, `INSERT INTO client_users(email,password_hash,verified_at,created_at) VALUES($1,$2,$3,$3) ON CONFLICT(email) DO NOTHING RETURNING id`, email, v.PasswordHash, now).Scan(&userID)
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, 409, "Akun dengan email tersebut sudah dibuat")
		return
	}
	if err != nil {
		slog.Error("email verification: create client account failed", "error", err)
		writeError(w, 503, "Akun client tidak dapat dibuat")
		return
	}
	if _, err = tx.Exec(ctx, `DELETE FROM email_verifications WHERE email=$1`, email); err != nil {
		slog.Error("email verification: delete used code failed", "error", err)
		writeError(w, 503, "Verifikasi tidak dapat diselesaikan")
		return
	}
	if err = tx.Commit(ctx); err != nil {
		slog.Error("email verification: commit failed", "error", err)
		writeError(w, 503, "Verifikasi tidak dapat diselesaikan")
		return
	}
	state, err := sessionStateFor(r)
	if err != nil {
		writeError(w, 500, "Session tidak tersedia")
		return
	}
	state.Clear()
	state.SetClient(userID, false)
	csrf, err := state.CSRFToken()
	if err != nil {
		writeError(w, 500, "Token keamanan tidak dapat dibuat")
		return
	}
	writeJSON(w, 200, map[string]any{"status": "verified", "user_id": userID, "csrf_token": csrf})
}

func (api authClientAPI) requestPasswordReset(w http.ResponseWriter, r *http.Request) {
	var p struct {
		Email string `json:"email"`
	}
	if err := decodeJSON(w, r, &p, 32<<10); err != nil {
		writeError(w, 422, "Format email tidak valid")
		return
	}
	email, ok := validEmail(p.Email)
	if !ok {
		writeError(w, 422, "Format email tidak valid")
		return
	}
	generic := map[string]any{"status": "accepted", "retry_after_seconds": 60}
	if !api.email.configured {
		writeJSON(w, http.StatusAccepted, generic)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	emailKey := resetEmailHash(api.appSecret, email)
	tx, err := api.pool.Begin(ctx)
	if err != nil {
		writeJSON(w, http.StatusAccepted, generic)
		return
	}
	defer tx.Rollback(ctx)
	if err = authLock(ctx, tx, "password-reset:"+emailKey); err != nil {
		writeJSON(w, http.StatusAccepted, generic)
		return
	}
	now := time.Now().UTC()
	_, _ = tx.Exec(ctx, `DELETE FROM password_reset_codes WHERE last_activity_at < $1`, now.Add(-2*time.Hour))
	var userExists bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM client_users WHERE email=$1)`, email).Scan(&userExists); err != nil || !userExists {
		_ = tx.Rollback(ctx)
		writeJSON(w, http.StatusAccepted, generic)
		return
	}
	var codeHash string
	var expiry, resend time.Time
	var attempts, sends int
	var window, locked sql.NullTime
	err = tx.QueryRow(ctx, `SELECT code_hash,expires_at,resend_after,attempts,send_count,send_window_started_at,locked_until FROM password_reset_codes WHERE email_hash=$1 FOR UPDATE`, emailKey).Scan(&codeHash, &expiry, &resend, &attempts, &sends, &window, &locked)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		writeJSON(w, http.StatusAccepted, generic)
		return
	}
	exists := err == nil
	if exists && recordWait(locked, sql.NullTime{Time: resend, Valid: true}, window, sends, now) > 0 {
		_ = tx.Rollback(ctx)
		writeJSON(w, http.StatusAccepted, generic)
		return
	}
	code, err := emailCode()
	if err != nil {
		writeJSON(w, http.StatusAccepted, generic)
		return
	}
	newHash := resetCodeHash(api.appSecret, email, code)
	start := now
	count := 0
	if exists && window.Valid && window.Time.Add(verificationWindow).After(now) {
		start = window.Time
		count = sends
	}
	count++
	if exists {
		_, err = tx.Exec(ctx, `UPDATE password_reset_codes SET code_hash=$2,expires_at=$3,resend_after=$4,attempts=0,send_count=$5,send_window_started_at=$6,locked_until=NULL,last_activity_at=$7 WHERE email_hash=$1`, emailKey, newHash, now.Add(verificationLifetime), now.Add(verificationCooldown), count, start, now)
	} else {
		_, err = tx.Exec(ctx, `INSERT INTO password_reset_codes(email_hash,code_hash,expires_at,resend_after,attempts,send_count,send_window_started_at,locked_until,created_at,last_activity_at) VALUES($1,$2,$3,$4,0,$5,$6,NULL,$7,$7)`, emailKey, newHash, now.Add(verificationLifetime), now.Add(verificationCooldown), count, start, now)
	}
	if err != nil {
		writeJSON(w, http.StatusAccepted, generic)
		return
	}
	if err = tx.Commit(ctx); err != nil {
		writeJSON(w, http.StatusAccepted, generic)
		return
	}
	emailCfg := api.email
	go func() { _ = sendVerificationEmail(emailCfg, email, code, true) }()
	writeJSON(w, http.StatusAccepted, generic)
}

func (api authClientAPI) confirmPasswordReset(w http.ResponseWriter, r *http.Request) {
	var p resetConfirmRequest
	if err := decodeJSON(w, r, &p, 64<<10); err != nil || !authCodePattern.MatchString(p.Code) || len([]rune(p.Password)) < 8 {
		writeError(w, 422, "Data reset kata sandi tidak valid")
		return
	}
	email, ok := validEmail(p.Email)
	if !ok {
		writeError(w, 422, "Format email tidak valid")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	emailKey := resetEmailHash(api.appSecret, email)
	tx, err := api.pool.Begin(ctx)
	if err != nil {
		writeError(w, 503, "Reset kata sandi tidak tersedia")
		return
	}
	defer tx.Rollback(ctx)
	if err = authLock(ctx, tx, "password-reset:"+emailKey); err != nil {
		writeError(w, 503, "Reset kata sandi tidak tersedia")
		return
	}
	var stored string
	var expires, resend time.Time
	var attempts, sends int
	var window, locked sql.NullTime
	err = tx.QueryRow(ctx, `SELECT code_hash,expires_at,resend_after,attempts,send_count,send_window_started_at,locked_until FROM password_reset_codes WHERE email_hash=$1 FOR UPDATE`, emailKey).Scan(&stored, &expires, &resend, &attempts, &sends, &window, &locked)
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, 400, "Kode tidak valid atau kedaluwarsa")
		return
	}
	if err != nil {
		writeError(w, 503, "Kode reset tidak tersedia")
		return
	}
	now := time.Now().UTC()
	if wait := secondsUntil(locked, now); wait > 0 {
		w.Header().Set("Retry-After", strconv.Itoa(wait))
		writeError(w, 429, "Kesempatan memasukkan kode habis. Coba lagi dalam 1 jam.")
		return
	}
	if !expires.After(now) {
		writeError(w, 400, "Kode tidak valid atau kedaluwarsa")
		return
	}
	if !checkHash(stored, resetCodeHash(api.appSecret, email, p.Code)) {
		attempts++
		api.persistResetFailure(ctx, tx, emailKey, attempts, now, w)
		return
	}
	var userID int64
	err = tx.QueryRow(ctx, `SELECT id FROM client_users WHERE email=$1 FOR UPDATE`, email).Scan(&userID)
	if errors.Is(err, sql.ErrNoRows) {
		attempts++
		api.persistResetFailure(ctx, tx, emailKey, attempts, now, w)
		return
	}
	if err != nil {
		writeError(w, 503, "Akun client tidak tersedia")
		return
	}
	passwordHash, err := auth.HashClientPassword(p.Password)
	if err != nil {
		writeError(w, 500, "Kata sandi tidak dapat diproses")
		return
	}
	if _, err = tx.Exec(ctx, `UPDATE client_users SET password_hash=$2 WHERE id=$1`, userID, passwordHash); err != nil {
		writeError(w, 503, "Kata sandi tidak dapat diperbarui")
		return
	}
	if _, err = tx.Exec(ctx, `UPDATE auth_sessions SET revoked_at=$2 WHERE user_type='client' AND user_id=$1 AND revoked_at IS NULL`, strconv.FormatInt(userID, 10), now); err != nil {
		writeError(w, 503, "Sesi akun tidak dapat diakhiri")
		return
	}
	if _, err = tx.Exec(ctx, `DELETE FROM password_reset_codes WHERE email_hash=$1`, emailKey); err != nil {
		writeError(w, 503, "Kode reset tidak dapat dihapus")
		return
	}
	if err = tx.Commit(ctx); err != nil {
		writeError(w, 503, "Kata sandi tidak dapat diperbarui")
		return
	}
	writeJSON(w, 200, map[string]string{"status": "password_reset"})
}

func (api authClientAPI) persistResetFailure(ctx context.Context, tx *dbstore.Tx, emailKey string, attempts int, now time.Time, w http.ResponseWriter) {
	if attempts >= verificationMaxAttempts {
		until := now.Add(verificationWindow)
		_, err := tx.Exec(ctx, `UPDATE password_reset_codes SET attempts=$2,locked_until=$3,resend_after=$3,last_activity_at=$4 WHERE email_hash=$1`, emailKey, attempts, until, now)
		if err != nil {
			writeError(w, 503, "Status reset tidak dapat disimpan")
			return
		}
		if err = tx.Commit(ctx); err != nil {
			writeError(w, 503, "Status reset tidak dapat disimpan")
			return
		}
		w.Header().Set("Retry-After", "3600")
		writeError(w, 429, "Kesempatan memasukkan kode habis. Coba lagi dalam 1 jam.")
		return
	}
	_, err := tx.Exec(ctx, `UPDATE password_reset_codes SET attempts=$2,last_activity_at=$3 WHERE email_hash=$1`, emailKey, attempts, now)
	if err != nil {
		writeError(w, 503, "Status reset tidak dapat disimpan")
		return
	}
	if err = tx.Commit(ctx); err != nil {
		writeError(w, 503, "Status reset tidak dapat disimpan")
		return
	}
	writeError(w, 400, "Kode tidak valid atau kedaluwarsa")
}

func sendVerificationEmail(cfg emailConfig, recipient, code string, passwordReset bool) error {
	if !cfg.configured {
		return errors.New("SMTP email belum dikonfigurasi")
	}
	if strings.ContainsAny(cfg.from+recipient, "\r\n") {
		return errors.New("Alamat email tidak valid")
	}
	from, err := mail.ParseAddress(cfg.from)
	if err != nil {
		return errors.New("Alamat email pengirim tidak valid")
	}
	subject := "Kode verifikasi Algentra Capital"
	heading := "Verifikasi email Anda"
	action := "verifikasi email"
	if passwordReset {
		subject = "Kode reset kata sandi Algentra Capital"
		heading = "Atur ulang kata sandi"
		action = "reset kata sandi"
	}
	boundary := fmt.Sprintf("algentra-%d", time.Now().UnixNano())
	var message bytes.Buffer
	fmt.Fprintf(&message, "From: %s\r\nTo: %s\r\nSubject: %s\r\nMIME-Version: 1.0\r\nContent-Type: multipart/alternative; boundary=%q\r\n\r\n", from.String(), recipient, subject, boundary)
	var textSocial, socialItems strings.Builder
	for _, social := range cfg.socials {
		fmt.Fprintf(&textSocial, "%s: %s\n", social.label, social.url)
		fmt.Fprintf(&socialItems, `<a href="%s" style="display:inline-block;margin:4px 5px;padding:7px 11px;border:1px solid #34463f;border-radius:999px;color:#c4d8ca;text-decoration:none;font-size:12px;line-height:1.2">%s</a>`, html.EscapeString(social.url), html.EscapeString(social.label))
	}
	textLinks := ""
	if textSocial.Len() > 0 {
		textLinks = "\nIkuti Algentra Capital:\n" + textSocial.String()
	}
	fmt.Fprintf(&message, "--%s\r\nContent-Type: text/plain; charset=utf-8\r\n\r\nALGENTRA CAPITAL | KEAMANAN AKUN\n\n%s\nGunakan kode berikut untuk %s akun Algentra Capital:\n\n%s\n\nKode berlaku selama 10 menit dan hanya dapat digunakan satu kali.\nJangan bagikan kode ini kepada siapa pun. Tim Algentra tidak akan meminta OTP Anda.\n\nJika Anda tidak meminta kode ini, abaikan email ini; akun Anda tetap aman.%s\nAlgentra Capital | Trading memiliki risiko.\r\n", boundary, heading, action, code, textLinks)
	socialHTML := ""
	if socialItems.Len() > 0 {
		socialHTML = `<tr><td style="padding:25px 30px 4px;text-align:center"><p style="margin:0 0 8px;color:#9aa9a1;font-size:10px;font-weight:700;letter-spacing:1.5px;text-transform:uppercase">Ikuti Algentra Capital</p><div>` + socialItems.String() + `</div></td></tr>`
	}
	codeHTML, headingHTML, actionHTML := html.EscapeString(code), html.EscapeString(heading), html.EscapeString(action)
	htmlBody := `<!doctype html>
<html lang="id"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"></head>
<body style="margin:0;padding:0;background:#0b1117;color:#e8eee9;font-family:Arial,Helvetica,sans-serif">
<div style="display:none;max-height:0;overflow:hidden;opacity:0;color:transparent">Kode keamanan Algentra Capital Anda berlaku 10 menit.</div>
<table role="presentation" width="100%" cellspacing="0" cellpadding="0" border="0" style="background:#0b1117"><tr><td align="center" style="padding:34px 14px">
<table role="presentation" width="100%" cellspacing="0" cellpadding="0" border="0" style="max-width:560px">
<tr><td style="padding:0 8px 20px"><table role="presentation" cellspacing="0" cellpadding="0" border="0"><tr>
<td width="42" height="42" align="center" valign="middle" style="border:1px solid #536d5e;border-radius:12px;background:#1a2823;color:#d4e5d9;font-size:13px;font-weight:800;letter-spacing:1px">AC</td>
<td style="padding-left:12px;color:#e8eee9;font-size:14px;font-weight:700;letter-spacing:.4px">ALGENTRA CAPITAL<br><span style="color:#98a69f;font-size:10px;font-weight:400;letter-spacing:1px">AI-DRIVEN COPY TRADING</span></td>
</tr></table></td></tr><tr><td style="padding:1px;border:1px solid #30413a;border-radius:18px;background:#111a22"><table role="presentation" width="100%" cellspacing="0" cellpadding="0" border="0">
<tr><td height="4" style="border-radius:18px 18px 0 0;background:#9fbea9;font-size:0;line-height:0">&nbsp;</td></tr>
<tr><td style="padding:34px 34px 10px"><p style="margin:0 0 9px;color:#a8c7b2;font-size:10px;font-weight:700;letter-spacing:1.6px;text-transform:uppercase">Kode keamanan akun</p><h1 style="margin:0 0 12px;color:#edf3ee;font-size:25px;line-height:1.25;letter-spacing:-.4px">` + headingHTML + `</h1><p style="margin:0;color:#aab6b0;font-size:14px;line-height:1.7">Gunakan kode di bawah untuk ` + actionHTML + ` akun Algentra Capital Anda.</p></td></tr>
<tr><td style="padding:18px 34px 12px"><table role="presentation" width="100%" cellspacing="0" cellpadding="0" border="0"><tr><td align="center" style="padding:20px 12px;border:1px solid #3e5549;border-radius:13px;background:#0c1519;color:#e5eee6;font-size:34px;font-weight:800;letter-spacing:10px;font-variant-numeric:tabular-nums">` + codeHTML + `</td></tr></table></td></tr>
<tr><td style="padding:0 34px 23px;text-align:center"><span style="display:inline-block;margin:5px;padding:6px 10px;border-radius:999px;background:#25332b;color:#c1d3c5;font-size:11px">Berlaku 10 menit</span><span style="display:inline-block;margin:5px;padding:6px 10px;border-radius:999px;background:#25332b;color:#c1d3c5;font-size:11px">Sekali pakai</span></td></tr>
<tr><td style="padding:0 34px 30px"><table role="presentation" width="100%" cellspacing="0" cellpadding="0" border="0"><tr><td style="padding:13px 15px;border-left:3px solid #c8b892;border-radius:5px;background:#201f1a;color:#d9d4c4;font-size:12px;line-height:1.65"><strong style="color:#eee3c4">Rahasiakan kode ini.</strong> Tim Algentra tidak akan meminta OTP Anda melalui telepon atau chat.</td></tr></table><p style="margin:18px 0 0;color:#899790;font-size:12px;line-height:1.65">Jika Anda tidak meminta kode ini, abaikan email ini. Akun Anda tetap aman.</p></td></tr></table></td></tr>` + socialHTML + `
<tr><td style="padding:19px 12px 0;color:#6f7d77;text-align:center;font-size:10px;line-height:1.7">Email otomatis untuk keamanan akun Anda.<br>Algentra Capital | Trading memiliki risiko; hasil historis bukan jaminan kinerja mendatang.</td></tr>
</table></td></tr></table></body></html>`
	fmt.Fprintf(&message, "--%s\r\nContent-Type: text/html; charset=utf-8\r\n\r\n%s\r\n--%s--\r\n", boundary, htmlBody, boundary)
	address := net.JoinHostPort(cfg.host, strconv.Itoa(cfg.port))
	dialer := net.Dialer{Timeout: 10 * time.Second}
	connection, err := dialer.Dial("tcp", address)
	if err != nil {
		return err
	}
	_ = connection.SetDeadline(time.Now().Add(10 * time.Second))
	client, err := smtp.NewClient(connection, cfg.host)
	if err != nil {
		_ = connection.Close()
		return err
	}
	defer client.Close()
	if cfg.startTLS {
		if err := client.StartTLS(&tls.Config{ServerName: cfg.host, MinVersion: tls.VersionTLS12}); err != nil {
			return err
		}
	}
	if err := client.Auth(smtp.PlainAuth("", cfg.username, cfg.password, cfg.host)); err != nil {
		return err
	}
	if err := client.Mail(from.Address); err != nil {
		return err
	}
	if err := client.Rcpt(recipient); err != nil {
		return err
	}
	writer, err := client.Data()
	if err != nil {
		return err
	}
	if _, err = writer.Write(message.Bytes()); err != nil {
		_ = writer.Close()
		return err
	}
	if err = writer.Close(); err != nil {
		return err
	}
	return client.Quit()
}
