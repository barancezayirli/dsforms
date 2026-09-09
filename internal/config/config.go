package config

import (
	"fmt"
	"os"
	"strconv"

	"github.com/barancezayirli/dsforms/internal/spam"
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

	// SpamThreshold is the instance-wide score at or above which a submission
	// is held for review. A form may override it; spam.DefaultThreshold is the
	// fallback when neither is set.
	SpamThreshold int

	// DigestTo receives the daily quarantine digest. Empty disables it — the
	// digest is opt-in because most instances hold little enough that the
	// sidebar badge is sufficient.
	DigestTo string

	BroadcastThrottleMs  int
	BroadcastMaxAttempts int

	BackupLocalDir string
}

// Load reads configuration from environment variables.
// It panics on missing required values so the app fails fast at startup.
func Load() Config {
	return Config{
		ListenAddr:           envOr("LISTEN_ADDR", ":8080"),
		BaseURL:              os.Getenv("BASE_URL"),
		DBPath:               envOr("DB_PATH", "/data/dsforms.db"),
		SecretKey:            requireEnv("SECRET_KEY"),
		SMTPHost:             os.Getenv("SMTP_HOST"),
		SMTPPort:             envOrInt("SMTP_PORT", 587),
		SMTPUser:             os.Getenv("SMTP_USER"),
		SMTPPass:             os.Getenv("SMTP_PASS"),
		SMTPFrom:             os.Getenv("SMTP_FROM"),
		RateBurst:            envOrInt("RATE_BURST", 5),
		RatePerMinute:        envOrInt("RATE_PER_MINUTE", 6),
		BroadcastThrottleMs:  envOrInt("BROADCAST_THROTTLE_MS", 200),
		BroadcastMaxAttempts: envOrInt("BROADCAST_MAX_ATTEMPTS", 3),
		BackupLocalDir:       os.Getenv("BACKUP_LOCAL_DIR"),

		// 0 means "unset" rather than a real threshold of zero, which would
		// hold every submission ever received. Clamped because the other end
		// is just as bad: a very high value silently disables the filter.
		SpamThreshold: spamThreshold(),
		DigestTo:      os.Getenv("DIGEST_TO"),
	}
}

// spamThreshold resolves SPAM_THRESHOLD. envOrInt only treats an empty string
// as unset, but an explicit 0 must mean "use the default" too — a literal
// threshold of zero would hold every submission ever received.
func spamThreshold() int {
	n := envOrInt("SPAM_THRESHOLD", spam.DefaultThreshold)
	if n == 0 {
		return spam.DefaultThreshold
	}
	return clampInt(n, 1, 20)
}

// clampInt bounds v to [lo, hi].
func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
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
