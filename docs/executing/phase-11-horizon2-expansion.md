# Phase 11: Horizon 2 — Capability Expansion

**Goal:** Expand Lumber's reach with new connectors, multi-provider fan-in, configuration files, and push-based ingestion — all building on the existing pipeline architecture without changing the core classification engine. After this phase: Lumber connects to 8+ log sources (including AWS CloudWatch, Datadog, Grafana Loki, and a generic webhook receiver), handles multiple providers concurrently, and supports YAML config files for complex deployments.

**Starting point:** v0.9.0. The CLI wizard (Phase 10) is in place with stdin and file connectors. Three cloud connectors exist (Vercel, Fly.io, Supabase) following a well-established pattern: `init()` registration, shared `httpclient`, httptest fixtures. The pipeline is single-connector — `main.go` resolves one connector and wires it into one pipeline.

**Dependencies on Phase 10:** stdin + file connectors and config validation fixes must be complete. The wizard is extended (not rebuilt) as new connectors are added.

**Reference:** `docs/plans/post-beta-proposals.md` sections 2.2–2.5.

---

## Overview

Phase 11 is split into four sub-phases, each independently shippable:

```
Phase 11a: Additional Connectors       (3 new cloud + 1 generic receiver)
Phase 11b: Multi-Provider Fan-In       (concurrent ingestion from N connectors)
Phase 11c: Configuration Files         (lumber.yaml support)
Phase 11d: Push-Based Ingestion        (webhook receiver + Fly.io NATS)
```

**Recommended order:** 11a → 11b → 11c → 11d. Each builds on the previous but can be deferred independently.

---

## Phase 11a: Additional Connectors

### What and Why

Three cloud connectors from the vision doc remain unbuilt: AWS CloudWatch, Datadog, Grafana Loki. Plus a generic webhook receiver that inverts the ingestion model (Lumber listens, providers push). Each follows the established connector pattern.

### Section 1: Grafana Loki Connector

**New file:** `internal/connector/loki/loki.go`

**Why first:** Standard REST API + LogQL. No new SDK dependencies. Well-documented. Lowest complexity of the three cloud connectors.

**API details:**
- **Stream:** `GET /loki/api/v1/tail?query={query}&start={start}` — WebSocket-based live tail. Alternatively, poll `GET /loki/api/v1/query_range` at intervals (same pattern as Vercel/Fly.io).
- **Query:** `GET /loki/api/v1/query_range?query={query}&start={start}&end={end}&limit={limit}` — returns log entries in JSON.
- **Auth:** `Authorization: Bearer <token>` header or basic auth, depending on Loki deployment.

**Config:**
```
LUMBER_CONNECTOR=loki
LUMBER_API_KEY=<bearer token or basic auth>
LUMBER_ENDPOINT=https://loki.example.com  (required — no default, Loki is self-hosted)
LUMBER_LOKI_QUERY={app="myservice"}       (LogQL selector, defaults to {})
```

New env var in `loadConnectorExtra()`:
```go
{"LUMBER_LOKI_QUERY", "query"},
```

**Stream implementation (poll-based first):**
```go
func (c *Connector) Stream(ctx context.Context, cfg connector.ConnectorConfig) (<-chan model.RawLog, error) {
    // Poll /query_range with a sliding window, same pattern as vercel/flyio.
    // Each poll: start = last_seen_timestamp, end = now, limit = 1000.
    // Dedup by Loki's built-in stream+timestamp identifier.
}
```

**Response parsing:**
Loki's `/query_range` returns:
```json
{
  "data": {
    "result": [{
      "stream": {"app": "myservice", "level": "error"},
      "values": [["1708300800000000000", "log line text"], ...]
    }]
  }
}
```

Each `values` entry is `[timestamp_ns_string, log_line]`. Map to `RawLog`:
- `Timestamp`: parse nanosecond unix timestamp
- `Source`: `"loki"`
- `Raw`: the log line text
- `Metadata`: the `stream` labels map

**Test file:** `internal/connector/loki/loki_test.go`

Tests using `httptest.Server`:
1. **Stream polls and returns logs** — mock server returns 2 log entries, verify 2 `RawLog` values on channel.
2. **Query with time range** — verify correct `start`/`end` query params sent to server.
3. **Query with limit** — verify `limit` param forwarded.
4. **Auth header** — verify `Authorization: Bearer <key>` sent on requests.
5. **Pagination** — mock server returns entries across 2 polls, verify all entries collected.
6. **Empty response** — server returns empty result set, no error, no logs emitted.
7. **Endpoint required** — no endpoint configured → error with helpful message.

**Estimated size:** ~200 lines implementation + ~200 lines tests.

---

### Section 2: AWS CloudWatch Connector

**New file:** `internal/connector/cloudwatch/cloudwatch.go`

**Why:** Most-requested cloud provider. Requires the AWS SDK as a new dependency.

**New dependency:** `github.com/aws/aws-sdk-go-v2` (core + `config` + `cloudwatchlogs` service package).

```bash
go get github.com/aws/aws-sdk-go-v2
go get github.com/aws/aws-sdk-go-v2/config
go get github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs
```

