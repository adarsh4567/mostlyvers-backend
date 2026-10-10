package config

import (
	"strings"
	"testing"
)

func TestDevelopmentDefaultsAvoidMacOSAirPlayPort(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://example.test/mostlyvers")
	t.Setenv("HTTP_PORT", "")
	t.Setenv("PUBLIC_BASE_URL", "")
	t.Setenv("ACCESS_TOKEN_PRIVATE_KEY_BASE64", "")
	t.Setenv("ACCESS_TOKEN_PUBLIC_KEY_BASE64", "")
	t.Setenv("CONTENT_KEY_ENCRYPTION_KEY_BASE64", "")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Port != "5001" {
		t.Fatalf("Port = %q, want 5001", cfg.Port)
	}
	if cfg.PublicBaseURL != "http://localhost:5001" {
		t.Fatalf("PublicBaseURL = %q, want http://localhost:5001", cfg.PublicBaseURL)
	}
	if cfg.R2PresignEndpoint != "http://localhost:9000" {
		t.Fatalf("R2PresignEndpoint = %q, want http://localhost:9000", cfg.R2PresignEndpoint)
	}
}

func TestLoadRejectsMarkdownFormattedURL(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://example.test/mostlyvers")
	t.Setenv("PUBLIC_BASE_URL", "[http://localhost:5001](http://localhost:5001)")
	t.Setenv("ACCESS_TOKEN_PRIVATE_KEY_BASE64", "")
	t.Setenv("ACCESS_TOKEN_PUBLIC_KEY_BASE64", "")
	t.Setenv("CONTENT_KEY_ENCRYPTION_KEY_BASE64", "")

	_, err := Load()
	if err == nil || !strings.Contains(err.Error(), "PUBLIC_BASE_URL must be an absolute http(s) URL") {
		t.Fatalf("Load() error = %v, want invalid PUBLIC_BASE_URL", err)
	}
}

func TestRenderPortAndFreeLaunchMode(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://example.test/mostlyvers")
	t.Setenv("HTTP_PORT", "")
	t.Setenv("PORT", "10000")
	t.Setenv("BILLING_MODE", "free_launch")
	t.Setenv("ACCESS_TOKEN_PRIVATE_KEY_BASE64", "")
	t.Setenv("ACCESS_TOKEN_PUBLIC_KEY_BASE64", "")
	t.Setenv("CONTENT_KEY_ENCRYPTION_KEY_BASE64", "")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Port != "10000" || cfg.BillingMode != "FREE_LAUNCH" {
		t.Fatalf("unexpected Render config: port=%s mode=%s", cfg.Port, cfg.BillingMode)
	}
}

func TestProductionUsesRenderExternalHostname(t *testing.T) {
	t.Setenv("PUBLIC_BASE_URL", "")
	t.Setenv("RENDER_EXTERNAL_HOSTNAME", "mostlyvers-api.onrender.com")
	if got := resolvePublicBaseURL("production"); got != "https://mostlyvers-api.onrender.com" {
		t.Fatalf("production PublicBaseURL = %q", got)
	}
	if got := resolvePublicBaseURL("development"); got != "http://localhost:5001" {
		t.Fatalf("development PublicBaseURL = %q", got)
	}
}

func TestValidateProductionEmailAcceptsEmailJS(t *testing.T) {
	err := validateProductionEmail(Config{
		EmailProvider: "emailjs", EmailJSServiceID: "service_123", EmailJSTemplateID: "template_123", EmailJSPublicKey: "public_123",
	})
	if err != nil {
		t.Fatalf("validateProductionEmail() error = %v", err)
	}
}

func TestValidateProductionEmailRejectsIncompleteEmailJS(t *testing.T) {
	err := validateProductionEmail(Config{EmailProvider: "emailjs", EmailJSServiceID: "service_123"})
	if err == nil || !strings.Contains(err.Error(), "EMAILJS_TEMPLATE_ID") {
		t.Fatalf("validateProductionEmail() error = %v, want missing EmailJS configuration", err)
	}
}
