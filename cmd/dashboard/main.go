package main

import (
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"gotrol/internal/config"
	"gotrol/internal/database"
	"gotrol/internal/report"
)

func main() {
	fmt.Println()
	fmt.Println("╔══════════════════════════════════════════════════════════════╗")
	fmt.Println("║       GoTrol Dashboard Monitoring Pengiriman Task ID         ║")
	fmt.Println("║                       Version 1.0.0                          ║")
	fmt.Println("╚══════════════════════════════════════════════════════════════╝")
	fmt.Println()

	cfg, err := config.Load("config.yaml")
	if err != nil {
		log.Fatalf(" Failed to load config: %v", err)
	}

	db, err := database.NewMySQL(cfg.Database)
	if err != nil {
		log.Fatalf(" Failed to connect to database: %v", err)
	}
	defer db.Close()
	log.Println(" Connected to MySQL database")

	store, err := report.NewStore(cfg.Report.DBPath)
	if err != nil {
		log.Fatalf(" Failed to initialize report store: %v", err)
	}
	defer store.Close()

	apiPort := cfg.API.Port
	if apiPort == 0 {
		apiPort = 8899
	}

	apiServer := report.NewAPIServer(store, db, apiPort)

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		if err := apiServer.Start(); err != nil {
			log.Printf("API server error: %v", err)
		}
	}()

	log.Printf(" Dashboard running at http://localhost:%d", apiPort)
	log.Println("Press Ctrl+C to stop...")

	<-sigChan
	log.Println("\n Shutting down dashboard...")
	apiServer.Stop()
}
