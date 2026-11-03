package httpapi

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	dbstore "github.com/rnpproject/algentra-backend/internal/database"
	"math"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"strings"
	"time"

	"database/sql"
	"github.com/rnpproject/algentra-backend/internal/ea/snapshot"
	telegramclient "github.com/rnpproject/algentra-backend/internal/telegram"
)

type dashboardAPI struct {
	pool            *dbstore.Pool
	state           *snapshot.Store
	telegram        *telegramclient.Manager
	geminiKey       string
	geminiModel     string
	websocketOrigin []string
}

func (api dashboardAPI) register(mux *http.ServeMux) {
	for _, prefix := range []string{"/api", "/api/dashboard", "/v1/dashboard"} {
		mux.HandleFunc("GET "+prefix+"/stats", api.stats)
		mux.HandleFunc("GET "+prefix+"/realtime/stream", api.realtimeStream)
		mux.HandleFunc("GET "+prefix+"/mt5-live", api.mt5Live)
		mux.HandleFunc("GET "+prefix+"/signals", api.signals)
		mux.HandleFunc("GET "+prefix+"/logs", api.logs)
		mux.HandleFunc("GET "+prefix+"/logs/export.csv", api.exportLogs)
		mux.HandleFunc("GET "+prefix+"/trades", api.trades)
		mux.HandleFunc("GET "+prefix+"/trades/export.csv", api.exportTrades)
		mux.HandleFunc("GET "+prefix+"/groups/stats", api.groupStats)
	}
	mux.HandleFunc("GET /ws", api.dashboardWebSocket)
}

func (api dashboardAPI) realtimeStream(w http.ResponseWriter, r *http.Request) {
	if !api.authorized(w, r) {
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "Streaming tidak didukung")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "private, no-store, no-transform")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		if err := api.writeDashboardEvent(w, r); err != nil {
			return
		}
		flusher.Flush()
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
		}
	}
}

func (api dashboardAPI) writeDashboardEvent(w http.ResponseWriter, r *http.Request) error {
	statsResponse := httptest.NewRecorder()
	api.stats(statsResponse, r)
	if statsResponse.Code != http.StatusOK {
		return fmt.Errorf("dashboard stats failed")
	}
	var stats map[string]any
	if err := json.Unmarshal(statsResponse.Body.Bytes(), &stats); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	var latest sql.NullInt64
	err := api.pool.QueryRow(ctx, `SELECT id FROM signals ORDER BY created_at DESC,id DESC LIMIT 1`).Scan(&latest)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	var latestID any
	if latest.Valid {
		latestID = latest.Int64
	}
	payload := map[string]any{"stats": stats, "latest_signal_id": latestID, "server_time": wibTime(time.Now().UTC())}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "event: dashboard\ndata: %s\n\n", encoded)
	return err
}

