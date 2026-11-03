package httpapi

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"strings"
	"time"

	telegramclient "github.com/rnpproject/algentra-backend/internal/telegram"
	"golang.org/x/text/cases"
)

var telegramPricePattern = regexp.MustCompile(`\d+(?:[.,]\d+)?`)
var telegramClockPattern = regexp.MustCompile(`\b\d{1,2}:\d{2}\b`)
var telegramMarkerPattern = regexp.MustCompile(`(?i)\b(?:TP\s*[0-9]*|TAKE[\s_-]*PROFIT|TARGET\s*[0-9]*|SL|STOP[\s_-]*LOSS|ENTRY|ZONE|NOW|MARKET|LIMIT|STOP)\b`)
var telegramMarketNowPattern = regexp.MustCompile(`(?i)\b(?:NOW|MARKET)\b`)
var telegramResultPattern = regexp.MustCompile(`(?i)\b(?:TP\s*[0-9]*|SL|STOP\s*LOSS)\s*(?:HIT|KEN[A]?|REACHED|TERCAPAI)\b|\b(?:CLOSED|CLOSE|PROFIT|LOSS)\s+(?:TRADE|POSITION|POSISI)\b|\b(?:CLOSE|TUTUP)\s+(?:NOW|ALL|POSITION|POSISI|SEKARANG|SEMUA)\b|\b(?:MOVE|ADJUST|GESER|PINDAH|UBAH)\b.{0,30}\b(?:SL|STOP\s*LOSS|BE|BREAKEVEN)\b`)

func (api telegramAPI) processMessage(parent context.Context, message telegramclient.IncomingMessage) error {
	err := api.processTelegramMessage(parent, message)
	if err != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		_, _ = api.pool.Exec(ctx, `INSERT INTO logs(level,source,message,created_at) VALUES('ERROR','Telegram',$1,$2)`, "Message processing failed: "+fmt.Sprintf("%T", err), time.Now().UTC())
		cancel()
	}
	return err
}

