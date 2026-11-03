package httpapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	maxCustomEAParams     = 200
	maxCustomEAParamValue = 256
)

// customEAInput describes one tunable `input` declared by an uploaded .mq5.
type customEAInput struct {
	Name    string `json:"name"`
	Type    string `json:"type"`
	Default string `json:"default"`
	Comment string `json:"comment"`
}

var (
	inputDeclaration = regexp.MustCompile(`^\s*(?:input|sinput)\s+([A-Za-z_][A-Za-z0-9_]*)\s+([A-Za-z_][A-Za-z0-9_]*)\s*(?:=\s*(.*?))?\s*;\s*(?://\s*(.*))?$`)
	paramNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,63}$`)
)

// parseMQ5Inputs lists the input variables of an .mq5 source. Compiled .ex5
// files cannot be inspected, so they yield no inputs.
func parseMQ5Inputs(source []byte) []customEAInput {
	text := string(source)
	// UTF-16 sources (MetaEditor default on Windows) contain NUL bytes.
	text = strings.ReplaceAll(strings.TrimPrefix(text, "\ufeff"), "\x00", "")
	inputs := make([]customEAInput, 0)
	seen := map[string]struct{}{}
	for _, line := range strings.Split(text, "\n") {
		match := inputDeclaration.FindStringSubmatch(strings.TrimRight(line, "\r"))
		if match == nil {
			continue
		}
		name := match[2]
		if _, duplicate := seen[name]; duplicate || len(inputs) >= maxCustomEAParams {
			continue
		}
		seen[name] = struct{}{}
		value := strings.TrimSpace(match[3])
		if len(value) >= 2 && value[0] == '"' && value[len(value)-1] == '"' {
			value = value[1 : len(value)-1]
		}
		inputs = append(inputs, customEAInput{Name: name, Type: match[1], Default: value, Comment: strings.TrimSpace(match[4])})
	}
	return inputs
}

func validCustomEAParams(params map[string]string, inputs []customEAInput) bool {
	if len(params) > maxCustomEAParams {
		return false
	}
	known := make(map[string]struct{}, len(inputs))
	for _, input := range inputs {
		known[input.Name] = struct{}{}
	}
	for name, value := range params {
		if !paramNamePattern.MatchString(name) || len(value) > maxCustomEAParamValue || strings.ContainsAny(value, "\x00\r\n") {
			return false
		}
		if _, ok := known[name]; len(known) > 0 && !ok {
			return false
		}
	}
	return true
}

func decodeStoredParams(raw sql.NullString) map[string]string {
	params := map[string]string{}
	if raw.Valid && raw.String != "" {
		_ = json.Unmarshal([]byte(raw.String), &params)
	}
	return params
}

func (api clientMT5API) setCustomEAPaused(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	ownerID, ok := requireClient(ctx, api.pool, w, r)
	if !ok {
		return
	}
	accountID, err := strconv.ParseInt(r.PathValue("account_id"), 10, 64)
	if err != nil || accountID <= 0 {
		writeError(w, http.StatusNotFound, "Akun MT5 tidak ditemukan")
		return
	}
	var payload struct {
		Paused *bool `json:"paused"`
	}
	if err := decodeJSON(w, r, &payload, 8<<10); err != nil || payload.Paused == nil {
		writeError(w, http.StatusUnprocessableEntity, "Status pause tidak valid")
		return
	}
	result, err := api.pool.Exec(ctx, `UPDATE mt5_accounts SET custom_ea_paused=$3 WHERE id=$1 AND owner_id=$2 AND ea_mode='CUSTOM'`, accountID, ownerID, *payload.Paused)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "Status pause tidak dapat disimpan")
		return
	}
	if result.RowsAffected() == 0 {
		writeError(w, http.StatusNotFound, "EA kustom belum dipasang pada akun ini")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "saved", "paused": *payload.Paused})
}

func (api clientMT5API) customEAParams(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	ownerID, ok := requireClient(ctx, api.pool, w, r)
	if !ok {
		return
	}
	accountID, err := strconv.ParseInt(r.PathValue("account_id"), 10, 64)
	if err != nil || accountID <= 0 {
		writeError(w, http.StatusNotFound, "Akun MT5 tidak ditemukan")
		return
	}
	var name, rawInputs, rawParams sql.NullString
	var paused bool
	err = api.pool.QueryRow(ctx, `SELECT custom_ea_name,custom_ea_inputs,custom_ea_params,custom_ea_paused FROM mt5_accounts WHERE id=$1 AND owner_id=$2 AND ea_mode='CUSTOM'`, accountID, ownerID).Scan(&name, &rawInputs, &rawParams, &paused)
	if err != nil {
		writeError(w, http.StatusNotFound, "EA kustom belum dipasang pada akun ini")
		return
	}
	inputs := []customEAInput{}
	if rawInputs.Valid && rawInputs.String != "" {
		_ = json.Unmarshal([]byte(rawInputs.String), &inputs)
	}
	params := decodeStoredParams(rawParams)

	if r.Method == http.MethodGet {
		keys := make([]string, 0, len(params))
		for key := range params {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		writeJSON(w, http.StatusOK, map[string]any{
			"custom_ea_name": name.String, "inputs": inputs, "params": params,
			// Compiled EAs expose no input list; users add parameters by name.
			"free_form": len(inputs) == 0, "paused": paused,
		})
		return
	}
	var payload struct {
		Params map[string]string `json:"params"`
	}
	if err := decodeJSON(w, r, &payload, 128<<10); err != nil || payload.Params == nil || !validCustomEAParams(payload.Params, inputs) {
		writeError(w, http.StatusUnprocessableEntity, "Parameter EA tidak valid")
		return
	}
	encoded, err := json.Marshal(payload.Params)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, "Parameter EA tidak valid")
		return
	}
	if _, err := api.pool.Exec(ctx, `UPDATE mt5_accounts SET custom_ea_params=$3 WHERE id=$1 AND owner_id=$2 AND ea_mode='CUSTOM'`, accountID, ownerID, string(encoded)); err != nil {
		writeError(w, http.StatusServiceUnavailable, "Parameter EA tidak dapat disimpan")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "saved", "params": payload.Params})
}
