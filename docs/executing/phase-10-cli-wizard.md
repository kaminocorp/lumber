# Phase 10: CLI Setup Wizard

**Goal:** Make Lumber's first-run experience zero-friction. When a user installs Lumber and runs it with no configuration, an interactive wizard guides them through setup — selecting a log source, providing credentials (if cloud), and choosing output options. The wizard produces a fully populated `Config` that feeds into the existing pipeline startup path. Experienced users bypass the wizard entirely via flags/env vars.

**Starting point:** `cmd/lumber/main.go` loads config from env vars + CLI flags, validates, and starts the pipeline. If no connector is configured, it defaults to `"vercel"` and immediately fails requiring an API key. There is no interactive mode, no stdin/file connector, and no guided setup.

**Dependency:** `charmbracelet/huh` — a Go library for building interactive CLI forms. Provides select menus (arrow-key navigation), text inputs with validation, password masking, and styled output. Built on `bubbletea`/`lipgloss`. This is Lumber's first interactive-UI dependency.

---

## What Changes and Why

### Current gaps

1. **No guided onboarding** — first-time users face a wall of env vars and flags with no guidance. The default connector (`vercel`) fails without `LUMBER_API_KEY`, producing a config validation error that doesn't explain what to do next.
2. **No local log ingestion** — all three connectors require cloud provider credentials. There's no way to classify a local log file or piped input without a cloud account.
3. **API key enforced for all connectors** — `config.go:178` requires `LUMBER_API_KEY` whenever any connector is set. Local connectors (stdin, file) don't need authentication.
4. **No connector defaults to empty** — `config.go:66` defaults `Connector.Provider` to `"vercel"`. A bare `lumber` command should trigger the wizard, not attempt a Vercel connection.

### Design principles

- **Progressive disclosure** — show only what's relevant to the chosen path. Local users never see API key prompts. Cloud users never see file path prompts.
- **Non-blocking for power users** — any flag or env var set → wizard is skipped entirely. The wizard is a fallback for unconfigured runs, not a gate.
- **Config-compatible output** — the wizard produces a `Config` struct identical to what `LoadWithFlags()` produces. No special pipeline path.
- **Graceful degradation** — if stdin is not a TTY (e.g., running in CI or piped input), skip the wizard and error with a helpful message listing required flags.

---

## Implementation Plan

### Section 1: Add `charmbracelet/huh` Dependency

**Files:** `go.mod`, `go.sum`

```bash
go get github.com/charmbracelet/huh@latest
go mod tidy
```

This pulls in `huh` and its transitive dependencies (`bubbletea`, `lipgloss`, `bubbles`, `x/ansi`, `x/term`). These are all well-maintained Charm ecosystem libraries.

**Verification:** `go build ./...` compiles cleanly.

---

### Section 2: Stdin Connector

**New file:** `internal/connector/stdin/stdin.go`

A connector that reads log lines from `os.Stdin`, one per line. Each line becomes a `model.RawLog` with `Source: "stdin"`, `Timestamp: time.Now()`, and `Raw` set to the line text.

**Stream behavior:**
- Opens a `bufio.Scanner` on `os.Stdin`
- Reads lines in a goroutine, sends each as a `RawLog` on the output channel
- Closes the channel on EOF or context cancellation
- Sets `Scanner.Buffer()` to 1MB to handle long log lines (default 64KB is too small for stack traces)

**Query behavior:**
- Returns `fmt.Errorf("stdin connector does not support query mode")` — stdin is inherently streaming, there's no historical query concept.

**Registration:**
```go
func init() {
    connector.Register("stdin", func() connector.Connector {
        return &Connector{}
    })
}
```

**Test file:** `internal/connector/stdin/stdin_test.go`

Tests:
1. **Stream reads lines** — feed a `strings.Reader` with 3 lines, verify 3 `RawLog` values arrive on the channel with correct `Raw` and `Source` fields.
2. **Stream respects context cancellation** — cancel context mid-read, verify channel closes without blocking.
3. **Stream handles empty input** — empty reader → channel closes immediately, no error.
4. **Query returns error** — calling `Query()` returns an error indicating stdin doesn't support query mode.
5. **Long lines** — feed a line > 64KB, verify it's read completely (tests the buffer size increase).