func (api dashboardAPI) stats(w http.ResponseWriter, r *http.Request) {
	if !api.authorized(w, r) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	statuses := map[string]int64{}
	rows, err := api.pool.Query(ctx, `SELECT upper(status),count(*) FROM signals GROUP BY upper(status)`)
	if err != nil {
		writeError(w, 503, "Statistik signal tidak tersedia")
		return
	}
	for rows.Next() {
		var status string
		var count int64
		if err := rows.Scan(&status, &count); err != nil {
			rows.Close()
			writeError(w, 503, "Statistik signal tidak tersedia")
			return
		}
		statuses[status] = count
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		writeError(w, 503, "Statistik signal tidak tersedia")
		return
	}
	rows.Close()
	known := map[string]bool{"EXECUTED": true, "WIN": true, "LOSS": true, "BE": true, "PENDING": true, "CLAIMED": true, "PROCESSING": true, "REJECTED": true, "FAILED": true, "IGNORED": true, "EXPIRED": true}
	unknown := make([]string, 0)
	var total int64
	for status, count := range statuses {
		total += count
		if !known[status] {
			unknown = append(unknown, status)
		}
	}
	sort.Strings(unknown)
	var wins, losses int64
	var profit, dailyLoss float64
	var activeClients int64
	var closedTrades int64
	if err := api.pool.QueryRow(ctx, `SELECT count(*) FILTER(WHERE closed_at IS NOT NULL AND upper(COALESCE(result,''))='WIN'),count(*) FILTER(WHERE closed_at IS NOT NULL AND upper(COALESCE(result,''))='LOSS'),COALESCE(sum(profit) FILTER(WHERE closed_at IS NOT NULL),0),COALESCE(sum(GREATEST(0,-profit)) FILTER(WHERE closed_at >= $1),0),count(*) FILTER(WHERE closed_at IS NOT NULL) FROM trades`, wibDayStart(time.Now().UTC())).Scan(&wins, &losses, &profit, &dailyLoss, &closedTrades); err != nil {
		writeError(w, 503, "Statistik trade tidak tersedia")
		return
	}
	if err := api.pool.QueryRow(ctx, `SELECT count(*) FROM client_users`).Scan(&activeClients); err != nil {
		writeError(w, 503, "Statistik klien tidak tersedia")
		return
	}
	controls, _, err := loadTradingControls(ctx, api.pool, time.Now().UTC())
	if err != nil {
		writeError(w, 503, "Kontrol trading tidak tersedia")
		return
	}
	decided := wins + losses
	winrate := float64(0)
	if decided > 0 {
		winrate = math.Round(float64(wins)*10000/float64(decided)) / 100
	}
	heartbeat, hasHeartbeat := api.state.CurrentHeartbeat()
	eaOnline := hasHeartbeat && heartbeat.Active && time.Since(heartbeat.LastHeartbeat) <= 90*time.Second
	var last any
	if hasHeartbeat {
		last = wibTime(heartbeat.LastHeartbeat)
	}
	payload := map[string]any{"signals_total": total, "signals_executed": statuses["EXECUTED"] + statuses["WIN"] + statuses["LOSS"] + statuses["BE"], "signals_pending": statuses["PENDING"] + statuses["CLAIMED"], "signals_processing": statuses["PROCESSING"], "signals_rejected": statuses["REJECTED"] + statuses["FAILED"], "signals_ignored": statuses["IGNORED"], "signals_expired": statuses["EXPIRED"], "signal_status_unknown": len(unknown) > 0, "signal_status_unknown_values": unknown, "active_clients": activeClients, "ea_online": eaOnline, "ea_last_heartbeat": last, "daily_loss": math.Round(dailyLoss*100) / 100, "trades_closed": closedTrades, "wins": wins, "losses": losses, "winrate": winrate, "total_profit": math.Round(profit*100) / 100}
	for key, value := range controls {
		if key == "daily_profit" || key == "daily_loss" || key == "open_trades" || key == "kill_switch" || key == "max_market_deviation_pct" || key == "trading_paused" {
			payload[key] = value
		}
	}
	telegramStatus := map[string]any{"connected": false, "state": "disconnected", "account_name": nil}
	if api.telegram != nil {
		telegramStatus = api.telegram.Status(ctx)
	}
	payload["telegram_connected"] = telegramStatus["connected"]
	payload["telegram_status"] = telegramStatus["state"]
	payload["telegram_account"] = telegramStatus["account_name"]
	payload["gemini_configured"] = api.geminiKey != ""
	payload["gemini_model"] = api.geminiModel
	writeJSON(w, 200, payload)
}

