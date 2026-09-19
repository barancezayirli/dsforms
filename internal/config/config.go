package config

import (
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"

	"github.com/barancezayirli/dsforms/internal/screen"
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
	// is held for review. A form may override it; screen.DefaultThreshold is the
	// fallback when neither is set.
	SpamThreshold int

	// DigestTo receives the daily quarantine digest. Empty disables it — the
	// digest is opt-in because most instances hold little enough that the
	// sidebar badge is sufficient.
	DigestTo string

	BroadcastThrottleMs  int
	BroadcastMaxAttempts int

	BackupLocalDir string

	// MCPEnabled turns on the /mcp endpoint. Off by default: an endpoint nobody
	// is using is attack surface nobody is watching, and most instances will
	// never want one.
	MCPEnabled bool

	// MCPAllowInsecure is the named opt-out from the cleartext refusal below.
	// It exists for localhost and private-network deployments, where BASE_URL is
	// legitimately http, and it is loud about it on every boot.
	MCPAllowInsecure bool

	// MCPTokenTTLDays is how long a newly minted API token lasts. 0 means it
	// never expires, which is a real choice rather than an unset value — an MCP
	// client in a config file is not somewhere a rotation reminder reaches.
	MCPTokenTTLDays int
}

// Load reads configuration from environment variables.
// It panics on missing required values so the app fails fast at startup.
func Load() Config {
	baseURL := os.Getenv("BASE_URL")
	mcpEnabled := envOrBool("MCP_ENABLED", false)
	mcpAllowInsecure := envOrBool("MCP_ALLOW_INSECURE", false)
	requireMCPTransportSecurity(mcpEnabled, mcpAllowInsecure, baseURL)

	return Config{
		ListenAddr:           envOr("LISTEN_ADDR", ":8080"),
		BaseURL:              baseURL,
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

		MCPEnabled:       mcpEnabled,
		MCPAllowInsecure: mcpAllowInsecure,
		// Clamped rather than rejected: a negative TTL has no sensible reading
		// other than "no expiry", and a token born expired would be a refusal
		// with no message attached to it.
		MCPTokenTTLDays: max(envOrInt("MCP_TOKEN_TTL_DAYS", 0), 0),
	}
}

// requireMCPTransportSecurity refuses to start an instance that would hand out
// API tokens over cleartext.
//
// dsforms never terminates TLS — it is designed to sit behind a proxy that
// does, which is why CreateSessionCookie derives the Secure flag from BASE_URL
// rather than from the connection. BASE_URL is therefore the only statement
// available about how clients actually reach this instance, and an MCP token
// travels in an Authorization header on every single request.
//
// This is a panic rather than a warning for the reason AGENT.md §4 gives: it is
// something a running process cannot fix, and a warning in a container log is
// one nobody reads before exposing the port. The opt-out is named rather than
// inferred from the hostname, because a private-network deployment behind a
// proxy that does not rewrite BASE_URL is legitimate and a localhost check
// cannot express it.
func requireMCPTransportSecurity(enabled, allowInsecure bool, baseURL string) {
	if !enabled {
		// An instance not serving MCP has no token to leak, and must not be
		// stopped from booting over a setting it is not using.
		return
	}
	if strings.HasPrefix(baseURL, "https://") {
		return
	}
	if allowInsecure {
		log.Printf("config: ⚠  MCP is enabled with BASE_URL %q, which is not https. "+
			"Every API token will cross the network in cleartext on every request. "+
			"MCP_ALLOW_INSECURE=true is set, so this is allowed — only do this on "+
			"localhost or a trusted private network.", baseURL)
		return
	}
	panic(fmt.Sprintf(
		"MCP_ENABLED is set but BASE_URL is %q, which is not https. API tokens are "+
			"sent in an Authorization header on every request, so over plain http "+
			"they are readable by anything between the client and this server. "+
			"Set BASE_URL to the https:// address clients actually use, or set "+
			"MCP_ALLOW_INSECURE=true if this instance is only reachable over "+
			"localhost or a trusted private network.", baseURL))
}

// envOrBool reads a boolean environment variable.
//
// Only the spellings people actually write are true, and anything else is
// false — including "yes", "on" and "1 " with a stray space. An unparseable
// value is refused outright rather than silently read as false: MCP_ENABLED=ture
// silently disabling the endpoint is a confusing afternoon, and the same typo on
// MCP_ALLOW_INSECURE would silently re-enable a refusal the operator meant to
// waive. This matches envOrInt, which panics on a malformed integer for the same
// reason.
func envOrBool(key string, fallback bool) bool {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return fallback
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		panic(fmt.Sprintf("environment variable %s must be true or false, got %q", key, v))
	}
	return b
}

// spamThreshold resolves SPAM_THRESHOLD. envOrInt only treats an empty string
// as unset, but an explicit 0 must mean "use the default" too — a literal
// threshold of zero would hold every submission ever received.
func spamThreshold() int {
	n := envOrInt("SPAM_THRESHOLD", screen.DefaultThreshold)

	// Through screen's own clamp, not a second implementation of the same
	// policy. This file already shared the *constants*; sharing only those left
	// the two disagreeing about what they mean — a typo of -6 for 6 clamped to
	// the floor of 1 here, holding essentially every submission, where the
	// decision itself would have used the default of 6. The bounds and the
	// interpretation of a value outside them are one policy.
	clamped := screen.ClampThreshold(n)
	if clamped != n {
		// Say so. An operator who sets 100 meaning "effectively off" gets 20,
		// which quarantines everything scoring 20 or more — the opposite of
		// their intent, and nothing in the log would have contradicted them.
		log.Printf("config: SPAM_THRESHOLD %d is out of range, using %d", n, clamped)
	}
	return clamped
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
