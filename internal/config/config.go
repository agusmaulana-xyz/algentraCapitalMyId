package config

import (
	"bufio"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"
)

type Settings struct {
	AppEnv                string
	AppSecretKey          string
	AdminUsername         string
	AdminPassword         string
	CookieSecure          bool
	DatabaseFile          string
	MT5BrokerDirectoryURL string
	EAAPIKey              string
	CopierWorkerAPIKey    string
	HTTPAddr              string
	WebSocketOrigins      []string
	ContactPersonName     string
	ContactWhatsApp       string
	ContactEmail          string
	ContactInstagram      string
	ContactTelegram       string
	ContactTelegramAdmin  string
	ContactTikTok         string
	ContactWebsite        string
	ContactFacebook       string
	ContactLinkedIn       string
	ContactX              string
	ContactYouTube        string
	GeminiAPIKey          string
	GeminiModel           string
	PaymentBankName       string
	PaymentNumber         string
	PaymentName           string
	AirPayConfigured      bool
	AirPayAPIKey          string
	AirPayMerchantUID     string
	TelegramBotToken      string
	TelegramAdminChatID   string
	TelegramAPIID         int
	TelegramAPIHash       string
	EmailSMTPHost         string
	EmailSMTPPort         int
	EmailSMTPUsername     string
	EmailSMTPPassword     string
	EmailFrom             string
	EmailSMTPStartTLS     bool
	EmailConfigured       bool
	ManagementFeeIDR      int64
	VPSOptions            []VPSOption
}

type VPSOption struct {
	Code     string
	Name     string
	VCPU     string
	RAM      string
	Disk     string
	PriceIDR int64
}