**Auth:** AWS SDK's default credential chain — env vars (`AWS_ACCESS_KEY_ID` + `AWS_SECRET_ACCESS_KEY`), shared credentials file, IAM role, etc. Lumber's `LUMBER_API_KEY` is **not used** for CloudWatch. Instead, we rely on the AWS SDK's built-in credential resolution.

**Config:**
```
LUMBER_CONNECTOR=cloudwatch
LUMBER_CLOUDWATCH_LOG_GROUP=/aws/lambda/my-function   (required)
LUMBER_CLOUDWATCH_LOG_STREAM=2026/03/01/[$LATEST]     (optional, filters to specific stream)
LUMBER_CLOUDWATCH_REGION=us-east-1                     (optional, SDK default chain)
```

New env vars in `loadConnectorExtra()`:
```go
{"LUMBER_CLOUDWATCH_LOG_GROUP", "log_group"},
{"LUMBER_CLOUDWATCH_LOG_STREAM", "log_stream"},
{"LUMBER_CLOUDWATCH_REGION", "region"},
```

**Stream implementation:**
```go
func (c *Connector) Stream(ctx context.Context, cfg connector.ConnectorConfig) (<-chan model.RawLog, error) {
    // Use FilterLogEvents with polling.
    // Track last seen timestamp + eventId for dedup.
    // Poll interval: cfg.Extra["poll_interval"] or default 5s.
}
```

**Query implementation:**
```go
func (c *Connector) Query(ctx context.Context, cfg connector.ConnectorConfig, params connector.QueryParams) ([]model.RawLog, error) {
    // FilterLogEvents with startTime/endTime from params.
    // Handle pagination via nextToken.
    // Respect params.Limit.
}
```

**Response mapping:**
Each `FilteredLogEvent` maps to:
- `Timestamp`: `event.Timestamp` (unix ms)
- `Source`: `"cloudwatch"`
- `Raw`: `event.Message`
- `Metadata`: `{"log_group": "...", "log_stream": "...", "event_id": "..."}`

**Config validation:** Add to `Validate()`:
```go
if c.Connector.Provider == "cloudwatch" {
    if c.Connector.Extra["log_group"] == "" {
        errs = append(errs, "LUMBER_CLOUDWATCH_LOG_GROUP is required for CloudWatch connector")
    }
}
```

**API key exception:** CloudWatch uses AWS SDK credentials, not `LUMBER_API_KEY`. Add `"cloudwatch"` to the local connectors set in the API key validation check (Section 4b of Phase 10):
```go
noApiKeyRequired := map[string]bool{"stdin": true, "file": true, "": true, "cloudwatch": true}
```

**Test file:** `internal/connector/cloudwatch/cloudwatch_test.go`

CloudWatch tests use a mock implementation of the CloudWatch Logs client interface (not httptest, since the AWS SDK handles HTTP internally):

```go
type mockCWClient struct {
    filterOutput *cloudwatchlogs.FilterLogEventsOutput
    err          error
}
```

Tests:
1. **Stream returns log events** — mock client returns 3 events, verify 3 `RawLog` values.
2. **Query with time range** — verify `StartTime`/`EndTime` set correctly on API call.
3. **Query pagination** — mock returns `nextToken` on first call, more events on second.
4. **Query limit** — verify `Limit` param forwarded.
5. **Log group required** — no log group → error.
6. **Log stream filter** — verify `LogStreamNames` filter applied when configured.

**Estimated size:** ~250 lines implementation + ~200 lines tests.

---

### Section 3: Datadog Connector

**New file:** `internal/connector/datadog/datadog.go`

**API details:**
- **Query:** `POST https://api.datadoghq.com/api/v2/logs/events/search` — JSON body with filter query, time range, cursor-based pagination.
- **Auth:** Two headers: `DD-API-KEY` and `DD-APPLICATION-KEY`.
- **Stream:** Poll the search endpoint with a sliding time window (same pattern as other connectors).

**Config:**
```
LUMBER_CONNECTOR=datadog
LUMBER_API_KEY=<dd-api-key>
LUMBER_DATADOG_APP_KEY=<dd-application-key>
LUMBER_ENDPOINT=https://api.datadoghq.com       (default, can change for EU: api.datadoghq.eu)
LUMBER_DATADOG_QUERY=service:my-app status:error (Datadog log query syntax)
```

New env vars in `loadConnectorExtra()`:
```go
{"LUMBER_DATADOG_APP_KEY", "app_key"},
{"LUMBER_DATADOG_QUERY", "query"},
```

**Auth note:** Datadog requires two keys. `LUMBER_API_KEY` maps to `DD-API-KEY`. The app key comes via `Extra["app_key"]` and maps to `DD-APPLICATION-KEY`. Both sent as headers on every request.

**Request/response:**
```go
// Request body
type searchRequest struct {
    Filter  searchFilter  `json:"filter"`
    Page    searchPage    `json:"page,omitempty"`
    Sort    string        `json:"sort"` // "timestamp" for ascending
}

type searchFilter struct {
    Query string `json:"query"`
    From  string `json:"from"` // RFC3339 or relative ("now-1h")
    To    string `json:"to"`
}

type searchPage struct {
    Cursor string `json:"cursor,omitempty"`
    Limit  int    `json:"limit,omitempty"` // max 1000
}
```

