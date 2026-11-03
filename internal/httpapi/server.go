package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	dbstore "github.com/rnpproject/algentra-backend/internal/database"
	"io"
	"net/http"
	"time"

	"github.com/rnpproject/algentra-backend/internal/auth"
	"github.com/rnpproject/algentra-backend/internal/database"
	"github.com/rnpproject/algentra-backend/internal/ea/snapshot"
	telegramclient "github.com/rnpproject/algentra-backend/internal/telegram"
)

type ServerConfig struct {
	Environment           string
	MT5BrokerDirectoryURL string
	ServiceName           string
	CookieSecure          bool
	WebSocketOrigins      []string
	TelegramManager       *telegramclient.Manager
	AppSecretKey          string
	WorkerAPIKey          string
	EAAPIKey              string
	Contact               ContactConfig
	Pricing               ServicePricing
	GeminiAPIKey          string
	GeminiModel           string
	ProjectRoot           string
	PaymentBankName       string
	PaymentNumber         string
	PaymentName           string
	AirPayConfigured      bool
	AirPayAPIKey          string
	AirPayMerchantUID     string
	TelegramBotToken      string
	TelegramAdminChatID   string
	BotContext            context.Context
	EmailConfigured       bool
	EmailSMTPHost         string
	EmailSMTPPort         int
	EmailSMTPUsername     string
	EmailSMTPPassword     string
	EmailFrom             string
	EmailSMTPStartTLS     bool
}

type ContactConfig struct {
	PersonName    string
	WhatsApp      string
	Email         string
	Instagram     string
	Telegram      string
	TelegramAdmin string
	TikTok        string
	Website       string
	Facebook      string
	LinkedIn      string
	X             string
	YouTube       string
}

type VPSPlan struct {
	Code     string
	Name     string
	VCPU     string
	RAM      string
	Disk     string
	PriceIDR int64
}

type ServicePricing struct {
	ManagementFeeIDR int64
	VPSOptions       []VPSPlan
}

