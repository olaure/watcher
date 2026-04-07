package cmd

import (
	"database/sql"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"watcher/api"
	"watcher/config"
	"watcher/runner"
)

const (
	serverLogFile    = "server.log"
	serverLogMaxSize = 10 * 1024 * 1024 // 10 MB
	serverLogBackups = 3
)

func ServeCmd(database *sql.DB, cfg config.Config, args []string) {
	// Ensure logs directory exists
	if err := os.MkdirAll(cfg.LogsDir, 0755); err != nil {
		fmt.Fprintf(os.Stderr, "Error creating logs directory %s: %v\n", cfg.LogsDir, err)
		os.Exit(1)
	}

	// Set up rotating server log in the configured logs directory.
	logPath := filepath.Join(cfg.LogsDir, serverLogFile)
	logWriter, err := NewRotatingWriter(logPath, serverLogMaxSize, serverLogBackups)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error opening server log %s: %v\n", logPath, err)
		os.Exit(1)
	}
	defer logWriter.Close()
	log.SetOutput(logWriter)

	// Parse prune interval
	pruneInterval, err := cfg.ParsePruneInterval()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Invalid prune_interval %q: %v\n", cfg.PruneInterval, err)
		os.Exit(1)
	}

	// Recover runs left in 'running' state from a previous server instance.
	runner.RecoverRuns(database, cfg.LogsDir)

	// Start pruner
	prunerStop := make(chan struct{})
	go runner.StartPruner(database, pruneInterval, prunerStop)

	// Set up HTTP server
	mux := api.NewMux(database, cfg)
	server := &http.Server{
		Addr:    cfg.ListenAddr,
		Handler: mux,
	}

	// Graceful shutdown
	go func() {
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
		<-sigCh
		log.Println("Shutting down...")
		runner.DetachActiveRuns(database)
		close(prunerStop)
		server.Close()
	}()

	log.Printf("Starting server on %s", cfg.ListenAddr)
	log.Printf("Logs dir: %s", cfg.LogsDir)
	log.Printf("Prune interval: %s", pruneInterval)

	if err := server.ListenAndServe(); err != http.ErrServerClosed {
		fmt.Fprintf(os.Stderr, "Server error: %v\n", err)
		os.Exit(1)
	}
	log.Println("Server stopped")
}