func Load() (Settings, error) {
	if strings.ToLower(valueOr(os.Getenv("APP_ENV"), "development")) == "development" {
		path := os.Getenv("ALGENTRA_ENV_FILE")
		if path == "" {
			path = filepath.Join(".env.local")
		}
		if err := loadEnvFile(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return Settings{}, fmt.Errorf("baca file environment: %w", err)
		}
	}
	s := Settings{
		AppEnv:                strings.ToLower(valueOr(os.Getenv("APP_ENV"), "development")),
		AppSecretKey:          os.Getenv("APP_SECRET_KEY"),
		AdminUsername:         valueOr(os.Getenv("ADMIN_USERNAME"), "admin"),
		AdminPassword:         os.Getenv("ADMIN_PASSWORD"),
		DatabaseFile:          strings.TrimSpace(os.Getenv("DATABASE_FILE")),
		MT5BrokerDirectoryURL: valueOr(strings.TrimSpace(os.Getenv("MT5_BROKER_DIRECTORY_URL")), "https://mt5.mtapi.io/Search"),
		EAAPIKey:              os.Getenv("EA_API_KEY"),
		CopierWorkerAPIKey:    os.Getenv("COPIER_WORKER_API_KEY"),
		HTTPAddr:              valueOr(os.Getenv("HTTP_ADDR"), "127.0.0.1:8000"),
		ContactPersonName:     valueOr(os.Getenv("CONTACT_PERSON_NAME"), "Algentra Capital"),
		ContactWhatsApp:       valueOr(os.Getenv("CONTACT_WHATSAPP"), "0852000000"),
		ContactEmail:          valueOr(os.Getenv("CONTACT_EMAIL"), "algentracapital@gmail.com"),
		ContactInstagram:      valueOr(os.Getenv("CONTACT_INSTAGRAM"), "https://www.instagram.com/algentracapital.id/"),
		ContactTelegram:       valueOr(os.Getenv("CONTACT_TELEGRAM"), "https://t.me/algentra_capital"),
		ContactTelegramAdmin:  valueOr(os.Getenv("CONTACT_TELEGRAM_ADMIN"), "https://t.me/algentracapital"),
		ContactTikTok:         valueOr(os.Getenv("CONTACT_TIKTOK"), "https://www.tiktok.com/algentracapital.id/"),
		ContactWebsite:        valueOr(os.Getenv("CONTACT_WEBSITE"), "https://algentracapital.my.id"),
		ContactFacebook:       valueOr(os.Getenv("CONTACT_FACEBOOK"), "https://www.facebook.com/algentracapital.id/"),
		ContactLinkedIn:       valueOr(os.Getenv("CONTACT_LINKEDIN"), "https://www.linkedin.com/algentracapital.id/"),
		ContactX:              valueOr(os.Getenv("CONTACT_X"), "https://www.instagram.com/algentracapital.id/"),
		ContactYouTube:        valueOr(os.Getenv("CONTACT_YOUTUBE"), "https://www.instagram.com/algentracapital.id/"),
		GeminiAPIKey:          strings.TrimSpace(os.Getenv("GEMINI_API_KEY")),
		GeminiModel:           valueOr(os.Getenv("GEMINI_MODEL"), "gemini-3.5-flash-lite"),
		PaymentBankName:       strings.TrimSpace(os.Getenv("PAYMENT_BANK_NAME")),
		PaymentNumber:         strings.TrimSpace(os.Getenv("PAYMENT_NUMBER")),
		PaymentName:           strings.TrimSpace(os.Getenv("PAYMENT_NAME")),
		AirPayAPIKey:          strings.TrimSpace(os.Getenv("AIRPAY_API_KEY")),
		AirPayMerchantUID:     strings.TrimSpace(os.Getenv("AIRPAY_MERCHANT_UID")),
		TelegramBotToken:      strings.TrimSpace(os.Getenv("TELEGRAM_BOT_TOKEN")),
		TelegramAdminChatID:   strings.TrimSpace(os.Getenv("TELEGRAM_ADMIN_CHAT_ID")),
		EmailSMTPHost:         strings.TrimSpace(os.Getenv("EMAIL_SMTP_HOST")),
		EmailSMTPUsername:     strings.TrimSpace(os.Getenv("EMAIL_SMTP_USERNAME")),
		EmailSMTPPassword:     os.Getenv("EMAIL_SMTP_PASSWORD"),
		EmailFrom:             strings.TrimSpace(os.Getenv("EMAIL_FROM")),
	}
	if s.DatabaseFile == "" {
		root := strings.TrimSpace(os.Getenv("PROJECT_ROOT"))
		if root == "" {
			root, _ = os.Getwd()
		}
		s.DatabaseFile = filepath.Join(root, "data", "database.sqlite")
	}
	var err error
	s.CookieSecure, err = boolEnv("COOKIE_SECURE", s.AppEnv == "production")
	if err != nil {
		return Settings{}, err
	}
	websocketOrigins := strings.TrimSpace(os.Getenv("WEBSOCKET_ALLOWED_ORIGINS"))
	if websocketOrigins == "" {
		if domain := strings.TrimSpace(os.Getenv("PUBLIC_DOMAIN")); domain != "" {
			websocketOrigins = "https://" + domain
		} else {
			websocketOrigins = "http://127.0.0.1:3000,http://localhost:3000"
		}
	}
	s.WebSocketOrigins, err = normalizeOrigins(websocketOrigins)
	if err != nil {
		return Settings{}, err
	}
	s.AirPayConfigured = s.AirPayAPIKey != "" && s.AirPayMerchantUID != ""
	s.EmailSMTPPort, err = intEnv("EMAIL_SMTP_PORT", 587)
	if err != nil || s.EmailSMTPPort < 1 || s.EmailSMTPPort > 65535 {
		return Settings{}, errors.New("EMAIL_SMTP_PORT harus antara 1 dan 65535")
	}
	s.EmailSMTPStartTLS, err = boolEnv("EMAIL_SMTP_STARTTLS", true)
	if err != nil {
		return Settings{}, err
	}
	configuredEmail := 0
	for _, value := range []string{s.EmailSMTPHost, s.EmailSMTPUsername, s.EmailSMTPPassword, s.EmailFrom} {
		if value != "" {
			configuredEmail++
		}
	}
	if configuredEmail != 0 && configuredEmail != 4 {
		return Settings{}, errors.New("EMAIL_SMTP_HOST, EMAIL_SMTP_USERNAME, EMAIL_SMTP_PASSWORD, dan EMAIL_FROM harus diisi bersama")
	}
	s.EmailConfigured = configuredEmail == 4
	s.TelegramAPIID, err = intEnv("TELEGRAM_API_ID", 0)
	if err != nil {
		return Settings{}, err
	}
	s.TelegramAPIHash = strings.TrimSpace(os.Getenv("TELEGRAM_API_HASH"))
	if err := s.Validate(); err != nil {
		return Settings{}, err
	}
	if err := loadServicePricing(&s); err != nil {
		return Settings{}, err
	}
	return s, nil
}