**Response mapping:**
Each log event maps to:
- `Timestamp`: `event.Attributes.Timestamp` (ISO 8601)
- `Source`: `"datadog"`
- `Raw`: `event.Attributes.Message`
- `Metadata`: `{"service": "...", "status": "...", "host": "..."}`

**Test file:** `internal/connector/datadog/datadog_test.go`

Tests using `httptest.Server`:
1. **Query returns log events** — mock search response with 2 events.
2. **Cursor pagination** — first response has cursor, second call uses it.
3. **Auth headers** — verify both `DD-API-KEY` and `DD-APPLICATION-KEY` headers sent.
4. **Custom endpoint** — verify EU endpoint URL used when configured.
5. **Query filter** — verify query string forwarded in request body.
6. **Stream polls** — verify repeated calls with advancing time window.
7. **App key required** — no app key → error.

**Config validation:** Add to `Validate()`:
```go
if c.Connector.Provider == "datadog" && c.Connector.Extra["app_key"] == "" {
    errs = append(errs, "LUMBER_DATADOG_APP_KEY is required for Datadog connector")
}
```

**Estimated size:** ~250 lines implementation + ~250 lines tests.

---

### Section 4: Generic Webhook Receiver

**New file:** `internal/connector/receiver/receiver.go`

**What:** Lumber starts an HTTP server and listens for POSTed log data. This inverts the connector model — instead of Lumber pulling logs from a provider, the provider (or any system) pushes logs to Lumber.

**Why:** Works with Vercel log drains, custom pipelines, any system that can POST JSON. Eliminates polling latency. Enables integration with systems that don't have a pull API.

**Config:**
```
LUMBER_CONNECTOR=receiver
LUMBER_RECEIVER_ADDR=:8080                (listen address, default :8080)
LUMBER_RECEIVER_PATH=/logs                (endpoint path, default /logs)
LUMBER_RECEIVER_AUTH_TOKEN=secret123      (optional bearer token for auth)
```

New env vars in `loadConnectorExtra()`:
```go
{"LUMBER_RECEIVER_ADDR", "addr"},
{"LUMBER_RECEIVER_PATH", "path"},
{"LUMBER_RECEIVER_AUTH_TOKEN", "auth_token"},
```

**HTTP server:**
```go
func (c *Connector) Stream(ctx context.Context, cfg connector.ConnectorConfig) (<-chan model.RawLog, error) {
    ch := make(chan model.RawLog, 1024)

    mux := http.NewServeMux()
    mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
        // Validate auth token if configured.
        // Parse request body — support multiple formats:
        //   1. JSON array of strings (["line1", "line2"])
        //   2. JSON array of objects with "message" field
        //   3. NDJSON (one JSON object per line)
        //   4. Plain text (one log per line)
        // Send each log to ch.
        // Respond 200 OK (or 202 Accepted).
    })

    server := &http.Server{Addr: addr, Handler: mux}

    // Start server in goroutine.
    go func() {
        if err := server.ListenAndServe(); err != http.ErrServerClosed {
            slog.Error("receiver server error", "error", err)
        }
    }()

    // Shutdown on context cancellation.
    go func() {
        <-ctx.Done()
        server.Shutdown(context.Background())
        close(ch)
    }()

    return ch, nil
}
```

**Content-Type detection:**
```go
switch {
case strings.Contains(contentType, "application/json"):
    // Try JSON array, then NDJSON
case strings.Contains(contentType, "text/plain"):
    // One log per line
default:
    // Try JSON, fall back to plain text
}
```

**Query mode:** Returns error — webhook receiver is inherently streaming.

**Auth middleware:**
```go
if authToken != "" && r.Header.Get("Authorization") != "Bearer "+authToken {
    http.Error(w, "unauthorized", http.StatusUnauthorized)
    return
}
```

**Test file:** `internal/connector/receiver/receiver_test.go`

Tests:
1. **JSON array of strings** — POST `["line1", "line2"]`, verify 2 `RawLog` entries.
2. **JSON array of objects** — POST `[{"message": "line1", "timestamp": "..."}]`, verify correct mapping.
3. **NDJSON** — POST multiple JSON lines, verify each parsed.
4. **Plain text** — POST text with newlines, one `RawLog` per line.
5. **Auth token required** — configured token, request without it → 401.
6. **Auth token valid** — configured token, request with it → 200.
7. **No auth** — no token configured → all requests accepted.
8. **Context cancellation** — cancel context, verify server shuts down and channel closes.
9. **Concurrent POSTs** — 10 concurrent requests, all logs arrive on channel.
10. **Query returns error** — `Query()` returns unsupported error.

**Estimated size:** ~200 lines implementation + ~250 lines tests.

---

### Section 5: Update Wizard with New Connectors

**File:** `internal/cli/wizard.go`

Add new providers to the cloud selection:

