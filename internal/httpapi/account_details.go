package httpapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	dbstore "github.com/rnpproject/algentra-backend/internal/database"
	"math"
	"net/http"
	"strconv"
	"time"
)

type mt5AccountDetailsAPI struct{ pool *dbstore.Pool }

func (api clientMT5API) accountDetails(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	ownerID, ok := requireClient(ctx, api.pool, w, r)
	if !ok {
		return
	}
	id, err := strconv.ParseInt(r.PathValue("account_id"), 10, 64)
	if err != nil || id < 1 {
		writeError(w, 404, "Akun MT5 tidak ditemukan")
		return
	}
	page := accountHistoryPage(r)
	data, err := loadAccountDetails(ctx, api.pool, id, &ownerID, page)
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, 404, "Akun MT5 tidak ditemukan")
		return
	}
	if err != nil {
		writeError(w, 503, "Detail akun MT5 tidak tersedia")
		return
	}
	writeJSON(w, 200, data)
}

func (api mt5AccountDetailsAPI) list(w http.ResponseWriter, r *http.Request) {
	if !api.authorized(w, r) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	rows, err := api.pool.Query(ctx, `SELECT a.id,a.label,a.server,a.login,a.plan,a.execution_mode,u.email FROM mt5_accounts a JOIN client_users u ON u.id=a.owner_id WHERE a.role='follower' ORDER BY a.id DESC LIMIT 500`)
	if err != nil {
		writeError(w, 503, "Daftar akun copy trading tidak tersedia")
		return
	}
	defer rows.Close()
	items := make([]map[string]any, 0)
	for rows.Next() {
		var id int64
		var label, server, login, plan, mode, email string
		if err := rows.Scan(&id, &label, &server, &login, &plan, &mode, &email); err != nil {
			writeError(w, 503, "Daftar akun copy trading tidak tersedia")
			return
		}
		items = append(items, map[string]any{"id": id, "label": label, "server": server, "login": login, "plan": plan, "execution_mode": mode, "owner_email": email})
	}
	if err := rows.Err(); err != nil {
		writeError(w, 503, "Daftar akun copy trading tidak tersedia")
		return
	}
	writeJSON(w, 200, map[string]any{"accounts": items})
}

func (api mt5AccountDetailsAPI) details(w http.ResponseWriter, r *http.Request) {
	if !api.authorized(w, r) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	id, err := strconv.ParseInt(r.PathValue("account_id"), 10, 64)
	if err != nil || id < 1 {
		writeError(w, 404, "Akun MT5 tidak ditemukan")
		return
	}
	page := accountHistoryPage(r)
	data, err := loadAccountDetails(ctx, api.pool, id, nil, page)
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, 404, "Akun MT5 tidak ditemukan")
		return
	}
	if err != nil {
		writeError(w, 503, "Detail akun MT5 tidak tersedia")
		return
	}
	writeJSON(w, 200, data)
}

func (api mt5AccountDetailsAPI) authorized(w http.ResponseWriter, r *http.Request) bool {
	if state := SessionFromRequest(r); state != nil {
		if username, ok := state.Values["admin_username"].(string); ok && username != "" {
			return true
		}
	}
	writeError(w, http.StatusUnauthorized, "Login admin diperlukan")
	return false
}

func accountHistoryPage(r *http.Request) int {
	page, err := strconv.Atoi(r.URL.Query().Get("page"))
	if err != nil || page < 1 {
		return 1
	}
	return min(page, 1_000_000)
}

