package httpapi

import (
	"bytes"
	"encoding/json"
	dbstore "github.com/rnpproject/algentra-backend/internal/database"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"time"

	"github.com/gorilla/websocket"
	"github.com/rnpproject/algentra-backend/internal/ea/snapshot"
)

func (api dashboardAPI) dashboardWebSocket(w http.ResponseWriter, r *http.Request) {
	state := SessionFromRequest(r)
	admin, authenticated := "", false
	if state != nil {
		admin, authenticated = state.Values["admin_username"].(string)
	}
	if !authenticated || strings.TrimSpace(admin) == "" {
		http.Error(w, "Login admin diperlukan", http.StatusUnauthorized)
		return
	}
	upgrader := websocket.Upgrader{
		ReadBufferSize:  1024,
		WriteBufferSize: 4096,
		CheckOrigin: func(r *http.Request) bool {
			return websocketOriginAllowed(r.Header.Get("Origin"), r.Host, api.websocketOrigin)
		},
	}
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()
	readDone := make(chan struct{})
	go func() {
		defer close(readDone)
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}()
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		recorder := httptest.NewRecorder()
		if err := api.writeDashboardEvent(recorder, r); err == nil && recorder.Code == http.StatusOK {
			line := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(recorder.Body.String()), "event: dashboard"))
			line = strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			var payload map[string]any
			if json.Unmarshal([]byte(line), &payload) == nil {
				payload["type"] = "dashboard_refresh"
				_ = conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
				if err := conn.WriteJSON(payload); err != nil {
					return
				}
			}
		}
		select {
		case <-r.Context().Done():
			return
		case <-readDone:
			return
		case <-ticker.C:
		}
	}
}

func publicPerformanceWebSocket(pool *dbstore.Pool, state *snapshot.Store, allowedOrigins []string) http.HandlerFunc {
	upgrader := websocket.Upgrader{
		ReadBufferSize:  1024,
		WriteBufferSize: 4096,
		CheckOrigin: func(r *http.Request) bool {
			return websocketOriginAllowed(r.Header.Get("Origin"), r.Host, allowedOrigins)
		},
	}
	return func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		readDone := make(chan struct{})
		go func() {
			defer close(readDone)
			for {
				if _, _, err := conn.ReadMessage(); err != nil {
					return
				}
			}
		}()
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()
		var lastPayload []byte
		lastHistoryAt := time.Time{}
		for {
			includeHistory := lastPayload == nil || time.Since(lastHistoryAt) >= time.Minute
			request := r.Clone(r.Context())
			query := request.URL.Query()
			query.Set("include_equity_curve", "false")
			if includeHistory {
				query.Set("include_equity_curve", "true")
			}
			request.URL.RawQuery = query.Encode()
			recorder := httptest.NewRecorder()
			publicPerformance(pool, state).ServeHTTP(recorder, request)
			payload := bytes.TrimSpace(recorder.Body.Bytes())
			if recorder.Code == http.StatusOK {
				if includeHistory {
					lastHistoryAt = time.Now()
				} else if len(lastPayload) > 0 {
					var current, previous map[string]any
					if json.Unmarshal(payload, &current) == nil && json.Unmarshal(lastPayload, &previous) == nil {
						if currentAccount, ok := current["master_account"].(map[string]any); ok {
							if previousAccount, ok := previous["master_account"].(map[string]any); ok {
								currentAccount["account_key"] = previousAccount["account_key"]
							}
						}
						for _, key := range []string{"equity_curve", "equity_candles"} {
							if value, ok := previous[key]; ok {
								current[key] = value
							}
						}
						if merged, err := json.Marshal(current); err == nil {
							payload = merged
						}
					}
				}
				if !bytes.Equal(payload, lastPayload) {
					_ = conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
					if err := conn.WriteMessage(websocket.TextMessage, payload); err != nil {
						return
					}
					lastPayload = append(lastPayload[:0], payload...)
				}
			}
			select {
			case <-r.Context().Done():
				return
			case <-readDone:
				return
			case <-ticker.C:
			}
		}
	}
}

func websocketOriginAllowed(origin, host string, allowedOrigins []string) bool {
	if strings.TrimSpace(origin) == "" || strings.TrimSpace(host) == "" {
		return true
	}
	parsed, err := url.Parse(origin)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return false
	}
	if strings.EqualFold(parsed.Host, host) {
		return true
	}
	canonical := strings.ToLower(parsed.Scheme + "://" + parsed.Host)
	for _, allowed := range allowedOrigins {
		if canonical == strings.ToLower(strings.TrimSpace(allowed)) {
			return true
		}
	}
	return false
}
