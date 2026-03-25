package config

import (
	"fmt"
	"os"
	"strconv"
)

// Config holds all application configuration loaded from environment variables.
type Config struct {
	ListenAddr string
	BaseURL    string
	DBPath     string
	SecretKey  string

	SMTPHost string
	SMTPPort int
	SMTPUser string
	SMTPPass string
	SMTPFrom string

	RateBurst     int
	RatePerMinute int

	BackupLocalDir string
}

// Load reads configuration from environment variables.
// It panics on missing required values so the app fails fast at startup.
func Load() Config {
	return Config{
		ListenAddr:     envOr("LISTEN_ADDR", ":8080"),
		BaseURL:        os.Getenv("BASE_URL"),
		DBPath:         envOr("DB_PATH", "/data/dsforms.db"),
		SecretKey:      requireEnv("SECRET_KEY"),
		SMTPHost:       requireEnv("SMTP_HOST"),
		SMTPPort:       envOrInt("SMTP_PORT", 587),
		SMTPUser:       requireEnv("SMTP_USER"),
		SMTPPass:       requireEnv("SMTP_PASS"),
		SMTPFrom:       requireEnv("SMTP_FROM"),
		RateBurst:      envOrInt("RATE_BURST", 5),
		RatePerMinute:  envOrInt("RATE_PER_MINUTE", 6),
		BackupLocalDir: os.Getenv("BACKUP_LOCAL_DIR"),
	}
}

func requireEnv(key string) string {
	v := os.Getenv(key)
	if v == "" {
		panic(fmt.Sprintf("required environment variable %s is not set", key))
	}
	return v
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func envOrInt(key string, fallback int) int {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		panic(fmt.Sprintf("environment variable %s must be an integer, got %q", key, v))
	}
	return n
}