```go
huh.NewSelect[string]().
    Title("Provider:").
    Options(
        huh.NewOption("Vercel", "vercel"),
        huh.NewOption("Fly.io", "flyio"),
        huh.NewOption("Supabase", "supabase"),
        huh.NewOption("AWS CloudWatch", "cloudwatch"),
        huh.NewOption("Datadog", "datadog"),
        huh.NewOption("Grafana Loki", "loki"),
    ).
    Value(&provider)
```

Add a third ingestion mode for the receiver:

```go
huh.NewSelect[string]().
    Title("How do you want to ingest logs?").
    Options(
        huh.NewOption("Local logs (file or pipe)", "local"),
        huh.NewOption("Cloud provider (pull)", "cloud"),
        huh.NewOption("Webhook receiver (push — Lumber listens for POSTed logs)", "receiver"),
    ).
    Value(&source)
```

Provider-specific prompts:

**CloudWatch:**
```go
huh.NewInput().Title("Log group:").Placeholder("/aws/lambda/my-function").Validate(notEmpty).Value(&logGroup)
huh.NewInput().Title("Log stream (optional):").Value(&logStream)
huh.NewInput().Title("Region (optional, uses AWS default chain):").Placeholder("us-east-1").Value(&region)
```
Note: No API key prompt — CloudWatch uses AWS credential chain.

**Datadog:**
```go
huh.NewInput().Title("API key:").EchoMode(huh.EchoModePassword).Validate(notEmpty).Value(&apiKey)
huh.NewInput().Title("Application key:").EchoMode(huh.EchoModePassword).Validate(notEmpty).Value(&appKey)
huh.NewInput().Title("Query (optional):").Placeholder("service:myapp").Value(&query)
huh.NewSelect[string]().Title("Region:").Options(
    huh.NewOption("US (datadoghq.com)", "https://api.datadoghq.com"),
    huh.NewOption("EU (datadoghq.eu)", "https://api.datadoghq.eu"),
).Value(&endpoint)
```

**Loki:**
```go
huh.NewInput().Title("Loki endpoint:").Placeholder("https://loki.example.com").Validate(notEmpty).Value(&endpoint)
huh.NewInput().Title("Bearer token (optional):").EchoMode(huh.EchoModePassword).Value(&apiKey)
huh.NewInput().Title("LogQL query (optional):").Placeholder(`{app="myservice"}`).Value(&query)
```

**Receiver:**
```go
huh.NewInput().Title("Listen address:").Placeholder(":8080").Value(&addr)
huh.NewInput().Title("Endpoint path:").Placeholder("/logs").Value(&path)
huh.NewInput().Title("Auth token (optional):").EchoMode(huh.EchoModePassword).Value(&authToken)
```

---

### Section 6: Update Registration and Imports

**File:** `cmd/lumber/main.go`

Add blank imports:
```go
_ "github.com/kaminocorp/lumber/internal/connector/loki"
_ "github.com/kaminocorp/lumber/internal/connector/cloudwatch"
_ "github.com/kaminocorp/lumber/internal/connector/datadog"
_ "github.com/kaminocorp/lumber/internal/connector/receiver"
```

**File:** `internal/config/config.go`

Update `flag.Usage` to list all connectors:
```
LUMBER_CONNECTOR      Log provider (vercel, flyio, supabase, cloudwatch, datadog, loki, receiver, stdin, file)
```

---

### Phase 11a File Summary

| File | Action |
|------|--------|
| `go.mod` / `go.sum` | modified (AWS SDK) |
| `internal/connector/loki/loki.go` | **new** |
| `internal/connector/loki/loki_test.go` | **new** |
| `internal/connector/cloudwatch/cloudwatch.go` | **new** |
| `internal/connector/cloudwatch/cloudwatch_test.go` | **new** |
| `internal/connector/datadog/datadog.go` | **new** |
| `internal/connector/datadog/datadog_test.go` | **new** |
| `internal/connector/receiver/receiver.go` | **new** |
| `internal/connector/receiver/receiver_test.go` | **new** |
| `internal/cli/wizard.go` | modified |
| `internal/config/config.go` | modified |
| `internal/config/config_test.go` | modified |
| `cmd/lumber/main.go` | modified |

**New files: 8. Modified files: 5. Total: 13.**

---

## Phase 11b: Multi-Provider Fan-In

### What and Why

The pipeline currently wires one connector. Real deployments need multiple sources: Vercel frontend logs + Supabase database logs + CloudWatch Lambda logs, all flowing through the same Lumber instance, classified by the same taxonomy, output to the same destinations.

The `Connector` interface already returns `<-chan model.RawLog` with a `Source` field on each log — the pipeline is source-agnostic downstream. The work is in launching multiple connectors and merging their channels.

### Section 1: Fan-In Channel Merger

**New file:** `internal/connector/fanin/fanin.go`

A utility that takes N `<-chan model.RawLog` channels and merges them into a single output channel. Uses the standard Go fan-in pattern with a WaitGroup.

```go
package fanin

// Merge combines multiple RawLog channels into one.
// The output channel closes when all input channels are closed.
func Merge(channels ...<-chan model.RawLog) <-chan model.RawLog {
    out := make(chan model.RawLog, 256)
    var wg sync.WaitGroup

    for _, ch := range channels {
        wg.Add(1)
        go func(c <-chan model.RawLog) {
            defer wg.Done()
            for log := range c {
                out <- log
            }
        }(ch)
    }

    go func() {
        wg.Wait()
        close(out)
    }()

    return out
}
```

