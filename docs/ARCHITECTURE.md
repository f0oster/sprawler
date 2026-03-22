# Architecture

This document covers how Sprawler is structured, how data flows through it, and how each package fits together. For configuration and usage, see [README.md](../README.md). For throttling mechanics and worker tuning, see [performance-tuning.md](performance-tuning.md).

## Entry point and wiring

`cmd/sprawler/main.go` is the entry point. It:

1. Loads config from env vars / `.env.local` / `.env`
2. Creates a `Storage` (SQLite + BatchWriter)
3. Creates two separate `api.Client` instances --one for SharePoint, one for OneDrive --so each has isolated API metrics
4. Builds a `SharePointProcessor` and `OneDriveProcessor`
5. Runs health checks on each processor
6. Executes each processor sequentially, logging results
7. On success, marks the run as complete and archives the database

There is no CLI flag parsing. All configuration comes from environment variables.

## Package map

| Package | What it does |
|---|---|
| `cmd/sprawler` | Entry point. Wires config, storage, API clients, and processors together. Runs processors sequentially. |
| `config` | Loads all `SPRAWLER_*` env vars into typed structs. Supports `.env.local` and `.env` via godotenv. |
| `model` | Domain types: `Site`, `SiteUser`, `SiteGroup`, `GroupMember`, `UserProfile`, `RunStatus`. Also error types (`OperationFailure`, `SPSiteOutcome`, `ODSiteOutcome`) and metric types (`APIMetrics`, `StorageStats`). |
| `auth/entraid` | Shared base for Entra ID auth strategies: token caching (`go-cache`, 5 min TTL, scoped by host+strategy+tenant+client) and Azure cloud detection (public, government, China). |
| `auth/entraid/clientcredential` | Certificate-based gosip auth strategy. Uses `azidentity.NewClientCertificateCredential`. Implements `gosip.AuthCnfg`. |
| `internal/processors` | `SharePointProcessor` and `OneDriveProcessor` --the core processing pipelines. Also contains `OutcomeCollector`, skip filtering (`site_filter.go`), and shared helpers (`helpers.go`). Each processor defines its own typed counter struct. |
| `internal/api` | `Client` wraps gosip for REST and CSOM calls. Includes `ThrottledTransport` (HTTP-level 429/503 gate with transport-level retries), retry policies, and per-request metrics via gosip hooks. |
| `internal/api/csom` | CSOM XML query building (`query_builder.go`), response parsing (`response_parser.go`), date parsing, and OneDrive-specific types. |
| `internal/storage` | `Storage` interface, `sqliteStorage` implementation, batch operation registration, and the storage factory. |
| `internal/batchwriter` | `BatchWriter` --FIFO batch dispatcher. Dispatches items individually in strict enqueue order. |
| `internal/metrics` | `RunMetrics` (run timing), `ProgressReporter` (periodic progress output), snapshot types, and `ProgressFormatter`. Processors define their own typed counter structs. |
| `internal/throttle` | `ThrottleScaler` --adaptive concurrency control based on backpressure signals (throttling and network errors). |
| `database/sqlc` | Generated code from sqlc --type-safe query functions for all tables. |
| `database/schema` | Source SQL schema (used by sqlc for code generation). |
| `internal/logger` | `Logger` --leveled logger (TRACE/DEBUG/INFO/WARN/ERROR) configured from `LOG_LEVEL` env var. |

## SharePoint processing pipeline

```
GetSites()                              REST API, paginated (5000/page)
    |                                   onPage callback increments SitesDiscovered
    v
rawSites chan (buf: 400)
    |
filterSites()                           Drops sites matching SPRAWLER_SP_SKIP_TEMPLATES
    |                                   and tenant root sites
    v
filteredSites chan (buf: 400)
    |
dispatchSitesToWorkers()                Saves site metadata via writer,
    |                                   fans out to worker channels,
    |                                   creates per-site tracker
    |---------------------|
    v                     v
userSites (buf: 400)   groupSites (buf: 200, optional)
    |                     |
    v                     v
User workers (6)       Group workers (4)
processSiteUsers()     processSiteGroups()
    |                     |
    v                     v
users chan (1000)       groups chan (500) + members chan (1000)
    |                     |
    |----------+----------|
               v
    bridgeChannelsToStorage()           Stream* goroutines -> BatchWriter
               |
               v
           SQLite DB
```