func (api dashboardAPI) mt5Live(w http.ResponseWriter, r *http.Request) {
	if !api.authorized(w, r) {
		return
	}
	current, ok := api.state.Current()
	if !ok {
		writeJSON(w, 200, map[string]any{"available": false, "online": false, "observed_at": nil, "account": nil, "positions": []any{}, "market_quotes": []any{}})
		return
	}
	var account any
	if current.Account != nil {
		a := current.Account
		account = map[string]any{"balance": a.Balance, "equity": a.Equity, "floating_profit": a.FloatingProfit, "currency": a.Currency, "trade_mode": a.TradeMode, "observed_at": wibTime(a.ObservedAt)}
	}
	positions := current.Positions
	if positions == nil {
		positions = []snapshot.Position{}
	}
	quotes := current.MarketQuotes
	if quotes == nil {
		quotes = []snapshot.Quote{}
	}
	writeJSON(w, 200, map[string]any{"available": true, "online": time.Since(current.ObservedAt) <= 20*time.Second, "observed_at": wibTime(current.ObservedAt), "account": account, "positions": positions, "market_quotes": quotes})
}

func wibDayStart(now time.Time) time.Time {
	zone := time.FixedZone("WIB", 7*60*60)
	local := now.In(zone)
	return time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, zone).UTC()
}

func (api dashboardAPI) authorized(w http.ResponseWriter, r *http.Request) bool {
	if state := SessionFromRequest(r); state != nil {
		if user, ok := state.Values["admin_username"].(string); ok && user != "" {
			return true
		}
	}
	writeError(w, http.StatusUnauthorized, "Login admin diperlukan")
	return false
}

func dashboardPage(r *http.Request, maxLimit int) (int, int, bool) {
	limit, offset := 100, 0
	if raw := r.URL.Query().Get("limit"); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 1 || value > maxLimit {
			return 0, 0, false
		}
		limit = value
	}
	if raw := r.URL.Query().Get("offset"); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 0 {
			return 0, 0, false
		}
		offset = value
	}
	return limit, offset, true
}

func (api dashboardAPI) signals(w http.ResponseWriter, r *http.Request) {
	if !api.authorized(w, r) {
		return
	}
	limit, offset, ok := dashboardPage(r, 500)
	if !ok || !validQueryLength(r, "status", 0, 32) || !validQueryLength(r, "group_id", 0, 128) {
		writeError(w, http.StatusUnprocessableEntity, "Parameter pagination/filter tidak valid")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	rows, err := api.pool.Query(ctx, `SELECT s.id,s.group_id,s.group_name,s.message_id,s.sender_id,s.sender_name,s.raw_text,s.parsed_json,s.normalized_text,s.confidence,s.status,s.created_at,t.ticket,CASE WHEN t.closed_at IS NOT NULL THEN t.profit ELSE NULL END FROM signals s LEFT JOIN trades t ON t.signal_id=s.id WHERE ($1='' OR s.status=$1) AND ($2='' OR s.group_id=$2) ORDER BY s.created_at DESC,s.id DESC OFFSET $3 LIMIT $4`, strings.ToUpper(r.URL.Query().Get("status")), r.URL.Query().Get("group_id"), offset, limit)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "Signal tidak tersedia")
		return
	}
	defer rows.Close()
	items := make([]map[string]any, 0)
	for rows.Next() {
		var id int64
		var groupID, groupName, messageID, senderID, senderName, parsed, normalized, ticket *string
		var raw, status string
		var confidence, profit *float64
		var created time.Time
		if err := rows.Scan(&id, &groupID, &groupName, &messageID, &senderID, &senderName, &raw, &parsed, &normalized, &confidence, &status, &created, &ticket, &profit); err != nil {
			writeError(w, 503, "Signal tidak dapat dibaca")
			return
		}
		items = append(items, map[string]any{"id": id, "group_id": groupID, "group_name": groupName, "message_id": messageID, "sender_id": senderID, "sender_name": senderName, "raw_text": raw, "parsed_json": parsed, "normalized_text": normalized, "confidence": confidence, "status": status, "created_at": wibTime(created), "ticket": ticket, "profit": profit})
	}
	if err := rows.Err(); err != nil {
		writeError(w, 503, "Signal tidak dapat dibaca")
		return
	}
	writeJSON(w, 200, items)
}