**Implementation note:** To make the connector testable, accept an `io.Reader` via an option rather than hardcoding `os.Stdin`. The default (no option) uses `os.Stdin`. Tests pass a `strings.Reader`.

```go
type Connector struct {
    reader io.Reader // defaults to os.Stdin
}

type Option func(*Connector)

func WithReader(r io.Reader) Option {
    return func(c *Connector) { c.reader = r }
}
```

The `init()` registration uses the default (no option). Tests use `WithReader()`.

---

### Section 3: File Connector

**New file:** `internal/connector/file/file.go`

A connector that reads log lines from a file on disk. Each line becomes a `model.RawLog` with `Source: "file"`, the filename in `Metadata["file"]`, and `Raw` set to the line text.

**Stream behavior:**
- Opens the file at the path specified in `ConnectorConfig.Extra["file"]`
- Reads lines via `bufio.Scanner` in a goroutine (same pattern as stdin)
- Closes the channel on EOF or context cancellation
- Sets scanner buffer to 1MB (same as stdin)

**Query behavior:**
- Same as stream — reads the entire file. The `QueryParams.Limit` field caps the number of lines returned. `Start`/`End` time filters are not applicable (file lines have no inherent timestamp) — ignored with a debug log.

**Registration:**
```go
func init() {
    connector.Register("file", func() connector.Connector {
        return &Connector{}
    })
}
```

**Config wiring:**
- File path comes from `ConnectorConfig.Extra["file"]`, populated by a new `-file` CLI flag or `LUMBER_FILE_PATH` env var.
- Validation: if connector is `"file"`, the file path must be set and the file must exist.

**Test file:** `internal/connector/file/file_test.go`

Tests:
1. **Stream reads file** — write a temp file with 5 lines, stream it, verify 5 `RawLog` values with correct content and metadata.
2. **Stream respects context cancellation** — large file, cancel early, verify partial read + clean shutdown.
3. **Query with limit** — 10-line file, limit=3, verify only 3 results returned.
4. **Missing file** — stream with nonexistent path returns error.
5. **Empty file** — stream completes immediately, no events.
6. **File metadata** — verify `Metadata["file"]` contains the filename.

---

### Section 4: Config Validation Fixes

**File:** `internal/config/config.go`

#### 4a: Change default connector to empty string

```go
// Before (line 66):
Provider: getenv("LUMBER_CONNECTOR", "vercel"),

// After:
Provider: getenv("LUMBER_CONNECTOR", ""),
```

An empty provider signals "not configured" and triggers the wizard.

#### 4b: Skip API key validation for local connectors

```go
// Before (line 178):
if c.Connector.Provider != "" && c.Connector.APIKey == "" {
    errs = append(errs, "LUMBER_API_KEY is required when a connector is configured")
}

// After:
localConnectors := map[string]bool{"stdin": true, "file": true, "": true}
if c.Connector.Provider != "" && c.Connector.APIKey == "" && !localConnectors[c.Connector.Provider] {
    errs = append(errs, "LUMBER_API_KEY is required for cloud connectors")
}
```

#### 4c: Add file connector validation

Add to `Validate()`:
```go
if c.Connector.Provider == "file" {
    filePath := c.Connector.Extra["file"]
    if filePath == "" {
        errs = append(errs, "file path is required for file connector (-file flag or LUMBER_FILE_PATH)")
    } else if _, err := os.Stat(filePath); os.IsNotExist(err) {
        errs = append(errs, fmt.Sprintf("log file not found: %s", filePath))
    }
}
```

#### 4d: Add `-file` CLI flag and `LUMBER_FILE_PATH` env var

In `LoadWithFlags()`, add a new flag:
```go
fileInput := flag.String("file", "", "Log file path (for file connector)")
```

In the `flag.Visit` block:
```go
case "file":
    if cfg.Connector.Extra == nil {
        cfg.Connector.Extra = make(map[string]string)
    }
    cfg.Connector.Extra["file"] = *fileInput
```

In `Load()`, add to `loadConnectorExtra()`:
```go
{"LUMBER_FILE_PATH", "file"},
```

#### 4e: Update config tests

**File:** `internal/config/config_test.go`

