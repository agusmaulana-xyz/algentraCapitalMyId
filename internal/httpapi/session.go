package httpapi

import (
	"bufio"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	dbstore "github.com/rnpproject/algentra-backend/internal/database"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	adminCookieName       = "alg_admin_session"
	clientCookieName      = "alg_client_session"
	hostAdminCookieName   = "__Host-alg_admin_session"
	hostClientCookieName  = "__Host-alg_client_session"
	sessionDefaultMaxAge  = 12 * time.Hour
	rememberClientMaxAge  = 30 * 24 * time.Hour
	authExpiresSessionKey = "auth_expires_at"
)

type sessionState struct {
	Values         map[string]any
	loadedHash     string
	loadedCookie   string
	expiredCookies []string
	originalJSON   string
}

type sessionContextKey struct{}

func SessionMiddleware(pool *dbstore.Pool, appSecret string, secure bool, next http.Handler) http.Handler {
	adminName, clientName := adminCookieName, clientCookieName
	if secure {
		adminName, clientName = hostAdminCookieName, hostClientCookieName
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		state := &sessionState{Values: make(map[string]any)}
		cookieNames := []struct{ userType, cookie string }{{"admin", adminName}, {"client", clientName}}
		for _, candidate := range cookieNames {
			cookie, err := r.Cookie(candidate.cookie)
			if err != nil || cookie.Value == "" {
				continue
			}
			loadCtx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
			loaded, valid, loadErr := loadSession(loadCtx, pool, appSecret, candidate.userType, cookie.Value)
			cancel()
			if loadErr != nil {
				writeError(w, http.StatusServiceUnavailable, "Session tidak dapat diperiksa")
				return
			}
			if !valid {
				state.expiredCookies = append(state.expiredCookies, candidate.cookie)
				continue
			}
			state.Values = loaded.Values
			state.loadedHash = loaded.loadedHash
			state.loadedCookie = candidate.cookie
			state.originalJSON = loaded.originalJSON
			break
		}
		r = r.WithContext(context.WithValue(r.Context(), sessionContextKey{}, state))
		writer := &sessionResponseWriter{ResponseWriter: w}
		writer.beforeWrite = func() error {
			persistCtx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
			defer cancel()
			return persistSession(persistCtx, w, pool, appSecret, secure, state)
		}
		next.ServeHTTP(writer, r)
	})
}

