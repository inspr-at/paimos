//go:build ignore

// SPDX-License-Identifier: AGPL-3.0-only

// Runs the candidate's real embedded migration runner without starting its API.
package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/inspr-at/paimos/internal/config"
	"github.com/inspr-at/paimos/internal/db"
)

func main() {
	if err := migrate(); err != nil {
		fmt.Fprintln(os.Stderr, "candidate migrations:", err)
		os.Exit(1)
	}
}

func migrate() error {
	cfg, err := config.FromEnv()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	pool, err := db.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()
	fmt.Println("candidate migrations applied")
	return nil
}
