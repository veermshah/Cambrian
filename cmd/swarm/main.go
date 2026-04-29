// Command swarm is the main entry point for the Cambrian agent swarm.
//
// At this stage (chunk 2) the binary loads configuration, runs DB migrations
// against the direct Postgres URL, opens the application connection pool,
// pings it, and exits cleanly. Later chunks add: Redis (chunk 3), the
// SwarmRuntime (chunk 14), the RootOrchestrator (chunk 21), the API server
// (chunk 29), and the Telegram notifier (chunk 28).
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/veermshah/cambrian/internal/config"
	"github.com/veermshah/cambrian/internal/db"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Printf("config: %v", err)
		os.Exit(1)
	}

	if err := db.Run(cfg.DatabaseURL); err != nil {
		log.Printf("migrate: %v", err)
		os.Exit(1)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	pool, err := db.NewPool(ctx, cfg.DatabasePoolURL)
	if err != nil {
		log.Printf("db pool: %v", err)
		os.Exit(1)
	}
	defer pool.Close()

	var one int
	if err := pool.QueryRow(ctx, "select 1").Scan(&one); err != nil {
		log.Printf("db smoke: %v", err)
		os.Exit(1)
	}

	fmt.Printf("network=%s db=ok\n", cfg.Network)
}