func NewHandler(pool *dbstore.Pool, cfg ServerConfig) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			writeMethodNotAllowed(w, http.MethodGet)
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "service": cfg.ServiceName})
	})
	mux.HandleFunc("/ready", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			writeMethodNotAllowed(w, http.MethodGet)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		if err := database.CheckReady(ctx, pool, "", ""); err != nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{
				"status": "not_ready", "checks": map[string]string{"database": "unavailable"},
			})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"status": "ready", "checks": map[string]string{"database": "ok"}})
	})
	for _, prefix := range []string{"/api/auth", "/v1/auth"} {
		mux.HandleFunc(prefix+"/login", adminLogin(pool))
		mux.HandleFunc(prefix+"/client-login", clientLogin(pool))
		mux.HandleFunc(prefix+"/session", sessionStatus())
		mux.HandleFunc(prefix+"/logout", logout())
	}
	authClientAPI{pool: pool, appSecret: cfg.AppSecretKey, email: emailConfig{configured: cfg.EmailConfigured, host: cfg.EmailSMTPHost, port: cfg.EmailSMTPPort, username: cfg.EmailSMTPUsername, password: cfg.EmailSMTPPassword, from: cfg.EmailFrom, startTLS: cfg.EmailSMTPStartTLS, socials: configuredEmailSocials(cfg.Contact)}}.register(mux)
	eaSnapshot := eaSnapshotAPI{pool: pool, key: cfg.EAAPIKey, state: &snapshot.Store{}}
	mt5Updates := newMT5UpdateHub()
	eaSnapshot.register(mux)
	registerPublicRoutes(mux, pool, cfg.Contact, eaSnapshot.state, cfg.WebSocketOrigins)
	registerDownloads(mux, cfg.ProjectRoot)
	mux.HandleFunc("GET /api/mt5/servers", mt5ServerDirectory(cfg.MT5BrokerDirectoryURL))
	(clientMT5API{pool: pool, state: eaSnapshot.state, pricing: cfg.Pricing, workerConfigured: cfg.WorkerAPIKey != "", copierAPIKey: cfg.WorkerAPIKey, appSecret: cfg.AppSecretKey, updates: mt5Updates}).register(mux)
	adminAccounts := mt5AccountDetailsAPI{pool: pool}
	for _, prefix := range []string{"/api/admin", "/v1/admin"} {
		mux.HandleFunc("GET "+prefix+"/copy-accounts", adminAccounts.list)
		mux.HandleFunc("GET "+prefix+"/copy-accounts/{account_id}/details", adminAccounts.details)
	}
	payments := paymentAPI{pool: pool, pricing: cfg.Pricing, workerConfigured: cfg.WorkerAPIKey != "", bankName: cfg.PaymentBankName, paymentNumber: cfg.PaymentNumber, paymentName: cfg.PaymentName, qrisAvailable: cfg.AirPayConfigured, projectRoot: cfg.ProjectRoot, airPayAPIKey: cfg.AirPayAPIKey, airPayMerchantUID: cfg.AirPayMerchantUID}
	if cfg.TelegramBotToken != "" && cfg.TelegramAdminChatID != "" {
		payments.bot = &telegramPaymentBot{pool: pool, token: cfg.TelegramBotToken, chatID: cfg.TelegramAdminChatID, pricing: cfg.Pricing, projectRoot: cfg.ProjectRoot}
		botCtx := cfg.BotContext
		if botCtx == nil {
			botCtx = context.Background()
		}
		go payments.bot.run(botCtx)
	}
	payments.register(mux)
	settingsAPI{pool: pool, appEnv: cfg.Environment, eaAPIKey: cfg.EAAPIKey, geminiAPIKey: cfg.GeminiAPIKey, geminiModel: cfg.GeminiModel}.register(mux)
	dashboardAPI{pool: pool, state: eaSnapshot.state, telegram: cfg.TelegramManager, geminiKey: cfg.GeminiAPIKey, geminiModel: cfg.GeminiModel, websocketOrigin: cfg.WebSocketOrigins}.register(mux)
	parser := parserAPI{pool: pool, key: cfg.GeminiAPIKey, model: cfg.GeminiModel, timeout: 25 * time.Second, retries: 3}
	telegramAPI{pool: pool, manager: cfg.TelegramManager, parser: parser}.register(mux)
	parser.register(mux)
	customerServiceAPI{pool: pool, ctx: cfg.BotContext, secret: cfg.AppSecretKey, cookieSecure: cfg.CookieSecure, key: cfg.GeminiAPIKey, model: cfg.GeminiModel, timeout: 60 * time.Second}.register(mux)
	copierRoutes := copierAPI{pool: pool, apiKey: cfg.WorkerAPIKey, secret: cfg.AppSecretKey, master: eaSnapshot.state, updates: mt5Updates}
	copierRoutes.register(mux, "/api/mt5")
	copierRoutes.register(mux, "/v1/mt5")
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusNotFound, map[string]string{"detail": "Not Found"})
	})
	handler := SessionMiddleware(pool, cfg.AppSecretKey, cfg.CookieSecure, mux)
	handler = OriginProtection(handler)
	handler = RateLimit(handler)
	return SecurityHeaders(handler, cfg.CookieSecure)
}

func clientLogin(pool *dbstore.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeMethodNotAllowed(w, http.MethodPost)
			return
		}
		var payload struct {
			Email      string `json:"email"`
			Password   string `json:"password"`
			RememberMe bool   `json:"remember_me"`
		}
		if err := decodeJSON(w, r, &payload, 64<<10); err != nil {
			writeError(w, http.StatusUnprocessableEntity, "Email atau kata sandi tidak valid")
			return
		}
		email, valid := validEmail(payload.Email)
		if !valid || payload.Password == "" {
			writeError(w, http.StatusUnprocessableEntity, "Format email tidak valid")
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		userID, err := auth.AuthenticateClient(ctx, pool, email, payload.Password)
		if errors.Is(err, auth.ErrInvalidCredentials) {
			writeError(w, http.StatusUnauthorized, "Email atau kata sandi salah")
			return
		}
		if err != nil {
			writeError(w, http.StatusServiceUnavailable, "Autentikasi tidak tersedia")
			return
		}
		state, err := sessionStateFor(r)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "Session tidak tersedia")
			return
		}
		state.SetClient(userID, payload.RememberMe)
		token, err := state.CSRFToken()
		if err != nil {
			writeError(w, http.StatusInternalServerError, "Token keamanan tidak dapat dibuat")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"status": "authenticated", "user_id": userID, "csrf_token": token})
	}
}

