package config

import (
	"github.com/barancezayirli/dsforms/internal/screen"

	"testing"
)

func setAllRequired(t *testing.T) {
	t.Helper()
	t.Setenv("SECRET_KEY", "test-secret-key-32-chars-long!!")
}

// TestLoad does not use t.Parallel because t.Setenv
// is incompatible with parallel test execution.
func TestLoad(t *testing.T) {
	tests := []struct {
		name      string
		setup     func(t *testing.T)
		wantPanic bool
		check     func(t *testing.T, cfg Config)
	}{
		{
			name: "all required vars set",
			setup: func(t *testing.T) {
				setAllRequired(t)
				t.Setenv("SMTP_HOST", "smtp.example.com")
				t.Setenv("SMTP_USER", "user@example.com")
				t.Setenv("SMTP_PASS", "password123")
				t.Setenv("SMTP_FROM", "DSForms <noreply@example.com>")
			},
			check: func(t *testing.T, cfg Config) {
				if cfg.SecretKey != "test-secret-key-32-chars-long!!" {
					t.Errorf("SecretKey = %q, want %q", cfg.SecretKey, "test-secret-key-32-chars-long!!")
				}
				if cfg.SMTPHost != "smtp.example.com" {
					t.Errorf("SMTPHost = %q, want %q", cfg.SMTPHost, "smtp.example.com")
				}
			},
		},
		{
			name: "missing SECRET_KEY panics",
			setup: func(t *testing.T) {
				setAllRequired(t)
				t.Setenv("SECRET_KEY", "")
			},
			wantPanic: true,
		},
		{
			name: "defaults applied",
			setup: func(t *testing.T) {
				setAllRequired(t)
			},
			check: func(t *testing.T, cfg Config) {
				if cfg.ListenAddr != ":8080" {
					t.Errorf("ListenAddr = %q, want %q", cfg.ListenAddr, ":8080")
				}
				if cfg.DBPath != "/data/dsforms.db" {
					t.Errorf("DBPath = %q, want %q", cfg.DBPath, "/data/dsforms.db")
				}
				if cfg.SMTPPort != 587 {
					t.Errorf("SMTPPort = %d, want %d", cfg.SMTPPort, 587)
				}
				if cfg.RateBurst != 5 {
					t.Errorf("RateBurst = %d, want %d", cfg.RateBurst, 5)
				}
				if cfg.RatePerMinute != 6 {
					t.Errorf("RatePerMinute = %d, want %d", cfg.RatePerMinute, 6)
				}
			},
		},
		{
			name: "custom values override defaults",
			setup: func(t *testing.T) {
				setAllRequired(t)
				t.Setenv("LISTEN_ADDR", ":9090")
				t.Setenv("DB_PATH", "/tmp/test.db")
				t.Setenv("SMTP_PORT", "465")
				t.Setenv("BASE_URL", "https://custom.example.com")
				t.Setenv("RATE_BURST", "10")
				t.Setenv("RATE_PER_MINUTE", "12")
			},
			check: func(t *testing.T, cfg Config) {
				if cfg.ListenAddr != ":9090" {
					t.Errorf("ListenAddr = %q, want %q", cfg.ListenAddr, ":9090")
				}
				if cfg.DBPath != "/tmp/test.db" {
					t.Errorf("DBPath = %q, want %q", cfg.DBPath, "/tmp/test.db")
				}
				if cfg.SMTPPort != 465 {
					t.Errorf("SMTPPort = %d, want %d", cfg.SMTPPort, 465)
				}
				if cfg.BaseURL != "https://custom.example.com" {
					t.Errorf("BaseURL = %q, want %q", cfg.BaseURL, "https://custom.example.com")
				}
				if cfg.RateBurst != 10 {
					t.Errorf("RateBurst = %d, want %d", cfg.RateBurst, 10)
				}
				if cfg.RatePerMinute != 12 {
					t.Errorf("RatePerMinute = %d, want %d", cfg.RatePerMinute, 12)
				}
			},
		},
		{
			name: "BACKUP_LOCAL_DIR empty when not set",
			setup: func(t *testing.T) {
				setAllRequired(t)
			},
			check: func(t *testing.T, cfg Config) {
				if cfg.BackupLocalDir != "" {
					t.Errorf("BackupLocalDir = %q, want empty", cfg.BackupLocalDir)
				}
			},
		},
		{
			name: "missing SMTP_HOST does not panic",
			setup: func(t *testing.T) {
				t.Setenv("SECRET_KEY", "test-secret-key-32-chars-long!!")
				// No SMTP vars set at all
			},
			check: func(t *testing.T, cfg Config) {
				if cfg.SMTPHost != "" {
					t.Errorf("SMTPHost = %q, want empty", cfg.SMTPHost)
				}
				if cfg.SMTPFrom != "" {
					t.Errorf("SMTPFrom = %q, want empty", cfg.SMTPFrom)
				}
			},
		},
		{
			name: "SMTP_PORT invalid panics",
			setup: func(t *testing.T) {
				setAllRequired(t)
				t.Setenv("SMTP_PORT", "notanumber")
			},
			wantPanic: true,
		},
		{
			name: "RATE_BURST invalid panics",
			setup: func(t *testing.T) {
				setAllRequired(t)
				t.Setenv("RATE_BURST", "notanumber")
			},
			wantPanic: true,
		},
		{
			name: "RATE_PER_MINUTE invalid panics",
			setup: func(t *testing.T) {
				setAllRequired(t)
				t.Setenv("RATE_PER_MINUTE", "notanumber")
			},
			wantPanic: true,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			tt.setup(t)
			if tt.wantPanic {
				defer func() {
					if r := recover(); r == nil {
						t.Fatal("expected panic, got none")
					}
				}()
				Load()
				return
			}
			cfg := Load()
			if tt.check != nil {
				tt.check(t, cfg)
			}
		})
	}
}