New tests:
1. **Stdin connector skips API key validation** — provider=stdin, no API key → validation passes.
2. **File connector requires file path** — provider=file, no file path → validation error.
3. **File connector validates file exists** — provider=file, file path to nonexistent file → validation error.
4. **File connector valid** — provider=file, file path to existing temp file → validation passes.
5. **Cloud connector still requires API key** — provider=vercel, no API key → validation error (unchanged behavior).

---

### Section 5: Wizard Implementation

**New file:** `internal/cli/wizard.go`

The wizard is a function that takes the current (incomplete) `Config` and returns a fully populated `Config` by prompting the user interactively.

```go
package cli

// RunWizard displays an interactive setup wizard and returns a populated Config.
// It should only be called when stdin is a TTY and no connector is configured.
func RunWizard(base config.Config) (config.Config, error)
```

#### Step 1: Welcome + Source Selection

```go
huh.NewSelect[string]().
    Title("How do you want to ingest logs?").
    Options(
        huh.NewOption("Local logs (file or pipe)", "local"),
        huh.NewOption("Cloud provider (live)", "cloud"),
    ).
    Value(&source)
```

#### Step 2a: Local Path — Sub-selection

If `source == "local"`:

```go
huh.NewSelect[string]().
    Title("Log source:").
    Options(
        huh.NewOption("Read a file", "file"),
        huh.NewOption("Pipe from stdin  (e.g. cat app.log | lumber)", "stdin"),
    ).
    Value(&localSource)
```

If `"file"` → prompt for file path:

```go
huh.NewInput().
    Title("File path:").
    Placeholder("/var/log/app.log").
    Validate(func(s string) error {
        if _, err := os.Stat(s); os.IsNotExist(err) {
            return fmt.Errorf("file not found: %s", s)
        }
        return nil
    }).
    Value(&filePath)
```

If `"stdin"` → set connector to `"stdin"`, skip further source prompts. Print a note: "Waiting for piped input... (usage: cat app.log | lumber)"

#### Step 2b: Cloud Path — Provider + Credentials

If `source == "cloud"`:

```go
huh.NewSelect[string]().
    Title("Provider:").
    Options(
        huh.NewOption("Vercel", "vercel"),
        huh.NewOption("Fly.io", "flyio"),
        huh.NewOption("Supabase", "supabase"),
    ).
    Value(&provider)
```

API key prompt (masked):

```go
huh.NewInput().
    Title("API key:").
    EchoMode(huh.EchoModePassword).
    Validate(func(s string) error {
        if strings.TrimSpace(s) == "" {
            return fmt.Errorf("API key is required")
        }
        return nil
    }).
    Value(&apiKey)
```

Provider-specific prompts (conditional):

**Vercel:**
```go
huh.NewInput().Title("Project ID (optional):").Placeholder("prj_xxxxx").Value(&projectID)
huh.NewInput().Title("Team ID (optional):").Placeholder("team_xxxxx").Value(&teamID)
```

**Fly.io:**
```go
huh.NewInput().Title("App name:").Validate(notEmpty).Value(&appName)
```

**Supabase:**
```go
huh.NewInput().Title("Project ref:").Validate(notEmpty).Value(&projectRef)
```

#### Step 3: Shared Options

Both local and cloud paths converge here:

```go
huh.NewSelect[string]().
    Title("Output verbosity:").
    Options(
        huh.NewOption("Standard (balanced)", "standard"),
        huh.NewOption("Minimal (most compact)", "minimal"),
        huh.NewOption("Full (everything)", "full"),
    ).
    Value(&verbosity)
```

Mode selection (cloud only — local defaults to stream):
```go
huh.NewSelect[string]().
    Title("Mode:").
    Options(
        huh.NewOption("Stream (live tail)", "stream"),
        huh.NewOption("Query (historical)", "query"),
    ).
    Value(&mode)
```

If query mode, prompt for time range:
```go
huh.NewInput().Title("From (RFC3339):").Placeholder("2026-01-01T00:00:00Z").Value(&from)
huh.NewInput().Title("To (RFC3339):").Placeholder("2026-01-01T01:00:00Z").Value(&to)
```

#### Step 4: Build Config

Map all wizard responses into the `base` Config struct and return it. The caller (`main.go`) proceeds with the normal validation + pipeline startup.

```go
cfg.Connector.Provider = provider
cfg.Connector.APIKey = apiKey
cfg.Engine.Verbosity = verbosity
cfg.Mode = mode
// ... etc
return cfg, nil
```

