package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestUptimeHealth(t *testing.T) {
	server := &Server{started: time.Now().Add(-time.Minute)}

	t.Run("GET returns a stable monitor keyword without caching", func(t *testing.T) {
		response := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodGet, "/health/uptime", nil)

		server.uptimeHealth(response, request)

		if response.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d", response.Code, http.StatusOK)
		}
		if got := response.Header().Get("Cache-Control"); got != "no-store" {
			t.Fatalf("Cache-Control = %q, want no-store", got)
		}
		if got := response.Header().Get("Content-Type"); !strings.HasPrefix(got, "text/plain") {
			t.Fatalf("Content-Type = %q, want text/plain", got)
		}
		if got := response.Body.String(); got != "MOSTLYVERS_UP\n" {
			t.Fatalf("body = %q, want MOSTLYVERS_UP", got)
		}
	})

	t.Run("HEAD verifies availability without a response body", func(t *testing.T) {
		response := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodHead, "/health/uptime", nil)

		server.uptimeHealth(response, request)

		if response.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d", response.Code, http.StatusOK)
		}
		if response.Body.Len() != 0 {
			t.Fatalf("HEAD body length = %d, want 0", response.Body.Len())
		}
	})
}