func loadAccountDetails(ctx context.Context, pool *dbstore.Pool, id int64, ownerID *int64, page int) (map[string]any, error) {
	query := `SELECT a.id,a.label,a.server,a.login,a.plan,a.execution_mode,a.active,a.last_seen_at,s.balance,s.equity,s.floating_profit,s.margin,s.free_margin,s.currency,s.trade_mode,s.terminal_trade_allowed,s.expert_trade_allowed,s.open_position_ids_json,s.observed_at FROM mt5_accounts a LEFT JOIN mt5_account_state s ON s.account_id=a.id WHERE a.id=$1 AND a.role='follower'`
	args := []any{id}
	if ownerID != nil {
		query += ` AND a.owner_id=$2`
		args = append(args, *ownerID)
	}
	var aid int64
	var label, server, login, plan, mode string
	var active bool
	var last, observed sql.NullTime
	var balance, equity, floating, margin, free sql.NullFloat64
	var currency, tradeMode, open sql.NullString
	var terminal, expert sql.NullBool
	err := pool.QueryRow(ctx, query, args...).Scan(&aid, &label, &server, &login, &plan, &mode, &active, &last, &balance, &equity, &floating, &margin, &free, &currency, &tradeMode, &terminal, &expert, &open, &observed)
	if err != nil {
		return nil, err
	}
	wib := time.FixedZone("WIB", 7*60*60)
	days, err := accountDailyProfit(ctx, pool, id, currency.String)
	if err != nil {
		return nil, err
	}
	var historyTotal int
	if err := pool.QueryRow(ctx, `SELECT count(DISTINCT COALESCE(NULLIF(position_id,''),deal_ticket)) FROM mt5_history_deals WHERE account_id=$1`, id).Scan(&historyTotal); err != nil {
		return nil, err
	}
	pageCount := max(1, int(math.Ceil(float64(historyTotal)/5)))
	page = min(page, pageCount)
	// One row per position (like MT5's Positions history): the closing deal of a
	// BUY is a SELL, so deals are grouped and the direction comes from the opening deal.
	rows, err := pool.Query(ctx, `
		SELECT k, MIN(symbol),
		       COALESCE(MAX(CASE WHEN entry='IN' THEN action END),''),
		       COALESCE(MAX(CASE WHEN entry<>'IN' THEN action END),''),
		       SUM(CASE WHEN entry='IN' THEN volume ELSE 0 END),
		       SUM(CASE WHEN entry<>'IN' THEN volume ELSE 0 END),
		       SUM(CASE WHEN entry='IN' THEN price*volume ELSE 0 END),
		       SUM(CASE WHEN entry<>'IN' THEN price*volume ELSE 0 END),
		       MIN(CASE WHEN entry='IN' THEN deal_time_msc END),
		       MAX(CASE WHEN entry<>'IN' THEN deal_time_msc END),
		       SUM(profit), SUM(commission), SUM(swap), SUM(fee)
		FROM (SELECT COALESCE(NULLIF(position_id,''),deal_ticket) AS k, * FROM mt5_history_deals WHERE account_id=$1)
		GROUP BY k
		ORDER BY MAX(deal_time_msc) DESC, k DESC
		LIMIT 5 OFFSET $2`, id, (page-1)*5)
	if err != nil {
		return nil, err
	}
	history := make([]map[string]any, 0)
	for rows.Next() {
		var ticket, symbol, openAction, closeAction string
		var volIn, volOut, valueIn, valueOut, profit, commission, swap, fee float64
		var openMSC, closeMSC sql.NullInt64
		if err := rows.Scan(&ticket, &symbol, &openAction, &closeAction, &volIn, &volOut, &valueIn, &valueOut, &openMSC, &closeMSC, &profit, &commission, &swap, &fee); err != nil {
			rows.Close()
			return nil, err
		}
		action, volume := openAction, volIn
		if action == "" {
			// Opened before the reported history window: derive from the closing deal.
			if closeAction == "SELL" {
				action = "BUY"
			} else if closeAction == "BUY" {
				action = "SELL"
			}
		}
		if volume <= 0 {
			volume = volOut
		}
		item := map[string]any{"ticket": ticket, "symbol": symbol, "action": action, "volume": volume, "open_time": nil, "close_time": nil, "open_price": nil, "close_price": nil, "status": "OPEN", "profit": profit, "commission": commission, "swap": swap, "fee": fee, "net_profit": profit + commission + swap + fee}
		if openMSC.Valid {
			item["open_time"] = time.UnixMilli(openMSC.Int64).In(wib).Format(time.RFC3339Nano)
		}
		if volIn > 0 {
			item["open_price"] = valueIn / volIn
		}
		if closeMSC.Valid {
			item["close_time"] = time.UnixMilli(closeMSC.Int64).In(wib).Format(time.RFC3339Nano)
			item["status"] = "CLOSED"
			if volOut > 0 {
				item["close_price"] = valueOut / volOut
			}
		}
		history = append(history, item)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	var positions []string
	if open.Valid {
		_ = json.Unmarshal([]byte(open.String), &positions)
	}
	return map[string]any{"id": aid, "label": label, "server": server, "login": login, "plan": plan, "execution_mode": mode, "active": active, "last_seen_at": nullableWIB(last, wib), "balance": nullableFloat(balance), "equity": nullableFloat(equity), "floating_profit": nullableFloat(floating), "margin": nullableFloat(margin), "free_margin": nullableFloat(free), "currency": nullableString(currency), "trade_mode": nullableString(tradeMode), "terminal_trade_allowed": nullableBool(terminal), "expert_trade_allowed": nullableBool(expert), "open_positions": len(positions), "state_updated_at": nullableWIB(observed, wib), "daily_profit": days, "history": history, "history_page": page, "history_pages": pageCount, "history_total": historyTotal}, nil
}

func accountDailyProfit(ctx context.Context, pool *dbstore.Pool, id int64, currency string) ([]map[string]any, error) {
	zone := time.FixedZone("WIB", 7*60*60)
	now := time.Now().In(zone)
	first := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, zone).AddDate(0, 0, -6)
	rows, err := pool.Query(ctx, `SELECT trade_date,profit,currency FROM mt5_account_daily_profit WHERE account_id=$1 AND trade_date >= $2 ORDER BY trade_date DESC`, id, first.Format("2006-01-02"))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	byDate := map[string]float64{}
	byCurrency := map[string]string{}
	for rows.Next() {
		var day time.Time
		var profit float64
		var cur string
		if err := rows.Scan(&day, &profit, &cur); err != nil {
			return nil, err
		}
		key := day.Format("2006-01-02")
		byDate[key] = profit
		byCurrency[key] = cur
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]map[string]any, 0, 7)
	for i := 0; i < 7; i++ {
		day := first.AddDate(0, 0, i)
		key := day.Format("2006-01-02")
		cur := byCurrency[key]
		if cur == "" {
			cur = currency
		}
		out = append(out, map[string]any{"date": key, "profit": byDate[key], "currency": cur, "timezone": "WIB"})
	}
	return out, nil
}

