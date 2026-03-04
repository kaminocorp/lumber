# Connector Blueprint

## Overview

Lumber's connector layer is the ingestion surface of the pipeline. It abstracts away every difference between log providers — auth mechanisms, pagination schemes, response formats, timestamp encodings, rate limits — and produces a uniform stream of `model.RawLog` entries that feed directly into the classification engine.

Three connectors ship with Lumber v0.4: Vercel, Fly.io, and Supabase. Each implements the same two-method interface (`Stream` + `Query`), uses the same shared HTTP client for auth and retry, and registers itself at import time via Go's `init()` mechanism. Adding a new provider means implementing one interface and adding one blank import.

---

## Interface

All connectors implement a single interface defined in `internal/connector/connector.go`:

```go
type Connector interface {
    Stream(ctx context.Context, cfg ConnectorConfig) (<-chan model.RawLog, error)
    Query(ctx context.Context, cfg ConnectorConfig, params QueryParams) ([]model.RawLog, error)
}
```

- **`Stream`** — opens a long-lived connection (poll-based for all three current providers) and sends raw logs as they arrive via a channel. The goroutine exits and closes the channel when the context is cancelled.
- **`Query`** — fetches a bounded batch of historical logs matching the given time range and optional limit. Returns a slice.

Both methods receive a `ConnectorConfig` carrying the provider name, API key, endpoint override, and an `Extra map[string]string` for provider-specific settings (project IDs, app names, table lists, poll interval).

```go
type ConnectorConfig struct {
    Provider string
    APIKey   string
    Endpoint string
    Extra    map[string]string
}

type QueryParams struct {
    Start  time.Time
    End    time.Time
    Limit  int
    Filter string
}
```

---

## Registry

Connectors register themselves via `init()` functions that call `connector.Register(name, constructor)`. The registry is a package-level `map[string]Constructor` in `internal/connector/registry.go`.

```go
type Constructor func() Connector

func Register(name string, ctor Constructor)
func Get(name string) (Constructor, error)
func Providers() []string
```

Registration happens at import time. `cmd/lumber/main.go` uses blank imports to trigger registration:

```go
_ "github.com/kaminocorp/lumber/internal/connector/flyio"
_ "github.com/kaminocorp/lumber/internal/connector/supabase"
_ "github.com/kaminocorp/lumber/internal/connector/vercel"
```

At startup, `connector.Get(cfg.Connector.Provider)` retrieves the constructor, and `ctor()` creates the connector instance. This pattern means the main package never imports provider-specific types — connectors are fully self-registering.

---

## Shared HTTP Client

All three connectors share an HTTP client (`internal/connector/httpclient/httpclient.go`) that handles the common concerns: Bearer auth, JSON response parsing, retry with backoff, and rate limit handling.

### API

```go
func New(baseURL, token string, opts ...Option) *Client
func (c *Client) GetJSON(ctx context.Context, path string, query url.Values, dest any) error
```

`GetJSON` is the single method all connectors use. It builds the full URL from `baseURL + path + query`, sets `Authorization: Bearer {token}`, sends a GET request, and unmarshals the JSON response into `dest`.

### Retry logic

Three categories of response:

| Status | Behavior |
|--------|----------|
| 2xx | Success — unmarshal and return |
| 429 | Rate limited — read `Retry-After` header (seconds), sleep, retry |
| 5xx | Server error — exponential backoff (1s, 2s, 4s), retry |
| Other non-2xx | Client error — return `*APIError` immediately (no retry) |

Maximum 3 retries. Retry sleep is context-aware: a `time.NewTimer` + `select` on `ctx.Done()` ensures prompt cancellation during a retry wait. If all retries are exhausted, the last `*APIError` is returned.

### Error type

```go
type APIError struct {
    StatusCode int
    Body       string // first 512 bytes of response body
}
```

Body is truncated to 512 bytes to prevent large error pages from polluting log output. The `retryAfter` field is unexported and used internally only for 429 retry scheduling.

### Configuration

| Option | Default | Effect |
|--------|---------|--------|
| `WithTimeout(d)` | 30s | Sets `http.Client.Timeout` |

