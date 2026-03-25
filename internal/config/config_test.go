package config

import (
	"testing"
)

func setAllRequired(t *testing.T) {
	t.Helper()
	t.Setenv("SECRET_KEY", "test-secret-key-32-chars-long!!")
	t.Setenv("SMTP_HOST", "smtp.example.com")
	t.Setenv("SMTP_USER", "user@example.com")
	t.Setenv("SMTP_PASS", "password123")
	t.Setenv("SMTP_FROM", "DSForms <noreply@example.com>")
}

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
			name: "missing SMTP_HOST panics",
			setup: func(t *testing.T) {
				setAllRequired(t)
				t.Setenv("SMTP_HOST", "")
			},
			wantPanic: true,
		},
		{
			name: "missing SMTP_USER panics",
			setup: func(t *testing.T) {
				setAllRequired(t)
				t.Setenv("SMTP_USER", "")
			},
			wantPanic: true,
		},
		{
			name: "missing SMTP_PASS panics",
			setup: func(t *testing.T) {
				setAllRequired(t)
				t.Setenv("SMTP_PASS", "")
			},
			wantPanic: true,
		},
		{
			name: "missing SMTP_FROM panics",
			setup: func(t *testing.T) {
				setAllRequired(t)
				t.Setenv("SMTP_FROM", "")
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