func (api dashboardAPI) logs(w http.ResponseWriter, r *http.Request) {
	if !api.authorized(w, r) {
		return
	}
	limit, offset, ok := dashboardPage(r, 500)
	if !ok || !validQueryLength(r, "level", 0, 16) || !validQueryLength(r, "search", 0, 200) {
		writeError(w, 422, "Parameter pagination/filter tidak valid")
		return
	}
	items, err := api.loadLogs(r.Context(), strings.ToUpper(r.URL.Query().Get("level")), r.URL.Query().Get("search"), limit, offset)
	if err != nil {
		writeError(w, 503, "Log tidak tersedia")
		return
	}
	writeJSON(w, 200, items)
}

func (api dashboardAPI) loadLogs(parent context.Context, level, search string, limit, offset int) ([]map[string]any, error) {
	ctx, cancel := context.WithTimeout(parent, 8*time.Second)
	defer cancel()
	rows, err := api.pool.Query(ctx, `SELECT id,level,source,message,created_at FROM logs WHERE ($1='' OR level=$1) AND ($2='' OR message ILIKE '%'||$2||'%') ORDER BY created_at DESC,id DESC OFFSET $3 LIMIT $4`, level, search, offset, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]map[string]any, 0)
	for rows.Next() {
		var id int64
		var l, source, message string
		var created time.Time
		if err := rows.Scan(&id, &l, &source, &message, &created); err != nil {
			return nil, err
		}
		items = append(items, map[string]any{"id": id, "level": l, "source": source, "message": message, "created_at": wibTime(created)})
	}
	return items, rows.Err()
}

func (api dashboardAPI) exportLogs(w http.ResponseWriter, r *http.Request) {
	if !api.authorized(w, r) {
		return
	}
	if !validQueryLength(r, "level", 0, 16) || !validQueryLength(r, "search", 0, 200) {
		writeError(w, 422, "Filter tidak valid")
		return
	}
	items, err := api.loadLogs(r.Context(), strings.ToUpper(r.URL.Query().Get("level")), r.URL.Query().Get("search"), 10000, 0)
	if err != nil {
		writeError(w, 503, "Log tidak tersedia")
		return
	}
	rows := [][]string{{"id", "created_at", "level", "source", "message"}}
	for _, item := range items {
		rows = append(rows, []string{fmt.Sprint(item["id"]), fmt.Sprint(item["created_at"]), csvSafe(item["level"]), csvSafe(item["source"]), csvSafe(item["message"])})
	}
	writeCSV(w, "logs.csv", rows)
}

func (api dashboardAPI) trades(w http.ResponseWriter, r *http.Request) {
	if !api.authorized(w, r) {
		return
	}
	limit, offset, ok := dashboardPage(r, 500)
	if !ok || !validQueryLength(r, "group_id", 0, 128) || !validQueryLength(r, "symbol", 0, 64) || !validQueryLength(r, "result", 0, 16) {
		writeError(w, 422, "Parameter pagination/filter tidak valid")
		return
	}
	items, err := api.loadTrades(r, limit, offset)
	if err != nil {
		if err == errInvalidDateRange {
			writeError(w, 422, "date_from harus sebelum atau sama dengan date_to")
		} else if errors.Is(err, errInvalidDate) {
			writeError(w, 422, "Format tanggal tidak valid")
		} else {
			writeError(w, 503, "Trade tidak tersedia")
		}
		return
	}
	writeJSON(w, 200, items)
}

var errInvalidDateRange = fmt.Errorf("invalid date range")
var errInvalidDate = fmt.Errorf("invalid date")