### Dependencies

Zero external dependencies. Uses stdlib only: `net/http`, `encoding/json`, `net/url`, `io`, `context`, `time`, `strconv`.

---

## Output Type

Every connector produces `model.RawLog` entries (defined in `internal/model/rawlog.go`):

```go
type RawLog struct {
    Timestamp time.Time
    Source    string         // provider name: "vercel", "flyio", "supabase"
    Raw       string         // original log text
    Metadata  map[string]any // provider-specific metadata
}
```

The `Source` field identifies the provider. `Raw` is the log message text that the classification engine will embed. `Metadata` carries provider-specific fields (level, instance, region, table name, proxy info) that are preserved but not classified — they're structural context for downstream consumers.

---

## Connectors

### Vercel

**Provider name:** `"vercel"`
**API:** Vercel REST API (`GET /v1/projects/{projectId}/logs`)
**Package:** `internal/connector/vercel/`

#### API shape

Vercel returns JSON with cursor-based pagination:

```json
{
    "data": [{"id": "...", "message": "...", "timestamp": 1700000000000, "source": "lambda", "level": "info", "proxy": {...}}],
    "pagination": {"next": "cursor_value_or_empty"}
}
```

Timestamps are **Unix milliseconds**. The `proxy` object (optional) contains HTTP-level metadata: `statusCode`, `path`, `method`, `host`.

#### Mapping to RawLog

`toRawLog(entry)` converts a Vercel log entry:

- `Timestamp` — `time.UnixMilli(entry.Timestamp)` (millisecond precision)
- `Source` — `"vercel"`
- `Raw` — `entry.Message` (the log text for classification)
- `Metadata` — always includes `level`, `source`, `id`. When `proxy` is non-nil, also includes `status_code`, `path`, `method`, `host`.

#### Query

1. Extract `project_id` from `cfg.Extra`. Error if missing.
2. Optionally extract `team_id` for team-scoped projects.
3. Build query params: `from`/`to` as Unix milliseconds from `params.Start`/`params.End`.
4. Pagination loop: follow `pagination.Next` cursor until empty or `params.Limit` reached.

Time filtering is **server-side** — Vercel supports `from` and `to` query parameters.

#### Stream

Poll-based with the shared pattern:
1. Immediate first poll (no wait for the first tick).
2. Ticker-based loop at `poll_interval` (default 5s).
3. Cursor tracks position across polls — the Vercel pagination cursor persists between requests.
4. API errors logged via `slog.Warn`, don't crash the stream.
5. Buffered channel (64), closed on context cancellation.
6. Sends are guarded by `select` on `ctx.Done()` to prevent goroutine leaks when the channel is full and context is cancelled.

#### Required config

| Extra key | Env var | Required |
|-----------|---------|----------|
| `project_id` | `LUMBER_VERCEL_PROJECT_ID` | Yes |
| `team_id` | `LUMBER_VERCEL_TEAM_ID` | No |
| `poll_interval` | `LUMBER_POLL_INTERVAL` | No (default 5s) |

---

### Fly.io

**Provider name:** `"flyio"`
**API:** Fly.io HTTP logs API (`GET /api/v1/apps/{app_name}/logs`)
**Package:** `internal/connector/flyio/`

#### API shape

Fly.io returns JSON with a nested `data[].attributes` structure:

```json
{
    "data": [{"id": "...", "type": "log", "attributes": {"timestamp": "2026-02-23T10:30:00.000Z", "message": "...", "level": "info", "instance": "148ed726c12358", "region": "ord", "meta": {}}}],
    "meta": {"next_token": "cursor_value_or_empty"}
}
```

Timestamps are **RFC 3339** (ISO 8601). The `meta` object on each log entry is a provider-specific bag of key-value pairs that gets merged into `RawLog.Metadata`.

#### Mapping to RawLog

`toRawLog(wrapper)` converts a Fly.io log wrapper:

- `Timestamp` — `time.Parse(time.RFC3339Nano, attributes.Timestamp)` (nanosecond-capable)
- `Source` — `"flyio"`
- `Raw` — `attributes.Message`
- `Metadata` — includes `level`, `instance`, `region`, `id`, plus all entries from `attributes.Meta` merged in

#### Query

1. Extract `app_name` from `cfg.Extra`. Error if missing.
2. Pagination loop with `next_token` cursor until empty or limit reached.
3. **Client-side time filter** — Fly.io has no server-side time range parameters. After converting each entry to `RawLog`, entries outside `[Start, End)` are skipped. The interval is half-open: `Start` inclusive, `End` exclusive — preventing overlap when querying consecutive time windows.

This is the key architectural difference from Vercel: Fly.io returns all available logs regardless of time range, and the connector filters locally.

#### Stream

Same poll-loop pattern as Vercel. Identical structure: immediate first poll, ticker loop, cursor persistence, error logging without crash, buffered channel.

#### Required config

| Extra key | Env var | Required |
|-----------|---------|----------|
| `app_name` | `LUMBER_FLY_APP_NAME` | Yes |
| `poll_interval` | `LUMBER_POLL_INTERVAL` | No (default 5s) |

---

### Supabase

**Provider name:** `"supabase"`
**API:** Supabase Management API analytics endpoint (`GET /v1/projects/{ref}/analytics/endpoints/logs.all`)
**Package:** `internal/connector/supabase/`

The most complex of the three. Supabase uses SQL-based queries, microsecond timestamps, multiple log tables with varying schemas, and a 24-hour maximum query window.

#### API shape

Queries are sent as SQL in a query parameter:

```
GET /v1/projects/{ref}/analytics/endpoints/logs.all?sql=SELECT...&iso_timestamp_start=...&iso_timestamp_end=...
```

Response uses `[]map[string]any` because the schema varies per table:

```json
{
    "result": [{"id": "uuid", "timestamp": 1700000000000000, "event_message": "POST /rest/v1/users 200"}]
}
```

Timestamps are **Unix microseconds** (not milliseconds). Auth uses a personal access token (PAT), not a service role key.

#### SQL builder

`buildSQL(table, fromMicros, toMicros)` generates:

```sql
SELECT id, timestamp, event_message FROM {table} WHERE timestamp >= {from} AND timestamp < {to} ORDER BY timestamp ASC LIMIT 1000
```

The table name is validated against an **allow-list** of all 7 known Supabase log tables before interpolation. This is the defense against SQL injection — arbitrary table names are rejected with an error.

```go
var allowedTables = map[string]bool{
    "edge_logs": true, "postgres_logs": true, "auth_logs": true, "function_logs": true,
    "storage_logs": true, "function_edge_logs": true, "realtime_logs": true,
}
```

#### Log tables

| Table | Category | Default |
|-------|----------|---------|
| `edge_logs` | API gateway | Yes |
| `postgres_logs` | Database | Yes |
| `auth_logs` | Authentication | Yes |
| `function_logs` | Edge Function console output | Yes |
| `storage_logs` | Object storage | No (opt-in) |
| `function_edge_logs` | Edge Function edge logs | No (opt-in) |
| `realtime_logs` | Realtime subscriptions | No (opt-in) |

Default 4 tables are queried unless overridden by the `tables` config key (comma-separated).

#### Mapping to RawLog

`toRawLog(row, table)` converts a result row:

- `Timestamp` — extract `row["timestamp"]` as `float64`, convert microseconds to `time.Unix(sec, rem*1000)` (nanosecond precision from microsecond source)
- `Source` — `"supabase"`
- `Raw` — `row["event_message"]`
- `Metadata` — `{"table": table}` plus all other row fields **except** `event_message` (excluded to avoid duplicating `Raw`)

#### Query

1. Extract `project_ref` from `cfg.Extra`. Error if missing.
2. Parse `tables` from `cfg.Extra["tables"]` (comma-separated). Default to 4 standard tables.
3. Default time range: last 1 hour if both `Start` and `End` are zero.
4. **24-hour window chunking** — Supabase enforces a 24-hour maximum per API call. Ranges exceeding 24 hours are split into consecutive 24-hour chunks, each queried independently.
5. For each chunk, for each table: build SQL, call `GetJSON`, collect rows.
6. **Sort and merge** — results from all tables and chunks are merged and sorted by timestamp.
7. Apply `params.Limit` if set.

