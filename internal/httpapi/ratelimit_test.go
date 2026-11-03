package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestLoginRequestsAreRateLimited(t *testing.T) {
	called := 0
	handler := RateLimit(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called++
		w.WriteHeader(http.StatusNoContent)
	}))
	for i := 0; i < 9; i++ {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodPost, "/api/auth/login", nil)
		handler.ServeHTTP(w, r)
		if i < 8 && w.Code != http.StatusNoContent {
			t.Fatalf("request %d status = %d", i+1, w.Code)
		}
		if i == 8 && (w.Code != http.StatusTooManyRequests || w.Header().Get("Retry-After") == "") {
			t.Fatalf("ninth request was not rate limited: %d", w.Code)
		}
	}
	if called != 8 {
		t.Fatalf("downstream handler called %d times", called)
	}
}

func TestWorkerReportsAreNotThrottledAtDefaultLimit(t *testing.T) {
	handler := RateLimit(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	for i := 0; i < 600; i++ {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/mt5/copier/jobs/1/report", nil))
		if w.Code != http.StatusNoContent {
			t.Fatalf("request %d status = %d", i+1, w.Code)
		}
	}
}
