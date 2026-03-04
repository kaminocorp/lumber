# Phase 3: Log Connectors — Implementation Plan

## Goal

Real-world log ingestion from three providers: Vercel, Fly.io, and Supabase. Each connector implements the existing `connector.Connector` interface (`Stream` + `Query`), producing `model.RawLog` entries that feed directly into the classification engine.

Phase 2 validated the classification pipeline against synthetic data. Phase 3 connects it to production log sources — proving the pipeline works end-to-end with real, messy, provider-specific log formats.

**Success criteria:**
- Shared HTTP client with auth, retry, and rate limit handling — used by all three connectors
- Vercel connector: cursor-paginated `Query()` and poll-based `Stream()` against the REST logs API
- Fly.io connector: cursor-paginated `Query()` and poll-based `Stream()` against the undocumented (but stable) HTTP logs API
- Supabase connector: multi-table SQL-based `Query()` and timestamp-cursor `Stream()` against the Management API analytics endpoint
- All connectors tested with `httptest` fixtures (no live API keys required for tests)
- Config wiring: provider-specific env vars populate `ConnectorConfig.Extra` map
- `go build ./cmd/lumber` compiles with all three connectors registered

---

## Current State

**Working:**
- `connector.Connector` interface: `Stream(ctx, cfg) (<-chan RawLog, error)` and `Query(ctx, cfg, params) ([]RawLog, error)`
- `connector.ConnectorConfig` with `Provider`, `APIKey`, `Endpoint`, `Extra map[string]string`
- `connector.QueryParams` with `Start`, `End`, `Limit`, `Filter`
- Registry: `Register(name, constructor)`, `Get(name)`, `Providers()`
- Vercel stub: registered as `"vercel"`, returns `errNotImplemented`
- Pipeline: `Stream()` reads channel, calls `engine.Process()` per log; `Query()` calls `engine.ProcessBatch()`
- Config loads `LUMBER_CONNECTOR`, `LUMBER_API_KEY`, `LUMBER_ENDPOINT` but does not populate `Extra`

**Not yet built:**
- No HTTP client utilities (no `net/http` usage anywhere in the codebase)
- No real connector implementations
- Config doesn't load provider-specific env vars into `Extra`
- Only Vercel is blank-imported in `main.go`

---

## Provider API Reference

### Vercel

- **Endpoint:** `GET https://api.vercel.com/v1/projects/{projectId}/logs`
- **Auth:** `Authorization: Bearer {token}`
- **Pagination:** cursor via `?next=cursor_value`
- **Time filter:** `?from=unix_ms&to=unix_ms`
- **Team scoping:** `?teamId=...`
- **Response shape:**
  ```json
  {
    "data": [
      {
        "id": "log_abc123",
        "message": "GET /api/hello 200 in 12ms",
        "timestamp": 1700000000000,
        "source": "lambda",
        "level": "info",
        "proxy": {
          "statusCode": 200,
          "path": "/api/hello",
          "method": "GET",
          "host": "my-app.vercel.app"
        }
      }
    ],
    "pagination": { "next": "cursor_value_or_empty" }
  }
  ```
- **Timestamp:** Unix milliseconds
- **Rate limits:** Varies by plan, standard Vercel API limits apply

### Fly.io

- **Endpoint:** `GET https://api.fly.io/api/v1/apps/{app_name}/logs`
- **Auth:** `Authorization: Bearer {token}` (personal, org, or deploy token)
- **Pagination:** cursor via `?next_token=...`
- **Filters:** `?instance=...`, `?region=...` (no time-range filter server-side)
- **Response shape:**
  ```json
  {
    "data": [
      {
        "id": "log_id",
        "type": "log",
        "attributes": {
          "timestamp": "2026-02-23T10:30:00.000Z",
          "message": "Listening on 0.0.0.0:8080",
          "level": "info",
          "instance": "148ed726c12358",
          "region": "ord",
          "meta": {}
        }
      }
    ],
    "meta": {
      "next_token": "cursor_value_or_empty"
    }
  }
  ```
- **Timestamp:** RFC 3339 (ISO 8601)
- **Note:** This is the same API `flyctl logs` uses internally. Undocumented but stable.

### Supabase

- **Endpoint:** `GET https://api.supabase.com/v1/projects/{ref}/analytics/endpoints/logs.all`
- **Auth:** `Authorization: Bearer {personal_access_token}` (PAT, not service role key)
- **Query params:** `?sql=...&iso_timestamp_start=...&iso_timestamp_end=...`
- **SQL:** BigQuery-flavored, selects from specific log tables
- **Constraints:** 1000 row cap per query, 24-hour max time window, 120 req/min rate limit
- **Response shape:**
  ```json
  {
    "result": [
      {
        "id": "uuid",
        "timestamp": 1700000000000000,
        "event_message": "POST /rest/v1/users 200"
      }
    ]
  }
  ```