#### Stream

Different from Vercel/Fly.io due to Supabase's multi-table nature:

1. Default poll interval **10s** (not 5s) — with 4 default tables queried per poll, 10s = 24 req/min, well within the 120 req/min rate limit.
2. Cursor is a **timestamp** (`lastMicros`), not a pagination token. Initialized to `now - 1 minute`.
3. Each poll queries all configured tables from `lastMicros + 1` to `now`.
4. `lastMicros` advances to the maximum timestamp seen across all results.
5. **Per-table error isolation** — one failing table (e.g., opt-in table not enabled) doesn't block the others. Errors are logged via `slog.Warn`, and the loop continues to the next table.

#### Required config

| Extra key | Env var | Required |
|-----------|---------|----------|
| `project_ref` | `LUMBER_SUPABASE_PROJECT_REF` | Yes |
| `tables` | `LUMBER_SUPABASE_TABLES` | No (default: 4 standard tables) |
| `poll_interval` | `LUMBER_POLL_INTERVAL` | No (default 10s) |

---

## Shared Streaming Pattern

All three connectors follow the same poll-loop pattern for `Stream()`:

```
1. Validate config (error if required keys missing)
2. Create httpclient.Client with base URL + API key
3. Parse poll_interval from cfg.Extra (provider-specific default)
4. Create buffered channel: make(chan model.RawLog, 64)
5. Launch goroutine:
   a. defer close(ch)
   b. Immediate first poll (don't wait for first tick)
   c. Create time.Ticker
   d. Loop: select on ctx.Done() or ticker.C
   e. On tick: poll API, send entries to channel, update cursor
   f. On ctx.Done(): return (deferred close fires)
6. Return channel, nil
```

Key properties:
- **Immediate first poll** — logs appear immediately after `Stream()` returns, no initial delay.
- **Non-fatal errors** — API failures are logged via `slog.Warn` and the cursor is preserved. The next poll retries from the same position.
- **Context-aware sends** — every `ch <- log` is wrapped in a `select` with `ctx.Done()` to prevent goroutine leaks when the channel buffer is full and context is cancelled.
- **Channel buffer of 64** — provides headroom for bursty log output without blocking the poll goroutine. Not a backpressure mechanism (Phase 5 concern).

---

## Config Wiring

Provider-specific env vars are loaded into `ConnectorConfig.Extra` by `loadConnectorExtra()` in `internal/config/config.go`:

| Env Var | Extra Key | Used By |
|---------|-----------|---------|
| `LUMBER_VERCEL_PROJECT_ID` | `project_id` | Vercel |
| `LUMBER_VERCEL_TEAM_ID` | `team_id` | Vercel |
| `LUMBER_FLY_APP_NAME` | `app_name` | Fly.io |
| `LUMBER_SUPABASE_PROJECT_REF` | `project_ref` | Supabase |
| `LUMBER_SUPABASE_TABLES` | `tables` | Supabase |
| `LUMBER_POLL_INTERVAL` | `poll_interval` | All connectors |

**Flat shared map.** All providers share the same `Extra` map. Key names are unique across providers, so there are no conflicts. `poll_interval` is intentionally shared — it makes sense to configure polling frequency globally rather than per-provider.

**Lazy allocation.** `loadConnectorExtra()` returns `nil` when no provider-specific env vars are set. The map is only allocated when the first non-empty env var is found. Connectors handle `nil` Extra gracefully — map lookups on nil maps return zero values in Go.

---

## Timestamp Handling

Each provider encodes timestamps differently. The connector layer normalizes all of them to `time.Time`:

