package httpapi

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	dbstore "github.com/rnpproject/algentra-backend/internal/database"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	eaClaimLease   = 90 * time.Second
	eaSignalMaxAge = time.Hour
	eaMaxTargets   = 20
)

type eaSignalAPI struct {
	pool *dbstore.Pool
	key  string
}

type parsedSignal struct {
	IsSignal   bool       `json:"is_signal"`
	Type       string     `json:"type"`
	Action     *string    `json:"action"`
	OrderType  *string    `json:"order_type"`
	Symbol     *string    `json:"symbol"`
	Entry      *float64   `json:"entry"`
	EntryLow   *float64   `json:"entry_low"`
	EntryHigh  *float64   `json:"entry_high"`
	TP         *[]float64 `json:"tp"`
	SL         *float64   `json:"sl"`
	Confidence float64    `json:"confidence"`
	Reason     string     `json:"reason"`
}

type pendingSignalRow struct {
	ID         int64
	GroupID    sql.NullString
	GroupName  sql.NullString
	RawText    string
	ParsedJSON sql.NullString
	CreatedAt  time.Time
}

func (api eaSignalAPI) authorizeEA(w http.ResponseWriter, r *http.Request) bool {
	if api.key == "" {
		writeError(w, http.StatusServiceUnavailable, "EA_API_KEY belum dikonfigurasi")
		return false
	}
	supplied := r.Header.Get("X-API-Key")
	if len(supplied) != len(api.key) || subtle.ConstantTimeCompare([]byte(supplied), []byte(api.key)) != 1 {
		writeError(w, http.StatusUnauthorized, "API key EA tidak valid")
		return false
	}
	return true
}

