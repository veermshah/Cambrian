// Command swarm is the main entry point for the Cambrian agent swarm.
//
// At this stage (chunk 1) the binary loads configuration and exits cleanly.
// Later chunks add: database migrations (chunk 2), the SwarmRuntime (chunk 14),
// the RootOrchestrator (chunk 21), the API server (chunk 29), and the
// Telegram notifier (chunk 28).
package main

import (
	"fmt"
	"log"
	"os"

	"github.com/veermshah/cambrian/internal/config"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Printf("config: %v", err)
		os.Exit(1)
	}
	fmt.Printf("network=%s\n", cfg.Network)
}
