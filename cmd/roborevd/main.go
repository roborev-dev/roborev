package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/wesm/roborev/internal/config"
	"github.com/wesm/roborev/internal/daemon"
	"github.com/wesm/roborev/internal/storage"
	"github.com/wesm/roborev/internal/version"
)

func main() {
	// Handle version command before anything else (for CI testing)
	if len(os.Args) > 1 && os.Args[1] == "version" {
		fmt.Printf("roborevd %s\n", version.Version)
		return
	}

	var (
		dbPath     = flag.String("db", storage.DefaultDBPath(), "path to sqlite database")
		configPath = flag.String("config", config.GlobalConfigPath(), "path to config file")
		addr       = flag.String("addr", "", "server address (overrides config)")
		workers    = flag.Int("workers", 0, "number of workers (overrides config)")
	)
	flag.Parse()

	log.SetFlags(log.Ldate | log.Ltime | log.Lshortfile)
	log.Println("Starting roborevd...")

	// Load configuration from specified path
	cfg, err := config.LoadGlobalFrom(*configPath)
	if err != nil {
		log.Printf("Warning: failed to load config from %s: %v", *configPath, err)
		cfg = config.DefaultConfig()
	}

	// Apply flag overrides
	if *addr != "" {
		cfg.ServerAddr = *addr
	}
	if *workers > 0 {
		cfg.MaxWorkers = *workers
	}

	// Open database
	db, err := storage.Open(*dbPath)
	if err != nil {
		log.Fatalf("Failed to open database: %v", err)
	}
	defer db.Close()
	log.Printf("Database: %s", *dbPath)

	// Create and start server
	server := daemon.NewServer(db, cfg)

	// Handle shutdown signals
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		sig := <-sigCh
		log.Printf("Received signal %v, shutting down...", sig)
		if err := server.Stop(); err != nil {
			log.Printf("Shutdown error: %v", err)
		}
		os.Exit(0)
	}()

	// Start server (blocks until shutdown)
	if err := server.Start(); err != nil {
		log.Fatalf("Server error: %v", err)
	}
}