// TestBroadcastConfigDefaults and TestBroadcastConfigOverride do not use
// t.Parallel because t.Setenv is incompatible with parallel test execution.

func TestBroadcastConfigDefaults(t *testing.T) {
	t.Setenv("SECRET_KEY", "x")
	cfg := Load()
	if cfg.BroadcastThrottleMs != 200 {
		t.Errorf("BroadcastThrottleMs = %d, want 200", cfg.BroadcastThrottleMs)
	}
	if cfg.BroadcastMaxAttempts != 3 {
		t.Errorf("BroadcastMaxAttempts = %d, want 3", cfg.BroadcastMaxAttempts)
	}
}

func TestBroadcastConfigOverride(t *testing.T) {
	t.Setenv("SECRET_KEY", "x")
	t.Setenv("BROADCAST_THROTTLE_MS", "50")
	t.Setenv("BROADCAST_MAX_ATTEMPTS", "5")
	cfg := Load()
	if cfg.BroadcastThrottleMs != 50 || cfg.BroadcastMaxAttempts != 5 {
		t.Errorf("override failed: %+v", cfg)
	}
}

// TestSpamThreshold covers the one config value that is clamped rather than
// merely defaulted. An out-of-range threshold is a footgun in both directions:
// 0 would hold every submission ever received, and a very high value would
// disable the filter without saying so.
//
// No t.Parallel: t.Setenv is incompatible with parallel tests.
func TestSpamThreshold(t *testing.T) {
	tests := []struct {
		name string
		set  string
		want int
	}{
		{name: "unset falls back to the default", set: "", want: 6},
		{name: "zero is treated as unset, not as a threshold", set: "0", want: 6},
		{name: "in range is taken as given", set: "4", want: 4},
		{name: "upper bound is allowed", set: "20", want: 20},
		{name: "above range clamps down", set: "500", want: 20},
		{name: "negative falls back to the default, not the floor", set: "-3", want: screen.DefaultThreshold},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setAllRequired(t)
			if tt.set != "" {
				t.Setenv("SPAM_THRESHOLD", tt.set)
			}
			if got := Load().SpamThreshold; got != tt.want {
				t.Errorf("SPAM_THRESHOLD=%q gave SpamThreshold = %d, want %d", tt.set, got, tt.want)
			}
		})
	}
}