**Test file:** `internal/connector/fanin/fanin_test.go`

Tests:
1. **Two channels merge** — 2 channels with 3 logs each → 6 logs on output.
2. **Channels close independently** — first channel closes early, second continues.
3. **All channels close → output closes** — verify output channel is closed after all inputs close.
4. **Empty channel** — one of N channels is empty, others work normally.
5. **Single channel passthrough** — Merge with 1 channel behaves as identity.
6. **Concurrent writes** — 5 channels writing simultaneously, all logs arrive.

---

### Section 2: Multi-Connector Config

**File:** `internal/config/config.go`

Replace single `ConnectorConfig` with a slice for multi-connector support, while maintaining backward compatibility:

```go
type Config struct {
    Connector  ConnectorConfig   // primary connector (backward-compatible)
    Connectors []ConnectorConfig // additional connectors for fan-in
    // ... rest unchanged
}
```

**Design choice:** Keep the single `Connector` field for the simple case (one provider). The `Connectors` slice is only populated when multi-provider is configured. This avoids breaking existing config loading.

**New CLI flag:**
```go
addConnector := flag.String("add-connector", "", "Additional connector (can be repeated): provider:key@endpoint")
```

**New env var:**
```
LUMBER_CONNECTORS=vercel:token1,supabase:token2   (comma-separated provider:key pairs)
```

**Parsing `LUMBER_CONNECTORS`:**
```go
func parseConnectors(s string) []ConnectorConfig {
    var configs []ConnectorConfig
    for _, entry := range strings.Split(s, ",") {
        parts := strings.SplitN(entry, ":", 2)
        cfg := ConnectorConfig{Provider: parts[0]}
        if len(parts) > 1 {
            cfg.APIKey = parts[1]
        }
        configs = append(configs, cfg)
    }
    return configs
}
```

**Validation:** Each connector in `Connectors` is validated independently (same rules as the primary connector).

---

### Section 3: Pipeline Fan-In Wiring

**File:** `cmd/lumber/main.go`

After resolving all connectors, launch each `Stream()` in its own goroutine, collect the channels, merge them:

```go
// Resolve all connectors.
var connectors []connector.Connector
primary, err := resolveConnector(cfg.Connector)
connectors = append(connectors, primary)

for _, cc := range cfg.Connectors {
    c, err := resolveConnector(cc)
    connectors = append(connectors, c)
}

// In stream mode: launch all, merge channels.
switch cfg.Mode {
case "stream":
    var channels []<-chan model.RawLog
    for i, conn := range connectors {
        ch, err := conn.Stream(ctx, connConfigs[i])
        channels = append(channels, ch)
    }
    merged := fanin.Merge(channels...)
    // Feed merged channel to pipeline.
```

**Important:** The `Pipeline` struct currently takes a single `connector.Connector` and calls `Stream()`/`Query()` internally. For fan-in, we need to either:

**Option A:** Change `Pipeline` to accept a pre-started `<-chan model.RawLog` instead of a `Connector` for stream mode. This decouples channel creation from the pipeline.

**Option B:** Create a `FanInConnector` that wraps N connectors and merges their streams internally, implementing the `Connector` interface.

**Recommendation: Option B.** It preserves the `Pipeline` API and keeps fan-in logic encapsulated:

```go
// internal/connector/fanin/connector.go

type FanInConnector struct {
    connectors []connector.Connector
    configs    []connector.ConnectorConfig
}

func (f *FanInConnector) Stream(ctx context.Context, _ connector.ConnectorConfig) (<-chan model.RawLog, error) {
    var channels []<-chan model.RawLog
    for i, conn := range f.connectors {
        ch, err := conn.Stream(ctx, f.configs[i])
        if err != nil {
            return nil, fmt.Errorf("connector %s: %w", f.configs[i].Provider, err)
        }
        channels = append(channels, ch)
        slog.Info("connector started", "provider", f.configs[i].Provider)
    }
    return Merge(channels...), nil
}

func (f *FanInConnector) Query(ctx context.Context, _ connector.ConnectorConfig, params connector.QueryParams) ([]model.RawLog, error) {
    // Query all connectors, combine results, sort by timestamp.
    var all []model.RawLog
    for i, conn := range f.connectors {
        results, err := conn.Query(ctx, f.configs[i], params)
        if err != nil {
            slog.Warn("connector query failed", "provider", f.configs[i].Provider, "error", err)
            continue // partial failure — log warning but continue with other connectors
        }
        all = append(all, results...)
    }
    sort.Slice(all, func(i, j int) bool {
        return all[i].Timestamp.Before(all[j].Timestamp)
    })
    if params.Limit > 0 && len(all) > params.Limit {
        all = all[:params.Limit]
    }
    return all, nil
}
```