func (api eaSignalAPI) pending(w http.ResponseWriter, r *http.Request) {
	if !api.authorizeEA(w, r) {
		return
	}
	limit := int64(1)
	if raw := r.URL.Query().Get("limit"); raw != "" {
		parsed, err := strconv.ParseInt(raw, 10, 32)
		if err != nil || parsed < 1 || parsed > 10 {
			writeError(w, http.StatusUnprocessableEntity, "limit harus antara 1 dan 10")
			return
		}
		limit = parsed
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	now := time.Now().UTC()
	responseControls, paused, err := loadTradingControls(ctx, api.pool, now)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "Kontrol trading tidak tersedia")
		return
	}
	delete(responseControls, "daily_profit")
	delete(responseControls, "trading_paused")
	if paused {
		responseControls["items"] = []any{}
		responseControls["server_time"] = wibTime(now)
		writeJSON(w, http.StatusOK, responseControls)
		return
	}
	tx, err := api.pool.Begin(ctx)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "Sinyal tidak tersedia")
		return
	}
	defer tx.Rollback(ctx)
	rows, err := tx.Query(ctx, `
		UPDATE signals SET status = 'CLAIMED', claimed_at = $3
		WHERE id IN (
			SELECT id FROM signals
			WHERE status = 'PENDING' OR (status = 'CLAIMED' AND (claimed_at IS NULL OR claimed_at < $1))
			ORDER BY created_at ASC, id ASC LIMIT $2
		)
		RETURNING id, group_id, group_name, raw_text, parsed_json, created_at
	`, now.Add(-eaClaimLease), limit, now)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "Sinyal tidak dapat diklaim")
		return
	}
	claimed := make([]pendingSignalRow, 0, limit)
	for rows.Next() {
		var row pendingSignalRow
		if err := rows.Scan(&row.ID, &row.GroupID, &row.GroupName, &row.RawText, &row.ParsedJSON, &row.CreatedAt); err != nil {
			rows.Close()
			writeError(w, http.StatusServiceUnavailable, "Sinyal tidak dapat dibaca")
			return
		}
		claimed = append(claimed, row)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		writeError(w, http.StatusServiceUnavailable, "Sinyal tidak dapat dibaca")
		return
	}
	rows.Close()
	sort.Slice(claimed, func(i, j int) bool {
		if claimed[i].CreatedAt.Equal(claimed[j].CreatedAt) {
			return claimed[i].ID < claimed[j].ID
		}
		return claimed[i].CreatedAt.Before(claimed[j].CreatedAt)
	})
	items := make([]map[string]any, 0, len(claimed))
	entryMode, err := loadEntryMode(ctx, tx)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "Mode eksekusi tidak tersedia")
		return
	}
	for _, row := range claimed {
		if now.Sub(row.CreatedAt) > eaSignalMaxAge {
			if _, err := tx.Exec(ctx, `UPDATE signals SET status = 'REJECTED' WHERE id = $1`, row.ID); err != nil {
				writeError(w, http.StatusServiceUnavailable, "Status sinyal tidak dapat diperbarui")
				return
			}
			if err := insertSystemLog(ctx, tx, now, "WARN", "EA", fmt.Sprintf("Signal %d ditolak karena kedaluwarsa", row.ID)); err != nil {
				writeError(w, http.StatusServiceUnavailable, "Log sinyal tidak dapat disimpan")
				return
			}
			continue
		}
		var data map[string]any
		var parsed parsedSignal
		if !row.ParsedJSON.Valid || json.Unmarshal([]byte(row.ParsedJSON.String), &data) != nil || json.Unmarshal([]byte(row.ParsedJSON.String), &parsed) != nil || !validParsedSignalJSON(data, parsed) {
			if _, err := tx.Exec(ctx, `UPDATE signals SET status = 'FAILED' WHERE id = $1`, row.ID); err != nil {
				writeError(w, http.StatusServiceUnavailable, "Status sinyal tidak dapat diperbarui")
				return
			}
			if err := insertSystemLog(ctx, tx, now, "ERROR", "EA", fmt.Sprintf("Signal %d memiliki parsed_json tidak valid", row.ID)); err != nil {
				writeError(w, http.StatusServiceUnavailable, "Log sinyal tidak dapat disimpan")
				return
			}
			continue
		}
		mode, ok := data["execution_mode"].(string)
		if !ok || mode != "SINGLE" && mode != "PARTIAL" {
			mode = entryMode
			data["execution_mode"] = mode
			encoded, err := json.Marshal(data)
			if err != nil {
				writeError(w, http.StatusServiceUnavailable, "Parsed signal tidak dapat diperbarui")
				return
			}
			if _, err := tx.Exec(ctx, `UPDATE signals SET parsed_json = $2 WHERE id = $1`, row.ID, string(encoded)); err != nil {
				writeError(w, http.StatusServiceUnavailable, "Parsed signal tidak dapat diperbarui")
				return
			}
		}
		symbol := "XAUUSD"
		if parsed.Symbol != nil && *parsed.Symbol != "" {
			symbol = *parsed.Symbol
		}
		created := row.CreatedAt.UTC()
		items = append(items, map[string]any{
			"signal_id": row.ID, "group_id": nullableString(row.GroupID), "group_name": nullableString(row.GroupName),
			"raw_text": row.RawText, "action": parsed.Action, "order_type": parsed.OrderType, "symbol": symbol,
			"entry": parsed.Entry, "entry_low": parsed.EntryLow, "entry_high": parsed.EntryHigh,
			"sl": parsed.SL, "tp": targetValues(parsed.TP), "entry_mode": mode, "confidence": parsed.Confidence,
			"rr_mode": responseControls["rr_mode"], "rr_fixed_sl_pips": responseControls["rr_fixed_sl_pips"],
			"rr_fixed_tp_pips": responseControls["rr_fixed_tp_pips"], "rr_reward_multiple": responseControls["rr_reward_multiple"],
			"created_at": wibTime(created), "created_epoch": created.Unix(),
		})
	}
	if err := tx.Commit(ctx); err != nil {
		writeError(w, http.StatusServiceUnavailable, "Klaim sinyal tidak dapat disimpan")
		return
	}
	responseControls["items"] = items
	responseControls["server_time"] = wibTime(now)
	writeJSON(w, http.StatusOK, responseControls)
}