#### Grouping prompts into forms

Use `huh.NewForm()` to group related prompts into multi-field forms where it makes sense. Each form is one "screen" in the wizard. This keeps the interaction tight rather than one-question-at-a-time:

- **Form 1:** Source selection (local vs cloud)
- **Form 2:** Source details (file path, or provider + API key + extras)
- **Form 3:** Options (verbosity, mode, query range)

Each form runs as `form.Run()` — the user sees all fields in the group, fills them, confirms.

---

### Section 6: Wizard Integration in main.go

**File:** `cmd/lumber/main.go`

#### 6a: Detect "unconfigured" state

After `cfg := config.LoadWithFlags()` and before validation, check whether the wizard should run:

```go
if cfg.Connector.Provider == "" && !cfg.ShowVersion {
    if !isTerminal(os.Stdin) {
        // Not a TTY — can't run interactive wizard.
        // Check if stdin has data (piped input) — auto-detect stdin connector.
        if stdinHasData() {
            cfg.Connector.Provider = "stdin"
        } else {
            fmt.Fprintf(os.Stderr, "No connector configured. Run interactively for setup wizard, or use:\n")
            fmt.Fprintf(os.Stderr, "  lumber -connector stdin       (pipe logs via stdin)\n")
            fmt.Fprintf(os.Stderr, "  lumber -connector file -file PATH\n")
            fmt.Fprintf(os.Stderr, "  lumber -connector vercel      (+ LUMBER_API_KEY)\n")
            os.Exit(1)
        }
    } else {
        // TTY — run interactive wizard.
        var err error
        cfg, err = cli.RunWizard(cfg)
        if err != nil {
            slog.Error("wizard failed", "error", err)
            os.Exit(1)
        }
    }
}
```

**TTY detection:**
```go
func isTerminal(f *os.File) bool {
    stat, _ := f.Stat()
    return (stat.Mode() & os.ModeCharDevice) != 0
}
```

**Stdin data detection** (for auto-detecting piped input):
```go
func stdinHasData() bool {
    stat, _ := os.Stdin.Stat()
    return (stat.Mode() & os.ModeCharDevice) == 0
}
```

This enables the convenient pipe-without-flags pattern:
```bash
cat app.log | lumber    # auto-detects stdin, no -connector flag needed
```

#### 6b: Import new connector packages

Add blank imports for the new connectors:
```go
_ "github.com/kaminocorp/lumber/internal/connector/stdin"
_ "github.com/kaminocorp/lumber/internal/connector/file"
```

#### 6c: Print startup banner

Before the wizard (or before pipeline start if wizard is skipped), print a minimal banner:

```go
fmt.Fprintf(os.Stderr, "\n  lumber %s\n\n", config.Version)
```

Using `os.Stderr` so it doesn't mix with NDJSON output on stdout.

---

### Section 7: Welcome Header & Styled Output

**New file:** `internal/cli/style.go`

Define reusable styles for the wizard using `lipgloss`:

```go
package cli

import "github.com/charmbracelet/lipgloss"

var (
    titleStyle   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("99"))
    successStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("42"))
    mutedStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
)
```

The wizard header:
```go
func printHeader(version string) {
    fmt.Fprintf(os.Stderr, "\n  %s\n\n", titleStyle.Render("lumber "+version))
    fmt.Fprintf(os.Stderr, "  %s\n\n", mutedStyle.Render("No connector configured. Let's set up."))
}
```

Post-wizard confirmation:
```go
func printReady(provider, mode string) {
    fmt.Fprintf(os.Stderr, "\n  %s %s → %s\n\n",
        successStyle.Render("✓"),
        provider,
        mode,
    )
}
```

---

### Section 8: Tests

**New file:** `internal/cli/wizard_test.go`

The `huh` library supports programmatic input for testing via `huh.NewForm().WithProgramInput(reader)`. This lets us simulate user selections without a real terminal.