func parseDashboardDate(raw string) (*time.Time, error) {
	if raw == "" {
		return nil, nil
	}
	if t, err := time.Parse(time.RFC3339Nano, raw); err == nil {
		return &t, nil
	}
	for _, layout := range []string{"2006-01-02T15:04:05.999999", "2006-01-02T15:04:05", "2006-01-02 15:04:05", "2006-01-02"} {
		if t, err := time.ParseInLocation(layout, raw, time.FixedZone("WIB", 7*60*60)); err == nil {
			return &t, nil
		}
	}
	return nil, errInvalidDate
}
func (api dashboardAPI) loadTrades(r *http.Request, limit, offset int) ([]map[string]any, error) {
	from, err := parseDashboardDate(r.URL.Query().Get("date_from"))
	if err != nil {
		return nil, err
	}
	to, err := parseDashboardDate(r.URL.Query().Get("date_to"))
	if err != nil {
		return nil, err
	}
	if from != nil && to != nil && from.After(*to) {
		return nil, errInvalidDateRange
	}
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	rows, err := api.pool.Query(ctx, `SELECT t.id,t.signal_id,t.ticket,s.group_id,s.group_name,t.symbol,t.action,t.lots,t.entry,t.sl,t.tp,t.exec_price,CASE WHEN t.closed_at IS NOT NULL THEN t.profit ELSE NULL END,t.result,t.opened_at,t.closed_at FROM trades t LEFT JOIN signals s ON s.id=t.signal_id WHERE ($1::timestamptz IS NULL OR t.opened_at >= $1) AND ($2::timestamptz IS NULL OR t.opened_at <= $2) AND ($3='' OR s.group_id=$3) AND ($4='' OR t.symbol=$4) AND ($5='' OR t.result=$5) ORDER BY t.opened_at DESC,t.id DESC OFFSET $6 LIMIT $7`, from, to, r.URL.Query().Get("group_id"), strings.ToUpper(r.URL.Query().Get("symbol")), strings.ToUpper(r.URL.Query().Get("result")), offset, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]map[string]any, 0)
	for rows.Next() {
		var id int64
		var signalID *int64
		var ticket, groupID, groupName, symbol, action interface{}
		var lots, entry, sl, execPrice, profit *float64
		var tp *string
		var result *string
		var opened, closed sql.NullTime
		if err := rows.Scan(&id, &signalID, &ticket, &groupID, &groupName, &symbol, &action, &lots, &entry, &sl, &tp, &execPrice, &profit, &result, &opened, &closed); err != nil {
			return nil, err
		}
		var openedAt, closedAt any
		if opened.Valid {
			openedAt = wibTime(opened.Time)
		}
		if closed.Valid {
			closedAt = wibTime(closed.Time)
		}
		items = append(items, map[string]any{"id": id, "signal_id": signalID, "ticket": ticket, "group_id": groupID, "group_name": groupName, "symbol": symbol, "action": action, "lots": lots, "entry": entry, "sl": sl, "tp": tp, "exec_price": execPrice, "profit": profit, "result": result, "opened_at": openedAt, "closed_at": closedAt})
	}
	return items, rows.Err()
}

func (api dashboardAPI) exportTrades(w http.ResponseWriter, r *http.Request) {
	if !api.authorized(w, r) {
		return
	}
	if !validQueryLength(r, "group_id", 0, 128) || !validQueryLength(r, "symbol", 0, 64) || !validQueryLength(r, "result", 0, 16) {
		writeError(w, 422, "Filter tidak valid")
		return
	}
	items, err := api.loadTrades(r, 10000, 0)
	if err != nil {
		if err == errInvalidDateRange {
			writeError(w, 422, "date_from harus sebelum atau sama dengan date_to")
		} else if errors.Is(err, errInvalidDate) {
			writeError(w, 422, "Format tanggal tidak valid")
		} else {
			writeError(w, 503, "Trade tidak tersedia")
		}
		return
	}
	out := [][]string{{"ticket", "group", "symbol", "action", "lots", "entry", "exec_price", "profit", "result", "opened_at", "closed_at"}}
	for _, i := range items {
		out = append(out, []string{csvSafe(i["ticket"]), csvSafe(i["group_name"]), csvSafe(i["symbol"]), csvSafe(i["action"]), csvNumber(i["lots"]), csvNumber(i["entry"]), csvNumber(i["exec_price"]), csvNumber(i["profit"]), csvSafe(i["result"]), csvSafe(i["opened_at"]), csvSafe(i["closed_at"])})
	}
	writeCSV(w, "trades.csv", out)
}

