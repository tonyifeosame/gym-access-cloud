package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"access-terminal-cloud-api/database"
	"access-terminal-cloud-api/handlers"
	"access-terminal-cloud-api/maintenance"

	"github.com/gin-gonic/gin"
	"github.com/joho/godotenv"
)

func main() {
	// Load environment variables
	if err := godotenv.Load(); err != nil {
		log.Println("No .env file found, using environment variables")
	}

	// Set Gin mode
	ginMode := os.Getenv("GIN_MODE")
	if ginMode == "" {
		ginMode = "release"
	}
	gin.SetMode(ginMode)

	// Connect to database
	dbConfig := database.GetConfigFromEnv()
	if err := database.Connect(dbConfig); err != nil {
		log.Fatalf("Failed to connect to database: %v", err)
	}
	defer database.Close()

	r := NewRouter()

	// Background maintenance
	maintCfg := maintenance.LoadConfig()
	maintCfg.Describe()

	var scheduler *maintenance.Scheduler
	if maintCfg.Enabled {
		scheduler = maintenance.NewScheduler(maintCfg.Tasks()...)
		scheduler.Start(context.Background())
		handlers.SetScheduler(scheduler)
	}

	// Start server
	port := os.Getenv("SERVER_PORT")
	if port == "" {
		port = "8080"
	}

	srv := &http.Server{Addr: ":" + port, Handler: r}

	// Serve in the background so the main goroutine can wait for a signal.
	go func() {
		log.Printf("Starting Access Terminal Cloud API server on port %s", port)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("Failed to start server: %v", err)
		}
	}()

	// Shut down in dependency order: stop accepting requests, then stop the
	// background tasks, then close the database. Draining in-flight requests
	// first matters because a device may be mid-acknowledgement, and dropping
	// that connection would have it retry a job it had already applied.
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, os.Interrupt, syscall.SIGTERM)
	<-quit
	log.Println("Shutdown signal received, draining connections...")

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		log.Printf("Server shutdown timed out: %v", err)
	}

	if scheduler != nil {
		if !scheduler.Stop(maintCfg.ShutdownTimeout) {
			log.Println("Maintenance tasks did not stop cleanly")
		}
	}

	log.Println("Shutdown complete")
}
