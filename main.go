package main

import (
	"context"
	"log"
	"net"
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

// Build metadata, stamped at link time by deploy/Dockerfile and the Makefile:
//
//	-ldflags="-X main.version=$(git describe ...) -X main.commit=$(git rev-parse ...)"
//
// These must be declared here, as package-level strings in `main`, for that to
// do anything. `-X` on a symbol the linker cannot find is silently ignored --
// no warning, no error, just an unstamped binary that looks fine. The build
// arguments were being passed for a while against variables that did not exist.
//
// Surfaced on /health, /health/live and as access_terminal_build_info so a
// running container can be matched to a commit without guessing.
var (
	version = "dev"
	commit  = "unknown"
)

// listenAddress resolves the address the HTTP server binds to.
//
// PORT before SERVER_PORT. Render -- and every other platform that starts a
// container it did not choose the port for -- injects PORT and routes to
// whatever the process bound there; a service that ignores it binds a port
// nothing is forwarded to and is marked unhealthy with no useful error. The
// existing SERVER_PORT is kept as the fallback so the compose and systemd
// deployments in deploy/ are unaffected.
//
// The host defaults to 0.0.0.0. That is a REQUIREMENT on a container platform,
// not a preference: the health check and the proxy both arrive over the
// container's network interface, and a process on 127.0.0.1 answers neither.
// BIND_ADDRESS exists for the opposite case -- on the VPS deployment, where
// Nginx terminates TLS on the same host, setting it to 127.0.0.1 keeps the API
// off every other interface.
func listenAddress() string {
	port := os.Getenv("PORT")
	if port == "" {
		port = os.Getenv("SERVER_PORT")
	}
	if port == "" {
		port = "8080"
	}

	host := os.Getenv("BIND_ADDRESS")
	if host == "" {
		host = "0.0.0.0"
	}

	return net.JoinHostPort(host, port)
}

// resolveCommit falls back to the platform's build metadata when the linker
// stamped none.
//
// The -X flags come from the Makefile and deploy/Dockerfile, which read git.
// A platform build does not run those: Render builds from a Dockerfile it
// invokes itself, with no build arguments, so `commit` stays "unknown" and
// /health cannot say which revision is serving. RENDER_GIT_COMMIT is set in the
// runtime environment there, which is enough to answer the question. Shortened
// to match the `git rev-parse --short` form the stamped builds use.
func resolveCommit() string {
	if commit != "unknown" && commit != "" {
		return commit
	}
	if c := os.Getenv("RENDER_GIT_COMMIT"); c != "" {
		if len(c) > 7 {
			return c[:7]
		}
		return c
	}
	return commit
}

func main() {
	// Load environment variables
	if err := godotenv.Load(); err != nil {
		log.Println("No .env file found, using environment variables")
	}

	commit = resolveCommit()
	handlers.SetBuildInfo(version, commit)

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
	addr := listenAddress()

	srv := &http.Server{Addr: addr, Handler: r}

	// Serve in the background so the main goroutine can wait for a signal.
	go func() {
		log.Printf("Starting Access Terminal Cloud API server on %s (version=%s commit=%s)",
			addr, version, commit)
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
