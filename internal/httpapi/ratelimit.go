package httpapi

import (
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

type requestWindow struct {
	started []time.Time
}

type requestLimiter struct {
	mu    sync.Mutex
	hits  map[string]requestWindow
	clean time.Time
}

func RateLimit(next http.Handler) http.Handler {
	l := &requestLimiter{hits: make(map[string]requestWindow)}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" || r.URL.Path == "/favicon.ico" || strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
			next.ServeHTTP(w, r)
			return
		}
		identity := remoteIdentity(r)
		key := identity + "\n" + r.URL.Path
		limit, period := requestLimit(r.URL.Path)
		now := time.Now()
		if !l.allow(key, limit, period, now) {
			w.Header().Set("Retry-After", "60")
			writeError(w, http.StatusTooManyRequests, "Terlalu banyak permintaan; coba lagi sebentar.")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func requestLimit(path string) (int, time.Duration) {
	switch path {
	case "/api/auth/login", "/api/auth/client-login":
		return 8, time.Minute
	case "/api/auth/register", "/api/auth/resend-code", "/api/auth/password-reset/request":
		return 4, time.Minute
	case "/api/auth/verify-email", "/api/auth/password-reset/confirm":
		return 10, time.Minute
	case "/api/ea/master/snapshot", "/api/mt5/follower/positions", "/api/mt5/follower/report":
		return 720, time.Minute
	case "/api/parser/test":
		return 20, time.Minute
	case "/api/cs/chat/message":
		return 12, time.Minute
	default:
		// Authenticated local worker traffic (jobs, status, account reports).
		if strings.HasPrefix(path, "/api/mt5/copier/") {
			return 1800, time.Minute
		}
		if strings.HasPrefix(path, "/api/telegram/") {
			return 12, time.Minute
		}
		return 120, time.Minute
	}
}

func (l *requestLimiter) allow(key string, maximum int, period time.Duration, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	window := l.hits[key]
	cutoff := now.Add(-period)
	active := window.started[:0]
	for _, started := range window.started {
		if started.After(cutoff) {
			active = append(active, started)
		}
	}
	if len(active) >= maximum {
		window.started = active
		l.hits[key] = window
		return false
	}
	window.started = append(active, now)
	l.hits[key] = window
	if l.clean.IsZero() || now.Sub(l.clean) > 5*time.Minute || len(l.hits) > 4096 {
		for oldKey, oldWindow := range l.hits {
			filtered := oldWindow.started[:0]
			for _, started := range oldWindow.started {
				if started.After(cutoff) {
					filtered = append(filtered, started)
				}
			}
			if len(filtered) == 0 {
				delete(l.hits, oldKey)
			} else {
				oldWindow.started = filtered
				l.hits[oldKey] = oldWindow
			}
		}
		l.clean = now
	}
	return true
}

func remoteIdentity(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	ip := net.ParseIP(host)
	if ip != nil && ip.IsLoopback() {
		if forwarded := net.ParseIP(strings.TrimSpace(r.Header.Get("CF-Connecting-IP"))); forwarded != nil {
			return forwarded.String()
		}
	}
	if ip != nil {
		return ip.String()
	}
	return "unknown"
}