func TestClampInt(t *testing.T) {
	t.Parallel()
	tests := []struct{ v, lo, hi, want int }{
		{5, 1, 20, 5},
		{0, 1, 20, 1},
		{99, 1, 20, 20},
		{1, 1, 20, 1},
		{20, 1, 20, 20},
	}
	for _, tt := range tests {
		if got := clampInt(tt.v, tt.lo, tt.hi); got != tt.want {
			t.Errorf("clampInt(%d, %d, %d) = %d, want %d", tt.v, tt.lo, tt.hi, got, tt.want)
		}
	}
}

// TestMCPRefusesToRunInCleartext is the security decision of the whole MCP
// feature, and it lives here because config is the only place that can refuse.
//
// dsforms never sees TLS — it is built to sit behind a proxy that terminates it,
// which is why the session cookie's Secure flag is derived from BASE_URL rather
// than from the connection. So BASE_URL is the only signal available, and a
// long-lived bearer token in an Authorization header over plain http is
// cleartext to everything between the client and that proxy.
//
// A warning in a container log is one nobody reads before exposing the port.
// This is a refusal to start, with a named opt-out for localhost and private
// networks, which is the bar AGENT.md §4 sets for a panic: something a running
// process cannot fix.
//
// Does not use t.Parallel because t.Setenv is incompatible with it.
func TestMCPRefusesToRunInCleartext(t *testing.T) {
	tests := []struct {
		name        string
		baseURL     string
		enabled     string
		allowPlain  string
		wantPanic   bool
		wantEnabled bool
	}{
		{
			name:        "https is fine",
			baseURL:     "https://forms.example.com",
			enabled:     "true",
			wantEnabled: true,
		},
		{
			name:      "http panics",
			baseURL:   "http://forms.example.com",
			enabled:   "true",
			wantPanic: true,
		},
		{
			name:      "an empty BASE_URL panics too",
			baseURL:   "",
			enabled:   "true",
			wantPanic: true,
		},
		{
			name:      "a scheme-less BASE_URL panics",
			baseURL:   "forms.example.com",
			enabled:   "true",
			wantPanic: true,
		},
		{
			// The prefix check must not be fooled by a host that merely starts
			// with the letters.
			name:      "httpsomething is not https",
			baseURL:   "http://httpsomething.example.com",
			enabled:   "true",
			wantPanic: true,
		},
		{
			name:        "the opt-out clears it",
			baseURL:     "http://localhost:8080",
			enabled:     "true",
			allowPlain:  "true",
			wantEnabled: true,
		},
		{
			// The whole check is downstream of MCP_ENABLED: an instance that is
			// not serving MCP has no token to leak, and must not be stopped from
			// booting over one.
			name:        "http is fine when MCP is off",
			baseURL:     "http://forms.example.com",
			enabled:     "",
			wantEnabled: false,
		},
		{
			name:        "off by default",
			baseURL:     "https://forms.example.com",
			enabled:     "",
			wantEnabled: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setAllRequired(t)
			t.Setenv("BASE_URL", tt.baseURL)
			t.Setenv("MCP_ENABLED", tt.enabled)
			t.Setenv("MCP_ALLOW_INSECURE", tt.allowPlain)

			if tt.wantPanic {
				defer func() {
					if r := recover(); r == nil {
						t.Fatal("MCP_ENABLED with a cleartext BASE_URL started anyway; " +
							"every token this instance issues would cross the network in the clear")
					}
				}()
				Load()
				return
			}

			cfg := Load()
			if cfg.MCPEnabled != tt.wantEnabled {
				t.Errorf("MCPEnabled = %v, want %v", cfg.MCPEnabled, tt.wantEnabled)
			}
		})
	}
}

// TestMCPTokenTTLDays. Zero means tokens never expire, which is a real choice
// rather than an unset value, so it must survive Load.
func TestMCPTokenTTLDays(t *testing.T) {
	tests := []struct {
		name string
		set  string
		want int
	}{
		{"unset means no expiry", "", 0},
		{"explicit zero means no expiry", "0", 0},
		{"a real value", "90", 90},
		{"negative is clamped to no expiry", "-1", 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setAllRequired(t)
			t.Setenv("MCP_TOKEN_TTL_DAYS", tt.set)
			if got := Load().MCPTokenTTLDays; got != tt.want {
				t.Errorf("MCPTokenTTLDays = %d, want %d", got, tt.want)
			}
		})
	}
}
