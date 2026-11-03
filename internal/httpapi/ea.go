package httpapi

import (
	"context"
	"crypto/subtle"
	dbstore "github.com/rnpproject/algentra-backend/internal/database"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/rnpproject/algentra-backend/internal/ea/snapshot"
)

type eaSnapshotAPI struct {
	pool  *dbstore.Pool
	key   string
	state *snapshot.Store
}

type masterSnapshotRequest struct {
	TerminalLogin  string            `json:"terminal_login"`
	TerminalServer string            `json:"terminal_server"`
	Positions      []masterPosition  `json:"positions"`
	Balance        *float64          `json:"balance"`
	Equity         *float64          `json:"equity"`
	FloatingProfit *float64          `json:"floating_profit"`
	Currency       *string           `json:"currency"`
	TradeMode      *string           `json:"trade_mode"`
	MarketQuotes   *[]snapshot.Quote `json:"market_quotes"`
}

type masterPosition struct {
	Ticket     string   `json:"ticket"`
	Symbol     string   `json:"symbol"`
	Action     string   `json:"action"`
	Lots       float64  `json:"lots"`
	EntryPrice float64  `json:"entry_price"`
	SL         *float64 `json:"sl"`
	TP         *float64 `json:"tp"`
}

type heartbeatRequest struct {
	Active   *bool   `json:"active"`
	Terminal *string `json:"terminal"`
	Version  *string `json:"version"`
	Symbol   *string `json:"symbol"`
}

var marketSymbolPattern = regexp.MustCompile(`^[A-Za-z0-9._-]{1,32}$`)

func (api eaSnapshotAPI) register(mux *http.ServeMux) {
	for _, path := range []string{"/api/ea/master/snapshot", "/v1/ea/master/snapshot"} {
		mux.HandleFunc("POST "+path, api.publishMasterSnapshot)
	}
	for _, path := range []string{"/api/ea/heartbeat", "/v1/ea/heartbeat"} {
		mux.HandleFunc("POST "+path, api.heartbeat)
	}
	signals := eaSignalAPI{pool: api.pool, key: api.key}
	for _, path := range []string{"/api/ea/pending", "/v1/ea/pending"} {
		mux.HandleFunc("GET "+path, signals.pending)
	}
	for _, path := range []string{"/api/ea/report", "/v1/ea/report"} {
		mux.HandleFunc("POST "+path, signals.report)
	}
	for _, path := range []string{"/api/ea/result", "/v1/ea/result"} {
		mux.HandleFunc("POST "+path, signals.result)
	}
}

func (api eaSnapshotAPI) heartbeat(w http.ResponseWriter, r *http.Request) {
	var payload heartbeatRequest
	if err := decodeJSON(w, r, &payload, 64<<10); err != nil {
		writeError(w, http.StatusUnprocessableEntity, "Heartbeat tidak valid")
		return
	}
	for _, field := range []struct {
		value *string
		limit int
	}{{payload.Terminal, 128}, {payload.Version, 64}, {payload.Symbol, 64}} {
		if field.value != nil && !validLength(*field.value, 0, field.limit) {
			writeError(w, http.StatusUnprocessableEntity, "Heartbeat tidak valid")
			return
		}
	}
	active := true
	if payload.Active != nil {
		active = *payload.Active
	}
	heartbeat := api.state.PublishHeartbeat(snapshot.Heartbeat{
		Active: active, LastHeartbeat: time.Now().UTC(), Terminal: payload.Terminal, Version: payload.Version, Symbol: payload.Symbol,
	})
	wib := time.FixedZone("WIB", 7*60*60)
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "active": heartbeat.Active, "last_heartbeat": heartbeat.LastHeartbeat.In(wib).Format(time.RFC3339Nano)})
}