func loadServicePricing(s *Settings) error {
	fee, err := int64Env("MANAGEMENT_FEE_IDR", 150000)
	if err != nil || fee < 0 {
		return errors.New("MANAGEMENT_FEE_IDR harus integer nonnegatif")
	}
	s.ManagementFeeIDR = fee
	for i := 1; i <= 5; i++ {
		prefix := fmt.Sprintf("VPS%d_", i)
		name := strings.TrimSpace(os.Getenv(prefix + "NAME"))
		if len([]rune(name)) > 80 {
			return fmt.Errorf("%sNAME maksimal 80 karakter", prefix)
		}
		if name == "" {
			continue
		}
		vcpu, ram, disk := strings.TrimSpace(os.Getenv(prefix+"VCPU")), strings.TrimSpace(os.Getenv(prefix+"RAM")), strings.TrimSpace(os.Getenv(prefix+"DISK"))
		if len([]rune(vcpu)) > 32 || len([]rune(ram)) > 32 || len([]rune(disk)) > 32 {
			return fmt.Errorf("spesifikasi %s melebihi 32 karakter", strings.TrimSuffix(prefix, "_"))
		}
		price, err := int64Env(prefix+"PRICE_IDR", 0)
		if err != nil || price < 0 {
			return fmt.Errorf("%sPRICE_IDR harus integer nonnegatif", prefix)
		}
		if vcpu == "" {
			vcpu = "—"
		}
		if ram == "" {
			ram = "—"
		}
		if disk == "" {
			disk = "—"
		}
		s.VPSOptions = append(s.VPSOptions, VPSOption{Code: fmt.Sprintf("VPS%d", i), Name: name, VCPU: vcpu, RAM: ram, Disk: disk, PriceIDR: price})
	}
	return nil
}

func int64Env(key string, fallback int64) (int64, error) {
	value, ok := os.LookupEnv(key)
	if !ok || strings.TrimSpace(value) == "" {
		return fallback, nil
	}
	parsed, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
	return parsed, err
}

func intEnv(key string, fallback int) (int, error) {
	v, err := int64Env(key, int64(fallback))
	if err != nil {
		return 0, err
	}
	if v > int64(^uint(0)>>1) || v < 0 {
		return 0, errors.New(key + " tidak valid")
	}
	return int(v), nil
}

