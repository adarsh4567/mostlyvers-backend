package main

import (
	"context"
	"strings"
	"testing"
)

func TestSeedIgnoresUnrelatedAPIEncryptionConfiguration(t *testing.T) {
	t.Setenv("OWNER_EMAIL", "owner@example.test")
	t.Setenv("OWNER_PASSWORD", "valid-test-password")
	t.Setenv("OWNER_NAME", "Owner")
	t.Setenv("OWNER_PHONE", "+910000000000")
	t.Setenv("DATABASE_URL", "")
	t.Setenv("CONTENT_KEY_ENCRYPTION_KEY_BASE64", "not-valid-base64")

	err := run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "DATABASE_URL is required") {
		t.Fatalf("run() error = %v, want DATABASE_URL validation independent of API encryption config", err)
	}
}