func (api dashboardAPI) groupStats(w http.ResponseWriter, r *http.Request) {
	if !api.authorized(w, r) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	rows, err := api.pool.Query(ctx, `SELECT s.group_id,s.group_name,count(DISTINCT s.id),count(t.id) FILTER(WHERE t.closed_at IS NOT NULL AND upper(COALESCE(t.result,''))='WIN'),count(t.id) FILTER(WHERE t.closed_at IS NOT NULL AND upper(COALESCE(t.result,''))='LOSS'),COALESCE(sum(t.profit) FILTER(WHERE t.closed_at IS NOT NULL),0),count(t.id) FILTER(WHERE t.closed_at IS NOT NULL) FROM signals s LEFT JOIN trades t ON t.signal_id=s.id GROUP BY s.group_id,s.group_name ORDER BY count(DISTINCT s.id) DESC`)
	if err != nil {
		writeError(w, 503, "Statistik grup tidak tersedia")
		return
	}
	defer rows.Close()
	out := make([]map[string]any, 0)
	for rows.Next() {
		var id, name *string
		var total, wins, losses, closedTrades int64
		var profit float64
		if err := rows.Scan(&id, &name, &total, &wins, &losses, &profit, &closedTrades); err != nil {
			writeError(w, 503, "Statistik grup tidak tersedia")
			return
		}
		rate := float64(0)
		if wins+losses > 0 {
			rate = float64(wins) * 100 / float64(wins+losses)
		}
		label := "Unknown"
		if name != nil && *name != "" {
			label = *name
		}
		out = append(out, map[string]any{"group_id": id, "group_name": label, "signals_total": total, "trades_closed": closedTrades, "wins": wins, "losses": losses, "winrate": mathRound(rate), "profit": mathRound(profit)})
	}
	if err := rows.Err(); err != nil {
		writeError(w, 503, "Statistik grup tidak tersedia")
		return
	}
	writeJSON(w, 200, out)
}

func validQueryLength(r *http.Request, key string, min, max int) bool {
	v := r.URL.Query().Get(key)
	n := len([]rune(v))
	return n == 0 && min == 0 || n >= min && n <= max
}
func csvSafe(v any) string {
	if v == nil {
		return ""
	}
	var s string
	switch value := v.(type) {
	case *string:
		if value == nil {
			return ""
		}
		s = *value
	case *float64:
		if value == nil {
			return ""
		}
		s = strconv.FormatFloat(*value, 'f', -1, 64)
	case *int64:
		if value == nil {
			return ""
		}
		s = strconv.FormatInt(*value, 10)
	case *time.Time:
		if value == nil {
			return ""
		}
		s = value.Format(time.RFC3339Nano)
	default:
		s = fmt.Sprint(v)
	}
	trim := strings.TrimLeft(s, " \t\r\n")
	if trim != "" && strings.ContainsRune("=+-@", rune(trim[0])) {
		return "'" + s
	}
	return s
}
func csvNumber(v any) string {
	switch value := v.(type) {
	case *float64:
		if value == nil {
			return ""
		}
		return strconv.FormatFloat(*value, 'f', -1, 64)
	case *int64:
		if value == nil {
			return ""
		}
		return strconv.FormatInt(*value, 10)
	case nil:
		return ""
	default:
		return fmt.Sprint(value)
	}
}
func writeCSV(w http.ResponseWriter, name string, rows [][]string) {
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", "attachment; filename="+strconv.Quote(name))
	writer := csv.NewWriter(w)
	for _, row := range rows {
		_ = writer.Write(row)
	}
	writer.Flush()
}
func wibNullableTime(t *time.Time) any {
	if t == nil {
		return nil
	}
	return wibTime(*t)
}
func mathRound(x float64) float64 {
	rounded, err := strconv.ParseFloat(strconv.FormatFloat(x, 'f', 2, 64), 64)
	if err != nil {
		return x
	}
	return rounded
}
