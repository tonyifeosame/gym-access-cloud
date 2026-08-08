package database

import (
	"database/sql"
	"fmt"
	"log"
	"os"
	"strconv"
	"time"

	_ "github.com/lib/pq"
)

var DB *sql.DB

// Config holds database configuration
type Config struct {
	Host     string
	Port     string
	User     string
	Password string
	DBName   string
	// SSLMode is passed straight to the driver. Kept configurable because a
	// deployment where the database is across a network needs `require` or
	// stronger, and one where it is a local socket cannot use it at all.
	SSLMode string
	// MaxOpenConns bounds connections to the database. Unbounded is the
	// dangerous default: a fleet of terminals all polling at once will open a
	// connection per in-flight request until PostgreSQL refuses new ones, and
	// then every request fails rather than queueing behind a busy pool.
	MaxOpenConns    int
	MaxIdleConns    int
	ConnMaxLifetime time.Duration
	ConnMaxIdleTime time.Duration
}

// Connect establishes a connection to PostgreSQL and configures the pool
func Connect(cfg Config) error {
	connStr := fmt.Sprintf("host=%s port=%s user=%s password=%s dbname=%s sslmode=%s",
		cfg.Host, cfg.Port, cfg.User, cfg.Password, cfg.DBName, cfg.SSLMode)

	var err error
	DB, err = sql.Open("postgres", connStr)
	if err != nil {
		return fmt.Errorf("error opening database: %w", err)
	}

	DB.SetMaxOpenConns(cfg.MaxOpenConns)
	DB.SetMaxIdleConns(cfg.MaxIdleConns)
	// Recycling connections keeps a long-lived process from holding one that a
	// restarted database or a failed-over replica no longer considers valid.
	DB.SetConnMaxLifetime(cfg.ConnMaxLifetime)
	DB.SetConnMaxIdleTime(cfg.ConnMaxIdleTime)

	if err = DB.Ping(); err != nil {
		return fmt.Errorf("error connecting to database: %w", err)
	}

	log.Printf("Connected to PostgreSQL %s@%s:%s/%s (sslmode=%s, max_open_conns=%d)",
		cfg.User, cfg.Host, cfg.Port, cfg.DBName, cfg.SSLMode, cfg.MaxOpenConns)
	return nil
}

// Close closes the database connection
func Close() {
	if DB != nil {
		DB.Close()
	}
}

// Pool defaults. Sized for a fleet of terminals polling on an interval rather
// than for sustained concurrent load: requests are short, so a modest pool with
// queueing behind it behaves better than a large one that lets every caller
// open its own connection.
const (
	defaultMaxOpenConns    = 25
	defaultMaxIdleConns    = 5
	defaultConnMaxLifetime = 30 * time.Minute
	defaultConnMaxIdleTime = 5 * time.Minute
)

// GetConfigFromEnv reads database configuration from environment variables.
//
// DB_SSLMODE defaults to `disable` to preserve the behaviour every existing
// deployment and the bundled docker-compose already rely on. It is called out
// in the README because a database reached over a network needs `require`.
func GetConfigFromEnv() Config {
	return Config{
		Host:            getEnv("DB_HOST", "localhost"),
		Port:            getEnv("DB_PORT", "5432"),
		User:            getEnv("DB_USER", "at_admin"),
		Password:        getEnv("DB_PASSWORD", ""),
		DBName:          getEnv("DB_NAME", "access_terminal"),
		SSLMode:         getEnv("DB_SSLMODE", "disable"),
		MaxOpenConns:    getEnvInt("DB_MAX_OPEN_CONNS", defaultMaxOpenConns),
		MaxIdleConns:    getEnvInt("DB_MAX_IDLE_CONNS", defaultMaxIdleConns),
		ConnMaxLifetime: getEnvSeconds("DB_CONN_MAX_LIFETIME_SECONDS", defaultConnMaxLifetime),
		ConnMaxIdleTime: getEnvSeconds("DB_CONN_MAX_IDLE_SECONDS", defaultConnMaxIdleTime),
	}
}

func getEnv(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

func getEnvInt(key string, defaultValue int) int {
	if raw := os.Getenv(key); raw != "" {
		if v, err := strconv.Atoi(raw); err == nil && v > 0 {
			return v
		}
	}
	return defaultValue
}

func getEnvSeconds(key string, defaultValue time.Duration) time.Duration {
	if raw := os.Getenv(key); raw != "" {
		if v, err := strconv.Atoi(raw); err == nil && v > 0 {
			return time.Duration(v) * time.Second
		}
	}
	return defaultValue
}