| Provider | Wire format | Precision | Conversion |
|----------|-------------|-----------|------------|
| Vercel | `int64` Unix milliseconds | Millisecond | `time.UnixMilli(ts)` |
| Fly.io | `string` RFC 3339 | Nanosecond-capable | `time.Parse(time.RFC3339Nano, ts)` |
| Supabase | `float64` Unix microseconds | Microsecond | `time.Unix(sec, rem*1000)` via int64 conversion |

All three converge on a `time.Time` value in `RawLog.Timestamp`. The engine never needs to know how the timestamp was originally encoded.

---

## Test Strategy

All connector tests use `httptest.NewServer` — no live API keys required. Each test creates a local HTTP server that returns fixture responses matching the real provider's response shape.

| Package | Tests | Coverage |
|---------|-------|----------|
| `httpclient` | 8 | Auth, query params, retries, rate limits, context cancellation, max retries |
| `vercel` | 8 | Mapping, pagination, missing config, API errors, streaming, context cancel |
| `flyio` | 7 | Mapping, pagination, client-side time filter, streaming, context cancel |
| `supabase` | 11 | SQL builder, injection prevention, multi-table, window chunking, default/custom tables, streaming |
| `config` | 4 | Defaults, extra population, empty omission, multi-provider |
| **Total** | **38** | |

Key test patterns:
- **Mapping tests** (`TestToRawLog`) — verify timestamp conversion, metadata population, field extraction
- **Pagination tests** — multi-page responses with cursors, verify all pages collected
- **Config validation** — missing required keys produce descriptive errors
- **Stream tests** — verify logs arrive on channel, context cancellation closes channel promptly
- **Injection prevention** (Supabase) — table names not in allow-list are rejected

---

## File Layout

```
internal/connector/
├── connector.go                    Connector interface, ConnectorConfig, QueryParams
├── registry.go                     Register/Get/Providers — self-registration pattern
├── httpclient/
│   ├── httpclient.go               Shared HTTP client: Bearer auth, JSON, retry, rate limits
│   └── httpclient_test.go          8 tests
├── vercel/
│   ├── vercel.go                   Vercel connector: cursor pagination, poll streaming
│   └── vercel_test.go              8 tests
├── flyio/
│   ├── flyio.go                    Fly.io connector: client-side time filter, poll streaming
│   └── flyio_test.go               7 tests
└── supabase/
    ├── supabase.go                 Supabase connector: SQL queries, window chunking, multi-table
    └── supabase_test.go            11 tests

internal/config/
├── config.go                       loadConnectorExtra(), Extra map wiring
└── config_test.go                  4 tests

cmd/lumber/
└── main.go                         Blank imports for all three connectors
```

---

## Key Constants

| Constant | Value | Location |
|----------|-------|----------|
| Vercel default endpoint | `https://api.vercel.com` | `vercel.go` |
| Vercel default poll interval | 5s | `vercel.go` |
| Fly.io default endpoint | `https://api.fly.io` | `flyio.go` |
| Fly.io default poll interval | 5s | `flyio.go` |
| Supabase default endpoint | `https://api.supabase.com` | `supabase.go` |
| Supabase default poll interval | 10s | `supabase.go` |
| Supabase max query window | 24 hours | `supabase.go` |
| Supabase default tables | 4 (edge, postgres, auth, function) | `supabase.go` |
| Supabase allowed tables | 7 (4 default + 3 opt-in) | `supabase.go` |
| HTTP client default timeout | 30s | `httpclient.go` |
| HTTP client max retries | 3 | `httpclient.go` |
| HTTP client backoff schedule | 1s, 2s, 4s | `httpclient.go` |
| API error body truncation | 512 bytes | `httpclient.go` |
| Stream channel buffer size | 64 | All connectors |
| SQL result limit | 1000 rows per query | `supabase.go` |

---

## Design Decisions

**Single `GetJSON` method on the shared HTTP client.** All three provider APIs use GET requests with JSON responses. The shared client doesn't need POST/PUT/DELETE — if a future connector needs them, they can be added without breaking existing connectors. Starting with the minimum viable surface keeps the abstraction honest.

