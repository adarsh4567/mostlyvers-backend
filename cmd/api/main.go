package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/mostlyvers/backend/db/migrations"
	"github.com/mostlyvers/backend/internal/api"
	"github.com/mostlyvers/backend/internal/config"
	emailpkg "github.com/mostlyvers/backend/internal/email"
	"github.com/mostlyvers/backend/internal/objectstore"
	"github.com/mostlyvers/backend/internal/payment"
	"github.com/mostlyvers/backend/internal/store"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		slog.Error("configuration invalid", "error", err)
		os.Exit(1)
	}
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	if err = migrations.Run(ctx, cfg.DatabaseURL, "up"); err != nil {
		slog.Error("migration failed", "error", err)
		os.Exit(1)
	}
	database, err := store.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		slog.Error("database unavailable", "error", err)
		os.Exit(1)
	}
	defer database.Close()
	objects, err := objectstore.New(ctx, cfg)
	if err != nil {
		slog.Error("object storage configuration failed", "error", err)
		os.Exit(1)
	}
	var sender emailpkg.Sender = emailpkg.Console{}
	if cfg.EmailProvider == "smtp" {
		sender = emailpkg.SMTP{Addr: cfg.SMTPAddr, From: cfg.EmailFrom}
	}
	if cfg.EmailProvider == "resend" {
		if cfg.ResendAPIKey == "" {
			slog.Error("RESEND_API_KEY is required")
			os.Exit(1)
		}
		sender = emailpkg.NewResend(cfg.ResendAPIKey, cfg.EmailFrom)
	}
	application := api.New(cfg, database, objects, sender, payment.Disabled{})
	go application.StartWorker(ctx)
	server := &http.Server{Addr: ":" + cfg.Port, Handler: application.Router(), ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 15 * time.Minute, WriteTimeout: 15 * time.Minute, IdleTimeout: 90 * time.Second, MaxHeaderBytes: 1 << 20}
	go func() {
		<-ctx.Done()
		shutdownCtx, stop := context.WithTimeout(context.Background(), 20*time.Second)
		defer stop()
		_ = server.Shutdown(shutdownCtx)
	}()
	slog.Info("MOSTLYVERS API listening", "port", cfg.Port, "environment", cfg.Environment)
	if err = server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		slog.Error("server stopped", "error", err)
		os.Exit(1)
	}
}