func SecurityHeaders(next http.Handler, secureCookies bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", "default-src 'self'; base-uri 'self'; frame-ancestors 'none'; object-src 'none'; img-src 'self' data:; font-src 'self' data:; media-src 'self'; connect-src 'self'; form-action 'self'")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")
		w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		if secureCookies {
			w.Header().Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
		}
		next.ServeHTTP(w, r)
	})
}

func writeMethodNotAllowed(w http.ResponseWriter, allowed string) {
	w.Header().Set("Allow", allowed)
	writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"detail": "Method Not Allowed"})
}

func adminLogin(pool *dbstore.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeMethodNotAllowed(w, http.MethodPost)
			return
		}
		var payload struct {
			Username string `json:"username"`
			Password string `json:"password"`
		}
		if err := decodeJSON(w, r, &payload, 64<<10); err != nil {
			writeError(w, http.StatusUnprocessableEntity, "Username atau password tidak valid")
			return
		}
		if len([]rune(payload.Username)) < 1 || len([]rune(payload.Username)) > 128 || len([]rune(payload.Password)) < 1 || len([]rune(payload.Password)) > 1024 {
			writeError(w, http.StatusUnprocessableEntity, "Username atau password tidak valid")
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		err := auth.AuthenticateAdmin(ctx, pool, payload.Username, payload.Password)
		if errors.Is(err, auth.ErrInvalidCredentials) {
			writeError(w, http.StatusUnauthorized, "Username atau password salah")
			return
		}
		if err != nil {
			writeError(w, http.StatusServiceUnavailable, "Autentikasi tidak tersedia")
			return
		}
		state, err := sessionStateFor(r)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "Session tidak tersedia")
			return
		}
		state.SetAdmin(payload.Username)
		token, err := state.CSRFToken()
		if err != nil {
			writeError(w, http.StatusInternalServerError, "Token keamanan tidak dapat dibuat")
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "authenticated", "username": payload.Username, "csrf_token": token})
	}
}

func sessionStatus() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			writeMethodNotAllowed(w, http.MethodGet)
			return
		}
		state, err := sessionStateFor(r)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "Session tidak tersedia")
			return
		}
		token, err := state.CSRFToken()
		if err != nil {
			writeError(w, http.StatusInternalServerError, "Token keamanan tidak dapat dibuat")
			return
		}
		_, admin := state.Values["admin_username"]
		_, client := state.Values["client_user_id"]
		writeJSON(w, http.StatusOK, map[string]any{"admin": admin, "client": client, "csrf_token": token})
	}
}

func logout() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeMethodNotAllowed(w, http.MethodPost)
			return
		}
		state, err := sessionStateFor(r)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "Session tidak tersedia")
			return
		}
		if !state.RequireCSRF(r) {
			writeError(w, http.StatusForbidden, "Token CSRF tidak valid atau kedaluwarsa")
			return
		}
		state.Clear()
		writeJSON(w, http.StatusOK, map[string]string{"status": "logged_out"})
	}
}

func decodeJSON(w http.ResponseWriter, r *http.Request, target any, maximum int64) error {
	r.Body = http.MaxBytesReader(w, r.Body, maximum)
	decoder := json.NewDecoder(r.Body)
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return errors.New("request contains extra JSON data")
	}
	return nil
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, detail string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"detail": detail})
}
