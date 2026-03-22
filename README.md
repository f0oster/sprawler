# Sprawler -- SharePoint Online Enumeration Tool

Sprawler is a SharePoint Online enumeration and metadata extraction tool written in Go, designed to make it easier to understand and manage very large environments.

Sprawler enumerates a tenant's SharePoint sites, OneDrive personal sites, User Profile Service profiles, and site memberships (via the Site User Information List), storing the results in a local SQLite database for offline analysis. This gives you a point-in-time snapshot of your entire SPO environment without needing to re-enumerate the tenant for each query. Sprawler is intended to run as a weekly batch job during off-peak hours.

While Sprawler records Site Collection Administrators, it does **not** provide detailed insights into *permissions* or *access levels* on a site or its document libraries. Such queries are not scalable in large tenants, so they fall outside the scope of this project. If you require that level of detail, there are existing tools better suited for it such as ShareGate.

Sprawler remains efficient and scalable in large environments by correlating users to sites solely through Site User Information List memberships. This approach identifies users who have interacted with a site -- visiting it, viewing a file, receiving a sharing link, or being granted access.

## Use cases

With the dataset Sprawler produces, you can:

* Find all sites a user has appeared in, to identify where deeper permission analysis is needed.
* Detect site access sprawl across the tenant.
* Find orphaned users in sites (e.g., users who need to be removed during offboarding).
* Detect mismatching PUIDs (e.g., when a new user is allocated the UPN of a former SharePoint user).

## Benchmarks

The following benchmark is from a production SPO environment. See [log_example.txt](log_example.txt) for the full run log.

- **Sites enumerated:** 610,580 total
  - 249,811 SharePoint sites (218,396 processed, 31,415 skipped)
  - 360,769 OneDrive personal sites
- **User profiles retrieved:** 338,466 via the SharePoint User Profile Service
- **Site user list entries extracted:** 14,700,000+
- **Duration:** 3h 8m

---

## Prerequisites

