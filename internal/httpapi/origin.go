package httpapi

import (
	"net/http"
	"net/url"
	"strings"
)

// OriginProtection blocks cross-site browser writes while leaving authenticated machine
// integrations (EA, copier worker, and account tokens) usable.
func OriginProtection(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet || r.Method == http.MethodHead || r.Method == http.MethodOptions || !strings.HasPrefix(r.URL.Path, "/api/") {
			next.ServeHTTP(w, r)
			return
		}
		if r.Header.Get("X-API-Key") != "" || r.Header.Get("X-Copier-Worker-Key") != "" || r.Header.Get("X-Account-Token") != "" {
			next.ServeHTTP(w, r)
			return
		}
		raw := r.Header.Get("Origin")
		if raw == "" {
			http.Error(w, `{"detail":"Origin wajib untuk request perubahan data"}`, http.StatusForbidden)
			return
		}
		origin, err := url.Parse(raw)
		if err != nil || origin.Host == "" || origin.User != nil || !strings.EqualFold(origin.Host, r.Host) || (origin.Scheme != "https" && origin.Scheme != "http") {
			http.Error(w, `{"detail":"Origin request tidak diizinkan"}`, http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}
