package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/rnpproject/algentra-backend/internal/auth"
	"github.com/rnpproject/algentra-backend/internal/config"
	"github.com/rnpproject/algentra-backend/internal/database"
	"github.com/rnpproject/algentra-backend/internal/httpapi"
	telegramclient "github.com/rnpproject/algentra-backend/internal/telegram"
)

func main() {
	settings, err := config.Load()
	if err != nil {
		slog.Error("konfigurasi service tidak valid", "error", err)
		os.Exit(1)
	}
	if settings.DatabaseFile == "" {
		slog.Error("DATABASE_FILE wajib diisi untuk Core API Go")
		os.Exit(1)
	}
	startupCtx, startupCancel := context.WithTimeout(context.Background(), 15*time.Second)
	pool, err := database.Open(startupCtx, settings.DatabaseFile)
	startupCancel()
	if err != nil {
		slog.Error("gagal menyiapkan koneksi database")
		os.Exit(1)
	}
	defer pool.Close()
	readyCtx, readyCancel := context.WithTimeout(context.Background(), 30*time.Second)
	err = database.CheckReady(readyCtx, pool, "", "")
	readyCancel()
	if err != nil {
		slog.Error("database belum siap atau schema tidak sesuai", "error", err)
		os.Exit(1)
	}
	adminCtx, adminCancel := context.WithTimeout(context.Background(), 10*time.Second)
	err = auth.EnsureAdmin(adminCtx, pool, settings.AdminUsername, settings.AdminPassword)
	adminCancel()
	if err != nil {
		slog.Error("akun admin tidak dapat disiapkan", "error", err)
		os.Exit(1)
	}
	settingsCtx, settingsCancel := context.WithTimeout(context.Background(), 10*time.Second)
	err = httpapi.EnsureStartupSettings(settingsCtx, pool)
	settingsCancel()
	if err != nil {
		slog.Error("pengaturan awal tidak dapat disiapkan", "error", err)
		os.Exit(1)
	}
	cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
	err = httpapi.PurgeExpiredConversations(cleanupCtx, pool)
	cleanupCancel()
	if err != nil {
		slog.Error("percakapan CS kedaluwarsa tidak dapat dibersihkan", "error", err)
		os.Exit(1)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go httpapi.CleanupExpiredConversationsPeriodically(ctx, pool)
	projectRoot := os.Getenv("PROJECT_ROOT")
	if projectRoot == "" {
		projectRoot, _ = os.Getwd()
	}
	tgManager := telegramclient.NewManager(settings.TelegramAPIID, settings.TelegramAPIHash, filepath.Join(projectRoot, "data", "telegram_user.gotd.session"))
	handler := httpapi.NewHandler(pool, httpapi.ServerConfig{
		Environment:           settings.AppEnv,
		MT5BrokerDirectoryURL: settings.MT5BrokerDirectoryURL,
		ServiceName:           "algentra-capital",
		CookieSecure:          settings.CookieSecure,
		WebSocketOrigins:      settings.WebSocketOrigins,
		TelegramManager:       tgManager,
		AppSecretKey:          settings.AppSecretKey,
		WorkerAPIKey:          settings.CopierWorkerAPIKey,
		EAAPIKey:              settings.EAAPIKey,
		GeminiAPIKey:          settings.GeminiAPIKey,
		GeminiModel:           settings.GeminiModel,
		PaymentBankName:       settings.PaymentBankName,
		PaymentNumber:         settings.PaymentNumber,
		PaymentName:           settings.PaymentName,
		AirPayConfigured:      settings.AirPayConfigured,
		AirPayAPIKey:          settings.AirPayAPIKey,
		AirPayMerchantUID:     settings.AirPayMerchantUID,
		TelegramBotToken:      settings.TelegramBotToken,
		TelegramAdminChatID:   settings.TelegramAdminChatID,
		BotContext:            ctx,
		EmailConfigured:       settings.EmailConfigured,
		EmailSMTPHost:         settings.EmailSMTPHost,
		EmailSMTPPort:         settings.EmailSMTPPort,
		EmailSMTPUsername:     settings.EmailSMTPUsername,
		EmailSMTPPassword:     settings.EmailSMTPPassword,
		EmailFrom:             settings.EmailFrom,
		EmailSMTPStartTLS:     settings.EmailSMTPStartTLS,
		ProjectRoot:           os.Getenv("PROJECT_ROOT"),
		Contact: httpapi.ContactConfig{
			PersonName:    settings.ContactPersonName,
			WhatsApp:      settings.ContactWhatsApp,
			Email:         settings.ContactEmail,
			Instagram:     settings.ContactInstagram,
			Telegram:      settings.ContactTelegram,
			TelegramAdmin: settings.ContactTelegramAdmin,
			TikTok:        settings.ContactTikTok,
			Website:       settings.ContactWebsite,
			Facebook:      settings.ContactFacebook,
			LinkedIn:      settings.ContactLinkedIn,
			X:             settings.ContactX,
			YouTube:       settings.ContactYouTube,
		},
		Pricing: httpapi.ServicePricing{
			ManagementFeeIDR: settings.ManagementFeeIDR,
			VPSOptions: func() []httpapi.VPSPlan {
				plans := make([]httpapi.VPSPlan, 0, len(settings.VPSOptions))
				for _, plan := range settings.VPSOptions {
					plans = append(plans, httpapi.VPSPlan{Code: plan.Code, Name: plan.Name, VCPU: plan.VCPU, RAM: plan.RAM, Disk: plan.Disk, PriceIDR: plan.PriceIDR})
				}
				return plans
			}(),
		},
	})
	if err := tgManager.Start(ctx); err != nil {
		slog.Warn("listener Telegram Go tidak dapat dimulai", "error", err)
	}
	server := &http.Server{
		Addr:              settings.HTTPAddr,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}

	serverError := make(chan error, 1)
	go func() {
		slog.Info("Core API Go mulai", "address", settings.HTTPAddr, "environment", settings.AppEnv)
		serverError <- server.ListenAndServe()
	}()
	select {
	case err := <-serverError:
		if !errors.Is(err, http.ErrServerClosed) {
			slog.Error("Core API berhenti", "error", err)
			os.Exit(1)
		}
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			slog.Error("shutdown Core API gagal", "error", err)
		}
	}
}
