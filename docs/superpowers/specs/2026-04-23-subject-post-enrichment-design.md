# Subject-Post Enrichment via Redis — Design

**Goal:** Enrich `like`, `repost`, `like-via-repost`, and `repost-via-repost` push notifications with the text of the subject post (the post being acted upon), pulled from a Redis-backed cache with an AppView `getPosts` fallback. Builds on the richer-notifications feature which shipped the formatter package and landed the consumer wiring; this spec delivers the `PostTextProvider` the earlier spec anticipated.

**Non-goals:**
- Fetching the full post record (images, embeds, facets) — only text + media-embed presence, matching what `extractPostContent` already extracts for reply/mention/quote.
- Caching profiles in Redis (current in-memory cache stays as-is).
- Caching the block graph in Redis (remains in SQLite + in-memory).
- Multi-instance Redis sharing / clustering. Single-process, single-container remains the deployment model.
- Cache hit-rate metrics / observability. Out of scope for YAGNI; can be added in a follow-up.

---

## Scope

| Reason | Enriched by this PR? | Source of text |
|---|---|---|
| `like` | yes | Redis / AppView `getPosts` on `record.subject.uri` |
| `repost` | yes | Redis / AppView `getPosts` on `record.subject.uri` |
| `like-via-repost` | yes | same as `like` — same `subject.uri` |
| `repost-via-repost` | yes | same as `repost` — same `subject.uri` |
| `reply` | no (already enriched from triggering commit) | `record.text` |
| `mention` | no (already enriched) | `record.text` |
| `quote` | no (already enriched) | `record.text` |
| `follow` / `verified` / `unverified` | no | n/a |

The two pairs share subject URIs — a like and a like-via-repost for the same post share one cache entry.

---

## Configuration

| Variable | Default | Semantics |
|---|---|---|
| `REDIS_URL` | `redis://redis:6379/0` | Connection URL. Set to empty string to disable Redis (gateway uses `NullProvider`, no enrichment for likes/reposts). |
| `REDIS_POST_TTL_SECONDS` | `86400` (24h) | Positive-hit TTL — how long a successful fetch stays cached. |
| `REDIS_POST_NEGATIVE_TTL_SECONDS` | `300` (5min) | Negative-hit TTL — how long "post not found" stays cached. |
| `POST_FETCH_TIMEOUT_SECONDS` | `2` | Per-fetch AppView timeout. |

**Default URL behavior:** The code default (`redis://redis:6379/0`) is deliberately the docker-compose hostname so `docker compose up` works out of the box. Bare-metal users get one of two outcomes:

- They explicitly set `REDIS_URL=` (empty) → `NullProvider`, enrichment disabled for likes/reposts. Today's behavior (ZWSP bodies) continues.
- They leave the default and don't run Redis → startup `PING` fails → gateway logs a warning and downgrades to `NullProvider`. Gateway still starts.

Redis is never a hard dependency.

---

## Architecture

### New package: `internal/posttext/`

```
internal/posttext/
├── provider.go       PostTextProvider interface + NullProvider (no-op)
├── redis.go          CachedProvider — Redis + singleflight + Fetcher
├── fetch.go          AppViewFetcher — HTTP client for app.bsky.feed.getPosts
├── provider_test.go
├── redis_test.go     uses alicebob/miniredis/v2 + stub Fetcher
└── fetch_test.go     uses httptest.Server
```

### Interface

```go
package posttext

type PostTextProvider interface {
    // PostText returns (text, hasEmbed, ok) for a post by AT-URI.
    // Blocks up to the provider's configured timeout. On miss, timeout,
    // or error, returns ok=false — the caller falls back to ZWSP body.
    PostText(ctx context.Context, uri string) (text string, hasEmbed bool, ok bool)
}
```

### Two implementations

**`NullProvider`** — zero-value struct, always returns `ok=false`. Used when `REDIS_URL` is empty OR when startup `PING` fails.

```go
type NullProvider struct{}

func (NullProvider) PostText(context.Context, string) (string, bool, bool) {
    return "", false, false
}
```

**`CachedProvider`** — the real implementation.

```go
type Config struct {
    URL         string
    PositiveTTL time.Duration
    NegativeTTL time.Duration
}

type CachedProvider struct {
    client  *redis.Client
    fetcher Fetcher
    group   singleflight.Group
    cfg     Config
}
```

`Fetcher` is a small interface that `AppViewFetcher` implements. This lets `redis_test.go` stub the fetcher without hitting AppView.

```go
type Fetcher interface {
    // Fetch calls the AppView. Returns (text, hasEmbed, found, err).
    // found=false, err=nil means "post doesn't exist"; don't retry.
    // err != nil means transient (HTTP/network/parse); caller won't cache.
    Fetch(ctx context.Context, uri string) (text string, hasEmbed bool, found bool, err error)
}
```

