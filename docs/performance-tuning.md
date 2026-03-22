# Performance Tuning

## Throttling

Sprawler interacts with SharePoint using CSOM and the SharePoint REST API, rather than Microsoft Graph.

Microsoft publishes Resource Unit (RU) costs for Graph requests: single-item queries typically cost 1 RU, while multi-item queries cost 2 RU. Microsoft does not publish RU costs for CSOM or REST operations, but notes that they generally consume more resources than their Graph equivalents. As a rough estimate, you can assume each request costs about 2 RU.

All CSOM and REST requests issued by Sprawler consume from the tenant’s SharePoint RU budget. Calls made through the PeopleManager API are also limited by a separate throttling budget for the User Profile Service.

Throttling is affected by overall tenant activity. Requests are more likely to be throttled during periods of heavy user activity, so running Sprawler during off-peak hours reduces the likelihood of application or tenant-level throttling.

**Resource Unit (RU) limits:**

| Scope | Interval | 0-1k licenses | 1k-5k | 5k-15k | 15k-50k | 50k+ |
|---|---|---|---|---|---|---|
| Per app per tenant | 1 min | 1,250 | 2,500 | 3,750 | 5,000 | 6,250 |
| Per app per tenant | 24 h | 1.2M | 2.4M | 3.6M | 4.8M | 6M |
| Tenant-wide (all apps) | 5 min | 18,750 | 37,500 | 56,250 | 75,000 | 93,750 |

**PeopleManager limits**:

| Scope | Interval | 0-1k licenses | 1k-5k | 5k-15k | 15k-50k | 50k+ |
|---|---|---|---|---|---|---|
| Tenant-wide | 5 min | 3,000 | 6,000 | 9,000 | 12,000 | 15,000 |

**APIs & API endpoints used by Sprawler:**

| Endpoint | API surface | Used by | Throttle budget |
|---|---|---|---|
| `/_api/web/lists/GetByTitle('.../AGGREGATED_SITECOLLECTIONS')/items` | SharePoint REST | SP site discovery | RU |
| `/_vti_bin/client.svc/ProcessQuery` (`GetSitePropertiesFromSharePointByFilters`) | CSOM | OD personal site discovery | RU |
| `/_api/web/siteusers` | SharePoint REST | SP + OD site user enumeration | RU |
| `/_api/web/sitegroups` | SharePoint REST | SP site group discovery | RU |
| `/_api/web/sitegroups/GetByID({id})/users` | SharePoint REST | SP site group member enumeration | RU |
| `/_api/sp.userprofiles.peoplemanager/GetPropertiesFor('{login}')` | PeopleManager | OD profile lookup | RU + PeopleManager |

### Throttle handling

Sprawler handles SharePoint throttling through two mechanisms.

#### Throttle Detection

`ThrottledTransport` is a custom `http.RoundTripper` passed into gosip that intercepts all outgoing API requests. When any request receives a 429 or 503 response, the transport pauses all workers globally for the duration specified by the `Retry-After` header (or `SPRAWLER_TRANSPORT_THROTTLE_PAUSE`, default 1 minute, if the header is absent). This prevents additional requests from immediately extending the throttle window. The transport also retries transient network errors (connection reset, EOF) up to `SPRAWLER_TRANSPORT_MAX_RETRIES` times (default 3) with configurable backoffs (`SPRAWLER_TRANSPORT_RETRY_BACKOFFS`, default `5s,10s,30s`) for idempotent methods.

#### Worker Scaling

Each pipeline maintains a `ThrottleScaler`, which controls the maximum number of workers that may run concurrently. The scaler monitors two backpressure signals: API throttling (429/503 responses) and network errors (connection resets).

When either signal fires, the scaler immediately halves the concurrency limit. Workers already processing a request are allowed to finish, but additional workers block until the number of active workers falls to the new limit.

If no signals fire for an entire `SPRAWLER_THROTTLE_RECOVERY_COOLDOWN` interval, the scaler restores one worker. Each restoration resets the cooldown timer. Any additional signal within that window resets the recovery process.

