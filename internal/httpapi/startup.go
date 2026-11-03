package httpapi

import (
	"context"
	"errors"
	dbstore "github.com/rnpproject/algentra-backend/internal/database"
	"log/slog"
	"time"
)

// EnsureStartupSettings preserves the defaults seeded by the previous API
// startup and removes controls retired by the current risk model.
func EnsureStartupSettings(ctx context.Context, pool *dbstore.Pool) error {
	if pool == nil {
		return errors.New("database pool wajib tersedia")
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	retired := []string{
		"default_symbol", "max_daily_loss_money", "max_lot", "max_open_trades",
		"max_signal_age_seconds", "allowed_symbols", "demo_mode",
	}
	if _, err = tx.Exec(ctx, `DELETE FROM settings WHERE key=ANY($1)`, retired); err != nil {
		return err
	}
	defaults := [][2]string{
		{"confidence_threshold", "0.75"},
		{"kill_switch", "false"},
		{"max_market_deviation_pct", "5.0"},
		{"allow_updates", "false"},
		{"registration_open", "true"},
		{"symbol_mapping", `{"GOLD":"XAUUSD","XAU":"XAUUSD","EMAS":"XAUUSD"}`},
	}
	for _, setting := range defaults {
		if _, err = tx.Exec(ctx, `INSERT INTO settings(key,value) VALUES($1,$2) ON CONFLICT(key) DO NOTHING`, setting[0], setting[1]); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func PurgeExpiredConversations(ctx context.Context, pool *dbstore.Pool) error {
	if pool == nil {
		return errors.New("database pool wajib tersedia")
	}
	_, err := pool.Exec(ctx, `DELETE FROM cs_conversations WHERE expires_at <= $1`, time.Now().UTC())
	return err
}

func CleanupExpiredConversationsPeriodically(ctx context.Context, pool *dbstore.Pool) {
	ticker := time.NewTicker(15 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			cleanupCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			err := PurgeExpiredConversations(cleanupCtx, pool)
			cancel()
			if err != nil {
				slog.Warn("pembersihan percakapan CS kedaluwarsa gagal", "error", err)
			}
		}
	}
}
