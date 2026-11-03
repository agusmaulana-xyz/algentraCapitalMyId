package httpapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	dbstore "github.com/rnpproject/algentra-backend/internal/database"
	"math"
	"net/http"
	"strings"
	"time"
)

type tradeResultRequest struct {
	Ticket   json.RawMessage `json:"ticket"`
	Profit   float64         `json:"profit"`
	Result   string          `json:"result"`
	ClosedAt json.RawMessage `json:"closed_at"`
}

func (api eaSignalAPI) result(w http.ResponseWriter, r *http.Request) {
	if !api.authorizeEA(w, r) {
		return
	}
	var payload tradeResultRequest
	if err := decodeJSON(w, r, &payload, 64<<10); err != nil || !validTradeResult(payload) {
		writeError(w, http.StatusUnprocessableEntity, "Hasil trade tidak valid")
		return
	}
	ticket, err := parseOptionalTicket(payload.Ticket)
	if err != nil || ticket == nil || strings.TrimSpace(*ticket) == "" {
		writeError(w, http.StatusUnprocessableEntity, "Ticket tidak valid")
		return
	}
	parsedClosedAt, err := parseTradeResultTime(payload.ClosedAt)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, "Waktu penutupan trade tidak valid")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	tx, err := api.pool.Begin(ctx)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "Hasil trade tidak dapat disimpan")
		return
	}
	defer tx.Rollback(ctx)
	var tradeID int64
	var signalID sql.NullInt64
	var closedAt sql.NullTime
	var priorResult sql.NullString
	err = tx.QueryRow(ctx, `SELECT id, signal_id, closed_at, result FROM trades WHERE ticket=$1 FOR UPDATE`, *ticket).
		Scan(&tradeID, &signalID, &closedAt, &priorResult)
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "Trade/ticket tidak ditemukan")
		return
	}
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "Trade tidak tersedia")
		return
	}
	if closedAt.Valid {
		writeJSON(w, http.StatusOK, map[string]any{"status": nullableString(priorResult), "duplicate": true})
		return
	}
	closed := time.Now().UTC()
	if parsedClosedAt != nil {
		closed = parsedClosedAt.UTC()
	}
	if _, err := tx.Exec(ctx, `UPDATE trades SET profit=$2, result=$3, closed_at=$4 WHERE id=$1`, tradeID, payload.Profit, payload.Result, closed); err != nil {
		writeError(w, http.StatusServiceUnavailable, "Trade tidak dapat ditutup")
		return
	}
	masterDay := closed.In(time.FixedZone("WIB", 7*60*60)).Format("2006-01-02")
	if _, err := tx.Exec(ctx, `INSERT INTO mt5_master_daily_profit(trade_date,profit,updated_at) VALUES($1,$2,$3) ON CONFLICT(trade_date) DO UPDATE SET profit=mt5_master_daily_profit.profit+EXCLUDED.profit,updated_at=EXCLUDED.updated_at`, masterDay, payload.Profit, time.Now().UTC()); err != nil {
		writeError(w, http.StatusServiceUnavailable, "Profit harian akun master tidak dapat disimpan")
		return
	}
	if signalID.Valid {
		var raw sql.NullString
		if err := tx.QueryRow(ctx, `SELECT parsed_json FROM signals WHERE id=$1`, signalID.Int64).Scan(&raw); err != nil && !errors.Is(err, sql.ErrNoRows) {
			writeError(w, http.StatusServiceUnavailable, "Signal tidak tersedia")
			return
		}
		parsed := make(map[string]any)
		if raw.Valid {
			_ = json.Unmarshal([]byte(raw.String), &parsed)
		}
		expectedLegs := expectedSignalLegs(parsed)
		var hasExecutions bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM ea_executions WHERE signal_id=$1)`, signalID.Int64).Scan(&hasExecutions); err != nil {
			writeError(w, http.StatusServiceUnavailable, "Eksekusi signal tidak tersedia")
			return
		}
		if hasExecutions {
			if err := syncSignalExecutionStatus(ctx, tx, signalID.Int64, expectedLegs); err != nil {
				writeError(w, http.StatusServiceUnavailable, "Status signal tidak dapat diperbarui")
				return
			}
		} else {
			if err := syncClosedSignalStatus(ctx, tx, signalID.Int64); err != nil {
				writeError(w, http.StatusServiceUnavailable, "Status signal tidak dapat diperbarui")
				return
			}
		}
	}
	if _, err := tx.Exec(ctx, `INSERT INTO logs (level,source,message,created_at) VALUES ('INFO','EA',$1,$2)`,
		fmt.Sprintf("Ticket %s ditutup: %s, profit %g", *ticket, payload.Result, payload.Profit), time.Now().UTC()); err != nil {
		writeError(w, http.StatusServiceUnavailable, "Log trade tidak dapat disimpan")
		return
	}
	if err := tx.Commit(ctx); err != nil {
		writeError(w, http.StatusServiceUnavailable, "Hasil trade tidak dapat disimpan")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": payload.Result, "ticket": *ticket, "duplicate": false})
}

func parseTradeResultTime(raw json.RawMessage) (*time.Time, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil || strings.TrimSpace(value) == "" {
		return nil, fmt.Errorf("closed_at must be a timestamp string")
	}
	for _, layout := range []string{time.RFC3339Nano, "2006.01.02T15:04:05", "2006-01-02T15:04:05"} {
		if parsed, err := time.Parse(layout, value); err == nil {
			return &parsed, nil
		}
	}
	return nil, fmt.Errorf("unsupported closed_at format")
}

func validTradeResult(p tradeResultRequest) bool {
	return finite(p.Profit) && (p.Result == "WIN" || p.Result == "LOSS" || p.Result == "BE" || p.Result == "EXPIRED") &&
		!(p.Result == "WIN" && p.Profit <= 0) && !(p.Result == "LOSS" && p.Profit >= 0)
}

func syncClosedSignalStatus(ctx context.Context, tx *dbstore.Tx, signalID int64) error {
	var openTrade int64
	err := tx.QueryRow(ctx, `SELECT id FROM trades WHERE signal_id=$1 AND closed_at IS NULL LIMIT 1`, signalID).Scan(&openTrade)
	if err == nil {
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	rows, err := tx.Query(ctx, `SELECT result,profit FROM trades WHERE signal_id=$1`, signalID)
	if err != nil {
		return err
	}
	var total float64
	allExpired, any := true, false
	for rows.Next() {
		var result sql.NullString
		var profit float64
		if err := rows.Scan(&result, &profit); err != nil {
			rows.Close()
			return err
		}
		any = true
		if !result.Valid || result.String != "EXPIRED" {
			allExpired = false
		}
		total += profit
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	if !any {
		return nil
	}
	if allExpired {
		return setSignalStatus(ctx, tx, signalID, "EXPIRED")
	}
	status := "LOSS"
	if math.Abs(total) < 0.005 {
		status = "BE"
	} else if total > 0 {
		status = "WIN"
	}
	return setSignalStatus(ctx, tx, signalID, status)
}
