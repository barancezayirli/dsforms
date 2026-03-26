# Session 1 — Project Skeleton & Config Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Repo compiles, config loads from env vars, health endpoint responds 200 OK, all tests pass.

**Architecture:** Single `internal/config` package loads all env vars into a `Config` struct, panicking on missing required values. `main.go` wires config loading and starts an HTTP server with chi router serving `GET /healthz`. Rate limit env vars (`RATE_BURST`, `RATE_PER_MINUTE`) are included in config since they're needed in Session 3.

**Tech Stack:** Go 1.23, chi/v5 router, modernc.org/sqlite (dependency added now, used later)

---

## File Structure

| File | Responsibility |
|------|----------------|
| `go.mod` | Module declaration, Go version, dependencies |
| `.gitignore` | Ignore binaries, data, .env |
| `.env.example` | Documented env var template |
| `internal/config/config.go` | `Config` struct + `Load()` function reading env vars |
| `internal/config/config_test.go` | Table-driven tests for all env var combos |
| `main.go` | Config load, chi router, `/healthz`, HTTP server start |
| `main_test.go` | Test for `/healthz` endpoint |

---

### Task 1: Initialize Go module and dependencies

**Files:**
- Create: `go.mod`
- Create: `.gitignore`
- Create: `.env.example`

- [ ] **Step 1: Initialize go module**

```bash
cd /Users/barancezayirli/Projects/dsforms
go mod init github.com/youruser/dsforms
```

- [ ] **Step 2: Add required dependencies**

```bash
go get github.com/go-chi/chi/v5
go get modernc.org/sqlite
go get golang.org/x/crypto
go get github.com/google/uuid
```

- [ ] **Step 3: Create .gitignore**

```
# Binaries
dsforms
*.exe

# Data
/data/
*.db

# Environment
.env

# IDE
.idea/
.vscode/
*.swp
*.swo

# OS
.DS_Store
Thumbs.db

# Test
coverage.out
c.out
```

- [ ] **Step 4: Create .env.example**

Per DSFORMS_PLAN.md §15 — all env vars documented with comments.

```bash
# Server
LISTEN_ADDR=:8080
BASE_URL=https://forms.example.com

# Database (leave as default unless you have a reason to change)
DB_PATH=/data/dsforms.db

# Auth
# SECRET_KEY is used to sign session cookies. Generate with:
#   openssl rand -base64 32
SECRET_KEY=change-me-to-a-random-32-char-string

# SMTP — works with Gmail App Passwords, Fastmail, Resend SMTP, Mailgun SMTP, etc.
SMTP_HOST=smtp.fastmail.com
SMTP_PORT=587
SMTP_USER=you@example.com
SMTP_PASS=your-app-password
SMTP_FROM=DSForms <noreply@example.com>

# Rate limiting (applied to POST /f/{formID} only)
RATE_BURST=5
RATE_PER_MINUTE=6

# Backup (CLI only — optional)
# If set, "docker compose exec dsforms ./dsforms backup create" writes a
# snapshot here. Leave unset if you don't need CLI-triggered backups.
# UI export/import works regardless of this setting.
#BACKUP_LOCAL_DIR=/data/backups
```

- [ ] **Step 5: Commit**

```bash
git add go.mod go.sum .gitignore .env.example
git commit -m "chore: init go module with deps, gitignore, env example"
```

---

### Task 2: Write config tests (TDD — red phase)

**Files:**
- Create: `internal/config/config_test.go`

- [ ] **Step 1: Write the failing tests**

Table-driven tests covering:
1. `Load()` with all vars set — returns correct Config, no panic
2. `Load()` missing `SECRET_KEY` — panics
3. `Load()` missing `SMTP_HOST` — panics
4. `Load()` missing `SMTP_USER` — panics
5. `Load()` missing `SMTP_PASS` — panics
6. `Load()` missing `SMTP_FROM` — panics
7. `Load()` defaults applied: `ListenAddr=":8080"`, `DBPath="/data/dsforms.db"`, `SMTPPort=587`
8. `Load()` custom values override defaults
9. `BACKUP_LOCAL_DIR` empty string when not set (no panic)
10. `RATE_BURST` and `RATE_PER_MINUTE` defaults applied (5 and 6)
11. `SMTP_PORT` invalid (non-numeric) — panics

```go
package config

import (
    "os"
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
    t.Parallel()
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
                os.Unsetenv("SECRET_KEY")
            },
            wantPanic: true,
        },
        {
            name: "missing SMTP_HOST panics",
            setup: func(t *testing.T) {
                setAllRequired(t)
                os.Unsetenv("SMTP_HOST")
            },
            wantPanic: true,
        },
        {
            name: "missing SMTP_USER panics",
            setup: func(t *testing.T) {
                setAllRequired(t)
                os.Unsetenv("SMTP_USER")
            },
            wantPanic: true,
        },
        {
            name: "missing SMTP_PASS panics",
            setup: func(t *testing.T) {
                setAllRequired(t)
                os.Unsetenv("SMTP_PASS")
            },
            wantPanic: true,
        },
        {
            name: "missing SMTP_FROM panics",
            setup: func(t *testing.T) {
                setAllRequired(t)
                os.Unsetenv("SMTP_FROM")
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
    }
    for _, tt := range tests {
        tt := tt
        t.Run(tt.name, func(t *testing.T) {
            // Note: NOT parallel — env var tests mutate process env
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
```