func refreshAccountDailyProfit(ctx context.Context, tx *dbstore.Tx, accountID int64, deals []followerDeal, currency string, now time.Time) error {
	if len(deals) == 0 {
		return nil
	}
	zone := time.FixedZone("WIB", 7*60*60)
	startByDay := map[string]time.Time{}
	for _, d := range deals {
		day := time.UnixMilli(d.TimeMSC).In(zone)
		day = time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, zone)
		startByDay[day.Format("2006-01-02")] = day.UTC()
	}
	for key, start := range startByDay {
		var total float64
		if err := tx.QueryRow(ctx, `SELECT COALESCE(sum(profit+commission+swap+fee),0) FROM mt5_history_deals WHERE account_id=$1 AND deal_time >= $2 AND deal_time < $3`, accountID, start, start.Add(24*time.Hour)).Scan(&total); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO mt5_account_daily_profit(account_id,trade_date,currency,profit,updated_at) VALUES($1,$2,$3,$4,$5) ON CONFLICT(account_id,trade_date) DO UPDATE SET currency=EXCLUDED.currency,profit=EXCLUDED.profit,updated_at=EXCLUDED.updated_at`, accountID, key, currency, total, now); err != nil {
			return err
		}
	}
	return nil
}

func nullableBool(value sql.NullBool) any {
	if !value.Valid {
		return nil
	}
	return value.Bool
}