- **Timestamp:** Microseconds (not milliseconds)
- **Log tables:**
  - **Default 4:** `edge_logs` (API gateway), `postgres_logs` (database), `auth_logs` (authentication), `function_logs` (Edge Function console output)
  - **Opt-in 3:** `storage_logs`, `function_edge_logs`, `realtime_logs`
- **Note:** API is marked experimental. Schema varies per table, so response is parsed as `[]map[string]any`.

---

## Section 1: Shared HTTP Client

**What:** A reusable internal package providing authenticated, retry-aware HTTP requests with JSON response parsing. Foundation for all three connectors.

### Tasks

1.1 **Create `internal/connector/httpclient/httpclient.go`**

Public API:

```go
// Client is an HTTP client with Bearer auth, base URL, and retry logic.
type Client struct { ... }

// New creates a Client. Options: WithTimeout (default 30s).
func New(baseURL, token string, opts ...Option) *Client

// GetJSON sends a GET request and unmarshals the JSON response into dest.
// Returns *APIError for non-2xx responses. Retries on 429 and 5xx.
func (c *Client) GetJSON(ctx context.Context, path string, query url.Values, dest any) error

// APIError represents a non-2xx HTTP response.
type APIError struct {
    StatusCode int
    Body       string // first 512 bytes
}

// Option configures Client behavior.
type Option func(*Client)
func WithTimeout(d time.Duration) Option
```

Implementation details:
- `GetJSON` builds full URL from `baseURL + path + query`, sets `Authorization: Bearer {token}` header
- Reads response body, checks status code. Non-2xx: return `*APIError` (except 429/5xx which retry)
- Retry logic (`doWithRetry`): on 429, read `Retry-After` header (seconds); on 5xx, exponential backoff (1s, 2s, 4s). Max 3 retries. Context-aware sleep using `time.NewTimer` + `select` on `ctx.Done()`
- No external dependencies — stdlib `net/http`, `encoding/json`, `net/url`, `io`, `context`, `time`, `strconv`

1.2 **Create `internal/connector/httpclient/httpclient_test.go`**

All tests use `httptest.NewServer`:
- `TestGetJSON_Success` — 200 with valid JSON, correct unmarshalling
- `TestGetJSON_BearerAuth` — verify `Authorization: Bearer xxx` header reaches server
- `TestGetJSON_QueryParams` — verify query string constructed correctly
- `TestGetJSON_APIError` — 400 response returns `*APIError` with status and body
- `TestGetJSON_RateLimit_RetryAfter` — first call returns 429 with `Retry-After: 1`, second returns 200. Verify success after retry.
- `TestGetJSON_RetryOn5xx` — first call returns 503, second returns 200
- `TestGetJSON_ContextCancelled` — cancelled context returns promptly
- `TestGetJSON_MaxRetriesExceeded` — 429 on every call, verify error after 3 retries

### Files

| File | Action |
|------|--------|
| `internal/connector/httpclient/httpclient.go` | Create |
| `internal/connector/httpclient/httpclient_test.go` | Create |

### Verification

```
go test ./internal/connector/httpclient/...
```

All tests pass. No ONNX model required.

---

## Section 2: Vercel Connector

**What:** Replace the Vercel stub with a full implementation — response type parsing, `RawLog` mapping, cursor-paginated `Query()`, and poll-based `Stream()`.

### Tasks

2.1 **Define response types** (unexported, in `vercel.go`)

```go
type logsResponse struct {
    Data       []logEntry `json:"data"`
    Pagination pagination `json:"pagination"`
}

type logEntry struct {
    ID        string     `json:"id"`
    Message   string     `json:"message"`
    Timestamp int64      `json:"timestamp"` // unix milliseconds
    Source    string     `json:"source"`    // "build", "edge", "lambda", "static"
    Level    string     `json:"level"`     // "info", "warning", "error"
    Proxy    *proxyInfo `json:"proxy,omitempty"`
}

type proxyInfo struct {
    StatusCode int    `json:"statusCode"`
    Path       string `json:"path"`
    Method     string `json:"method"`
    Host       string `json:"host"`
}

type pagination struct {
    Next string `json:"next"`
}
```

2.2 **Implement `toRawLog(entry logEntry) model.RawLog`**