func loadSession(ctx context.Context, pool *dbstore.Pool, appSecret, userType, token string) (*sessionState, bool, error) {
	hash := sessionHash(appSecret, token)
	var storedType, userID, data string
	var expiresAtRaw string
	var revokedAtRaw sql.NullString
	err := pool.QueryRow(ctx, `
		SELECT user_type, user_id, session_data, expires_at, revoked_at
		FROM auth_sessions WHERE session_hash = $1
	`, hash).Scan(&storedType, &userID, &data, &expiresAtRaw, &revokedAtRaw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	expiresAt, err := parseStoredTimestamp(expiresAtRaw)
	if err != nil {
		return nil, false, fmt.Errorf("parse waktu kedaluwarsa sesi: %w", err)
	}
	var revokedAt *time.Time
	if revokedAtRaw.Valid {
		parsed, err := parseStoredTimestamp(revokedAtRaw.String)
		if err != nil {
			return nil, false, fmt.Errorf("parse waktu pencabutan sesi: %w", err)
		}
		revokedAt = &parsed
	}
	now := time.Now().UTC()
	if revokedAt != nil || !expiresAt.After(now) || storedType != userType {
		return nil, false, nil
	}
	values := make(map[string]any)
	if err := json.Unmarshal([]byte(data), &values); err != nil {
		return nil, false, nil
	}
	_, hasAdmin := values["admin_username"]
	_, hasClient := values["client_user_id"]
	if hasAdmin || hasClient {
		expiry, exists := values[authExpiresSessionKey]
		seconds, ok := expiry.(float64)
		if !exists || !ok || float64(now.Unix()) >= seconds {
			if _, err := pool.Exec(ctx, `UPDATE auth_sessions SET revoked_at = $2 WHERE session_hash = $1 AND revoked_at IS NULL`, hash, now); err != nil {
				return nil, false, err
			}
			return nil, false, nil
		}
	}
	if _, err := pool.Exec(ctx, `UPDATE auth_sessions SET last_seen_at = $2 WHERE session_hash = $1`, hash, now); err != nil {
		return nil, false, err
	}
	canonical, err := json.Marshal(values)
	if err != nil {
		return nil, false, nil
	}
	return &sessionState{Values: values, loadedHash: hash, originalJSON: string(canonical)}, true, nil
}

func parseStoredTimestamp(value string) (time.Time, error) {
	value = strings.TrimSpace(value)
	for _, layout := range []string{
		time.RFC3339Nano,
		"2006-01-02 15:04:05.999999999 -0700 MST",
		"2006-01-02 15:04:05 -0700 MST",
		"2006-01-02 15:04:05.999999999-07:00",
		"2006-01-02 15:04:05.999999999",
		"2006-01-02 15:04:05-07:00",
		"2006-01-02 15:04:05",
	} {
		if parsed, err := time.Parse(layout, value); err == nil {
			return parsed, nil
		}
	}
	return time.Time{}, fmt.Errorf("format timestamp tidak dikenal: %q", value)
}

func persistSession(ctx context.Context, w http.ResponseWriter, pool *dbstore.Pool, appSecret string, secure bool, state *sessionState) error {
	for _, name := range state.expiredCookies {
		setExpiredCookie(w, name, secure)
	}
	serialized, err := json.Marshal(state.Values)
	if err != nil {
		return err
	}
	if string(serialized) == state.originalJSON {
		return nil
	}
	if state.loadedHash != "" {
		if _, err := pool.Exec(ctx, `UPDATE auth_sessions SET revoked_at = $2 WHERE session_hash = $1 AND revoked_at IS NULL`, state.loadedHash, time.Now().UTC()); err != nil {
			return err
		}
	}
	userType, userID := sessionIdentity(state.Values)
	if userType == "" {
		if state.loadedCookie != "" {
			setExpiredCookie(w, state.loadedCookie, secure)
		}
		return nil
	}
	maxAge := sessionDefaultMaxAge
	if remember, _ := state.Values["remember_client"].(bool); remember && userType == "client" {
		maxAge = rememberClientMaxAge
	}
	now := time.Now().UTC()
	expiresAt := now.Add(maxAge)
	if expiry, ok := state.Values[authExpiresSessionKey].(float64); ok {
		expiresAt = time.Unix(int64(expiry), 0).UTC()
	}
	rawToken := make([]byte, 32)
	if _, err := rand.Read(rawToken); err != nil {
		return err
	}
	token := base64.RawURLEncoding.EncodeToString(rawToken)
	hash := sessionHash(appSecret, token)
	if _, err := pool.Exec(ctx, `
		INSERT INTO auth_sessions (session_hash, user_type, user_id, session_data, created_at, last_seen_at, expires_at)
		VALUES ($1, $2, $3, $4, $5, $5, $6)
	`, hash, userType, userID, string(serialized), now, expiresAt); err != nil {
		return err
	}
	newName := cookieNameFor(userType, secure)
	if state.loadedCookie != "" && state.loadedCookie != newName {
		setExpiredCookie(w, state.loadedCookie, secure)
	}
	maxAgeSeconds := int(time.Until(expiresAt).Seconds())
	if maxAgeSeconds < 0 {
		maxAgeSeconds = 0
	}
	http.SetCookie(w, &http.Cookie{
		Name: newName, Value: token, Path: "/", MaxAge: maxAgeSeconds,
		HttpOnly: true, Secure: secure, SameSite: http.SameSiteLaxMode,
	})
	return nil
}

func sessionIdentity(values map[string]any) (string, string) {
	if username, ok := values["admin_username"].(string); ok && username != "" {
		return "admin", username
	}
	switch id := values["client_user_id"].(type) {
	case int:
		if id > 0 {
			return "client", strconv.Itoa(id)
		}
	case int64:
		if id > 0 {
			return "client", strconv.FormatInt(id, 10)
		}
	case float64:
		if id > 0 {
			return "client", strconv.FormatInt(int64(id), 10)
		}
	}
	return "", ""
}

func sessionHash(secret, token string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(token))
	return hex.EncodeToString(mac.Sum(nil))
}