### `AppViewFetcher`

```go
type AppViewFetcher struct {
    Client  *http.Client    // caller-provided, so tests can swap it
    BaseURL string          // defaults to https://public.api.bsky.app
}
```

Calls `GET {BaseURL}/xrpc/app.bsky.feed.getPosts?uris[]={uri}`. Parses the response's `posts[0].record` for `text` and the `embed.$type` for `hasEmbed` (same rules as `extractPostContent`: images/video/external/recordWithMedia qualify; plain `record` does not).

Empty `posts` array → `found=false, err=nil`.

---

## Data flow

### Cache-hit path

```
Jetstream: app.bsky.feed.like (subject = at://…/bob/post/abc)
    │
    ▼
consumer.handleLike
    │
    ▼
ctx := context.WithTimeout(parent, POST_FETCH_TIMEOUT_SECONDS)
postText, hasEmbed, _ := c.postText.PostText(ctx, like.Subject.URI)
    │
    ▼  Redis GET "posttext:v1:at://…/bob/post/abc" → {"t":"post text","e":false}
    │
    ▼  decode JSON, return (text, hasEmbed, true)
    ▼
consumer.sendNotification(..., postText, hasEmbed)
    │
    ▼
notification.Format → enriched body
```

### Cache-miss path

```
consumer.handleLike
    │
    ▼
provider.PostText(ctx, uri)
    │
    ▼  Redis GET → redis.Nil (miss)
    │
    ▼  singleflight.Do(uri, fetchFn)
    │     │  (100 concurrent likes on the same URI share this one fetch)
    │     │
    │     ▼  HTTP GET public.api.bsky.app/xrpc/app.bsky.feed.getPosts?uris[]=<uri>
    │     │      (per-request deadline: POST_FETCH_TIMEOUT_SECONDS)
    │     │
    │     ▼  parse → (text, hasEmbed, found, err)
    │     │
    │     ▼  if err == nil:
    │     │      if found: Redis SET <key> <{t,e}> EX PositiveTTL
    │     │      else:     Redis SET <key> "null" EX NegativeTTL
    │     │  if err != nil: don't cache (transient)
    ▼
return (text, hasEmbed, ok)
```

### Redis-degraded path

```
provider.PostText(ctx, uri)
    │
    ▼  Redis GET returns err (connection refused, timeout)
    │     log at debug, don't cache
    ▼
singleflight.Do → fetcher.Fetch directly (bypasses Redis)
    │
    ▼
return (text, hasEmbed, ok)
```

The gateway keeps working while Redis is down — we just lose the caching layer. When Redis recovers, next miss populates the cache normally.

**Redis client timeouts.** The `redis.Client` is configured with short per-operation timeouts so a dead Redis doesn't stall notification dispatch. Target values:
- `DialTimeout: 2s` — initial connect, only paid once per connection
- `ReadTimeout: 500ms` — per `GET`/`SET` operation
- `WriteTimeout: 500ms` — per write
- `PoolSize: 10` — matches the consumer's worker count (8) plus a small headroom

On a sustained Redis outage, each `PostText` call spends up to 500ms waiting for the read before falling through. Combined with the `POST_FETCH_TIMEOUT_SECONDS` AppView deadline (default 2s), total worst-case wall time per notification is ~2.5s. Per-worker goroutines block during that time; with 8 workers and a bounded channel, that's a manageable back-pressure ceiling.

### NullProvider path

```
provider.PostText → returns ("", false, false) immediately, no I/O

consumer.sendNotification(..., "", false)
    │
    ▼
notification.Format → ZWSP body (today's behavior preserved)
```

---

## Cache format

**Key:** `posttext:v1:<at-uri>`. `v1` is a literal namespace so a future format change can bump to `v2` without colliding with stale keys.

**Positive value:** JSON object `{"t": "post text here", "e": true}`.

**Negative value:** the literal string `null` (4 bytes including JSON null).

**Empty-text posts are still positive.** A post with `text == ""` (image-only, for example) caches as `{"t":"","e":true}` — an exact mirror of what `getPosts` returned. The formatter sees `postText == ""` and falls to ZWSP body, same as `reply`/`mention`/`quote` with empty text. The cache entry exists so subsequent likes on the same post don't re-fetch.

**Cache-entry decision matrix:**