func validateParsedSignal(value parsedSignal) bool {
	if value.Type != "NEW_SIGNAL" && value.Type != "UPDATE" && value.Type != "NOT_SIGNAL" {
		return false
	}
	if value.Action != nil && *value.Action != "BUY" && *value.Action != "SELL" {
		return false
	}
	if value.OrderType != nil && *value.OrderType != "MARKET" && *value.OrderType != "LIMIT" && *value.OrderType != "STOP" && *value.OrderType != "AUTO" {
		return false
	}
	if !finite(value.Confidence) || value.Confidence < 0 || value.Confidence > 1 || utf8.RuneCountInString(value.Reason) > 300 {
		return false
	}
	for _, number := range []*float64{value.Entry, value.EntryLow, value.EntryHigh, value.SL} {
		if number != nil && !finite(*number) {
			return false
		}
	}
	if value.TP != nil {
		for _, target := range *value.TP {
			if !finite(target) {
				return false
			}
		}
	}
	return true
}

func validParsedSignalJSON(data map[string]any, value parsedSignal) bool {
	for _, key := range []string{"is_signal", "type", "action", "order_type", "symbol", "entry", "tp", "sl", "confidence", "reason"} {
		if _, ok := data[key]; !ok {
			return false
		}
	}
	if _, ok := data["is_signal"].(bool); !ok {
		return false
	}
	if data["reason"] == nil {
		return false
	}
	if _, ok := data["reason"].(string); !ok {
		return false
	}
	return validateParsedSignal(value)
}

func targetValues(value *[]float64) []float64 {
	if value == nil {
		return []float64{}
	}
	result := make([]float64, 0, len(*value))
	for _, target := range *value {
		if finite(target) && target > 0 {
			result = append(result, target)
			if len(result) == eaMaxTargets {
				break
			}
		}
	}
	return result
}

func loadEntryMode(ctx context.Context, tx *dbstore.Tx) (string, error) {
	var value string
	err := tx.QueryRow(ctx, `SELECT value FROM settings WHERE key = 'entry_mode'`).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return "PARTIAL", nil
	}
	if err != nil {
		return "", err
	}
	var mode string
	if json.Unmarshal([]byte(value), &mode) != nil || mode != "SINGLE" && mode != "PARTIAL" {
		return "PARTIAL", nil
	}
	return mode, nil
}

