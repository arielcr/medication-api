// Command medication-api runs the Medication REST API server.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
)

// version is injected at build time via -ldflags "-X main.version=...".
var version = "dev"

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "medication-api: %v\n", err)
		os.Exit(2)
	}
}

func run() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	fmt.Printf("medication-api %s: starting (press Ctrl+c to stop)\n", version)

	<-ctx.Done()

	fmt.Println("medication-api: shutdown signal received, exiting")

	return nil
}