- `Timestamp`: `time.UnixMilli(entry.Timestamp)`
- `Source`: `"vercel"`
- `Raw`: `entry.Message`
- `Metadata`: `map[string]any{"level": entry.Level, "source": entry.Source, "id": entry.ID}`. If `entry.Proxy` is non-nil, also add `"status_code"`, `"path"`, `"method"`, `"host"`.

2.3 **Implement `Query()`**

1. Extract `project_id` from `cfg.Extra["project_id"]`. Return error if missing.
2. Optionally extract `team_id` from `cfg.Extra["team_id"]`.
3. Create `httpclient.Client` with `cfg.Endpoint` (or default `https://api.vercel.com`) and `cfg.APIKey`.
4. Build query params: `from` = `params.Start.UnixMilli()`, `to` = `params.End.UnixMilli()` (if non-zero). `teamId` if present.
5. Pagination loop: call `GetJSON` on `/v1/projects/{projectId}/logs`, append results, follow `pagination.Next` cursor until empty or `params.Limit` reached.
6. Convert all entries via `toRawLog`, return slice.

2.4 **Implement `Stream()`**

1. Same config extraction as `Query`.
2. Parse `poll_interval` from `cfg.Extra["poll_interval"]` (default 5s).
3. Create buffered channel `make(chan model.RawLog, 64)`.
4. Launch goroutine:
   - Maintain cursor (initially empty).
   - On each tick: call API with cursor, send new entries to channel, update cursor.
   - On context cancellation: close channel, return.
   - On API error: log to stderr, continue polling (don't crash).
5. Return channel.

2.5 **Write tests** (`internal/connector/vercel/vercel_test.go`)

- `TestToRawLog` — timestamp conversion, metadata fields, proxy fields when present and absent
- `TestQuery_Success` — `httptest` server returns fixture, verify RawLog slice
- `TestQuery_Pagination` — two pages (first has `pagination.next`, second has empty), verify both collected
- `TestQuery_MissingProjectID` — returns descriptive error
- `TestQuery_APIError` — server returns 401, error propagates
- `TestStream_ReceivesLogs` — serve fixture responses, verify logs arrive on channel
- `TestStream_ContextCancel` — cancel context, verify channel closes promptly

### Files

| File | Action |
|------|--------|
| `internal/connector/vercel/vercel.go` | Replace stub |
| `internal/connector/vercel/vercel_test.go` | Create |

### Verification

```
go test ./internal/connector/vercel/...
```

---

## Section 3: Fly.io Connector

**What:** New connector for Fly.io's HTTP logs API. Same structural pattern as Vercel — cursor pagination, poll-based streaming. Key difference: no server-side time filter, so `Query()` applies client-side filtering.

### Tasks

3.1 **Create `internal/connector/flyio/flyio.go`**

Register as `"flyio"` via `init()`.

3.2 **Define response types** (unexported)

```go
type logsResponse struct {
    Data []logWrapper `json:"data"`
    Meta meta         `json:"meta"`
}

type logWrapper struct {
    ID         string        `json:"id"`
    Type       string        `json:"type"`
    Attributes logAttributes `json:"attributes"`
}

type logAttributes struct {
    Timestamp string         `json:"timestamp"` // RFC 3339
    Message   string         `json:"message"`
    Level     string         `json:"level"`
    Instance  string         `json:"instance"`
    Region    string         `json:"region"`
    Meta      map[string]any `json:"meta"`
}

type meta struct {
    NextToken string `json:"next_token"`
}
```

3.3 **Implement `toRawLog(w logWrapper) model.RawLog`**

- `Timestamp`: parse `w.Attributes.Timestamp` via `time.Parse(time.RFC3339Nano, ...)`
- `Source`: `"flyio"`
- `Raw`: `w.Attributes.Message`
- `Metadata`: `map[string]any{"level", "instance", "region", "id"}` plus entries from `w.Attributes.Meta`

3.4 **Implement `Query()`**

1. Extract `app_name` from `cfg.Extra["app_name"]`. Error if missing.
2. Create `httpclient.Client` with base URL `https://api.fly.io` and `cfg.APIKey`.
3. Path: `/api/v1/apps/{app_name}/logs`.
4. Paginate with `next_token` query param until empty or `params.Limit` reached.
5. **Client-side time filter:** if `params.Start` or `params.End` are non-zero, skip entries outside the range. Fly.io has no server-side time filter.
6. Convert via `toRawLog`, return slice.

3.5 **Implement `Stream()`**

Same poll-loop pattern as Vercel:
- Cursor via `next_token`
- Default 5s poll interval, configurable via `cfg.Extra["poll_interval"]`
- Buffered channel (64), close on context cancellation
- Log API errors to stderr, don't crash

3.6 **Write tests** (`internal/connector/flyio/flyio_test.go`)

- `TestToRawLog` — RFC 3339 timestamp parsing, metadata population
- `TestQuery_Success` — fixture response, verify mapping
- `TestQuery_Pagination` — two pages via cursor
- `TestQuery_ClientSideTimeFilter` — entries outside `Start`/`End` excluded
- `TestQuery_MissingAppName` — descriptive error
- `TestStream_ReceivesLogs` — logs arrive on channel
- `TestStream_ContextCancel` — channel closes

### Files

| File | Action |
|------|--------|
| `internal/connector/flyio/flyio.go` | Create |
| `internal/connector/flyio/flyio_test.go` | Create |

### Verification

```
go test ./internal/connector/flyio/...
```

---

## Section 4: Supabase Connector

**What:** New connector for Supabase's Management API analytics endpoint. Most complex of the three — SQL-based querying across multiple log tables, microsecond timestamps, 24-hour window chunking, no built-in pagination.

### Tasks

4.1 **Create `internal/connector/supabase/supabase.go`**

Register as `"supabase"` via `init()`.

4.2 **Define constants and SQL builder**

Default tables (queried unless overridden):
```go
var defaultTables = []string{"edge_logs", "postgres_logs", "auth_logs", "function_logs"}
```

SQL builder function:
```go
func buildSQL(table string, fromMicros, toMicros int64) string
```

Generates: `SELECT id, timestamp, event_message FROM {table} WHERE timestamp >= {from} AND timestamp < {to} ORDER BY timestamp ASC LIMIT 1000`

Table name is validated against an allow-list to prevent SQL injection.

4.3 **Define response types**

```go
type logsResponse struct {
    Result []map[string]any `json:"result"`
}
```

Using `[]map[string]any` because schema varies per table. Known fields extracted by name.

4.4 **Implement `toRawLog(row map[string]any, table string) model.RawLog`**

- `Timestamp`: extract `row["timestamp"]` as float64 (JSON number), convert microseconds → `time.Unix(sec, usecRemainder*1000)`
- `Source`: `"supabase"`
- `Raw`: extract `row["event_message"]` as string
- `Metadata`: `map[string]any{"table": table}` plus any other fields in the row (varies by table)

4.5 **Implement `Query()`**

1. Extract `project_ref` from `cfg.Extra["project_ref"]`. Error if missing.
2. Parse `tables` from `cfg.Extra["tables"]` (comma-separated). Default to `defaultTables`.
3. Create `httpclient.Client` with base URL `https://api.supabase.com` and `cfg.APIKey`.
4. Path: `/v1/projects/{ref}/analytics/endpoints/logs.all`.
5. Convert `params.Start`/`params.End` to microseconds. Default to last 1 hour if both zero.
6. **24-hour window chunking:** if the range exceeds 24 hours, split into 24-hour chunks and query each sequentially.
7. For each table in each time chunk: build SQL via `buildSQL`, call `GetJSON` with `?sql=...&iso_timestamp_start=...&iso_timestamp_end=...`.
8. Convert all rows via `toRawLog`, merge results across tables and chunks, sort by timestamp.
9. Apply `params.Limit` if set.
10. Return.

4.6 **Implement `Stream()`**

1. Parse config (same as `Query`).
2. Default poll interval 10s (with 4 tables per poll, 10s = 24 req/min, well within 120 req/min limit). Configurable via `cfg.Extra["poll_interval"]`.
3. Maintain `lastTimestamp` cursor (microseconds). Initialize to `now - 1 minute`.
4. Each tick: query all configured tables from `lastTimestamp + 1` to `now`. Send results to channel. Update `lastTimestamp` to max timestamp seen.
5. Buffered channel (64), close on context cancellation.

4.7 **Write tests** (`internal/connector/supabase/supabase_test.go`)

- `TestBuildSQL` — verify SQL output for different tables and time ranges
- `TestBuildSQL_InvalidTable` — reject table names not in allow-list
- `TestToRawLog` — microsecond timestamp conversion, event_message mapping, table in metadata
- `TestQuery_SingleTable` — fixture response, verify RawLog mapping
- `TestQuery_MultipleTables` — two tables, results interleaved and sorted by timestamp
- `TestQuery_SlidingWindow` — 48-hour range splits into two 24-hour API calls
- `TestQuery_MissingProjectRef` — descriptive error
- `TestQuery_DefaultTables` — when `tables` not set in Extra, queries 4 default tables
- `TestQuery_CustomTables` — custom comma-separated table list from Extra
- `TestStream_ReceivesLogs` — logs arrive on channel
- `TestStream_ContextCancel` — channel closes

### Files

| File | Action |
|------|--------|
| `internal/connector/supabase/supabase.go` | Create |
| `internal/connector/supabase/supabase_test.go` | Create |

### Verification

```
go test ./internal/connector/supabase/...
```

---

## Section 5: Config Wiring and Main

**What:** Connect all three connectors to the config system. Load provider-specific env vars into the `Extra` map. Register all connectors via blank imports in `main.go`.

### Tasks

5.1 **Add `Extra` to `config.ConnectorConfig` and populate in `Load()`**

Modify `internal/config/config.go`:

- Add `Extra map[string]string` field to `config.ConnectorConfig`
- Add `loadConnectorExtra() map[string]string` helper that reads provider-specific env vars:

| Env Var | Extra Key | Used By |
|---------|-----------|---------|
| `LUMBER_VERCEL_PROJECT_ID` | `project_id` | Vercel |
| `LUMBER_VERCEL_TEAM_ID` | `team_id` | Vercel |
| `LUMBER_FLY_APP_NAME` | `app_name` | Fly.io |
| `LUMBER_SUPABASE_PROJECT_REF` | `project_ref` | Supabase |
| `LUMBER_SUPABASE_TABLES` | `tables` | Supabase |
| `LUMBER_POLL_INTERVAL` | `poll_interval` | All |

Only non-empty values are added. Returns `nil` if no provider-specific vars are set.

5.2 **Update `cmd/lumber/main.go`**

- Add blank imports for new connectors:
  ```go
  _ "github.com/kaminocorp/lumber/internal/connector/flyio"
  _ "github.com/kaminocorp/lumber/internal/connector/supabase"
  ```
- Pass `Extra` when constructing `connector.ConnectorConfig`

5.3 **Write tests** (`internal/config/config_test.go`)

- `TestLoad_Defaults` — no env vars, verify default values
- `TestLoad_ConnectorExtra` — set `LUMBER_VERCEL_PROJECT_ID=proj_123`, verify `Extra["project_id"] == "proj_123"`
- `TestLoad_EmptyExtraOmitted` — empty env vars don't create entries in the Extra map
- `TestLoad_MultipleProviders` — all provider vars set simultaneously, all present in Extra

### Files

| File | Action |
|------|--------|
| `internal/config/config.go` | Modify |
| `internal/config/config_test.go` | Create |
| `cmd/lumber/main.go` | Modify |

### Verification

```
go build ./cmd/lumber && go test ./internal/config/...
```

Binary compiles with all three connectors registered. Config loads all env vars.

---

## Implementation Order

```
Section 1: Shared HTTP Client
    │
    ├──→ Section 2: Vercel Connector
    ├──→ Section 3: Fly.io Connector
    └──→ Section 4: Supabase Connector
              │
              └──→ Section 5: Config Wiring & Main
```

Section 1 first (shared foundation). Sections 2-4 depend on Section 1 but are independent of each other. Section 5 last (ties everything together).

---

## Files Summary

| File | Action | Section |
|------|--------|---------|
| `internal/connector/httpclient/httpclient.go` | Create | 1 |
| `internal/connector/httpclient/httpclient_test.go` | Create | 1 |
| `internal/connector/vercel/vercel.go` | Replace stub | 2 |
| `internal/connector/vercel/vercel_test.go` | Create | 2 |
| `internal/connector/flyio/flyio.go` | Create | 3 |
| `internal/connector/flyio/flyio_test.go` | Create | 3 |
| `internal/connector/supabase/supabase.go` | Create | 4 |
| `internal/connector/supabase/supabase_test.go` | Create | 4 |
| `internal/config/config.go` | Modify | 5 |
| `internal/config/config_test.go` | Create | 5 |
| `cmd/lumber/main.go` | Modify | 5 |

---

## What's explicitly not in scope

These are deferred to later phases per the beta roadmap:

- **Buffering and backpressure** — channels are fixed at 64, no overflow handling (Phase 5)
- **Graceful drain on shutdown** — context cancellation closes channels, in-flight logs may be lost (Phase 5)
- **Per-log error isolation** — malformed API responses surface as errors, not skip-and-continue (Phase 5)
- **CLI flags** — connector selection and config via env vars only (Phase 5)
- **Real-world API validation** — all tests use `httptest` fixtures; live validation deferred (Phase 6)
- **Log drains / NATS / push-based ingestion** — polling only for beta
- **Additional connectors** (AWS, Datadog, Grafana Loki) — post-beta