func (s Settings) Validate() error {
	if s.AppEnv != "production" && s.AppEnv != "development" && s.AppEnv != "test" {
		return errors.New("APP_ENV harus production, development, atau test")
	}
	if s.AppEnv == "production" && strings.ToLower(os.Getenv("APP_ENV")) != "production" {
		return errors.New("APP_ENV=production harus disetel pada environment proses")
	}
	if len(s.AppSecretKey) < 32 || hasPlaceholder(s.AppSecretKey, "replace-with", "change-this", "changethis", "your-secret") {
		return errors.New("APP_SECRET_KEY harus berupa secret acak minimal 32 karakter")
	}
	if len(s.AdminPassword) < 12 || hasPlaceholder(s.AdminPassword, "replace-with", "change-this", "changethis", "your-password") {
		return errors.New("ADMIN_PASSWORD harus berupa password unik minimal 12 karakter")
	}
	if strings.TrimSpace(s.AdminUsername) == "" {
		return errors.New("ADMIN_USERNAME tidak boleh kosong")
	}
	if utf8.RuneCountInString(s.PaymentBankName) > 120 {
		return errors.New("PAYMENT_BANK_NAME maksimal 120 karakter")
	}
	if utf8.RuneCountInString(s.PaymentNumber) > 80 {
		return errors.New("PAYMENT_NUMBER maksimal 80 karakter")
	}
	if utf8.RuneCountInString(s.PaymentName) > 120 {
		return errors.New("PAYMENT_NAME maksimal 120 karakter")
	}
	if (s.TelegramBotToken == "") != (s.TelegramAdminChatID == "") {
		return errors.New("TELEGRAM_BOT_TOKEN dan TELEGRAM_ADMIN_CHAT_ID harus diisi bersamaan")
	}
	if (s.TelegramAPIID == 0) != (s.TelegramAPIHash == "") {
		return errors.New("TELEGRAM_API_ID dan TELEGRAM_API_HASH harus diisi bersamaan")
	}
	if s.TelegramAPIID < 0 || (s.TelegramAPIID > 0 && !validTelegramAPIHash(s.TelegramAPIHash)) {
		return errors.New("TELEGRAM_API_ID harus positif dan TELEGRAM_API_HASH valid")
	}
	if s.TelegramAdminChatID != "" {
		validDigits := true
		for _, digit := range s.TelegramAdminChatID {
			if digit < '0' || digit > '9' {
				validDigits = false
				break
			}
		}
		if !validDigits {
			return errors.New("TELEGRAM_ADMIN_CHAT_ID harus berupa ID chat pribadi admin yang positif")
		}
		if id, err := strconv.ParseInt(s.TelegramAdminChatID, 10, 64); err != nil || id < 1 {
			return errors.New("TELEGRAM_ADMIN_CHAT_ID harus berupa ID chat pribadi admin yang positif")
		}
	}
	if s.AppEnv == "production" && !s.CookieSecure {
		return errors.New("COOKIE_SECURE tidak boleh false di production")
	}
	if s.DatabaseFile == "" {
		return errors.New("DATABASE_FILE wajib diisi")
	}
	if s.MT5BrokerDirectoryURL != "" {
		directoryURL, err := url.Parse(s.MT5BrokerDirectoryURL)
		if err != nil || !strings.EqualFold(directoryURL.Scheme, "https") || directoryURL.Hostname() == "" || directoryURL.User != nil || directoryURL.Fragment != "" {
			return errors.New("MT5_BROKER_DIRECTORY_URL harus berupa URL HTTPS yang valid")
		}
	}
	if (s.AirPayAPIKey == "") != (s.AirPayMerchantUID == "") {
		return errors.New("AIRPAY_API_KEY dan AIRPAY_MERCHANT_UID harus diisi bersama")
	}
	if s.EAAPIKey != "" && (len(s.EAAPIKey) < 24 || hasPlaceholder(s.EAAPIKey, "replace-with", "change-this", "changethis", "your-api-key")) {
		return errors.New("EA_API_KEY harus berupa API key non-placeholder minimal 24 karakter")
	}
	if s.CopierWorkerAPIKey != "" && (len(s.CopierWorkerAPIKey) < 32 || hasPlaceholder(s.CopierWorkerAPIKey, "replace-with", "change-this", "your-api-key")) {
		return errors.New("COPIER_WORKER_API_KEY harus berupa secret non-placeholder minimal 32 karakter")
	}

	return nil
}

func validTelegramAPIHash(value string) bool {
	if len(value) != 32 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func loadEnvFile(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 1024), 1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		key, value, ok := strings.Cut(line, "=")
		if !ok || strings.TrimSpace(key) == "" {
			continue
		}
		key = strings.TrimSpace(key)
		if _, exists := os.LookupEnv(key); exists {
			continue
		}
		value = strings.TrimSpace(value)
		if len(value) >= 2 && ((value[0] == '"' && value[len(value)-1] == '"') || (value[0] == '\'' && value[len(value)-1] == '\'')) {
			value = value[1 : len(value)-1]
		} else if comment := strings.Index(value, " #"); comment >= 0 {
			value = strings.TrimSpace(value[:comment])
		}
		if err := os.Setenv(key, value); err != nil {
			return err
		}
	}
	return scanner.Err()
}

func normalizeOrigins(raw string) ([]string, error) {
	seen := make(map[string]struct{})
	var origins []string
	for _, value := range strings.Split(raw, ",") {
		value = strings.TrimRight(strings.TrimSpace(value), "/")
		if value == "" {
			continue
		}
		u, err := url.Parse(value)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
			return nil, errors.New("WEBSOCKET_ALLOWED_ORIGINS berisi origin HTTP(S) yang tidak valid")
		}
		canonical := strings.ToLower(u.Scheme + "://" + u.Host)
		if _, ok := seen[canonical]; !ok {
			seen[canonical] = struct{}{}
			origins = append(origins, canonical)
		}
	}
	return origins, nil
}

func boolEnv(key string, fallback bool) (bool, error) {
	value, ok := os.LookupEnv(key)
	if !ok || strings.TrimSpace(value) == "" {
		return fallback, nil
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return false, fmt.Errorf("%s harus true atau false", key)
	}
	return parsed, nil
}

func valueOr(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

func hasPlaceholder(value string, markers ...string) bool {
	value = strings.ToLower(value)
	for _, marker := range markers {
		if strings.Contains(value, marker) {
			return true
		}
	}
	return false
}