func loadTradingControls(ctx context.Context, pool *dbstore.Pool, now time.Time) (map[string]any, bool, error) {
	var killRaw, deviationRaw string
	err := pool.QueryRow(ctx, `SELECT value FROM settings WHERE key = 'kill_switch'`).Scan(&killRaw)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, false, err
	}
	var kill bool
	if err == nil {
		_ = json.Unmarshal([]byte(killRaw), &kill)
	}
	err = pool.QueryRow(ctx, `SELECT value FROM settings WHERE key = 'max_market_deviation_pct'`).Scan(&deviationRaw)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, false, err
	}
	deviation := 5.0
	if err == nil {
		var candidate float64
		if json.Unmarshal([]byte(deviationRaw), &candidate) == nil && finite(candidate) && candidate >= 0.1 && candidate <= 100 {
			deviation = candidate
		}
	}
	local := now.In(time.FixedZone("WIB", 7*60*60))
	start := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, local.Location()).UTC()
	var profit float64
	var openTrades int64
	if err := pool.QueryRow(ctx, `SELECT COALESCE(SUM(profit),0) FROM trades WHERE closed_at IS NOT NULL AND closed_at >= $1`, start).Scan(&profit); err != nil {
		return nil, false, err
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM trades WHERE opened_at IS NOT NULL AND closed_at IS NULL`).Scan(&openTrades); err != nil {
		return nil, false, err
	}
	loss := math.Max(0, -profit)
	rrSettings, err := loadRRSettings(ctx, pool)
	if err != nil {
		return nil, false, err
	}
	controls := map[string]any{
		"kill_switch": kill, "daily_profit": math.Round(profit*100) / 100, "daily_loss": math.Round(loss*100) / 100,
		"open_trades": openTrades, "max_market_deviation_pct": deviation,
		"trading_paused": kill,
	}
	for key, value := range rrSettings {
		controls[key] = value
	}
	return controls, kill, nil
}

func loadRRSettings(ctx context.Context, pool *dbstore.Pool) (map[string]any, error) {
	values := map[string]any{
		"rr_mode": "SIGNAL", "rr_fixed_sl_pips": 200.0,
		"rr_fixed_tp_pips": 400.0, "rr_reward_multiple": 2.0,
	}
	rows, err := pool.Query(ctx, `SELECT key,value FROM settings WHERE key IN ('rr_mode','rr_fixed_sl_pips','rr_fixed_tp_pips','rr_reward_multiple')`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var key, raw string
		if err := rows.Scan(&key, &raw); err != nil {
			return nil, err
		}
		switch key {
		case "rr_mode":
			var mode string
			if json.Unmarshal([]byte(raw), &mode) == nil && (mode == "SIGNAL" || mode == "FIXED" || mode == "BY_SL") {
				values[key] = mode
			}
		case "rr_fixed_sl_pips", "rr_fixed_tp_pips", "rr_reward_multiple":
			var number float64
			if json.Unmarshal([]byte(raw), &number) == nil && finite(number) && number > 0 {
				values[key] = number
			}
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return values, nil
}

func insertSystemLog(ctx context.Context, tx *dbstore.Tx, now time.Time, level, source, message string) error {
	_, err := tx.Exec(ctx, `INSERT INTO logs (level, source, message, created_at) VALUES ($1,$2,$3,$4)`, level, source, message, now)
	return err
}

type executionReportRequest struct {
	SignalID  int64           `json:"signal_id"`
	Status    string          `json:"status"`
	Ticket    json.RawMessage `json:"ticket"`
	Symbol    *string         `json:"symbol"`
	Action    *string         `json:"action"`
	Lots      *float64        `json:"lots"`
	ExecPrice *float64        `json:"exec_price"`
	Reason    *string         `json:"reason"`
	Leg       *int            `json:"leg"`
}

func (api eaSignalAPI) report(w http.ResponseWriter, r *http.Request) {
	if !api.authorizeEA(w, r) {
		return
	}
	var payload executionReportRequest
	if err := decodeJSON(w, r, &payload, 64<<10); err != nil || !validExecutionReport(payload) {
		writeError(w, http.StatusUnprocessableEntity, "Laporan eksekusi tidak valid")
		return
	}
	ticket, err := parseOptionalTicket(payload.Ticket)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, "Ticket tidak valid")
		return
	}
	if payload.Status == "EXECUTED" && (ticket == nil || strings.TrimSpace(*ticket) == "" || *ticket == "0") {
		writeError(w, http.StatusUnprocessableEntity, "Ticket wajib diisi untuk status EXECUTED")
		return
	}
	if payload.Status != "EXECUTED" {
		if ticket != nil && (*ticket == "" || *ticket == "0") {
			ticket = nil
		}
		if payload.Lots != nil && *payload.Lots == 0 {
			payload.Lots = nil
		}
		if payload.ExecPrice != nil && *payload.ExecPrice == 0 {
			payload.ExecPrice = nil
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	tx, err := api.pool.Begin(ctx)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "Laporan eksekusi tidak dapat disimpan")
		return
	}
	defer tx.Rollback(ctx)
	var status string
	var parsedRaw sql.NullString
	err = tx.QueryRow(ctx, `SELECT status, parsed_json FROM signals WHERE id = $1 FOR UPDATE`, payload.SignalID).Scan(&status, &parsedRaw)
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "Signal tidak ditemukan")
		return
	}
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "Signal tidak tersedia")
		return
	}
	parsed := make(map[string]any)
	if parsedRaw.Valid {
		_ = json.Unmarshal([]byte(parsedRaw.String), &parsed)
	}
	expectedLegs := expectedSignalLegs(parsed)
	leg := 0
	if payload.Leg != nil {
		leg = *payload.Leg
	}
	multiLeg := expectedLegs > 1 && leg >= 1 && leg <= expectedLegs
	if payload.Status == "EXECUTED" && (expectedLegs > 1 && !multiLeg || expectedLegs == 1 && leg != 0) {
		writeError(w, http.StatusUnprocessableEntity, "Nomor entry tidak sesuai jumlah TP pada signal")
		return
	}
	var executionID int64
	var executionStatus string
	var executionTicket sql.NullString
	err = tx.QueryRow(ctx, `SELECT id, status, ticket FROM ea_executions WHERE signal_id = $1 AND leg = $2 FOR UPDATE`, payload.SignalID, leg).Scan(&executionID, &executionStatus, &executionTicket)
	executionExists := err == nil
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusServiceUnavailable, "Laporan eksekusi tidak dapat dibaca")
		return
	}
	if executionExists && executionStatus == payload.Status && (payload.Status != "EXECUTED" || executionTicket.Valid && ticket != nil && executionTicket.String == *ticket) {
		writeJSON(w, http.StatusOK, map[string]any{"status": status, "duplicate": true})
		return
	}
	if executionExists && executionStatus == "EXECUTED" && payload.Status != "EXECUTED" {
		writeJSON(w, http.StatusOK, map[string]any{"status": status, "duplicate": true})
		return
	}
	isFinal := status == "EXECUTED" || status == "REJECTED" || status == "FAILED" || status == "EXPIRED"
	canUpgrade := payload.Status == "EXECUTED" && executionExists && (executionStatus == "FAILED" || executionStatus == "REJECTED")
	if isFinal && !canUpgrade {
		writeJSON(w, http.StatusOK, map[string]any{"status": status, "duplicate": true})
		return
	}
	if !isFinal && status != "CLAIMED" {
		writeError(w, http.StatusConflict, "Signal belum diklaim oleh EA")
		return
	}
	now := time.Now().UTC()
	if payload.Status == "EXECUTED" && ticket != nil {
		var tradeID int64
		var tradeSignal sql.NullInt64
		err = tx.QueryRow(ctx, `SELECT id, signal_id FROM trades WHERE ticket = $1 FOR UPDATE`, *ticket).Scan(&tradeID, &tradeSignal)
		tradeExists := err == nil
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			writeError(w, http.StatusServiceUnavailable, "Ticket trade tidak dapat diperiksa")
			return
		}
		if tradeExists && tradeSignal.Valid && tradeSignal.Int64 != payload.SignalID {
			writeError(w, http.StatusConflict, "Ticket sudah terhubung ke signal lain")
			return
		}
		if err := upsertExecution(ctx, tx, executionExists, executionID, payload.SignalID, leg, payload, ticket, now); err != nil {
			writeError(w, http.StatusServiceUnavailable, "Laporan eksekusi tidak dapat disimpan")
			return
		}
		symbol := parsedString(parsed, "symbol")
		if payload.Symbol != nil {
			symbol = *payload.Symbol
		}
		action := parsedString(parsed, "action")
		if payload.Action != nil {
			action = *payload.Action
		}
		entry := parsed["entry"]
		if entry == nil && payload.ExecPrice != nil {
			entry = *payload.ExecPrice
		}
		targets := targetValuesFromMap(parsed)
		if expectedLegs > 1 {
			if leg <= len(targets) {
				targets = []float64{targets[leg-1]}
			} else {
				targets = []float64{}
			}
		}
		tpJSON, _ := json.Marshal(targets)
		if tradeExists {
			_, err = tx.Exec(ctx, `UPDATE trades SET signal_id=$2, symbol=$3, action=$4, entry=$5, sl=$6, tp=$7, lots=$8, exec_price=$9 WHERE id=$1`,
				tradeID, payload.SignalID, nullableAny(symbol), nullableAny(action), entry, parsed["sl"], string(tpJSON), payload.Lots, payload.ExecPrice)
		} else {
			_, err = tx.Exec(ctx, `INSERT INTO trades (signal_id,ticket,symbol,action,entry,sl,tp,lots,exec_price,profit,opened_at) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,0,$10)`,
				payload.SignalID, *ticket, nullableAny(symbol), nullableAny(action), entry, parsed["sl"], string(tpJSON), payload.Lots, payload.ExecPrice, now)
		}
		if err != nil {
			writeError(w, http.StatusServiceUnavailable, "Trade tidak dapat disimpan")
			return
		}
		if err := syncSignalExecutionStatus(ctx, tx, payload.SignalID, expectedLegs); err != nil {
			writeError(w, http.StatusServiceUnavailable, "Status signal tidak dapat diperbarui")
			return
		}
	} else if multiLeg {
		if err := upsertExecution(ctx, tx, executionExists, executionID, payload.SignalID, leg, payload, nil, now); err != nil {
			writeError(w, http.StatusServiceUnavailable, "Laporan eksekusi tidak dapat disimpan")
			return
		}
		if err := syncSignalExecutionStatus(ctx, tx, payload.SignalID, expectedLegs); err != nil {
			writeError(w, http.StatusServiceUnavailable, "Status signal tidak dapat diperbarui")
			return
		}
	} else {
		if _, err := tx.Exec(ctx, `UPDATE signals SET status = $2 WHERE id = $1`, payload.SignalID, payload.Status); err != nil {
			writeError(w, http.StatusServiceUnavailable, "Status signal tidak dapat diperbarui")
			return
		}
	}
	level := "WARN"
	if payload.Status == "EXECUTED" {
		level = "INFO"
	}
	message := fmt.Sprintf("Signal %d: %s", payload.SignalID, payload.Status)
	if payload.Reason != nil && *payload.Reason != "" {
		message += " — " + *payload.Reason
	}
	if err := insertSystemLog(ctx, tx, now, level, "EA", message); err != nil {
		writeError(w, http.StatusServiceUnavailable, "Log eksekusi tidak dapat disimpan")
		return
	}
	if err := tx.Commit(ctx); err != nil {
		writeError(w, http.StatusServiceUnavailable, "Laporan eksekusi tidak dapat disimpan")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": payload.Status, "signal_id": payload.SignalID, "leg": leg, "duplicate": false})
}

func validExecutionReport(p executionReportRequest) bool {
	if p.SignalID <= 0 || p.Status != "EXECUTED" && p.Status != "REJECTED" && p.Status != "FAILED" {
		return false
	}
	if p.Symbol != nil && utf8.RuneCountInString(*p.Symbol) > 64 || p.Reason != nil && utf8.RuneCountInString(*p.Reason) > 1000 {
		return false
	}
	if p.Action != nil && *p.Action != "BUY" && *p.Action != "SELL" {
		return false
	}
	if p.Lots != nil && (!finite(*p.Lots) || *p.Lots < 0 || *p.Lots > 1000) || p.ExecPrice != nil && (!finite(*p.ExecPrice) || *p.ExecPrice < 0) {
		return false
	}
	if p.Leg != nil && (*p.Leg < 0 || *p.Leg > 20) {
		return false
	}
	_, err := parseOptionalTicket(p.Ticket)
	return err == nil
}

func upsertExecution(ctx context.Context, tx *dbstore.Tx, exists bool, id, signalID int64, leg int, p executionReportRequest, ticket *string, now time.Time) error {
	if exists {
		_, err := tx.Exec(ctx, `UPDATE ea_executions SET status=$2, ticket=$3, lots=$4, exec_price=$5, reason=$6 WHERE id=$1`, id, p.Status, ticket, p.Lots, p.ExecPrice, p.Reason)
		return err
	}
	_, err := tx.Exec(ctx, `INSERT INTO ea_executions (signal_id,leg,status,ticket,lots,exec_price,reason,created_at) VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`, signalID, leg, p.Status, ticket, p.Lots, p.ExecPrice, p.Reason, now)
	return err
}

func parsedString(values map[string]any, key string) string {
	value, _ := values[key].(string)
	return value
}

func nullableAny(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func targetValuesFromMap(parsed map[string]any) []float64 {
	values, ok := parsed["tp"].([]any)
	if !ok {
		return []float64{}
	}
	targets := make([]float64, 0, len(values))
	for _, item := range values {
		value, ok := item.(float64)
		if ok && finite(value) && value > 0 {
			targets = append(targets, value)
			if len(targets) == eaMaxTargets {
				break
			}
		}
	}
	return targets
}

func expectedSignalLegs(parsed map[string]any) int {
	if parsed["execution_mode"] == "SINGLE" {
		return 1
	}
	if len(targetValuesFromMap(parsed)) > 0 {
		return len(targetValuesFromMap(parsed))
	}
	if parsed["entry_low"] != nil && parsed["entry_high"] != nil {
		return 2
	}
	return 1
}

func syncSignalExecutionStatus(ctx context.Context, tx *dbstore.Tx, signalID int64, expectedLegs int) error {
	rows, err := tx.Query(ctx, `SELECT leg,status FROM ea_executions WHERE signal_id = $1`, signalID)
	if err != nil {
		return err
	}
	byLeg := make(map[int]string)
	for rows.Next() {
		var leg int
		var status string
		if err := rows.Scan(&leg, &status); err != nil {
			rows.Close()
			return err
		}
		byLeg[leg] = status
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	legs := []int{0}
	if expectedLegs > 1 {
		legs = make([]int, expectedLegs)
		for index := range legs {
			legs[index] = index + 1
		}
	}
	statuses := make([]string, 0, len(legs))
	for _, leg := range legs {
		status, ok := byLeg[leg]
		if !ok {
			return setSignalStatus(ctx, tx, signalID, "CLAIMED")
		}
		statuses = append(statuses, status)
	}
	hasExecuted, hasFailed := false, false
	for _, status := range statuses {
		hasExecuted = hasExecuted || status == "EXECUTED"
		hasFailed = hasFailed || status == "FAILED"
	}
	if !hasExecuted {
		if hasFailed {
			return setSignalStatus(ctx, tx, signalID, "FAILED")
		}
		return setSignalStatus(ctx, tx, signalID, "REJECTED")
	}
	var openTrade int64
	err = tx.QueryRow(ctx, `SELECT id FROM trades WHERE signal_id=$1 AND closed_at IS NULL LIMIT 1`, signalID).Scan(&openTrade)
	if err == nil {
		return setSignalStatus(ctx, tx, signalID, "EXECUTED")
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	rows, err = tx.Query(ctx, `SELECT result,profit FROM trades WHERE signal_id=$1`, signalID)
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
	if any && allExpired {
		return setSignalStatus(ctx, tx, signalID, "EXPIRED")
	}
	if !any {
		return setSignalStatus(ctx, tx, signalID, "EXECUTED")
	}
	status := "LOSS"
	if math.Abs(total) < 0.005 {
		status = "BE"
	} else if total > 0 {
		status = "WIN"
	}
	return setSignalStatus(ctx, tx, signalID, status)
}

func setSignalStatus(ctx context.Context, tx *dbstore.Tx, signalID int64, status string) error {
	_, err := tx.Exec(ctx, `UPDATE signals SET status=$2 WHERE id=$1`, signalID, status)
	return err
}

func parseOptionalTicket(raw json.RawMessage) (*string, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var text string
	if raw[0] == '"' {
		if err := json.Unmarshal(raw, &text); err != nil {
			return nil, err
		}
	} else {
		var number json.Number
		decoder := json.NewDecoder(strings.NewReader(string(raw)))
		decoder.UseNumber()
		if err := decoder.Decode(&number); err != nil {
			return nil, err
		}
		text = number.String()
	}
	return &text, nil
}

func parseTradeTicket(raw json.RawMessage) (string, error) {
	ticket, err := parseOptionalTicket(raw)
	if err != nil || ticket == nil || strings.TrimSpace(*ticket) == "" {
		return "", errors.New("ticket tidak valid")
	}
	return *ticket, nil
}

func decimalInt(raw json.RawMessage) (int64, error) {
	var number json.Number
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.UseNumber()
	if err := decoder.Decode(&number); err != nil {
		return 0, err
	}
	return strconv.ParseInt(number.String(), 10, 64)
}