Tests:
1. **Local file path** — simulate selecting local → file → provide path → standard verbosity. Verify resulting config has correct connector, file path, and verbosity.
2. **Local stdin path** — simulate selecting local → stdin. Verify connector is `"stdin"`.
3. **Cloud Vercel path** — simulate cloud → vercel → API key → project ID → standard. Verify all fields populated.
4. **Cloud Fly.io path** — simulate cloud → flyio → API key → app name. Verify `Extra["app_name"]` set.
5. **Cloud Supabase path** — simulate cloud → supabase → API key → project ref. Verify `Extra["project_ref"]` set.
6. **File validation** — simulate file path to nonexistent file, verify validation error triggers re-prompt.

**Integration test in `cmd/lumber/`:**

A test that verifies the auto-detect logic:
- `isTerminal` returns false for a pipe
- `stdinHasData` returns true for a pipe with data
- The combination correctly sets `connector.Provider = "stdin"`

---

### Section 9: Update Flag Usage Text

**File:** `internal/config/config.go`

Update `flag.Usage` to reflect the new connectors and wizard:

```go
flag.Usage = func() {
    fmt.Fprintf(os.Stderr, `lumber %s — log normalization pipeline

Usage:
  lumber                                Interactive setup wizard
  lumber -connector stdin               Classify piped logs
  lumber -connector file -file PATH     Classify a log file
  lumber -connector vercel              Stream from Vercel (requires LUMBER_API_KEY)
  cat app.log | lumber                  Auto-detect piped input

Flags:
`, Version)
    flag.PrintDefaults()
    fmt.Fprintf(os.Stderr, `
Environment variables:
  LUMBER_CONNECTOR      Log provider (vercel, flyio, supabase, stdin, file)
  LUMBER_API_KEY        Provider API key/token (cloud connectors only)
  LUMBER_FILE_PATH      Log file path (file connector)
  LUMBER_VERBOSITY      Output verbosity (minimal, standard, full)
  LUMBER_DEDUP_WINDOW   Dedup window duration (e.g. 5s, 0 to disable)
  LUMBER_LOG_LEVEL      Internal log level (debug, info, warn, error)

  See README for full configuration reference.
`)
}
```

---

### Section 10: Version Bump & Docs

**File:** `internal/config/config.go`

```go
const Version = "0.9.0"
```

**File:** `docs/changelog.md`

New entry at top documenting:
- Interactive setup wizard on first run
- stdin connector (pipe any logs through Lumber)
- file connector (classify a local log file)
- Auto-detection of piped stdin input
- Config validation fix: local connectors no longer require API key
- Default connector changed from `"vercel"` to empty (triggers wizard)
- New dependency: `charmbracelet/huh`

**File:** `README.md`

Update quick-start section to show the wizard flow and pipe usage.

---

## File Summary

| File | Action | Section |
|------|--------|---------|
| `go.mod` / `go.sum` | modified | 1 |
| `internal/connector/stdin/stdin.go` | **new** | 2 |
| `internal/connector/stdin/stdin_test.go` | **new** | 2 |
| `internal/connector/file/file.go` | **new** | 3 |
| `internal/connector/file/file_test.go` | **new** | 3 |
| `internal/config/config.go` | modified | 4, 9 |
| `internal/config/config_test.go` | modified | 4 |
| `internal/cli/wizard.go` | **new** | 5 |
| `internal/cli/style.go` | **new** | 7 |
| `internal/cli/wizard_test.go` | **new** | 8 |
| `cmd/lumber/main.go` | modified | 6 |
| `docs/changelog.md` | modified | 10 |
| `README.md` | modified | 10 |

**New files: 7. Modified files: 6. Total: 13.**

---

## Verification Checklist

After implementation, verify:

- [ ] `go build ./...` — compiles cleanly
- [ ] `go test ./...` — all tests pass
- [ ] `lumber` with no config → wizard launches (TTY)
- [ ] `lumber` with no config → helpful error message (non-TTY, no pipe)
- [ ] `cat testfile.log | lumber` → auto-detects stdin, classifies lines
- [ ] `lumber -connector stdin < testfile.log` → same behavior
- [ ] `lumber -connector file -file testfile.log` → reads file, classifies
- [ ] `lumber -connector vercel` without API key → validation error
- [ ] `lumber -connector stdin` → no API key error
- [ ] `lumber --version` → prints version, no wizard
- [ ] `lumber --help` → updated usage text
- [ ] Wizard: local → file → valid path → starts pipeline
- [ ] Wizard: local → stdin → prompts for piped input
- [ ] Wizard: cloud → vercel → API key → starts pipeline