**Poll-based streaming over push-based.** All three connectors use poll-loop streaming rather than WebSockets, SSE, or NATS. This is intentional for v0.4: polling is simpler to implement, test, and debug. Each connector is ~200 lines. Push-based protocols (Fly.io's NATS, webhook receivers) can be added later behind the same `Stream()` interface.

**Immediate first poll.** Every `Stream()` goroutine polls once before entering the ticker loop. Without this, there's a silent delay equal to the poll interval before any logs appear — confusing for users who expect immediate output.

**Client-side time filter for Fly.io.** The Fly.io HTTP logs API has no server-side time range parameters. The connector fetches all available pages and filters locally using a half-open interval `[Start, End)`. This means Fly.io `Query()` may fetch more data than needed, but the overhead is acceptable for typical log volumes and it avoids creating a fake time-filter abstraction.

**Allow-list for Supabase SQL table names.** Table names are interpolated directly into SQL strings. The allow-list is a hard-coded `map[string]bool` of all 7 known Supabase log tables — the only defense against SQL injection, and a deliberate one. No user-supplied string can appear as a table name unless it matches one of the 7 entries.

**Per-table error isolation in Supabase streaming.** When polling multiple tables, one failing table (e.g., an opt-in table the user hasn't enabled) doesn't crash the entire stream. The error is logged and the loop continues to the next table. This is critical for usability — users shouldn't need to enumerate exactly which tables are enabled.

**10s default poll for Supabase (vs. 5s for Vercel/Fly.io).** With 4 default tables, each poll generates 4 API requests. At 10s intervals, that's 24 req/min — well within Supabase's 120 req/min rate limit. A 5s interval would hit 48 req/min, still safe but less conservative.

**Flat `Extra` map shared across all providers.** Key names are unique across providers (`project_id` for Vercel, `app_name` for Fly.io, `project_ref` for Supabase). A single flat map is simpler than per-provider nested maps, and `poll_interval` is intentionally shared. If key collisions ever arise, the fix is to prefix keys, but the current set has no conflicts.

**Metadata excludes the raw message.** Supabase's `toRawLog` explicitly skips `event_message` when building the metadata map — it's already in `RawLog.Raw`. This prevents duplication and keeps the metadata map clean for downstream consumers.

**`slog.Warn` for poll errors, not `fmt.Fprintf(os.Stderr, ...)`.** Stream poll errors use Go 1.21's `slog` structured logging, which integrates with Lumber's own logging infrastructure (level filtering, JSON output when stdout is in use). This was a v0.4.1 refinement from the original `log.Printf` approach.

---

## Known Limitations

These are explicitly deferred to Phase 5 (Pipeline Integration) and beyond:

1. **No buffering or backpressure** — stream channels are fixed at 64 entries with no overflow handling. If the consumer is slower than the producer, the producer blocks on channel sends.
2. **No graceful drain on shutdown** — context cancellation closes channels immediately. In-flight logs between the connector and engine may be lost.
3. **No per-log error isolation** — a malformed API response surfaces as an error on the entire `Query()` call, rather than skipping individual bad entries.
4. **Env-var-only config** — connector selection and provider-specific settings are only configurable via environment variables. No CLI flags.
5. **No deduplication across polls** — if the cursor doesn't advance (e.g., API returns the same data), duplicate logs will be processed. The engine's dedup layer catches some of this, but it's not connector-aware.
6. **All tests use `httptest` fixtures** — no live API validation. Real-world response format drift could cause silent failures. Live validation is deferred to Phase 6.
7. **Polling only** — no push-based ingestion (log drains, NATS, webhooks). Polling works but adds latency equal to the poll interval.

---

## Dependencies

The connector layer adds zero external dependencies to the Go module. Everything is stdlib:

- `net/http` — HTTP requests
- `encoding/json` — JSON marshalling/unmarshalling
- `net/url` — URL and query string construction
- `io` — response body reading
- `context` — cancellation propagation
- `time` — timestamps, durations, tickers, timers
- `strconv` — integer/float string conversion
- `sort` — result sorting (Supabase multi-table merge)
- `strings` — table list parsing
- `log/slog` — structured error logging in poll loops
