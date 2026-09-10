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

	"access-terminal-cloud-api/bootstrap"
	"access-terminal-cloud-api/database"
	"access-terminal-cloud-api/handlers"
	"access-terminal-cloud-api/maintenance"
	"access-terminal-cloud-api/middleware"
	"access-terminal-cloud-api/models"

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

// sealingKeyStartupFault decides whether this installation must refuse to start.
//
// SEPARATED FROM main() SO IT CAN BE ASSERTED. The refusal it drives is one of
// the few startup decisions that is not observable from any request afterwards
// -- a deployment that gets it wrong looks completely healthy -- so the decision
// needs a test, and main() cannot have one.
//
// The two returns are DIFFERENT ANSWERS and callers must not conflate them.
// `refuse` is "this deployment is misconfigured and must not run". `err` is
// "the check itself could not be carried out", which is not grounds for
// refusing anything: a diagnostic that can take the API down is more dangerous
// than the thing it diagnoses. On an error the caller logs and carries on,
// which is what the zero `refuse` expresses.
func sealingKeyStartupFault() (refuse bool, err error) {
	// A deployment that can read its keys is not in the failure state at all,
	// and asking the database would be work with no question behind it.
	if database.SealingMasterKeyConfigured() {
		return false, nil
	}

	// No master key AND no stored keys is simply an installation that has not
	// turned this feature on. It is entitled to run exactly as it did before
	// 026, so this is where the check must NOT fire.
	hasKeys, err := database.AnySealingKeyExists()
	if err != nil {
		return false, err
	}
	return hasKeys, nil
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

	// Which integration credentials this deployment mints and accepts (030).
	//
	// FATAL ON AN UNRECOGNISED VALUE, and only on an unrecognised one -- unset
	// is `live`, which is the safe default. A typo in the one variable that
	// decides whether test credentials authenticate must not be what quietly
	// enables them, and there is no sensible fallback: guessing `live` would
	// silently ignore a staging operator's intent, and guessing `test` would
	// have production accept staging keys.
	apiEnvironment, err := models.NormaliseEnvironment(os.Getenv("API_ENVIRONMENT"))
	if err != nil {
		log.Fatalf("API_ENVIRONMENT: %v", err)
	}
	handlers.SetAPIEnvironment(apiEnvironment)
	log.Printf("Integration credentials: %s environment (atp_%s_…)",
		apiEnvironment, apiEnvironment)

	// Connect to database
	dbConfig := database.GetConfigFromEnv()
	if err := database.Connect(dbConfig); err != nil {
		log.Fatalf("Failed to connect to database: %v", err)
	}
	defer database.Close()

	// The shared rate-limit store (031), closing SEC-09 for the credential
	// endpoints.
	//
	// AFTER Connect, because it holds the pool. Login, claim, platform login and
	// adopt move onto it; announce stays in process, for the reasons recorded in
	// middleware/rate_limit.go. A deployment that could not reach the database
	// would not get this far, so there is no path where the limiters silently
	// fall back to per-instance allowances.
	middleware.UseSharedRateStore(database.NewPostgresRateStore(database.DB))

	// Create the first operator, if this system has none and the environment
	// says who it should be.
	//
	// Before the server listens, and fatal on error: a half-configured or
	// invalid bootstrap is a deployment mistake, and starting anyway would leave
	// a console nobody can sign in to while reporting itself healthy. It is only
	// ever fatal in the case where the bootstrap would actually run -- on a
	// system that already has an operator the variables are not read for their
	// content at all, so a stale value cannot stop the API starting.
	if _, err := bootstrap.EnsureFirstOperator(); err != nil {
		log.Fatalf("Operator bootstrap: %v", err)
	}

	// And the first PLATFORM ADMINISTRATOR, on the same terms: only on an
	// installation that has none, fatal only where it would actually run.
	//
	// This is the identity that creates companies. Without it a fresh
	// installation can serve exactly the one tenant migration 002 created, which
	// was the audit's first blocker (GP-01).
	if _, err := bootstrap.EnsureFirstPlatformAdmin(); err != nil {
		log.Fatalf("Platform bootstrap: %v", err)
	}

	// The sealing key that biometric replication depends on (026).
	//
	// FATAL when this installation holds sealing keys and cannot read them. That
	// combination means every company key in the database is unrecoverable, so:
	// no terminal claimed from now on can ever be given the key its fleet is
	// already sealing under, replication silently stops for all of them, and
	// stored material becomes bytes nobody can decrypt. Recovery is a new key
	// and re-enrolling every person.
	//
	// EVERY PART OF THAT DAMAGE IS INVISIBLE FROM THE OUTSIDE. Doors keep
	// opening, heartbeats keep arriving, the console looks healthy, and the only
	// symptom is that newly claimed terminals quietly never learn anybody. A
	// deployment that dropped the variable would find out weeks later, from a
	// customer. Refusing to start converts it into a failure at deploy time,
	// which is when somebody is actually looking.
	//
	// NOT fatal on an installation with no sealing keys: that is simply a
	// deployment which has not turned this feature on, and it is entitled to run
	// exactly as it did before 026.
	refuse, checkErr := sealingKeyStartupFault()
	if checkErr != nil {
		// NOT FATAL. This check exists to catch a misconfiguration, and it is
		// not a health check for the database -- turning a transient query
		// failure here into a refusal to start would make a diagnostic more
		// dangerous than the thing it diagnoses.
		log.Printf("SEALING: could not check for sealing keys (%v). "+
			"If this installation holds any, terminals claimed now will "+
			"collect no sealing key and will never receive an enrolment.", checkErr)
	}
	if refuse {
		log.Fatalf("SEALING: this installation holds biometric sealing keys but %s "+
			"is not set, so none of them can be read. Terminals claimed from now on "+
			"would collect no sealing key and would never receive an enrolment, "+
			"silently. Set %s to the 32-byte base64 master key this database was "+
			"written with, or the keys are unrecoverable.",
			database.EnvSealingMasterKey, database.EnvSealingMasterKey)
	}

	// SEC-05. Announced at startup rather than left to be discovered, because
	// this is the one setting that lets a site's provisioning key authenticate
	// as any terminal at that site -- and the deployment that has it on is
	// usually the one that turned it on for a migration and forgot.
	if middleware.LegacyDeviceAuthEnabled() {
		log.Printf("SECURITY: %s is enabled. A site provisioning key plus a serial "+
			"can authenticate AS a terminal, which means holding that key is "+
			"equivalent to holding every device key at the site. This exists only "+
			"to keep firmware predating per-device credentials working while a "+
			"fleet is upgraded. Claim the remaining terminals and unset it.",
			middleware.LegacyDeviceAuthEnv)
	}

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