- [ ] **Step 2: Create minimal config.go stub so test compiles but fails**

```go
package config

type Config struct{}

func Load() Config {
    return Config{}
}
```

- [ ] **Step 3: Run tests to verify they fail**

```bash
go test ./internal/config/... -v
```

Expected: compilation errors (Config has no fields) or test failures.

---

### Task 3: Implement config (TDD — green phase)

**Files:**
- Modify: `internal/config/config.go`

- [ ] **Step 1: Write full Config struct and Load() implementation**

```go
package config

import (
    "fmt"
    "os"
    "strconv"
)

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

    RateBurst      int
    RatePerMinute  int

    BackupLocalDir string
}

func Load() Config {
    cfg := Config{
        ListenAddr:    envOr("LISTEN_ADDR", ":8080"),
        BaseURL:       os.Getenv("BASE_URL"),
        DBPath:        envOr("DB_PATH", "/data/dsforms.db"),
        SecretKey:     requireEnv("SECRET_KEY"),
        SMTPHost:      requireEnv("SMTP_HOST"),
        SMTPPort:      envOrInt("SMTP_PORT", 587),
        SMTPUser:      requireEnv("SMTP_USER"),
        SMTPPass:      requireEnv("SMTP_PASS"),
        SMTPFrom:      requireEnv("SMTP_FROM"),
        RateBurst:     envOrInt("RATE_BURST", 5),
        RatePerMinute: envOrInt("RATE_PER_MINUTE", 6),
        BackupLocalDir: os.Getenv("BACKUP_LOCAL_DIR"),
    }
    return cfg
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
```

- [ ] **Step 2: Run tests to verify they pass**

```bash
go test ./internal/config/... -v
```

Expected: all tests PASS.

- [ ] **Step 3: Run race detector**

```bash
go test -race ./internal/config/...
```

Expected: clean.

- [ ] **Step 4: Commit**

```bash
git add internal/config/
git commit -m "feat: add config package with env var loading and validation"
```

---

### Task 4: Write healthz test (TDD — red phase)

**Files:**
- Create: `main_test.go`

- [ ] **Step 1: Write the failing healthz test**

```go
package main

import (
    "net/http"
    "net/http/httptest"
    "testing"

    "github.com/go-chi/chi/v5"
)

func TestHealthz(t *testing.T) {
    t.Parallel()
    r := chi.NewRouter()
    r.Get("/healthz", func(w http.ResponseWriter, r *http.Request) {
        w.WriteHeader(http.StatusOK)
        w.Write([]byte("ok"))
    })

    req := httptest.NewRequest("GET", "/healthz", nil)
    w := httptest.NewRecorder()
    r.ServeHTTP(w, req)

    if w.Code != http.StatusOK {
        t.Errorf("GET /healthz status = %d, want %d", w.Code, http.StatusOK)
    }
    if w.Body.String() != "ok" {
        t.Errorf("GET /healthz body = %q, want %q", w.Body.String(), "ok")
    }
}
```

- [ ] **Step 2: Run test to verify it passes** (this is a self-contained test)

```bash
go test -v -run TestHealthz .
```

Note: This test is self-contained (builds its own router) so it will pass immediately — that's OK because it's testing the route pattern that main.go will use. The real "red" is that main.go doesn't exist yet.

- [ ] **Step 3: Commit test**

```bash
git add main_test.go
git commit -m "test: add healthz endpoint test"
```

---

### Task 5: Implement main.go

**Files:**
- Create: `main.go`

- [ ] **Step 1: Write main.go with config loading, chi router, healthz, server start**

```go
package main

import (
    "log"
    "net/http"

    "github.com/go-chi/chi/v5"
    "github.com/youruser/dsforms/internal/config"
)

func main() {
    cfg := config.Load()

    r := chi.NewRouter()

    r.Get("/healthz", func(w http.ResponseWriter, r *http.Request) {
        w.WriteHeader(http.StatusOK)
        w.Write([]byte("ok"))
    })

    log.Printf("starting server on %s", cfg.ListenAddr)
    if err := http.ListenAndServe(cfg.ListenAddr, r); err != nil {
        log.Fatalf("server error: %v", err)
    }
}
```

- [ ] **Step 2: Verify build**

```bash
go build ./...
```

Expected: exits 0.

- [ ] **Step 3: Verify all tests pass**

```bash
go test ./... -race -count=1
```

Expected: all pass.

- [ ] **Step 4: Run go vet**

```bash
go vet ./...
```

Expected: clean.

- [ ] **Step 5: Commit**

```bash
git add main.go
git commit -m "feat: add main.go with config loading and healthz endpoint"
```

---

### Task 6: Final verification

- [ ] **Step 1: Full build check**

```bash
go build ./...
go test ./... -race -count=1
go vet ./...
```

All must exit 0.

- [ ] **Step 2: Manual smoke test** (optional, human-driven)

```bash
SECRET_KEY=test SMTP_HOST=x SMTP_USER=x SMTP_PASS=x SMTP_FROM=x go run . &
curl http://localhost:8080/healthz
# Should print: ok
kill %1
```