For example, if repeated throttling reduces a pool of 6 workers down to 1, a 1-minute cooldown requires roughly 5 minutes to return to full capacity. Any additional throttled response or network error within that window resets the recovery timer and halves the worker count again. In practice, a smaller worker pool that avoids throttling often achieves better throughput than a larger pool that repeatedly scales down and recovers.

The scaler tracks summary statistics (scale-down events, scale-up events, minimum concurrency reached, total time at reduced capacity) which are reported in the final processor summary.

## Worker concurrency and request volume

Be careful using workers. Workers can very quickly consume your resource allowances (RUs).

### SharePoint pipeline

SharePoint enumeration hosts two independent worker pools: **user workers** and **group workers**. When group discovery and enumeration is enabled, each discovered site is dispatched to both pools concurrently.

The total API request volume for a single site depends on two factors:
* how many workers are active
* how many users, groups, and group members exist within each site.

Consider a site containing 12,000 users and 20 groups (one of which has 8,000 members). Both worker pools process the site at the same time:

```
Example site: https://contoso.sharepoint.com/sites/engineering
12,000 users, 20 groups

1. User enumeration - SiteUsers(), responses paged at 5,000:
     page 1: users 1-5,000
     page 2: users 5,001-10,000
     page 3: users 10,001-12,000
   = 3 API calls

2. Group discovery - GetSiteGroups():
   = 1 API call → returns 20 groups

3. Group member enumeration - one request per group, responses paged at 5,000:
     19 groups with < 5,000 members  → 1 request each = 19 calls
     1 group with 8,000 members      → 2 pages         =  2 calls
   = 21 API calls

Total for this site: 3 + 1 + 21 = 25 API calls
Estimated RU cost:  25 × ~2 RU = ~50 RU  (SharePoint RU budget only)
```

At an average API response time of ~85 ms, a worker can sustain roughly **12 requests per second** if it is continuously tasked with work. With `SPRAWLER_SP_USER_WORKERS=3` and `SPRAWLER_SP_GROUP_WORKERS=3`, the SharePoint pipeline runs six workers concurrently:

```
6 workers × 12 req/s ≈ ~72 API requests/second
                       ≈ ~144 RU/second
                       ≈ ~8,640 RU/minute
```

### OneDrive pipeline

OneDrive enumeration uses a single worker pool (`SPRAWLER_OD_USER_WORKERS`).

For each personal site discovered by the CSOM API, a worker performs two operations:

1. Enumerate the site's users  
2. Query the User Profile Service for the site owner's profile properties

```
Example personal site: https://contoso-my.sharepoint.com/personal/jdoe_contoso_com
47 users

1. User enumeration - SiteUsers(), responses paged at 5,000:
     page 1: users 1-47
   = 1 API call

2. Profile lookup - GetUserProfile() for the site owner:
   = 1 API call → /_api/sp.userprofiles.peoplemanager/GetPropertiesFor('{loginName}')

Total for this site: 1 + 1 = 2 API calls
Estimated RU cost:          2 × ~2 RU = ~4 RU   (SharePoint RU budget)
PeopleManager request cost: 1 request            (PeopleManager budget)
```

Most personal sites have fewer than 5,000 users and require only 2 API calls. Sites with more than 5,000 users add one additional request per page.

The profile lookup endpoint:

```
/_api/sp.userprofiles.peoplemanager/GetPropertiesFor('{loginName}')
```

is provided by the **PeopleManager API**. Calls to this endpoint consume from both the SharePoint RU budget and the PeopleManager request budget, and requests to this API are subject to a separate throttling policy from standard SharePoint REST operations. In my testing, responses from this API are also noticeably slower than most other SharePoint REST API operations.

With `SPRAWLER_OD_USER_WORKERS=6` and an observed average of ~10 requests/second per worker:

```
6 workers × ~10 req/s ≈ ~60 API requests/second
                       ≈ ~120 RU/second             (SharePoint RU budget)
                       ≈ ~7,200 RU/minute

At ~2 requests per site, this translates to ~30 sites/second.

Of those, ~1 in 2 requests are PeopleManager calls:
~30 PeopleManager requests/second ≈ ~9,000 over 5 minutes  (PeopleManager budget)
```

OneDrive per-worker throughput is lower than the SharePoint pipeline due to PeopleManager latency, and increasing concurrency does not scale linearly. If throughput levels off without producing any throttling, PeopleManager is typically the bottleneck.

## Identifying the bottleneck

The progress logs contain several indicators that help identify what is limiting throughput. Understanding the data flow is key: workers pull sites from input channels, make API calls, and push results onto output channels. Output channels drain into the BatchWriter queue, which flushes to SQLite in bulk transactions.

```
[SP  ] 03:16:00 [INFO] [5m0s] Sites: 23k/250k (407 inflight) | 1.7M users | skip: 1 template, 1 tenant_root
                       6/6 workers | 79.4/sec | ETA: 47m 27s | API: 23833 req 66ms [200:23833] | DB: 1.6M, 16549 queued | Buf: sites->users 400/400
```

### API-bound (normal)

This is the expected state during most of a run. The signs:

- **`Buf: sites->users 400/400`** (or `sites->workers 1000/1000` for OneDrive) -- the input channel is full. The dispatcher has sites ready, but workers are waiting on API responses. This is normal and means the API response time is the limiting factor.
- **`DB: ..., 0 queued`** (or low hundreds) -- the database queue is empty or nearly empty. SQLite is keeping up easily.
- **No `429` in the API status codes** -- no throttling is occurring.
- **Rate is steady** -- the sites/sec metric holds constant across consecutive progress reports.

**What to do:** Nothing. This is the optimal state. Adding more workers will increase throughput up to the point where throttling starts, but won't help if the API is already saturated by your current worker count.

### Database-bound

The database can't flush writes fast enough, causing backpressure that blocks workers from issuing new API requests. The signs:

- **`DB: ..., 16549 queued`** -- the queue count is high and stays high (or grows) across consecutive reports. A brief spike at startup is normal as the first large batch accumulates, but sustained high values indicate a bottleneck.
- **`Buf: users->dataWriter 1000/1000`** (or `groups->dataWriter`, `members->dataWriter`) -- the output channels between workers and the storage layer are full. Workers have results ready to write but the streamer can't drain them because the BatchWriter queue is full.
- **Rate drops** without any throttling -- throughput declines even though no `429` or `503` responses appear.
- **Input channels may empty** -- `sites->users` drops below capacity because workers are blocked on their output sends, not on API calls.