**main.go changes:**
```go
if len(cfg.Connectors) > 0 {
    // Multi-connector: build FanInConnector.
    allConfigs := append([]config.ConnectorConfig{cfg.Connector}, cfg.Connectors...)
    conn = fanin.NewConnector(resolvedConnectors, resolvedConfigs)
} else {
    // Single connector (existing behavior).
    conn = resolveConnector(cfg.Connector)
}
// Pipeline construction unchanged — p := pipeline.New(conn, eng, out, pipeOpts...)
```

---

### Section 4: Wizard Multi-Connector Support

**File:** `internal/cli/wizard.go`

After the initial connector setup, add a "Add another connector?" loop:

```go
for {
    var addMore bool
    huh.NewConfirm().
        Title("Add another log source?").
        Value(&addMore).
        Run()

    if !addMore {
        break
    }
    // Run provider selection + config prompts again.
    // Append to cfg.Connectors.
}
```

---

### Section 5: Tests

**File:** `internal/connector/fanin/fanin_test.go` (extended from Section 1)

Additional tests for `FanInConnector`:
1. **Stream merges two connectors** — two mock connectors, each emitting 3 logs → 6 logs total.
2. **Stream partial failure** — one connector returns error, other starts successfully. Stream returns error (fail-fast on stream — all connectors must start).
3. **Query combines results** — two connectors return 3 results each → 6 results sorted by timestamp.
4. **Query partial failure** — one connector query fails → results from other returned with warning logged.
5. **Query limit** — combined 10 results, limit 5 → 5 results.
6. **Single connector passthrough** — FanInConnector with 1 connector behaves identically to bare connector.

---

### Phase 11b File Summary

| File | Action |
|------|--------|
| `internal/connector/fanin/fanin.go` | **new** |
| `internal/connector/fanin/connector.go` | **new** |
| `internal/connector/fanin/fanin_test.go` | **new** |
| `internal/config/config.go` | modified |
| `internal/config/config_test.go` | modified |
| `internal/cli/wizard.go` | modified |
| `cmd/lumber/main.go` | modified |

**New files: 3. Modified files: 4. Total: 7.**

---

## Phase 11c: Configuration Files

### What and Why

Env vars + CLI flags work for simple setups but become unwieldy with multiple connectors, provider-specific settings, output destinations, and custom options. A `lumber.yaml` config file makes complex deployments manageable, versionable, and shareable.

### Section 1: YAML Config Parser

**New dependency:** `gopkg.in/yaml.v3`

```bash
go get gopkg.in/yaml.v3
```

**New file:** `internal/config/file.go`

```go
package config

// FileConfig represents the structure of lumber.yaml.
type FileConfig struct {
    Mode       string             `yaml:"mode"`
    Verbosity  string             `yaml:"verbosity"`
    LogLevel   string             `yaml:"log_level"`
    Confidence float64            `yaml:"confidence_threshold"`
    Dedup      string             `yaml:"dedup_window"`
    Connectors []FileConnector    `yaml:"connectors"`
    Output     FileOutput         `yaml:"output"`
    Model      FileModel          `yaml:"model"`
}

type FileConnector struct {
    Provider   string            `yaml:"provider"`
    APIKey     string            `yaml:"api_key"`
    Endpoint   string            `yaml:"endpoint"`
    Properties map[string]string `yaml:"properties"` // provider-specific: project_id, app_name, etc.
}

type FileOutput struct {
    Stdout  *FileStdout  `yaml:"stdout"`
    File    *FileFile    `yaml:"file"`
    Webhook *FileWebhook `yaml:"webhook"`
}

type FileStdout struct {
    Pretty bool `yaml:"pretty"`
}

type FileFile struct {
    Path    string `yaml:"path"`
    MaxSize string `yaml:"max_size"` // human-readable: "100MB", "1GB"
}

type FileWebhook struct {
    URL     string            `yaml:"url"`
    Headers map[string]string `yaml:"headers"`
}

type FileModel struct {
    Dir        string `yaml:"dir"`         // shorthand: directory containing all 3 files
    Model      string `yaml:"model"`       // explicit model path
    Vocab      string `yaml:"vocab"`       // explicit vocab path
    Projection string `yaml:"projection"`  // explicit projection path
}
```

**Loading:**
```go
func LoadFile(path string) (FileConfig, error) {
    data, err := os.ReadFile(path)
    if err != nil {
        return FileConfig{}, err
    }

    // Expand ${ENV_VAR} references in the YAML before parsing.
    expanded := os.ExpandEnv(string(data))

    var fc FileConfig
    if err := yaml.Unmarshal([]byte(expanded), &fc); err != nil {
        return FileConfig{}, fmt.Errorf("parse %s: %w", path, err)
    }
    return fc, nil
}
```

**Env var interpolation:** `os.ExpandEnv()` handles `${VAR}` and `$VAR` syntax natively. This lets users keep secrets out of config files:
```yaml
connectors:
  - provider: vercel
    api_key: ${LUMBER_API_KEY}
```

---

### Section 2: Config Merge Logic

**File:** `internal/config/config.go`

**Precedence:** CLI flags > env vars > config file > defaults.

Update `LoadWithFlags()`:

