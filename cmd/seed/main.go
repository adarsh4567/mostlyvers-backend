package main

import (
	"context"
	"errors"
	"log"
	"os"
	"strings"

	"github.com/mostlyvers/backend/db/migrations"
	"github.com/mostlyvers/backend/internal/auth"
	"github.com/mostlyvers/backend/internal/store"
)

func main() {
	if err := run(context.Background()); err != nil {
		log.Fatal(err)
	}
}

func run(ctx context.Context) error {
	email := strings.TrimSpace(os.Getenv("OWNER_EMAIL"))
	password := os.Getenv("OWNER_PASSWORD")
	name := strings.TrimSpace(os.Getenv("OWNER_NAME"))
	phone := strings.TrimSpace(os.Getenv("OWNER_PHONE"))
	if email == "" || password == "" || name == "" || phone == "" {
		return errors.New("OWNER_EMAIL, OWNER_PASSWORD, OWNER_NAME and OWNER_PHONE are required")
	}
	databaseURL := strings.TrimSpace(os.Getenv("DATABASE_URL"))
	if databaseURL == "" {
		return errors.New("DATABASE_URL is required")
	}
	if err := migrations.Run(ctx, databaseURL, "up"); err != nil {
		return err
	}
	database, err := store.Open(ctx, databaseURL)
	if err != nil {
		return err
	}
	defer database.Close()
	hash, err := auth.HashPassword(password)
	if err != nil {
		return err
	}
	tx, err := database.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	id := store.NewID()
	_, err = tx.Exec(ctx, `INSERT INTO accounts(id,role,email,normalized_email,password_hash,status,email_verified_at) VALUES($1,'OWNER',$2,$3,$4,'ACTIVE',now())`, id, email, store.NormalizeEmail(email), hash)
	if err == nil {
		_, err = tx.Exec(ctx, `INSERT INTO owner_profiles(account_id,name,phone) VALUES($1,$2,$3)`, id, name, phone)
	}
	if err != nil {
		return err
	}
	if err = tx.Commit(ctx); err != nil {
		return err
	}
	log.Printf("owner %s created", email)
	return nil
}