func cookieNameFor(userType string, secure bool) string {
	if userType == "admin" {
		if secure {
			return hostAdminCookieName
		}
		return adminCookieName
	}
	if secure {
		return hostClientCookieName
	}
	return clientCookieName
}

func setExpiredCookie(w http.ResponseWriter, name string, secure bool) {
	http.SetCookie(w, &http.Cookie{
		Name: name, Value: "", Path: "/", MaxAge: -1, Expires: time.Unix(0, 0),
		HttpOnly: true, Secure: secure, SameSite: http.SameSiteLaxMode,
	})
}

func SessionFromRequest(r *http.Request) *sessionState {
	state, _ := r.Context().Value(sessionContextKey{}).(*sessionState)
	return state
}

func ClientIDFromRequest(r *http.Request) (int64, bool) {
	state := SessionFromRequest(r)
	if state == nil {
		return 0, false
	}
	switch value := state.Values["client_user_id"].(type) {
	case int:
		return int64(value), value > 0
	case int64:
		return value, value > 0
	case float64:
		return int64(value), value > 0 && value == float64(int64(value))
	default:
		return 0, false
	}
}

func (s *sessionState) SetAdmin(username string) {
	s.Values = map[string]any{
		"admin_username":  username,
		"auth_expires_at": time.Now().UTC().Add(sessionDefaultMaxAge).Unix(),
		"remember_client": false,
	}
}

func (s *sessionState) SetClient(userID int64, remember bool) {
	maxAge := sessionDefaultMaxAge
	if remember {
		maxAge = rememberClientMaxAge
	}
	s.Values = map[string]any{
		"client_user_id":  userID,
		"auth_expires_at": float64(time.Now().UTC().Add(maxAge).Unix()),
		"remember_client": remember,
	}
}

func (s *sessionState) CSRFToken() (string, error) {
	if token, ok := s.Values["csrf_token"].(string); ok && len(token) >= 32 {
		return token, nil
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	s.Values["csrf_token"] = token
	return token, nil
}

func (s *sessionState) RequireCSRF(r *http.Request) bool {
	expected, _ := s.Values["csrf_token"].(string)
	supplied := r.Header.Get("X-CSRF-Token")
	return len(expected) > 0 && len(supplied) > 0 && subtle.ConstantTimeCompare([]byte(expected), []byte(supplied)) == 1
}

type sessionResponseWriter struct {
	http.ResponseWriter
	beforeWrite func() error
	committed   bool
	failed      bool
}

func (w *sessionResponseWriter) WriteHeader(status int) {
	if w.committed {
		return
	}
	w.committed = true
	if err := w.beforeWrite(); err != nil {
		w.failed = true
		w.Header().Set("Content-Type", "application/json")
		w.ResponseWriter.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.ResponseWriter.Write([]byte(`{"detail":"Session tidak dapat disimpan"}`))
		return
	}
	w.ResponseWriter.WriteHeader(status)
}

func (w *sessionResponseWriter) Write(body []byte) (int, error) {
	if !w.committed {
		w.WriteHeader(http.StatusOK)
	}
	if w.failed {
		return 0, nil
	}
	return w.ResponseWriter.Write(body)
}

func (w *sessionResponseWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w *sessionResponseWriter) Flush() {
	if !w.committed {
		w.WriteHeader(http.StatusOK)
	}
	if w.failed {
		return
	}
	if flusher, ok := w.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

func (w *sessionResponseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if !w.committed {
		if err := w.beforeWrite(); err != nil {
			w.failed = true
			return nil, nil, err
		}
		w.committed = true
	}
	hijacker, ok := w.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, errors.New("HTTP connection does not support hijacking")
	}
	return hijacker.Hijack()
}

func (s *sessionState) Clear() { s.Values = make(map[string]any) }

func sessionStateFor(r *http.Request) (*sessionState, error) {
	s := SessionFromRequest(r)
	if s == nil {
		return nil, fmt.Errorf("session middleware tidak tersedia")
	}
	return s, nil
}