| Fetch result | Cache action | Return to caller |
|---|---|---|
| `found=true, text="hello", embed=images, err=nil` | `SET {"t":"hello","e":true}` EX PositiveTTL | `("hello", true, true)` |
| `found=true, text="", embed=images, err=nil` | `SET {"t":"","e":true}` EX PositiveTTL | `("", true, true)` but caller falls to ZWSP since text is empty |
| `found=false, err=nil` | `SET null` EX NegativeTTL | `("", false, false)` |
| `err != nil` (network, 5xx, parse) | no SET | `("", false, false)` |

**Why not cache transient errors:** A 503 from AppView should be retried on the next like, not suppressed for 5 minutes.

---

## Concurrency

`singleflight.Group` keyed by URI. When 100 concurrent goroutines call `PostText` for the same URI during a cache miss, exactly one AppView fetch happens; the other 99 wait for the result.

Singleflight only dedups within a single process. Multi-replica deployments would fetch N times; not a concern per the project's single-process constraint. If that ever changes, the Redis layer itself absorbs most of the pressure (one fetch cold-fills the cache; next replica's miss hits).

---

## Code organization changes outside the new package

### `internal/jetstream/consumer.go`

- `Consumer` struct gains one field: `postText posttext.PostTextProvider`.
- `NewConsumer` signature grows one parameter: the provider. Callers pass either `posttext.NullProvider{}` or a `*posttext.CachedProvider`.
- `handleLike` and `handleRepost` (and their `-via-repost` paths) call `c.postText.PostText(ctx, subjectURI)` and pass the result to `sendNotification`. The context is derived from a parent (the commit's goroutine context) with a timeout of `POST_FETCH_TIMEOUT_SECONDS`.
- No changes to `handlePost`, `handleFollow`, `handleVerificationCreate`/`Delete`. Those paths still pass `"", false`.

### `cmd/server/main.go`

- Reads `REDIS_URL`, `REDIS_POST_TTL_SECONDS`, `REDIS_POST_NEGATIVE_TTL_SECONDS`, `POST_FETCH_TIMEOUT_SECONDS`.
- If `REDIS_URL` is empty → use `posttext.NullProvider{}`.
- Otherwise, call `posttext.New(ctx, cfg, fetcher)`. On error (ping failed), log warning and fall back to `NullProvider`.
- Passes the provider into `jetstream.NewConsumer`.

### `go.mod`

Two new dependencies:
- `github.com/redis/go-redis/v9` — the canonical Go Redis client.
- `golang.org/x/sync` — for `singleflight` (already standard).

Test-only dependencies (under `require` with `// indirect` flag won't apply since they're explicit):
- `github.com/alicebob/miniredis/v2` — in-process Redis stub for `redis_test.go`.

### `docker-compose.yml`

- New `redis` service using `redis:7-alpine`. No volume (cache is disposable). Healthcheck via `redis-cli ping`. Internal network only; no port exposed.
- `push-gateway` service gains `REDIS_URL=redis://redis:6379/0` in its `environment` block and `depends_on.redis.condition: service_healthy`.

### `README.md`, `docs/NOTIFICATIONS.md`

- README gains a row for Redis in the configuration table and a short paragraph on subject-post enrichment.
- NOTIFICATIONS.md updates `like`, `repost`, `like-via-repost`, `repost-via-repost` examples to show a non-ZWSP body when the cache has a hit. Each example gets a note that empty bodies occur when the cache misses or the subject post has no text.

---

## Error handling

| Event | Response |
|---|---|
| Redis `GET` returns `redis.Nil` | Normal miss — fall through to fetch. |
| Redis `GET` returns connection error / timeout | Log at debug, treat as miss, fetch directly, skip the subsequent `SET`. |
| Redis `SET` returns error | Log at debug, continue. Notification path is unaffected. |
| AppView returns HTTP 200 with empty `posts` array | `found=false, err=nil` → cache negative. |
| AppView returns 4xx (except 404) or 5xx | Transient; don't cache, return `ok=false`. Next call retries. |
| AppView HTTP client error (timeout, DNS, TLS) | Same as 5xx. |
| Malformed AppView response (JSON parse error) | Same as transient — don't cache. |
| Startup Redis `PING` fails | Log warning, downgrade to `NullProvider`. Gateway continues. |
| Context deadline exceeded during fetch | Return `("", false, false)`. Don't cache. |

No new log categories. Everything goes to `[posttext]`, matching the existing `[jetstream]` / `[push]` / `[profile]` conventions.

**Log volume discipline.** Redis connection errors could fire on every notification if Redis is down. All Redis errors log at `debug` level, not `info`. Operators who suspect cache misbehavior grep `[posttext]` in debug logs; healthy operation stays silent.

---

## Testing

### `fetch_test.go` — AppView client

Uses `httptest.NewServer`. Verifies:

- Positive response with text and no embed → `(text, false, true, nil)`.
- Positive response with `embed.$type = app.bsky.embed.images` → `(..., true, ...)`.
- Positive with `embed.$type = app.bsky.embed.video` → `(..., true, ...)`.
- Positive with `embed.$type = app.bsky.embed.external` → `(..., true, ...)`.
- Positive with `embed.$type = app.bsky.embed.recordWithMedia` → `(..., true, ...)`.
- Positive with `embed.$type = app.bsky.embed.record` only → `(..., false, ...)`.
- Response with `posts: []` → `("", false, false, nil)` (found=false, no error).
- HTTP 500 → `("", false, false, err)`.
- HTTP 404 → `("", false, false, err)` (not-found is signaled by empty posts array, not by 404).
- Context cancelled mid-request → returns context error.
- Malformed JSON → returns parse error.
- URL encoding — subject URI containing special characters (`://`, `/`) round-trips correctly in the query string.

### `redis_test.go` — CachedProvider

Uses `miniredis.RunT(t)` plus a stub `Fetcher` that records calls and returns configured results.

- Empty cache + fetcher returns positive → one fetch → cache populated → second call hits cache, no fetch.
- Cache hit for negative → fetcher not called → returns `("", false, false)`.
- Cache hit for positive-empty-text → fetcher not called → returns `("", true, true)` (hasEmbed preserved).
- 100 concurrent calls for same URI → fetcher called exactly once (singleflight guard).
- Redis unreachable (close miniredis mid-test) → fetcher called every time, results returned, no caching side effects.
- Positive TTL honored — use `miniredis.FastForward(PositiveTTL + 1s)`, next call re-fetches.
- Negative TTL honored — shorter than positive.
- Transient fetcher error → no `SET` → next call retries.
- Malformed value in Redis (e.g., someone wrote garbage at the key) → treat as miss, re-fetch, overwrite.

### `provider_test.go` — NullProvider

One test: call with arbitrary URI, assert `ok=false`.

### `consumer_test.go` — integration with consumer

One new test: `handleLike` calls `c.postText.PostText(ctx, subjectURI)` exactly once. Uses a test-double `fakeProvider` that records calls. Doesn't assert on the final push body (same rationale as the previous feature's testing section — no sender mock exists).

