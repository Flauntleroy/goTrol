package main

import (
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"gotrol/internal/config"
	"gotrol/internal/database"
	"gotrol/internal/report"
	"gotrol/internal/service"
)

const version = "1.0.0"

func main() {
	if len(os.Args) < 2 {
		printUsage()
		return
	}

	command := os.Args[1]

	switch command {
	case "run":
		runService()
	case "batch":
		runBatch()
	case "status":
		checkStatus()
	case "help", "-h", "--help":
		printUsage()
	case "version", "-v", "--version":
		fmt.Printf("goTrol v%s\n", version)
	default:
		fmt.Printf("Unknown command: %s\n", command)
		printUsage()
	}
}

func printUsage() {
	fmt.Println(`
╔══════════════════════════════════════════════════════════════╗
║               goTrol - JKN Task ID Auto Service              ║
╚══════════════════════════════════════════════════════════════╝

Usage: gotrol <command> [options]

Commands:
  run                          Start the background service (auto monitoring)
  batch <type> <options>       Run manual batch operations
  status                       Check service status
  version                      Show version
  help                         Show this help

Batch Types:
  batch autoorder --today      Auto Order only (no BPJS send)
  batch autoorder --date YYYY-MM-DD
  batch updatewaktu --today    Update Waktu only (send to BPJS)
  batch updatewaktu --date YYYY-MM-DD
  batch all --today            Both Auto Order + Update Waktu
  batch all --date YYYY-MM-DD

Examples:
  gotrol run
  gotrol batch autoorder --today
  gotrol batch updatewaktu --date 2025-12-28
  gotrol batch all --today
`)
}

func runService() {
	printBanner()

	// Load config
	cfg, err := config.Load("config.yaml")
	if err != nil {
		log.Fatalf("❌ Failed to load config: %v", err)
	}

	// Connect to MySQL
	db, err := database.NewMySQL(cfg.Database)
	if err != nil {
		log.Fatalf("❌ Failed to connect to MySQL: %v", err)
	}
	defer db.Close()
	log.Println("✓ Connected to MySQL database")

	// Load BPJS credentials
	creds, err := db.GetBPJSCredentials()
	if err != nil {
		log.Fatalf("❌ Failed to load BPJS credentials: %v", err)
	}
	log.Println("✓ BPJS credentials loaded from settings")

	// Initialize report store
	reportStore, err := report.NewStore(cfg.Report.DBPath)
	if err != nil {
		log.Fatalf("❌ Failed to initialize report store: %v", err)
	}
	defer reportStore.Close()

	// NOTE: API server removed from here - now runs as separate GoTrolDashboard.exe
	log.Println("ℹ️  Dashboard dipindah ke GoTrolDashboard.exe")

	// Start watcher
	watcher := service.NewWatcher(db, creds, reportStore, cfg.Watcher.GetPollDuration())

	// Handle graceful shutdown
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		<-sigChan
		log.Println("\n🛑 Shutting down...")
		watcher.Stop()
		os.Exit(0)
	}()

	watcher.Start()
}

func runBatch() {
	if len(os.Args) < 4 {
		fmt.Println("Usage: gotrol batch <type> --today|--date YYYY-MM-DD")
		fmt.Println("Types: autoorder, updatewaktu, all")
		return
	}

	batchType := os.Args[2]
	dateFlag := os.Args[3]

	var date string
	if dateFlag == "--today" {
		date = time.Now().Format("2006-01-02")
	} else if dateFlag == "--date" && len(os.Args) > 4 {
		date = os.Args[4]
	} else {
		fmt.Println("Invalid date flag. Use --today or --date YYYY-MM-DD")
		return
	}

	printBanner()

	// Load config
	cfg, err := config.Load("config.yaml")
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}

	// Connect to MySQL
	db, err := database.NewMySQL(cfg.Database)
	if err != nil {
		log.Fatalf("Failed to connect to MySQL: %v", err)
	}
	defer db.Close()
	log.Println("Connected to MySQL database")

	// Load BPJS credentials
	creds, err := db.GetBPJSCredentials()
	if err != nil {
		log.Fatalf("Failed to load BPJS credentials: %v", err)
	}
	log.Println("BPJS credentials loaded from settings")

	// Initialize report store
	reportStore, err := report.NewStore(cfg.Report.DBPath)
	if err != nil {
		log.Fatalf("Failed to initialize report store: %v", err)
	}
	defer reportStore.Close()

	// Create batch handler
	batch := service.NewBatchHandler(db, creds, reportStore)

	switch batchType {
	case "autoorder":
		total, success, err := batch.BatchAutoOrder(date)
		if err != nil {
			log.Fatalf("Batch error: %v", err)
		}
		fmt.Printf("\nResult: %d/%d processed successfully\n", success, total)

	case "updatewaktu":
		total, success, err := batch.BatchUpdateWaktu(date)
		if err != nil {
			log.Fatalf("Batch error: %v", err)
		}
		fmt.Printf("\nResult: %d/%d sent successfully\n", success, total)

	case "all":
		total, success, err := batch.BatchAll(date)
		if err != nil {
			log.Fatalf("Batch error: %v", err)
		}
		fmt.Printf("\nResult: %d/%d completed successfully\n", success, total)

	default:
		fmt.Printf("Unknown batch type: %s\n", batchType)
		fmt.Println("Types: autoorder, updatewaktu, all")
	}
}

func checkStatus() {
	cfg, err := config.Load("config.yaml")
	if err != nil {
		fmt.Println("Cannot load config")
		return
	}

	fmt.Printf("\n📡 Checking API at http://localhost:%d/api/status...\n", cfg.API.Port)

	// Simple check - try to connect
	db, err := database.NewMySQL(cfg.Database)
	if err != nil {
		fmt.Println("Database: Not connected")
	} else {
		fmt.Println("Database: Connected")
		db.Close()
	}
}

func printBanner() {
	fmt.Println(`
╔══════════════════════════════════════════════════════════════╗
║               GoTrol - Task ID Auto Service RSHAA            ║
║                       Version ` + version + `                ║
╚══════════════════════════════════════════════════════════════╝
`)
}