**What to do:** See [Step 4: Tune database write throughput](#step-4-tune-database-write-throughput). Increase `SPRAWLER_DB_QUEUE_CAPACITY` first, then `SPRAWLER_DB_BATCH_SIZE` and `SPRAWLER_DB_FLUSH_INTERVAL`.

### Throttle-bound

SharePoint is rate-limiting requests, causing the scaler to reduce concurrency. The signs:

- **`429:N` or `503:N` in API status codes** -- throttled responses are visible in the status code breakdown.
- **`3/6 workers (2 scaled)`** -- the scaler has reduced concurrency. The number in parentheses is the total scale-down events, not the number of workers removed.
- **Rate drops sharply then partially recovers** -- throughput drops when the scaler fires, then gradually recovers as workers are restored during cooldown.
- **Gate wait time** -- the final summary line shows `Total gate wait: Xs` indicating how long workers spent blocked behind the throttle gate.

**What to do:** See [Step 2: Size the worker pools](#step-2-size-the-worker-pools) and [Step 3: Configure throttle recovery](#step-3-configure-throttle-recovery). Reduce worker count to stay under the RU budget, or increase `SPRAWLER_THROTTLE_RECOVERY_COOLDOWN` so recovery is more gradual.

### PeopleManager-bound (OneDrive only)

The User Profile Service endpoint is slower than other APIs. The signs:

- **Throughput plateaus** -- the sites/sec rate levels off well below what the worker count should produce, with no throttling or database backlog.
- **`Buf: sites->workers 1000/1000`** -- the input channel is full, which looks like the normal API-bound state, but the rate is lower than expected for the worker count.
- **No `429` in API status codes** -- the PeopleManager API is slow, not throttled.

**What to do:** Counter-intuitively, reducing `SPRAWLER_OD_USER_WORKERS` can sometimes improve throughput, as the PeopleManager endpoint may perform better under lighter concurrent load.

## Database write throughput

Workers do not write to SQLite directly. Each worker sends records into a buffered channel, which a storage streamer drains into the `BatchWriter`. The writer accumulates records in memory and flushes them in bulk transactions when either `SPRAWLER_DB_BATCH_SIZE` is reached or `SPRAWLER_DB_FLUSH_INTERVAL` elapses.

If the writer cannot flush to disk fast enough, its internal queue (`SPRAWLER_DB_QUEUE_CAPACITY`) fills. Once full, streamers block waiting to enqueue, the data channels between workers and streamers back up, and workers eventually block on their channel sends. At this point, workers stop issuing API requests -- not because of throttling, but because they are waiting for the write pipeline to drain.

If you observe low throughput with no indication of throttling, the write pipeline is a possible contributor. See [Step 4: Tune database write throughput](#step-4-tune-database-write-throughput) for recommendations.

## Tuning guide

Tuning Sprawler is about balancing three competing constraints: **API throughput** (how fast you can pull data), **throttle avoidance** (staying under Microsoft's rate limits), and **write throughput** (how fast results land in SQLite). Adjusting one often affects the others. The guidance below works through each lever in the order you should typically consider them.

### Step 1: Establish a baseline

Before changing anything, run a short test to see how the defaults behave against your tenant.

```env
SPRAWLER_DEBUG_MAX_PAGES=10
LOG_LEVEL=DEBUG
```

Watch the progress logs for:
- **Throttling**: your worker counts are too aggressive for your tenant's RU budget.
- **Low throughput without throttling**: if the processing rate is lower than expected but no throttling is being reported, the database writer may not be keeping up. See [Database write throughput](#database-write-throughput) below for recommendations.
- **Throughput rate**: The sites/second metric reported in the progress summary gives you a baseline to compare performance after making configuration changes.

### Step 2: Size the worker pools

Worker concurrency is the primary throughput lever, and it is also the primary cause of throttling. In my own testing, each worker within the SharePoint pipeline sustained up to 12 requests/second with an average latency of (~85 ms). Assuming you see a similar rate, you can estimate your request rate as:

```
total workers × 12 ≈ requests/second
```

Compare this against your tenant's per-minute RU budget to judge headroom. If the estimated rate approaches or exceeds the limit, you should reduce the worker count preemptively rather than relying on the ThrottleScaler to correct after the fact.

#### SharePoint workers

The SharePoint pipeline uses two independent worker pools:

| Variable | Default | Effect |
|---|---|---|
| `SPRAWLER_SP_USER_WORKERS` | 6 | Workers calling `GetSiteUsers()`. Each site produces 1+ paged requests depending on user count. |
| `SPRAWLER_SP_GROUP_WORKERS` | 6 | Workers calling `GetSiteGroups()` and then enumerating members per group. Group member enumeration fans out to one request per group, making this pool heavier per site. |

Group processing is the most API-intensive part of the SharePoint pipeline. A single site with 20 groups generates at least 21 API calls (1 for group discovery, and 1 per group for member enumeration). If you don't specifically need group membership data, it is recommended you disable it entirely:

```env
SPRAWLER_SP_PROCESS_GROUPS=false
SPRAWLER_SP_PROCESS_GROUP_MEMBERS=false
```

If you do need groups but are hitting throttling during SharePoint site enumeration, you should reduce `SPRAWLER_SP_GROUP_WORKERS`.

#### OneDrive workers

| Variable | Default | Effect |
|---|---|---|
| `SPRAWLER_OD_USER_WORKERS` | 6 | Workers processing personal sites. Each worker makes a `GetSiteUsers()` call plus a `GetPropertiesFor()` call to the PeopleManager API. |

In my experience, the PeopleManager API often plateaus and degrades in performance before workers can produce throttling. If you see throughput level off or decrease without any reports of throttling, it's likely that the PeopleManager API is the bottleneck. In these scenarios, it's recommended to reduce workers which can actually improve performance.

### Step 3: Configure throttle recovery

When throttling or network errors occur, the ThrottleScaler halves active concurrency immediately and then recovers one worker at a time after a cooldown period with no new signals.

| Variable | Default | Effect |
|---|---|---|
| `SPRAWLER_THROTTLE_RECOVERY_COOLDOWN` | 1m | Time without any backpressure signal before restoring one worker. Each restoration resets the timer. |
| `SPRAWLER_THROTTLE_CHECK_INTERVAL` | 10s | How often the scaler checks for new backpressure signals. Lower values respond faster but add overhead. |

**Recovery math:** a pool of 6 workers reduced to 1 requires 5 recovery cycles. At the default 1m cooldown, that's ~5 minutes to return to full capacity - assuming no additional throttling or network errors. Any new signal during recovery resets the timer and may trigger another halving.

- **Frequent throttling:** increase to `2m`-`5m`. A slower recovery reduces the likelihood of being throttled again.
- **Rare throttling:** the default `1m` is fine.

In practice, a smaller worker pool that avoids throttling entirely often achieves a more sustained throughput than a larger pool that repeatedly scales down and recovers.

### Step 4: Tune database write throughput

As described in [Database write throughput](#database-write-throughput), workers block when the write pipeline can't keep up. If you're seeing low throughput without throttling, these settings control how quickly records move from the queue to disk:

| Variable | Default | What it does |
|---|---|---|
| `SPRAWLER_DB_QUEUE_CAPACITY` | 8000 | Buffer between storage streamers and the writer. A larger queue absorbs bursts without blocking workers, at the cost of memory. |
| `SPRAWLER_DB_BATCH_SIZE` | 500 | Records per bulk transaction. Larger batches reduce the per-transaction overhead of SQLite commits. |
| `SPRAWLER_DB_FLUSH_INTERVAL` | 5s | Maximum wait before flushing, even if the batch isn't full. Lower values provide faster visibility in the database but increase transaction frequency. |

Start by increasing `SPRAWLER_DB_QUEUE_CAPACITY` - it's the simplest change and only costs memory. If that isn't sufficient, increase `SPRAWLER_DB_BATCH_SIZE` and `SPRAWLER_DB_FLUSH_INTERVAL` together so the writer performs fewer, larger transactions.

### Step 5: Adjust timeouts

Timeouts prevent slow requests from blocking workers, but values that are too aggressive can cause sites to be skipped if the API responds slowly or is subject to throttling.

| Variable | Default | Applies to |
|---|---|---|
| `SPRAWLER_SP_USER_FETCH_TIMEOUT` | 10m | SP `GetSiteUsers()` |
| `SPRAWLER_SP_GROUP_FETCH_TIMEOUT` | 10m | SP `GetSiteGroups()` |
| `SPRAWLER_SP_MEMBER_FETCH_TIMEOUT` | 10m | SP per-group member enumeration |
| `SPRAWLER_OD_USER_FETCH_TIMEOUT` | 10m | OD `GetSiteUsers()` |
| `SPRAWLER_OD_PROFILE_FETCH_TIMEOUT` | 10m | OD `GetPropertiesFor()` UPS lookup |

If you see sites recorded as failed due to context deadlines being hit, you should increase the relevant timeout. If the volume of failures is considerable, you should reduce the number of workers to a more conservative number.