func (api eaSnapshotAPI) publishMasterSnapshot(w http.ResponseWriter, r *http.Request) {
	if api.key == "" {
		writeError(w, http.StatusServiceUnavailable, "EA_API_KEY belum dikonfigurasi")
		return
	}
	supplied := r.Header.Get("X-API-Key")
	if len(supplied) != len(api.key) || subtle.ConstantTimeCompare([]byte(supplied), []byte(api.key)) != 1 {
		writeError(w, http.StatusUnauthorized, "API key EA tidak valid")
		return
	}
	var payload masterSnapshotRequest
	if err := decodeJSON(w, r, &payload, 2<<20); err != nil || !validMasterSnapshot(payload) {
		writeError(w, http.StatusUnprocessableEntity, "Snapshot master tidak valid")
		return
	}
	now := time.Now().UTC()
	var account *snapshot.Account
	if payload.Balance != nil && payload.Equity != nil && payload.FloatingProfit != nil && payload.Currency != nil && payload.TradeMode != nil {
		account = &snapshot.Account{
			Balance: *payload.Balance, Equity: *payload.Equity, FloatingProfit: *payload.FloatingProfit,
			Currency: strings.ToUpper(*payload.Currency), TradeMode: *payload.TradeMode, ObservedAt: now,
		}
	}
	positions := make([]snapshot.Position, 0, len(payload.Positions))
	for _, position := range payload.Positions {
		positions = append(positions, snapshot.Position{Ticket: position.Ticket, Symbol: position.Symbol, Action: position.Action, Lots: position.Lots, EntryPrice: position.EntryPrice, SL: position.SL, TP: position.TP})
	}
	api.state.Publish(now, account, positions, payload.MarketQuotes)
	if account != nil {
		if err := api.saveEquityCandle(r.Context(), payload, *account, now); err != nil {
			writeError(w, http.StatusServiceUnavailable, "Snapshot equity tidak dapat disimpan")
			return
		}
	}
	wib := time.FixedZone("WIB", 7*60*60)
	writeJSON(w, http.StatusOK, map[string]any{
		"status": "ok", "positions": len(payload.Positions), "last_snapshot_at": now.In(wib).Format(time.RFC3339Nano),
	})
}

func validMasterSnapshot(p masterSnapshotRequest) bool {
	if !validLength(p.TerminalLogin, 1, 32) || !validLength(p.TerminalServer, 1, 128) || p.Positions == nil || len(p.Positions) > 500 {
		return false
	}
	if p.Balance != nil && (!finite(*p.Balance) || *p.Balance < 0) ||
		p.Equity != nil && !finite(*p.Equity) || p.FloatingProfit != nil && !finite(*p.FloatingProfit) {
		return false
	}
	if p.Currency != nil && !validAlphaNumeric(*p.Currency, 3, 16) {
		return false
	}
	if p.TradeMode != nil && *p.TradeMode != "real" && *p.TradeMode != "demo" && *p.TradeMode != "contest" {
		return false
	}
	positions := make(map[string]struct{}, len(p.Positions))
	for _, position := range p.Positions {
		if !validLength(position.Ticket, 1, 64) || !validLength(position.Symbol, 1, 64) ||
			(position.Action != "BUY" && position.Action != "SELL") || !finite(position.Lots) || position.Lots <= 0 || position.Lots > 1000 ||
			!finite(position.EntryPrice) || position.EntryPrice <= 0 {
			return false
		}
		if position.SL != nil && (!finite(*position.SL) || *position.SL <= 0) || position.TP != nil && (!finite(*position.TP) || *position.TP <= 0) {
			return false
		}
		if _, duplicate := positions[position.Ticket]; duplicate {
			return false
		}
		positions[position.Ticket] = struct{}{}
	}
	if p.MarketQuotes != nil {
		if len(*p.MarketQuotes) > 100 {
			return false
		}
		for _, quote := range *p.MarketQuotes {
			if !marketSymbolPattern.MatchString(quote.Symbol) || !finite(quote.Bid) || !finite(quote.Ask) || quote.Bid <= 0 || quote.Ask < quote.Bid || quote.TimeMSC < 1 || quote.TimeMSC > 4102444800000 {
				return false
			}
		}
	}
	return true
}

func (api eaSnapshotAPI) saveEquityCandle(ctx context.Context, payload masterSnapshotRequest, account snapshot.Account, now time.Time) error {
	accountKey := strings.ToUpper(strings.TrimSpace(payload.TerminalLogin) + "@" + strings.TrimSpace(payload.TerminalServer))
	minute := now.Truncate(time.Minute)
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	_, err := api.pool.Exec(ctx, `
		INSERT INTO mt5_master_equity_candles (
			account_key, minute_start, open_equity, high_equity, low_equity,
			close_equity, currency, observed_at
		) VALUES ($1,$2,$3,$3,$3,$3,$4,$5)
		ON CONFLICT (account_key, minute_start) DO UPDATE SET
			high_equity = GREATEST(mt5_master_equity_candles.high_equity, EXCLUDED.close_equity),
			low_equity = LEAST(mt5_master_equity_candles.low_equity, EXCLUDED.close_equity),
			close_equity = EXCLUDED.close_equity, currency = EXCLUDED.currency,
			observed_at = EXCLUDED.observed_at
	`, accountKey, minute, account.Equity, account.Currency, now)
	if err != nil {
		return err
	}
	_, err = api.pool.Exec(ctx, `DELETE FROM mt5_master_equity_candles WHERE minute_start < $1`, now.Add(-45*24*time.Hour))
	return err
}
