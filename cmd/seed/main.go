package main

import (
	"context"
	"log"
	"os"
	"strings"

	"github.com/mostlyvers/backend/db/migrations"
	"github.com/mostlyvers/backend/internal/auth"
	"github.com/mostlyvers/backend/internal/config"
	"github.com/mostlyvers/backend/internal/store"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatal(err)
	}
	email := strings.TrimSpace(os.Getenv("OWNER_EMAIL"))
	password := os.Getenv("OWNER_PASSWORD")
	name := strings.TrimSpace(os.Getenv("OWNER_NAME"))
	phone := strings.TrimSpace(os.Getenv("OWNER_PHONE"))
	if email == "" || password == "" || name == "" || phone == "" {
		log.Fatal("OWNER_EMAIL, OWNER_PASSWORD, OWNER_NAME and OWNER_PHONE are required")
	}
	ctx := context.Background()
	if err = migrations.Run(ctx, cfg.DatabaseURL, "up"); err != nil {
		log.Fatal(err)
	}
	database, err := store.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		log.Fatal(err)
	}
	defer database.Close()
	hash, err := auth.HashPassword(password)
	if err != nil {
		log.Fatal(err)
	}
	tx, err := database.Pool.Begin(ctx)
	if err != nil {
		log.Fatal(err)
	}
	defer tx.Rollback(ctx)
	id := store.NewID()
	_, err = tx.Exec(ctx, `INSERT INTO accounts(id,role,email,normalized_email,password_hash,status,email_verified_at) VALUES($1,'OWNER',$2,$3,$4,'ACTIVE',now())`, id, email, store.NormalizeEmail(email), hash)
	if err == nil {
		_, err = tx.Exec(ctx, `INSERT INTO owner_profiles(account_id,name,phone) VALUES($1,$2,$3)`, id, name, phone)
	}
	if err != nil {
		log.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		log.Fatal(err)
	}
	log.Printf("owner %s created", email)
}
