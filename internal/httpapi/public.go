package httpapi

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	dbstore "github.com/rnpproject/algentra-backend/internal/database"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/rnpproject/algentra-backend/internal/ea/snapshot"
)

const registrationLaunch = "2027-01-16T02:00:00Z" // 09:00 Asia/Jakarta

func registerPublicRoutes(mux *http.ServeMux, pool *dbstore.Pool, contact ContactConfig, state *snapshot.Store, websocketOrigins []string) {
	for _, prefix := range []string{"/api/public", "/v1/public"} {
		mux.HandleFunc("GET "+prefix+"/registration-status", registrationStatus(pool))
		mux.HandleFunc("GET "+prefix+"/contact", publicContact(contact))
		mux.HandleFunc("GET "+prefix+"/performance", publicPerformance(pool, state))
		mux.HandleFunc("GET "+prefix+"/performance/stream", publicPerformanceStream(pool, state))
	}
	mux.HandleFunc("GET /ws/public/performance", publicPerformanceWebSocket(pool, state, websocketOrigins))
}

func publicPerformanceStream(pool *dbstore.Pool, state *snapshot.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "public, no-store, no-transform")
		w.Header().Set("X-Accel-Buffering", "no")
		flusher, ok := w.(http.Flusher)
		if !ok {
			writeError(w, http.StatusInternalServerError, "Streaming tidak didukung")
			return
		}
		lastPayload := []byte(nil)
		lastHistoryAt := time.Time{}
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()
		for {
			includeHistory := lastPayload == nil || time.Since(lastHistoryAt) >= time.Minute
			request := r.Clone(r.Context())
			query := request.URL.Query()
			query.Set("include_equity_curve", strconv.FormatBool(includeHistory))
			request.URL.RawQuery = query.Encode()
			recorder := httptest.NewRecorder()
			publicPerformance(pool, state).ServeHTTP(recorder, request)
			body := bytes.TrimSpace(recorder.Body.Bytes())
			if recorder.Code == http.StatusOK {
				if includeHistory {
					lastHistoryAt = time.Now()
				}
				if !bytes.Equal(body, lastPayload) {
					_, _ = fmt.Fprintf(w, "event: performance\ndata: %s\n\n", body)
					lastPayload = append(lastPayload[:0], body...)
				} else {
					_, _ = io.WriteString(w, ": heartbeat\n\n")
				}
			} else {
				_, _ = io.WriteString(w, ": heartbeat\n\n")
			}
			flusher.Flush()
			select {
			case <-r.Context().Done():
				return
			case <-ticker.C:
			}
		}
	}
}

type equityCandle struct {
	MinuteStart time.Time
	Open        float64
	High        float64
	Low         float64
	Close       float64
	Currency    string
	ObservedAt  time.Time
}