```go
func LoadWithFlags() Config {
    cfg := Load()  // env vars + defaults

    // Load config file if it exists.
    configPath := getenv("LUMBER_CONFIG", "")
    flag.StringVar(&configPath, "config", configPath, "Path to config file (lumber.yaml)")

    // ... parse flags ...

    // Apply config file (between defaults and env/flags).
    if configPath != "" {
        fc, err := LoadFile(configPath)
        if err != nil {
            cfg.parseErrors = append(cfg.parseErrors, fmt.Sprintf("config file: %s", err))
        } else {
            cfg = mergeFileConfig(cfg, fc)
        }
    } else {
        // Auto-discover: look for lumber.yaml in current directory.
        if _, err := os.Stat("lumber.yaml"); err == nil {
            fc, err := LoadFile("lumber.yaml")
            if err == nil {
                cfg = mergeFileConfig(cfg, fc)
            }
        }
    }

    // Re-apply CLI flags (they take highest precedence).
    flag.Visit(func(f *flag.Flag) { /* ... existing overlay logic ... */ })

    return cfg
}
```

**mergeFileConfig():**
```go
func mergeFileConfig(base Config, fc FileConfig) Config {
    // Only override base values if the file config field is non-zero.
    if fc.Mode != "" { base.Mode = fc.Mode }
    if fc.Verbosity != "" { base.Engine.Verbosity = fc.Verbosity }
    // ... etc for each field ...

    // Connectors: first entry becomes primary, rest become additional.
    if len(fc.Connectors) > 0 {
        base.Connector = fileConnectorToConfig(fc.Connectors[0])
        for _, fc := range fc.Connectors[1:] {
            base.Connectors = append(base.Connectors, fileConnectorToConfig(fc))
        }
    }

    return base
}
```

---

### Section 3: Example Config File

**New file:** `lumber.example.yaml`

```yaml
# lumber.example.yaml — example configuration
# Copy to lumber.yaml and customize.

mode: stream
verbosity: standard
log_level: info
confidence_threshold: 0.5
dedup_window: 5s

# Model files (default: ./models/)
model:
  dir: ./models

# Log sources — first is primary, additional are merged via fan-in
connectors:
  - provider: vercel
    api_key: ${LUMBER_API_KEY}
    properties:
      project_id: prj_xxxxx
      team_id: team_xxxxx

# Uncomment to add more sources:
#  - provider: supabase
#    api_key: ${LUMBER_SUPABASE_KEY}
#    properties:
#      project_ref: abc123

# Output destinations — all active outputs receive every event
output:
  stdout:
    pretty: false
  # file:
  #   path: /var/log/lumber/events.jsonl
  #   max_size: 100MB
  # webhook:
  #   url: https://example.com/webhook
  #   headers:
  #     Authorization: "Bearer ${WEBHOOK_TOKEN}"
```

---

### Section 4: Tests

**New file:** `internal/config/file_test.go`

Tests:
1. **Parse valid YAML** — complete config file → correct `FileConfig` struct.
2. **Env var expansion** — YAML with `${TEST_VAR}` → expanded value.
3. **Merge precedence** — file sets verbosity, env var overrides it → env var wins.
4. **Multi-connector** — YAML with 2 connectors → primary + 1 additional.
5. **Auto-discover** — `lumber.yaml` in current dir → auto-loaded.
6. **Explicit path** — `-config /path/to/custom.yaml` → loaded.
7. **Invalid YAML** — malformed file → parse error in `parseErrors`.
8. **Missing file** — `-config nonexistent.yaml` → error.
9. **Empty file** — empty YAML → defaults preserved.
10. **Partial config** — YAML sets only mode → other fields remain as defaults.

---

### Phase 11c File Summary

| File | Action |
|------|--------|
| `go.mod` / `go.sum` | modified (yaml.v3) |
| `internal/config/file.go` | **new** |
| `internal/config/file_test.go` | **new** |
| `internal/config/config.go` | modified |
| `lumber.example.yaml` | **new** |

**New files: 3. Modified files: 2. Total: 5.**

---

## Phase 11d: Push-Based Ingestion

### What and Why

All cloud connectors are poll-based (request logs on an interval). Many providers support push delivery natively: Vercel log drains, Fly.io NATS streaming, generic webhook delivery. Push eliminates polling latency and API quota waste.

Phase 11d extends existing connectors with push-based variants where the provider supports it. The generic webhook receiver (built in Phase 11a, Section 4) already handles arbitrary push sources — this phase adds provider-native push protocols.

### Section 1: Vercel Log Drain Receiver

**File:** `internal/connector/vercel/logdrain.go`

Vercel log drains push logs to a configured HTTP endpoint. This is a specialization of the generic webhook receiver with Vercel-specific parsing and verification.

**How Vercel log drains work:**
1. User configures a log drain in Vercel dashboard → provides Lumber's URL
2. Vercel POSTs NDJSON payloads to that URL
3. Each line is a JSON object with Vercel's log schema

**Implementation:**
- Reuse the HTTP server pattern from the receiver connector
- Add Vercel-specific verification: check `x-vercel-signature` header (HMAC-SHA1 of body with integration secret)
- Parse Vercel's NDJSON format into `model.RawLog`

