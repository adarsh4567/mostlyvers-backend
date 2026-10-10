package email

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) { return f(request) }

func TestEmailJSSendsConfiguredTemplateWithoutLeakingCredentialsIntoParams(t *testing.T) {
	var received map[string]any
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Method != http.MethodPost {
			t.Fatalf("method = %s, want POST", r.Method)
		}
		if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
			t.Fatal(err)
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader("OK")), Header: make(http.Header)}, nil
	})

	sender := NewEmailJS("service_123", "template_123", "public_123", "private_123")
	sender.client = &http.Client{Transport: transport}
	err := sender.Send(context.Background(), Message{
		To: "reader@example.com", Subject: "123456 is your code", HTML: "<p>123456</p>",
		TemplateParams: map[string]string{"otp_code": "123456", "reader_name": "Reader", "expires_minutes": "10"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if received["service_id"] != "service_123" || received["template_id"] != "template_123" || received["user_id"] != "public_123" {
		t.Fatalf("unexpected EmailJS identifiers: %#v", received)
	}
	if received["accessToken"] != "private_123" {
		t.Fatalf("accessToken = %#v", received["accessToken"])
	}
	params, ok := received["template_params"].(map[string]any)
	if !ok {
		t.Fatalf("template_params = %#v", received["template_params"])
	}
	if params["to_email"] != "reader@example.com" || params["otp_code"] != "123456" || params["expires_minutes"] != "10" {
		t.Fatalf("unexpected template params: %#v", params)
	}
	if _, exists := params["accessToken"]; exists {
		t.Fatal("private key leaked into template parameters")
	}
}

func TestEmailJSErrorContainsStatusButNotResponseOrCredentials(t *testing.T) {
	transport := roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusForbidden, Body: io.NopCloser(strings.NewReader("provider detail containing private-secret")), Header: make(http.Header)}, nil
	})

	sender := NewEmailJS("service_123", "template_123", "public_123", "private-secret")
	sender.client = &http.Client{Transport: transport}
	err := sender.Send(context.Background(), Message{To: "reader@example.com"})
	if err == nil || !strings.Contains(err.Error(), "403") {
		t.Fatalf("error = %v, want safe 403 error", err)
	}
	if strings.Contains(err.Error(), "private-secret") || strings.Contains(err.Error(), "provider detail") {
		t.Fatalf("error contains sensitive provider data: %v", err)
	}
}
