package main

import (
	"context"
	"log"
	"os"

	"github.com/mostlyvers/backend/db/migrations"
	"github.com/mostlyvers/backend/internal/config"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatal(err)
	}
	command := "up"
	if len(os.Args) > 1 {
		command = os.Args[1]
	}
	if err = migrations.Run(context.Background(), cfg.DatabaseURL, command); err != nil {
		log.Fatal(err)
	}
}