**Config:**
```
LUMBER_CONNECTOR=vercel-drain
LUMBER_RECEIVER_ADDR=:8080
LUMBER_VERCEL_DRAIN_SECRET=<integration secret for signature verification>
```

**Scope:** ~100 lines — most logic is shared with the generic receiver. The Vercel-specific parts are signature verification and response parsing.

---

### Section 2: Fly.io NATS Streaming

**File:** `internal/connector/flyio/nats.go`

Fly.io exposes a NATS-based live log tail. This is a true streaming protocol — no polling.

**New dependency:** `github.com/nats-io/nats.go`

```bash
go get github.com/nats-io/nats.go
```

**Config:**
```
LUMBER_CONNECTOR=flyio-nats
LUMBER_API_KEY=<fly auth token>
LUMBER_FLY_APP_NAME=my-app
LUMBER_FLY_NATS_URL=nats://...  (discovered from Fly API)
```

**Implementation:**
```go
func (c *NATSConnector) Stream(ctx context.Context, cfg connector.ConnectorConfig) (<-chan model.RawLog, error) {
    // 1. Discover NATS URL from Fly API (if not configured directly).
    // 2. Connect to NATS server with auth token.
    // 3. Subscribe to app's log subject.
    // 4. Forward messages to channel.
    // 5. Clean up on context cancellation.
}
```

**Registration:**
```go
func init() {
    connector.Register("flyio-nats", func() connector.Connector {
        return &NATSConnector{}
    })
}
```

**Note:** The existing `flyio` connector (poll-based) remains available. Users choose between `flyio` (poll) and `flyio-nats` (push) based on their preference and whether NATS access is available.

---

### Section 3: Wizard Updates

**File:** `internal/cli/wizard.go`

For providers that support both pull and push, add a sub-selection:

```go
// After selecting Vercel:
huh.NewSelect[string]().
    Title("Ingestion method:").
    Options(
        huh.NewOption("Poll REST API (standard)", "vercel"),
        huh.NewOption("Log drain (Lumber receives pushes)", "vercel-drain"),
    ).
    Value(&provider)

// After selecting Fly.io:
huh.NewSelect[string]().
    Title("Ingestion method:").
    Options(
        huh.NewOption("Poll HTTP API (standard)", "flyio"),
        huh.NewOption("NATS streaming (real-time push)", "flyio-nats"),
    ).
    Value(&provider)
```

---

### Section 4: Tests

Tests for each new variant follow the same patterns as existing connector tests:
- NATS connector: mock NATS server (nats-server test helper or `natstest` package)
- Vercel drain: httptest server POSTing to the receiver, verify signature validation

---

### Phase 11d File Summary

| File | Action |
|------|--------|
| `go.mod` / `go.sum` | modified (nats.go) |
| `internal/connector/vercel/logdrain.go` | **new** |
| `internal/connector/vercel/logdrain_test.go` | **new** |
| `internal/connector/flyio/nats.go` | **new** |
| `internal/connector/flyio/nats_test.go` | **new** |
| `internal/cli/wizard.go` | modified |

**New files: 4. Modified files: 2. Total: 6.**

---

## Full Phase 11 Summary

| Sub-Phase | Scope | New Files | Modified | New Deps |
|-----------|-------|-----------|----------|----------|
| **11a: Connectors** | Loki + CloudWatch + Datadog + Receiver | 8 | 5 | aws-sdk-go-v2 |
| **11b: Fan-In** | Channel merger + FanInConnector + multi-config | 3 | 4 | — |
| **11c: Config Files** | YAML parser + merge + auto-discover | 3 | 2 | yaml.v3 |
| **11d: Push Ingestion** | Vercel drain + Fly.io NATS | 4 | 2 | nats.go |
| **Total** | | **18** | **13** | **3 new deps** |

---

## Verification Checklist

### Phase 11a
- [ ] `go test ./internal/connector/loki/...` — all tests pass
- [ ] `go test ./internal/connector/cloudwatch/...` — all tests pass
- [ ] `go test ./internal/connector/datadog/...` — all tests pass
- [ ] `go test ./internal/connector/receiver/...` — all tests pass
- [ ] Wizard shows all new providers
- [ ] Each connector registers and can be selected via `-connector` flag

### Phase 11b
- [ ] `go test ./internal/connector/fanin/...` — all tests pass
- [ ] `LUMBER_CONNECTORS=vercel:tk1,supabase:tk2 lumber` starts both connectors
- [ ] Logs from both connectors arrive on stdout with correct `Source` field
- [ ] Query mode combines results from both connectors

### Phase 11c
- [ ] `go test ./internal/config/...` — all tests pass
- [ ] `lumber -config lumber.yaml` loads config from file
- [ ] `lumber.yaml` in current dir auto-discovered
- [ ] Env vars override file values
- [ ] CLI flags override both
- [ ] `${VAR}` syntax expanded in YAML

### Phase 11d
- [ ] `go test ./internal/connector/vercel/...` — logdrain tests pass
- [ ] `go test ./internal/connector/flyio/...` — NATS tests pass
- [ ] Vercel log drain receives and verifies signed payloads
- [ ] Fly.io NATS connector streams logs in real-time