### How it works

`api.GetSites()` paginates the tenant admin aggregated site collections list (`DO_NOT_DELETE_SPLIST_TENANTADMIN_AGGREGATED_SITECOLLECTIONS`) and streams each `model.Site` onto a buffered channel. An `onPage` callback fires after each page is fetched, incrementing `SitesDiscovered` before the channel sends --giving accurate discovery progress even under backpressure. The REST API returns up to 5000 sites per page using gosip's `GetPaged()` / `GetNextPage()` cursor-based pagination.

`filterSites()` runs `shouldSkipSite()` on each site. The filter checks the site's template name against a configurable skip set built from `SPRAWLER_SP_SKIP_TEMPLATES` (default: TEAMCHANNEL#0, TEAMCHANNEL#1, APPCATALOG#0, REDIRECTSITE#0) and detects tenant root sites by URL path. Tenant root skipping is always on regardless of the template list.

`dispatchSitesToWorkers()` saves each site's metadata directly via the writer, then sends the site to both the user and group worker channels. It also creates an `spSiteTracker` per site so that the user and group workers can independently record their results and only finalize the site's outcome when both are done (via an atomic counter).

User workers call `api.GetSiteUsers()`, which creates a per-site gosip client (cloning the parent's auth, retry, hooks, and transport but pointed at the specific site URL) and calls `/_api/web/siteusers`. Group workers similarly call `api.GetSiteGroups()` for `/_api/web/sitegroups`, then loop through each group calling `/_api/web/sitegroups/GetByID({id})/users` with an independent per-group timeout.

All extracted data is pushed onto output channels, which `bridgeChannelsToStorage()` drains into the `BatchWriter` via `Stream*` goroutines.

### Site outcome tracking

Each site gets an `spSiteTracker` stored in a `sync.Map`. The tracker uses atomic fields so user and group workers can write concurrently without locking. When the last worker for a site finishes (tracked via `remaining.Add(-1) == 0`), `finalizeSiteOutcome()` checks whether any operation failed. If so, it records an `SPSiteOutcome` with the error category, HTTP status, per-site processing duration, and counts of what did succeed. These outcomes are persisted to the `site_outcomes` table after the pipeline completes.

## OneDrive processing pipeline

```
GetPersonalSites()                      CSOM, paginated via continuation token
    |                                   onPage callback increments SitesDiscovered
    v
personalSites chan (buf: configurable)
    |
countDispatched (buf: 1000)             Increments SitesDispatched for progress tracking
    |
    v
Workers (6)
processSite()
    |-- site metadata -----------> sites chan (1000)
    |-- GetSiteUsers() ----------> users chan (1000)     [skip if locked]
    +-- GetUserProfile() --------> profiles chan (1000)   [skip if no owner]
               |
               v
    bridgeChannelsToStorage()
               |
               v
           SQLite DB
```

### How it works

`api.GetPersonalSites()` builds CSOM XML queries via `csom.BuildOneDriveQuery()` and executes them against the tenant admin endpoint using gosip's `ProcessQuery()`. The CSOM method is `GetSitePropertiesFromSharePointByFilters` with `IncludePersonalSite=1` and `Template=SPSPERS`. Pagination uses the `NextStartIndexFromSharePoint` field from each response as a continuation token. Each page's `SiteProperties` are converted to `model.Site` and streamed to a channel.

Each worker handles the full lifecycle for a single site:

1. **Site metadata** --pushed directly to the sites output channel (already retrieved from CSOM)
2. **Site users** --calls `api.GetSiteUsers()` (same as SharePoint). Skipped if the site's `LockState` is `Locked`, `ReadOnly`, `NoAccess`, or `NoAdditions`.
3. **User profile** --builds a claims-format account name (`i:0#.f|membership|{email}`) from the site's `CreatedByEmail` field, then calls `api.GetUserProfile()` which hits `/_api/sp.userprofiles.peoplemanager/GetPropertiesFor('{login}')`. Skipped if `CreatedByEmail` is empty (no-owner site). If the returned profile has an empty `PersonalUrl` or `AadObjectId`, the site is classified as orphaned (original owner deleted).

### Locked and orphaned site handling

Locked sites still get their metadata saved but user enumeration is skipped. The lock state check uses a hardcoded set of known lock states. Orphaned sites (profile lookup returns empty identifiers) are counted but no profile record is created.

## API client

`internal/api.Client` wraps gosip's `SPClient` and adds:

**ThrottledTransport** --a shared `http.RoundTripper` that gates all outgoing HTTP requests when a 429/503 with `Retry-After` is received. When any request gets throttled, the transport records the backoff deadline. All subsequent requests from any worker block until that deadline passes. This prevents the thundering-herd problem where workers independently discover throttling and pile up retries. The transport also retries transient network errors (connection reset, EOF) with configurable retry count and backoffs (`SPRAWLER_TRANSPORT_MAX_RETRIES`, `SPRAWLER_TRANSPORT_RETRY_BACKOFFS`) for idempotent methods. The default pause when no Retry-After header is present is configurable via `SPRAWLER_TRANSPORT_THROTTLE_PAUSE`. Gate activations, total gate wait time, and transport retry counts are tracked for diagnostics.

**Retry policies** --gosip's built-in retry with per-status-code policies configurable via `SPRAWLER_TRANSPORT_RETRY_{status}` env vars (defaults: 429 up to 10 times, 5xx 5-10 times, 401 up to 5 times).

**Metrics hooks** --gosip's `OnRequest`, `OnResponse`, `OnError`, and `OnRetry` hooks track request count, success/error counts, throttle count, auth errors, network errors, timeout count, average latency, and per-status-code breakdowns. All counters use `atomic.Int64`. Status code tracking uses a private `counterMap` type with double-checked RWMutex locking for the dynamic set of HTTP status codes encountered at runtime.

**Per-site clients** --`newSiteClient()` creates a gosip client pointed at a specific site URL but sharing the parent's auth credentials, retry policies, hooks, and transport. This means per-site REST calls (site users, site groups) authenticate against the target site while metrics and throttle gating remain centralized.

## ThrottleScaler

`ThrottleScaler` dynamically adjusts worker concurrency based on backpressure signals. It monitors both API throttling (429/503 responses) and network errors (connection resets). It uses a channel-based semaphore: workers call `Acquire()` before processing a site and `Release()` when done.

A background monitor goroutine checks all signals at a configurable interval (`SPRAWLER_THROTTLE_CHECK_INTERVAL`, default 10 seconds):
- If any signal fires (new throttling or network errors detected), it applies the most aggressive scale-down action --typically halving the concurrency limit by draining tokens from the semaphore
- If all signals have been quiet for their respective cooldown periods, it scales up by 1 token
- Scale-down is immediate for tokens sitting in the semaphore buffer. For tokens currently held by workers, `Release()` uses a CAS loop: if `liveTokens > currentLimit`, it swallows the token instead of returning it

The scaler tracks summary statistics: total scale-down events, scale-up events, minimum limit reached, and total time spent at reduced concurrency. These are reported in the final processor summary.

Both processors create their own `ThrottleScaler` instance. The SharePoint scaler's `maxWorkers` is the sum of user + group workers; the OneDrive scaler's is just the user worker count.

## Storage layer

### Interface

`storage.Storage` defines the persistence contract. Current implementations: `sqliteStorage`. The interface supports streaming writes (`StreamSites(ctx, <-chan Site)`, `StreamUsers(ctx, <-chan SiteUser)`, etc.), outcome persistence, run status tracking, archival, and health checks.

### SQLite configuration

The SQLite connection is opened with aggressive performance pragmas:
- WAL journal mode
- `synchronous = OFF` (no fsync on writes --acceptable because the data can be re-fetched)
- Configurable page cache (`SPRAWLER_DB_CACHE_SIZE`, default ~256MB) and busy timeout (`SPRAWLER_DB_BUSY_TIMEOUT`, default 5000ms)
- Memory temp store, 256 MB mmap
- Single connection (`MaxOpenConns = 1`) --all writes are serialized through the BatchWriter anyway

Schema is embedded via `//go:embed` and applied on startup. If `SPRAWLER_DB_RECREATE=true` (default), the existing DB file is deleted first.

### BatchWriter

`BatchWriter` (`internal/batchwriter`) is the core write engine. It has a single input channel that carries `Item{Kind, Value}` envelopes. A single-threaded run loop drains the channel and dispatches each item individually in strict FIFO order.

Key behaviors:
- **FIFO ordering** --items execute in exactly the order they were received
- **Flush triggers** --configurable time interval, explicit `Flush()` call, or channel close
- **Failure tracking** --failed writes are optionally appended to an NDJSON file (`SPRAWLER_DB_FAILURE_LOG_PATH`) for post-run analysis

Producers (the `Stream*` goroutines) send items via `Add()`.

### Batch operations

`batch_operations.go` registers a handler for each `Kind` string (`"sites"`, `"site_users"`, `"user_profiles"`, `"site_groups"`, `"group_members"`). Each handler receives a single item and calls the corresponding sqlc `Insert*` or `Upsert*` function. INSERT mode is used when the database is recreated (guaranteed no duplicates); UPSERT mode is used for incremental runs.

### Archival

On successful completion, `ArchiveDatabase()` closes the DB connection and renames the file to `sharepoint_export_complete.db`, replacing any previous archive.

## Metrics and progress reporting

### RunMetrics

Each processor creates a `RunMetrics` instance at the start of its `Process()` call. It holds run timing and references to external data sources (API client metrics, storage stats). Counters are owned by each processor as typed structs with `atomic.Int64` fields.

### Per-Processor Counters

Each processor defines its own unexported counter struct (`sharePointCounters`, `oneDriveCounters`) with only the fields it needs. Counters are created per-run and passed through the pipeline. Write access is compile-time safe --there are no string keys. Reads of multiple counter fields are not transactionally consistent; this is acceptable for progress reporting.

### ProgressReporter

A goroutine that fires on a configurable interval and logs a hierarchical multi-line progress report. Configured with closures for computed sections (site progress, scaler stats) and `NamedCounter` slices for raw counter sections (entities, skips). Output format is customizable via a `ProgressFormatter` function. The default formatter produces 2 lines:
- **Line 1** (always present): Sites done/total, inflight, entity counts, skip breakdown
- **Line 2** (omitted if empty): Workers, rate, ETA, API stats, storage throughput/queue, channel backpressure

### OutcomeCollector

Thread-safe collector for per-site outcome records (`SPSiteOutcome`, `ODSiteOutcome`). Records which operations succeeded or failed for each site, with error categories, HTTP status codes, and per-site processing duration. Provides `FailureSummary()` for aggregated failure breakdown by operation and error category (e.g., `users/throttle: 3, groups/timeout: 1`). Outcomes are persisted to the `site_outcomes` table after pipeline completion.

## Authentication

`auth/entraid/clientcredential` implements gosip's `AuthCnfg` interface using Azure Identity SDK's `ClientCertificateCredential`. It extends `entraid.Base` which provides:
- Token caching via `go-cache` (5-minute TTL, keyed by `host@strategy@tenantID@clientID`)
- Azure cloud detection from the SharePoint domain (public, government, China)

The `clientcredential` strategy:
- Reads a `.pfx` / `.p12` certificate file
- Parses it with `azidentity.ParseCertificates`
- Requests tokens scoped to the SharePoint host (`https://{host}/.default`)
- Sets the `Authorization: Bearer {token}` header on each request via `SetAuth()`

The same credential is shared across all per-site clients -- `newSiteClient()` copies the auth config with a different `SiteURL` but the same tenant/client/cert, so the cached token is reused.

## Error handling

Errors are classified into categories (`throttle`, `auth`, `timeout`, `server_error`, `network`, `unknown`) by `parseError()` in `helpers.go`, which parses the HTTP status code from gosip error strings and identifies network-level errors via `errors.Is`/`errors.As`. Each failed site operation produces an `OperationFailure` with the category, status code, and detail string. These are aggregated into per-site outcome records by `OutcomeCollector` and persisted to the `site_outcomes` table for post-run analysis.

## Database schema

| Table | Primary key | Foreign keys |
|---|---|---|
| `run_status` | `id` | -- |
| `sites` | `site_id` | -- |
| `user_profiles` | `personal_url` | -- |
| `site_users` | `(id, site_id)` | `site_id -> sites` |
| `site_groups` | `(id, site_id)` | `site_id -> sites` |
| `group_members` | `(id, group_id, site_id)` | `(group_id, site_id) -> site_groups` |
| `site_outcomes` | `(site_id, processor)` | -- |

Indexes exist on commonly queried columns: `site_url`, `login_name`, `user_principal_name`, `aad_object_id`, `profile_sid`, `is_site_admin`, `site_id` (on join tables).