### Manual verification in the implementation plan

A non-automated step: `docker compose up -d`, tail logs, confirm `[posttext] redis connected`, trigger a like against a registered DID, confirm the push payload body contains the subject-post text.

---

## Docker compose

```yaml
services:
  push-gateway:
    build: .
    ports:
      - "8080:8080"
    volumes:
      - push-data:/data
    environment:
      - PUSH_GATEWAY_DID=did:web:push.example.org
      - PUSH_GATEWAY_PORT=8080
      - SQLITE_PATH=/data/push-gateway.db
      - JETSTREAM_URL=wss://jetstream2.us-east.bsky.network/subscribe
      - EXPO_PUSH_ACCESS_TOKEN=
      - DEV_MODE=false
      - REDIS_URL=redis://redis:6379/0
    depends_on:
      redis:
        condition: service_healthy
    restart: unless-stopped
    healthcheck:
      test: ["CMD", "wget", "-q", "--spider", "http://localhost:8080/health"]
      interval: 30s
      timeout: 5s
      retries: 3
      start_period: 10s

  redis:
    image: redis:7-alpine
    restart: unless-stopped
    healthcheck:
      test: ["CMD", "redis-cli", "ping"]
      interval: 10s
      timeout: 3s
      retries: 3

volumes:
  push-data:
```

Deliberate choices:
- **No Redis volume.** The cache is disposable; losing it on restart is fine. Consumer warms it back up naturally as likes/reposts arrive.
- **`redis:7-alpine`** — small (~45 MB), stable.
- **No port exposed.** Redis is reachable only from the gateway container over the default compose network.
- **`depends_on.condition: service_healthy`** — ensures the gateway doesn't start calling Redis before it's ready.

---

## Out-of-scope hooks for future work

These are deliberate gaps that a future PR can fill without restructuring:

- **Cache hit-rate metrics.** `CachedProvider` could expose counters (hits, misses, negative hits, fetch errors). Today's `Consumer.GetStats` returns aggregate counters; wiring a new source is additive.
- **Profile cache in Redis.** The current in-memory cache in `internal/profile/resolver.go` survives within a single run. Moving it to Redis gives it durability across restarts. This spec is scoped to post text only.
- **Block graph in Redis.** Would enable multi-replica deployments. Out of scope.
- **Multi-URI `getPosts` batching.** The `getPosts` endpoint accepts up to 25 URIs per call. For a single like event we only have one URI to fetch, so batching doesn't help unless the consumer coalesces misses across events. A future optimization.