- **Go 1.23+**
- **Microsoft Entra ID app registration** with certificate-based authentication:
  1. Create or use an existing app registration in the [Azure Entra ID Portal](https://portal.azure.com/#blade/Microsoft_AAD_RegisteredApps/ApplicationsListBlade).
  2. Under **Certificates & secrets**, upload your certificate (.pfx/.p12).
  3. Under **API permissions**, add `Sites.FullControl.All` (Application) for SharePoint. Grant admin consent. It may be possible to achieve the same results with a least-privilege combination of permissions, but this has not been tested.

## Quick start

**1. Build Sprawler**

```bash
make build
```

**2. Set up your environment**

```bash
cp .env.example .env.local
```

Edit `.env.local` and fill in your tenant details:

```env
SPRAWLER_SP_TENANT_ADMIN_SITE=https://yourtenant-admin.sharepoint.com
SPRAWLER_ENTRA_TENANT_ID=your-tenant-id
SPRAWLER_ENTRA_CLIENT_ID=your-client-id
SPRAWLER_ENTRA_CERT_PATH=/path/to/your/certificate.pfx
```

**3. Run a smoke test**

Before enumerating your full tenant, verify connectivity and permissions with a small run. Set `SPRAWLER_DEBUG_MAX_PAGES=1` in `.env.local` to process just one page (~5000 sites), then run:

```bash
./bin/sprawler
```

Watch the output for errors. If authentication or permissions are misconfigured, the health check will fail immediately. If the run completes, you should see a `./data/sharepoint_export_complete.db` file.

**4. Run a full enumeration**

Remove or set `SPRAWLER_DEBUG_MAX_PAGES=0` in `.env.local`, then run Sprawler again. A full run can take several hours depending on tenant size -- see [Benchmarks](#benchmarks) for reference.

**5. Query the results**

Open the database with any SQLite client:

```bash
sqlite3 ./data/sharepoint_export_complete.db
```

```sql
-- How many sites were enumerated?
SELECT COUNT(*) FROM sites;

-- Find all sites a specific user appears in
SELECT sites.site_url, sites.title
FROM site_users
JOIN sites ON site_users.site_id = sites.site_id
WHERE site_users.user_principal_name = 'user@contoso.com';
```

See [Example queries](#example-queries) for more.

### Other Makefile targets

```bash
make test     # Run all tests
make check    # Run vet + gofmt and fail on formatting issues
make cover    # Run tests with per-package coverage report
```

See [CONTRIBUTING.md](CONTRIBUTING.md) for the full list.

---

## How it works

Sprawler enumerates a tenant in two phases. Each phase streams sites through parallel workers to improve performance.

### Phase 1 -- SharePoint Site Collections

Enumerates the tenant admin aggregated site collections list via the REST API. For each processable site, Sprawler extracts:

- **Site metadata** -- URL, title, template, creation date, last activity, storage used, lock state, group ID, site ID, created-by email
- **Site User Information List** -- login name, email, title, UPN, principal type, site admin flag, guest flags, user ID (NameId + issuer)
- **Site groups** (optional, off by default) -- title, login name, description, owner title
- **Group members** (optional, off by default) -- login name, email, title, UPN, principal type

### Phase 2 -- OneDrive Personal Sites

Enumerates personal sites via the CSOM `GetSitePropertiesFromSharePointByFilters` method. For each site, Sprawler extracts:

- **Site metadata** -- URL, title, template, creation date, last modified, storage used, lock state, site ID
- **Site User Information List** -- login name, email, title, UPN, principal type, site admin flag, guest flags, user ID (NameId + issuer)
- **UPS profile** (via PeopleManager API) -- personal URL, Entra object ID, account name, UPN, profile SID, instantiation state, profile GUID

---

## Configuration

Sprawler is configured using environment variables with the `SPRAWLER_` prefix. Copy `.env.example` to `.env.local` and customize the values for your environment.

### Authentication (Required)
- `SPRAWLER_SP_ADMIN_USER` - SharePoint admin user account (e.g., `admin@yourtenant.onmicrosoft.com`)
- `SPRAWLER_SP_TENANT_ADMIN_SITE` - SharePoint tenant admin site URL (e.g., `https://yourtenant-admin.sharepoint.com`)
- `SPRAWLER_ENTRA_TENANT_ID` - Microsoft Entra ID tenant ID
- `SPRAWLER_ENTRA_CLIENT_ID` - Microsoft Entra ID application (client) ID
- `SPRAWLER_ENTRA_CERT_PATH` - Path to certificate file (.pfx or .p12) for authentication

### Database Settings
- `SPRAWLER_DB_TYPE` - Database type (`sqlite` - only option currently)
- `SPRAWLER_DB_PATH` - Directory for database file (default: `./data`)
- `SPRAWLER_DB_NAME` - Database filename (default: `spo.db`)
- `SPRAWLER_DB_RECREATE` - Recreate database on each run (`true`/`false`)
- `SPRAWLER_DB_BATCH_SIZE` - Records per batch for database writes (default: `500`)
- `SPRAWLER_DB_FLUSH_INTERVAL` - Time interval to flush pending writes (default: `5s`)
- `SPRAWLER_DB_QUEUE_CAPACITY` - Maximum queued records (default: `8000`)
- `SPRAWLER_DB_ENABLE_FAILURE_LOG` - Write failed batches to NDJSON log (default: `true`)
- `SPRAWLER_DB_FAILURE_LOG_PATH` - Path for failed writes log (default: `./failed_writes.ndjson`)
- `SPRAWLER_DB_BUSY_TIMEOUT` - SQLite lock wait timeout in milliseconds (default: `5000`)
- `SPRAWLER_DB_CACHE_SIZE` - SQLite page cache size, negative = KB (default: `-262144` / ~256MB)

### Transport / Retry
- `SPRAWLER_TRANSPORT_MAX_RETRIES` - Max transport retries for transient network errors (default: `3`)
- `SPRAWLER_TRANSPORT_RETRY_BACKOFFS` - Comma-separated backoff durations per retry (default: `5s,10s,30s`)
- `SPRAWLER_TRANSPORT_THROTTLE_PAUSE` - Default pause when 429/503 lacks Retry-After header (default: `1m`)
- `SPRAWLER_TRANSPORT_RETRY_500` - Gosip retries for HTTP 500 (default: `5`)
- `SPRAWLER_TRANSPORT_RETRY_503` - Gosip retries for HTTP 503 (default: `10`)
- `SPRAWLER_TRANSPORT_RETRY_504` - Gosip retries for HTTP 504 (default: `10`)
- `SPRAWLER_TRANSPORT_RETRY_429` - Gosip retries for HTTP 429 (default: `10`)
- `SPRAWLER_TRANSPORT_RETRY_401` - Gosip retries for HTTP 401 (default: `5`)

### SharePoint Processing
- `SPRAWLER_SP_PAGE_SIZE` - Results per page from SharePoint API (max: `5000`, default: `5000`)
- `SPRAWLER_SP_SKIP_TEMPLATES` - Comma-separated site templates to skip (default: `TEAMCHANNEL#0,TEAMCHANNEL#1,APPCATALOG#0,REDIRECTSITE#0`)
- `SPRAWLER_SP_USER_WORKERS` - Concurrent workers for site user enumeration (default: `6`)
- `SPRAWLER_SP_GROUP_WORKERS` - Concurrent workers for site group enumeration (default: `4`)
- `SPRAWLER_SP_PROCESS_GROUPS` - Enable site group processing (`true`/`false`, default: `false`)
- `SPRAWLER_SP_PROCESS_GROUP_MEMBERS` - Enable group member processing (`true`/`false`, default: `false`)
- `SPRAWLER_SP_USER_FETCH_TIMEOUT` - Timeout for user enumeration requests (default: `10m`)
- `SPRAWLER_SP_GROUP_FETCH_TIMEOUT` - Timeout for group enumeration requests (default: `10m`)
- `SPRAWLER_SP_MEMBER_FETCH_TIMEOUT` - Timeout for per-group member fetches (default: `10m`)
- `SPRAWLER_SP_PROGRESS_INTERVAL` - Interval between progress log lines (default: `1m`)
- `SPRAWLER_SP_EXPECTED_SITES` - Expected total SharePoint sites, enables ETA in progress logs (optional)

### OneDrive Processing
- `SPRAWLER_OD_CSOM_BUFFER_SIZE` - Buffer size for CSOM enumeration (default: `500`)
- `SPRAWLER_OD_USER_WORKERS` - Concurrent workers for OneDrive processing (default: `6`)
- `SPRAWLER_OD_USER_FETCH_TIMEOUT` - Timeout for OneDrive user enumeration (default: `10m`)
- `SPRAWLER_OD_PROFILE_FETCH_TIMEOUT` - Timeout for user profile retrieval (default: `10m`)
- `SPRAWLER_OD_PROGRESS_INTERVAL` - Interval between progress log lines (default: `1m`)
- `SPRAWLER_OD_EXPECTED_SITES` - Expected total OneDrive sites, enables ETA in progress logs (optional)

### Throttle
- `SPRAWLER_THROTTLE_RECOVERY_COOLDOWN` - Wait period before scaling concurrency back up after throttling or network errors (default: `1m`)
- `SPRAWLER_THROTTLE_CHECK_INTERVAL` - How often the scaler checks for backpressure signals (default: `10s`)

### Debug/Testing
- `SPRAWLER_DEBUG_MAX_PAGES` - Limit processing to specific number of pages (`0` = no limit)
- `SPRAWLER_DEBUG_MAX_ONEDRIVE_SITES` - Limit OneDrive sites processed (`0` = no limit)
- `LOG_LEVEL` - Logging level (`TRACE`, `DEBUG`, `INFO`, `WARN`, `ERROR`)

---

## Output

Sprawler writes all results to a SQLite database at `<DB_PATH>/<DB_NAME>` (default: `./data/spo.db`).

### Tables

| Table | Contents |
|---|---|
| `sites` | Site metadata for both SharePoint and OneDrive sites |
| `site_users` | Site User Information List entries per site |
| `site_groups` | SharePoint groups per site |
| `group_members` | Members of each site group |
| `user_profiles` | User Profile Service data for OneDrive users |
| `site_outcomes` | Per-site failure details (only sites with errors are recorded) |
| `run_status` | Tracks whether the export completed successfully |

On successful completion the database file is renamed to `./data/sharepoint_export_complete.db`.

If `SPRAWLER_DB_ENABLE_FAILURE_LOG` is `true` (the default), any failed batch writes are appended to the file specified by `SPRAWLER_DB_FAILURE_LOG_PATH` (default: `./failed_writes.ndjson`) for debugging.

### Example queries

```sql
-- Find all sites a user appears in, with their profile SID for cross-referencing
SELECT
    sites.site_url,
    sites.title,
    site_users.user_principal_name,
    site_users.userid_name_id AS profile_sid,
    site_users.userid_name_id_issuer AS identity_authority,
    user_profiles.profile_sid
FROM sites
JOIN site_users ON sites.site_id = site_users.site_id
LEFT JOIN user_profiles ON site_users.user_principal_name = user_profiles.user_principal_name
WHERE site_users.user_principal_name = 'user@contoso.com';

-- Find orphaned site UIL entries (users without a matching user profile)
SELECT
    sites.site_url,
    sites.title,
    site_users.user_principal_name,
    site_users.userid_name_id AS profile_sid,
    site_users.userid_name_id_issuer AS identity_authority
FROM sites
JOIN site_users ON sites.site_id = site_users.site_id
LEFT JOIN user_profiles ON site_users.user_principal_name = user_profiles.user_principal_name
WHERE user_profiles.profile_sid IS NULL
  AND site_users.userid_name_id_issuer = 'urn:federation:microsoftonline';

-- Count sites per orphaned user (users in site UILs but with no user profile)
SELECT
    site_users.user_principal_name AS orphaned_user,
    COUNT(DISTINCT sites.site_id) AS site_count
FROM site_users
JOIN sites ON sites.site_id = site_users.site_id
LEFT JOIN user_profiles ON site_users.user_principal_name = user_profiles.user_principal_name
WHERE user_profiles.profile_sid IS NULL
  AND site_users.userid_name_id_issuer = 'urn:federation:microsoftonline'
  AND sites.template_name != 'TEAMCHANNEL#0'
GROUP BY site_users.user_principal_name
ORDER BY site_count DESC;

-- List all users in a specific site's User Information List
SELECT
    site_users.id,
    site_users.login_name,
    site_users.principal_type,
    site_users.title,
    site_users.userid_name_id,
    site_users.userid_name_id_issuer,
    sites.title AS site_title
FROM sites
JOIN site_users ON sites.site_id = site_users.site_id
WHERE sites.title = 'Engineering Team'
ORDER BY sites.site_id;

-- Look up a user's SharePoint User Profile Service record
-- (only exists for users with an allocated OneDrive)
SELECT *
FROM user_profiles
WHERE user_profiles.user_principal_name = 'user@contoso.com';

-- Detect mismatching profile IDs (site UIL user ID differs from stored profile SID)
SELECT
    site_users.userid_name_id,
    user_profiles.profile_sid,
    site_users.user_principal_name,
    sites.site_url
FROM site_users
JOIN user_profiles ON site_users.user_principal_name = user_profiles.user_principal_name
JOIN sites ON site_users.site_id = sites.site_id
WHERE site_users.userid_name_id != user_profiles.profile_sid;
```

---

## Progress output

While running, Sprawler logs a two-line progress report at a configurable interval (default 1 minute); line 2 is omitted when empty.

**SharePoint:**
```
[SP  ] 03:16:00 [INFO] [5m0s] Sites: 23k/250k (407 inflight) | 1.7M users | skip: 1 template, 1 tenant_root
                       6/6 workers | 79.4/sec | ETA: 47m 27s | API: 23833 req 66ms [200:23833] | DB: 1.6M, 16549 queued | Buf: sites->users 400/400
```

**OneDrive:**
```
[OD  ] 03:54:59 [INFO] [5m0s] Sites: 12k/350k (1007 inflight) | 139k users, 12k profiles
                       6/6 workers | 41.4/sec | ETA: 2h 15m | API: 24852 req 65ms [200:24852] | DB: 6.8M, 0 queued | Buf: sites->workers 1000/1000
```

**Key things to look for:**

- **Sites** -- `Sites: 23k/250k` shows completed vs expected total. The parenthetical shows inflight (dispatched to workers but not finished). When `SPRAWLER_SP_EXPECTED_SITES` is not set, the `/total` suffix is omitted.
- **Workers and rate** -- `6/6 workers` shows current/max from the throttle scaler. If the scaler reduces concurrency due to throttling or network errors, this shows e.g. `3/6 workers (2 scaled)` where the number reflects how many scale-down events have occurred. The rate reflects recent windowed throughput, not a lifetime average.
- **Skip** -- `skip: 1 template, 1 tenant_root` shows how many sites were filtered out by template matching (`SPRAWLER_SP_SKIP_TEMPLATES`) and tenant root detection.
- **API status codes** -- `[200:23833]` shows the count of responses per HTTP status code. Throttled requests appear as `429:N` and auth failures as `401:N` or `403:N` in this breakdown. Transport-level retries (connection resets, EOF) are shown separately when they occur.
- **DB queued** -- items waiting to be written to SQLite. If this stays high, the database can't keep up with workers. See [performance tuning](docs/performance-tuning.md) for guidance.

**Channel backpressure (Buf):**

When pipeline channels have items buffered, a `Buf:` section appears showing where data is queuing up. This helps identify the bottleneck when throughput is lower than expected.

SharePoint channels:
| Channel | What it means when full |
|---|---|
| `sites->users` | User workers are busy. This is normal -- it means workers are actively processing and the dispatcher is feeding sites faster than the API can respond. Only a concern if throughput is unexpectedly low. |
| `sites->groups` | Group workers are busy. Group member enumeration fans out to many API calls per site, so this channel tends to fill more easily. |
| `users->dataWriter` | SQLite can't keep up with user extraction. Increase `SPRAWLER_DB_BATCH_SIZE` or `SPRAWLER_DB_QUEUE_CAPACITY`. |
| `groups->dataWriter` | SQLite can't keep up with group extraction. Same tuning as above. |
| `members->dataWriter` | SQLite can't keep up with member extraction. Same tuning as above. |

OneDrive channels:
| Channel | What it means when full |
|---|---|
| `sites->workers` | Workers are busy. The PeopleManager API has higher latency than other endpoints, so this is common during OneDrive processing. If throughput plateaus without throttling, the PeopleManager API is typically the bottleneck -- reducing workers can sometimes help. |
| `users->dataWriter` | SQLite can't keep up with user extraction. |
| `profiles->dataWriter` | SQLite can't keep up with profile extraction. |

The `sites->*` input channels are typically full during processing -- this is normal and means the API is the bottleneck, which is expected. The `*->dataWriter` channels filling up is less common and indicates a database write bottleneck. See [performance tuning](docs/performance-tuning.md#identifying-the-bottleneck) for details on diagnosing each scenario.

## Further reading

- [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) -- internal architecture, pipeline diagrams, and package map
- [docs/performance-tuning.md](docs/performance-tuning.md) -- throttling mechanics, RU budgets, and worker sizing

## Acknowledgements

Sprawler is built on the work of these open source projects:

- [Gosip](https://github.com/koltyakov/gosip) -- Sprawler is built on top of this excellent library, and wouldn't exist without the hard work of Koltyakov and other contributors.
- [sqlc](https://sqlc.dev/) -- generates type-safe Go code from SQL queries
- [go-sqlite3](https://github.com/mattn/go-sqlite3) -- CGo SQLite driver for Go
- [Azure Identity SDK](https://github.com/Azure/azure-sdk-for-go/tree/main/sdk/azidentity) -- Microsoft Entra ID authentication
- [godotenv](https://github.com/joho/godotenv) -- .env file loading

## License

Sprawler is licensed under the [MIT License](LICENSE).