func publicPerformance(pool *dbstore.Pool, state *snapshot.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		includeCurve := true
		if raw, exists := r.URL.Query()["include_equity_curve"]; exists {
			if len(raw) != 1 {
				writeError(w, http.StatusUnprocessableEntity, "Parameter include_equity_curve tidak valid")
				return
			}
			parsed, err := strconv.ParseBool(raw[0])
			if err != nil {
				writeError(w, http.StatusUnprocessableEntity, "Parameter include_equity_curve tidak valid")
				return
			}
			includeCurve = parsed
		}
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		var registered int64
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM client_users`).Scan(&registered); err != nil {
			writeError(w, http.StatusServiceUnavailable, "Performa publik tidak tersedia")
			return
		}
		now := time.Now().UTC()
		masterSnapshot, snapshotAvailable := state.Current()
		account := masterSnapshot.Account
		masterOnline := snapshotAvailable && account != nil && now.Sub(masterSnapshot.ObservedAt) <= 20*time.Second && now.Sub(account.ObservedAt) <= 30*time.Second
		quotes := make([]map[string]any, 0)
		if snapshotAvailable {
			for _, quote := range masterSnapshot.MarketQuotes {
				if !marketSymbolPattern.MatchString(quote.Symbol) || !finite(quote.Bid) || !finite(quote.Ask) || quote.Bid <= 0 || quote.Ask < quote.Bid {
					continue
				}
				quotes = append(quotes, map[string]any{
					"symbol": quote.Symbol, "bid": quote.Bid, "ask": quote.Ask,
					"updated_at": quoteTime(quote.TimeMSC),
				})
			}
		}
		marketOnline := masterOnline && snapshotAvailable && masterSnapshot.MarketObservedAt != nil && now.Sub(*masterSnapshot.MarketObservedAt) <= 30*time.Second
		payload := map[string]any{
			"master_account": map[string]any{
				"available": account != nil, "online": masterOnline, "account_key": nil,
				"balance":         accountValue(account, func(a *snapshot.Account) any { return a.Balance }),
				"equity":          accountValue(account, func(a *snapshot.Account) any { return a.Equity }),
				"floating_profit": accountValue(account, func(a *snapshot.Account) any { return a.FloatingProfit }),
				"currency":        accountValue(account, func(a *snapshot.Account) any { return a.Currency }),
				"trade_mode":      accountValue(account, func(a *snapshot.Account) any { return a.TradeMode }),
				"updated_at":      accountTime(account),
			},
			"market_quotes": quotes, "market_quotes_online": marketOnline,
			"market_quotes_reported_at": optionalTime(masterSnapshot.MarketObservedAt),
			"registered_clients":        registered,
		}
		masterCurrency := ""
		if account != nil {
			masterCurrency = account.Currency
		}
		dailyProfit, err := loadMasterDailyProfit(ctx, pool, now, masterCurrency)
		if err != nil {
			writeError(w, http.StatusServiceUnavailable, "Profit harian akun master tidak tersedia")
			return
		}
		payload["daily_profit"] = dailyProfit
		if includeCurve {
			candles, accountKey, err := loadEquityCandles(ctx, pool, now)
			if err != nil {
				writeError(w, http.StatusServiceUnavailable, "Performa publik tidak tersedia")
				return
			}
			payload["master_account"].(map[string]any)["account_key"] = accountKey
			curve := make([]map[string]any, 0, len(candles))
			candlePayload := make([]map[string]any, 0, len(candles))
			currentMinute := now.Truncate(time.Minute)
			for _, candle := range candles {
				curve = append(curve, map[string]any{"hour": wibTime(candle.MinuteStart), "equity": roundMoney(candle.Close), "currency": candle.Currency})
				candlePayload = append(candlePayload, map[string]any{
					"minute": wibTime(candle.MinuteStart), "open": roundMoney(candle.Open), "high": roundMoney(candle.High),
					"low": roundMoney(candle.Low), "close": roundMoney(candle.Close), "currency": candle.Currency,
					"closed": candle.MinuteStart.Before(currentMinute), "observed_at": wibTime(candle.ObservedAt),
				})
			}
			payload["equity_curve"] = curve
			payload["equity_candles"] = candlePayload
		}
		writeJSON(w, http.StatusOK, payload)
	}
}

func loadMasterDailyProfit(ctx context.Context, pool *dbstore.Pool, now time.Time, currency string) ([]map[string]any, error) {
	zone := time.FixedZone("WIB", 7*60*60)
	local := now.In(zone)
	first := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, zone).AddDate(0, 0, -6)
	rows, err := pool.Query(ctx, `SELECT trade_date,profit FROM mt5_master_daily_profit WHERE trade_date >= $1 ORDER BY trade_date`, first.Format("2006-01-02"))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := map[string]float64{}
	for rows.Next() {
		var day string
		var profit float64
		if err := rows.Scan(&day, &profit); err != nil {
			return nil, err
		}
		parsedDay, err := time.Parse("2006-01-02", day)
		if err != nil {
			return nil, fmt.Errorf("parse tanggal profit harian master: %w", err)
		}
		values[parsedDay.Format("2006-01-02")] = profit
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]map[string]any, 0, 7)
	for i := 0; i < 7; i++ {
		day := first.AddDate(0, 0, i)
		key := day.Format("2006-01-02")
		out = append(out, map[string]any{"date": key, "profit": roundMoney(values[key]), "currency": currency, "timezone": "WIB"})
	}
	return out, nil
}

func loadEquityCandles(ctx context.Context, pool *dbstore.Pool, now time.Time) ([]equityCandle, any, error) {
	var accountKey string
	err := pool.QueryRow(ctx, `SELECT account_key FROM mt5_master_equity_candles ORDER BY observed_at DESC LIMIT 1`).Scan(&accountKey)
	if errors.Is(err, sql.ErrNoRows) {
		return []equityCandle{}, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	local := now.In(time.FixedZone("WIB", 7*60*60))
	sessionDate := local
	if local.Hour() < 7 {
		sessionDate = sessionDate.AddDate(0, 0, -1)
	}
	sessionStart := time.Date(sessionDate.Year(), sessionDate.Month(), sessionDate.Day(), 7, 0, 0, 0, sessionDate.Location()).UTC()
	rows, err := pool.Query(ctx, `
		SELECT minute_start, open_equity, high_equity, low_equity, close_equity, currency, observed_at
		FROM mt5_master_equity_candles
		WHERE account_key = $1 AND minute_start >= $2
		ORDER BY minute_start ASC
	`, accountKey, sessionStart)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	candles := make([]equityCandle, 0)
	for rows.Next() {
		var candle equityCandle
		var minuteStartRaw, observedAtRaw string
		if err := rows.Scan(&minuteStartRaw, &candle.Open, &candle.High, &candle.Low, &candle.Close, &candle.Currency, &observedAtRaw); err != nil {
			return nil, nil, err
		}
		candle.MinuteStart, err = parseStoredTimestamp(minuteStartRaw)
		if err != nil {
			return nil, nil, fmt.Errorf("parse waktu candle equity: %w", err)
		}
		candle.ObservedAt, err = parseStoredTimestamp(observedAtRaw)
		if err != nil {
			return nil, nil, fmt.Errorf("parse waktu observasi candle equity: %w", err)
		}
		candles = append(candles, candle)
	}
	return candles, accountKey, rows.Err()
}

func accountValue(account *snapshot.Account, pick func(*snapshot.Account) any) any {
	if account == nil {
		return nil
	}
	return pick(account)
}

func accountTime(account *snapshot.Account) any {
	if account == nil {
		return nil
	}
	return wibTime(account.ObservedAt)
}

func optionalTime(value *time.Time) any {
	if value == nil {
		return nil
	}
	return wibTime(*value)
}

func wibTime(value time.Time) string {
	return value.In(time.FixedZone("WIB", 7*60*60)).Format(time.RFC3339Nano)
}

func quoteTime(milliseconds int64) string {
	if milliseconds < 1 || milliseconds > 4102444800000 {
		return ""
	}
	return wibTime(time.UnixMilli(milliseconds).UTC())
}

func roundMoney(value float64) float64 {
	rounded, err := strconv.ParseFloat(strconv.FormatFloat(value, 'f', 2, 64), 64)
	if err != nil {
		return value
	}
	return rounded
}

func publicContact(contact ContactConfig) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		socials := make([]map[string]string, 0, 8)
		for _, entry := range []struct{ value, icon, label string }{
			{contact.Instagram, "instagram", "Instagram"},
			{contact.Telegram, "telegram", "Telegram"},
			{contact.TikTok, "tiktok", "TikTok"},
			{contact.Website, "website", "Website"},
			{contact.Facebook, "facebook", "Facebook"},
			{contact.LinkedIn, "linkedin", "LinkedIn"},
			{contact.X, "x", "X"},
			{contact.YouTube, "youtube", "YouTube"},
		} {
			value := strings.TrimSpace(entry.value)
			if value == "" {
				continue
			}
			if !strings.Contains(value, "://") {
				value = "https://" + value
			}
			parsed, err := url.Parse(value)
			if err != nil {
				continue
			}
			segments := strings.Split(strings.Trim(parsed.Path, "/"), "/")
			display := segments[len(segments)-1]
			if display != "" && !strings.HasPrefix(display, "@") {
				display = "@" + display
			}
			if display == "" {
				display = parsed.Hostname()
				if display == "" {
					display = value
				}
			}
			socials = append(socials, map[string]string{"url": value, "icon": entry.icon, "label": entry.label, "display": display})
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"person":   optionalContact(contact.PersonName),
			"whatsapp": optionalContact(contact.WhatsApp),
			"email":    optionalContact(contact.Email),
			"socials":  socials,
		})
	}
}

func optionalContact(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func registrationStatus(pool *dbstore.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		open, err := registrationIsOpen(ctx, pool)
		if err != nil {
			writeError(w, http.StatusServiceUnavailable, "Status pendaftaran tidak tersedia")
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"registration_open": open})
	}
}

func registrationIsOpen(ctx context.Context, pool *dbstore.Pool) (bool, error) {
	var value string
	err := pool.QueryRow(ctx, `SELECT value FROM settings WHERE key = 'registration_open'`).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	var open bool
	if json.Unmarshal([]byte(value), &open) != nil || !open {
		var autoValue string
		err = pool.QueryRow(ctx, `SELECT value FROM settings WHERE key = 'registration_auto_open'`).Scan(&autoValue)
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		var autoOpen bool
		if json.Unmarshal([]byte(autoValue), &autoOpen) != nil || !autoOpen {
			return false, nil
		}
		launch, _ := time.Parse(time.RFC3339, registrationLaunch)
		return !time.Now().UTC().Before(launch), nil
	}
	return true, nil
}