func (api telegramAPI) processTelegramMessage(parent context.Context, message telegramclient.IncomingMessage) error {
	ctx, cancel := context.WithTimeout(parent, 40*time.Second)
	defer cancel()
	raw := strings.TrimSpace(message.Text)
	if raw == "" || len([]rune(raw)) > 12000 {
		return nil
	}
	var displayName string
	err := api.pool.QueryRow(ctx, `SELECT COALESCE(NULLIF(alias,''),name) FROM groups WHERE chat_id=$1 AND enabled=TRUE`, message.GroupID).Scan(&displayName)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	rows, err := api.pool.Query(ctx, `SELECT raw_text FROM signals WHERE group_id=$1 ORDER BY created_at DESC,id DESC LIMIT 5`, message.GroupID)
	if err != nil {
		return err
	}
	recent := make([]string, 0, 5)
	for rows.Next() {
		var prior string
		if err := rows.Scan(&prior); err != nil {
			rows.Close()
			return err
		}
		recent = append(recent, prior)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	for left, right := 0, len(recent)-1; left < right; left, right = left+1, right-1 {
		recent[left], recent[right] = recent[right], recent[left]
	}
	if message.ReplyText != "" && !containsString(recent, message.ReplyText) {
		recent = append(recent, message.ReplyText)
		if len(recent) > 5 {
			recent = recent[len(recent)-5:]
		}
	}
	contextText := strings.Join(recent, "\n")
	if !isTelegramSignalCandidate(raw, contextText) {
		return nil
	}
	var duplicateID int64
	err = api.pool.QueryRow(ctx, `SELECT id FROM signals WHERE group_id=$1 AND message_id=$2 LIMIT 1`, message.GroupID, message.MessageID).Scan(&duplicateID)
	if err == nil {
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	stamp := message.CreatedAt.UTC()
	if stamp.IsZero() {
		stamp = time.Now().UTC()
	}
	content := cases.Fold().String(strings.Join(strings.Fields(raw), " "))
	hash := sha256.Sum256([]byte(message.GroupID + "|" + stamp.Format("200601021504") + "|" + content))
	contentHash := hex.EncodeToString(hash[:])
	err = api.pool.QueryRow(ctx, `SELECT id FROM signals WHERE content_hash=$1`, contentHash).Scan(&duplicateID)
	if err == nil {
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	var prior *string
	if contextText != "" {
		prior = &contextText
	}
	classification := fallbackClassification(raw)
	promptContent := parserPromptContent(raw, prior)
	if parserPromptInjection.MatchString(promptContent) {
		return nil
	}
	if !parserStructuredEnough(raw, classification) && api.parser.key == "" {
		if !api.parser.regexFallback {
			return api.saveTelegramSignal(ctx, message, stamp, displayName, raw, nil, nil, nil, contentHash, "FAILED", "ERROR", "Gemini", "Parser gagal: GEMINI_API_KEY belum dikonfigurasi")
		}
		classification = normalizeParserEntryRange(fallbackClassification(parserPromptContent(raw, prior)), raw)
	} else if !parserStructuredEnough(raw, classification) {
		parsed, parseErr := api.parser.gemini(ctx, raw, prior)
		if parseErr != nil {
			if !api.parser.regexFallback {
				return api.saveTelegramSignal(ctx, message, stamp, displayName, raw, nil, nil, nil, contentHash, "FAILED", "ERROR", "Gemini", "Parser gagal: layanan Gemini tidak tersedia")
			}
			classification = normalizeParserEntryRange(fallbackClassification(parserPromptContent(raw, prior)), raw)
		} else {
			classification = parsed
		}
	}
	classification = preferFarthestTelegramTarget(classification)
	status, symbol, normalized, reason := validateParserClassification(classification, readParserSettings(ctx, api.pool))
	if status == "IGNORED" {
		return nil
	}
	if symbol != nil {
		classification.Symbol = symbol
	}
	if classification.TP == nil {
		classification.TP = []float64{}
	}
	parsed, err := json.Marshal(classification)
	if err != nil {
		return err
	}
	level := "INFO"
	if status != "PENDING" {
		level = "WARN"
	}
	return api.saveTelegramSignal(ctx, message, stamp, displayName, raw, parsed, normalized, &classification.Confidence, contentHash, status, level, "Signal", displayName+": "+status+" — "+reason)
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func isTelegramSignalCandidate(text, contextText string) bool {
	text = strings.TrimSpace(text)
	if text == "" || telegramResultPattern.MatchString(text) {
		return false
	}
	direction := parserAction.MatchString(text)
	if !direction && !parserAction.MatchString(contextText) {
		return false
	}
	priceText := telegramClockPattern.ReplaceAllString(text, " ")
	hasLevel := telegramPricePattern.MatchString(priceText)
	if !hasLevel && !(telegramMarketNowPattern.MatchString(text) && parserSymbol.MatchString(text)) {
		return false
	}
	return direction || telegramMarkerPattern.MatchString(text)
}

func (api telegramAPI) saveTelegramSignal(ctx context.Context, message telegramclient.IncomingMessage, stamp time.Time, displayName, raw string, parsed []byte, normalized *string, confidence *float64, contentHash, status, level, source, logMessage string) error {
	tx, err := api.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var parsedValue, normalizedValue, senderID, senderName any
	if parsed != nil {
		parsedValue = string(parsed)
	}
	if normalized != nil {
		normalizedValue = *normalized
	}
	if message.SenderID != "" {
		senderID = message.SenderID
	}
	if message.SenderName != "" {
		senderName = message.SenderName
	}
	var insertedID int64
	err = tx.QueryRow(ctx, `INSERT INTO signals(group_id,group_name,message_id,sender_id,sender_name,raw_text,parsed_json,normalized_text,confidence,content_hash,status,created_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12) ON CONFLICT DO NOTHING RETURNING id`, message.GroupID, displayName, message.MessageID, senderID, senderName, raw, parsedValue, normalizedValue, confidence, contentHash, status, stamp).Scan(&insertedID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := insertSystemLog(ctx, tx, time.Now().UTC(), level, source, logMessage); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func preferFarthestTelegramTarget(value signalClassification) signalClassification {
	if value.Type != "NEW_SIGNAL" || value.Action == nil || value.SL == nil || len(value.TP) < 2 || (*value.Action != "BUY" && *value.Action != "SELL") || !finite(*value.SL) || *value.SL <= 0 {
		return value
	}
	farthest := value.TP[0]
	for _, target := range value.TP {
		if !finite(target) || target <= 0 {
			return value
		}
		if *value.Action == "BUY" && target > farthest || *value.Action == "SELL" && target < farthest {
			farthest = target
		}
	}
	var reference *float64
	if value.EntryLow != nil && value.EntryHigh != nil {
		if *value.Action == "BUY" {
			reference = value.EntryHigh
		} else {
			reference = value.EntryLow
		}
	} else if value.Entry != nil {
		reference = value.Entry
	}
	if reference == nil {
		if *value.Action == "BUY" {
			nearest := value.TP[0]
			for _, target := range value.TP[1:] {
				nearest = math.Min(nearest, target)
			}
			if *value.SL >= nearest {
				return value
			}
		} else {
			nearest := value.TP[0]
			for _, target := range value.TP[1:] {
				nearest = math.Max(nearest, target)
			}
			if *value.SL <= nearest {
				return value
			}
		}
		value.TP = []float64{farthest}
		value.Reason = fmt.Sprintf("Target terjauh %g dipilih; EA memvalidasi rasio risiko dari quote live", farthest)
		return value
	}
	if !finite(*reference) || *reference <= 0 {
		return value
	}
	if *value.Action == "BUY" && (*value.SL >= *reference) || *value.Action == "SELL" && (*value.SL <= *reference) {
		return value
	}
	nearestDistance := math.Inf(1)
	farthestDistance := 0.0
	for _, target := range value.TP {
		distance := math.Abs(target - *reference)
		if distance < nearestDistance {
			nearestDistance = distance
		}
		if distance > farthestDistance {
			farthestDistance = distance
		}
	}
	for _, target := range value.TP {
		if *value.Action == "BUY" && target <= *reference || *value.Action == "SELL" && target >= *reference {
			return value
		}
	}
	riskDistance := math.Abs(*reference - *value.SL)
	if riskDistance > nearestDistance && riskDistance <= farthestDistance {
		value.TP = []float64{farthest}
		value.Reason = fmt.Sprintf("Target terjauh %g dipilih karena TP terdekat lebih pendek dari risiko SL", farthest)
	}
	return value
}
