# Subject-Post Enrichment via Redis Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Enrich `like`, `repost`, `like-via-repost`, and `repost-via-repost` push notifications with the text of the subject post, fetched via a Redis-backed cache that falls back to AppView's `app.bsky.feed.getPosts` on miss.

**Architecture:** A new `internal/posttext` package owns a `PostTextProvider` interface. Two implementations: `NullProvider` (no-op, used when Redis is unavailable) and `CachedProvider` (Redis + singleflight + `AppViewFetcher`). The consumer's `handleLike` / `handleRepost` call the provider with a timeout-bounded context, then pass results to `sendNotification` the same way `reply`/`mention`/`quote` already do. `main.go` reads configuration env vars and picks the implementation. Docker-compose gains a `redis:7-alpine` service.

**Tech Stack:** Go 1.25, `github.com/redis/go-redis/v9` (Redis client), `golang.org/x/sync/singleflight` (dedup concurrent misses), `github.com/alicebob/miniredis/v2` (test-only in-process Redis), `net/http/httptest` (AppView fetcher tests). No other new runtime dependencies.

**Spec:** `docs/superpowers/specs/2026-04-23-subject-post-enrichment-design.md`

---

## Project Context (read before every task)

`atproto-push-gateway` is a self-hosted push-notification gateway for ATProto. After the richer-notifications feature (merged as Task 1–6 on the `better-notifs` branch, base of this branch), reply/mention/quote notifications carry the triggering post's text. This plan adds the remaining four reasons that reference a *subject* post the gateway didn't see on commit.

Relevant paths:

```
internal/posttext/                (NEW) Redis-backed subject-post cache + AppView fetcher
internal/jetstream/consumer.go    consumer gains a PostTextProvider; handleLike/Repost use it
internal/notification/formatter.go  unchanged
cmd/server/main.go                reads env vars; constructs provider; passes to NewConsumer
docker-compose.yml                adds redis:7-alpine service
go.mod                            + go-redis/v9, + golang.org/x/sync, + miniredis/v2 (test-only)
README.md, docs/NOTIFICATIONS.md  doc updates
```

Go module path: `github.com/dracoblue/atproto-push-gateway`

**Running tests:**
```
cd /home/yolo/Projects/atproto-push-gateway
go test ./...
```

**Running one package:**
```
cd /home/yolo/Projects/atproto-push-gateway
go test ./internal/posttext -v
```

**Building:**
```
cd /home/yolo/Projects/atproto-push-gateway
go build ./...
```

**Current consumer seam** — `internal/jetstream/consumer.go:446` (`handleLike`) and `:469` (`handleRepost`) — currently passes `"", false` as `postText, hasEmbed` to `sendNotification`. This plan replaces those literals with values fetched via the provider.

---

## File Structure

| File | Status | Responsibility |
|---|---|---|
| `internal/posttext/provider.go` | Create | `PostTextProvider` interface + `NullProvider` |
| `internal/posttext/provider_test.go` | Create | NullProvider test |
| `internal/posttext/fetch.go` | Create | `AppViewFetcher` HTTP client for `app.bsky.feed.getPosts` |
| `internal/posttext/fetch_test.go` | Create | httptest-based fetcher tests |
| `internal/posttext/redis.go` | Create | `CachedProvider` with Redis + singleflight |
| `internal/posttext/redis_test.go` | Create | miniredis + stub Fetcher tests |
| `internal/jetstream/consumer.go` | Modify | Add `postText` field, update `NewConsumer` signature, wire into `handleLike`/`handleRepost` |
| `internal/jetstream/consumer_test.go` | Modify | Add fake provider + integration test |
| `cmd/server/main.go` | Modify | Read env vars, construct provider, pass to `NewConsumer` |
| `docker-compose.yml` | Modify | Add `redis:7-alpine` service, wire `REDIS_URL` into gateway |
| `go.mod`, `go.sum` | Modify | `go get` the three new dependencies |
| `README.md` | Modify | Document new env vars + behavior |
| `docs/NOTIFICATIONS.md` | Modify | Update like/repost/like-via-repost/repost-via-repost payloads |

---

## Task 1: Scaffold `internal/posttext` package with `PostTextProvider` interface

**Files:**
- Create: `internal/posttext/provider.go`
- Create: `internal/posttext/provider_test.go`

Builds the interface and the trivial `NullProvider`. No Redis, no HTTP. This task is pure scaffolding.

- [ ] **Step 1.1: Write the failing test**

Create `internal/posttext/provider_test.go` with this content:

```go
package posttext

import (
	"context"
	"testing"
)

func TestNullProviderAlwaysReturnsMiss(t *testing.T) {
	p := NullProvider{}
	text, hasEmbed, ok := p.PostText(context.Background(), "at://did:plc:abc/app.bsky.feed.post/xyz")
	if ok {
		t.Errorf("NullProvider should always return ok=false, got ok=true")
	}
	if text != "" {
		t.Errorf("NullProvider should return empty text, got %q", text)
	}
	if hasEmbed {
		t.Errorf("NullProvider should return hasEmbed=false, got true")
	}
}
```

- [ ] **Step 1.2: Run the test to verify it fails**

```
cd /home/yolo/Projects/atproto-push-gateway
go test ./internal/posttext -v
```

Expected: build error — package `posttext` has no `NullProvider` and no `PostTextProvider`.

- [ ] **Step 1.3: Create the provider implementation**

Create `internal/posttext/provider.go`:

```go
// Package posttext resolves the text and media-embed status of ATProto posts
// by AT-URI. Used by the push-notification consumer to enrich like/repost
// bodies with the subject post's content.
package posttext

import "context"

// PostTextProvider looks up a post by AT-URI. Implementations may hit a cache,
// the AppView, or return a synthetic miss. Callers pass a context whose
// deadline bounds how long PostText is allowed to block.
type PostTextProvider interface {
	// PostText returns (text, hasEmbed, ok) for the given AT-URI.
	// ok=false means "I don't have this post" — the caller falls back to
	// an unenriched notification (ZWSP body). Never returns an error;
	// transient failures surface as ok=false.
	PostText(ctx context.Context, uri string) (text string, hasEmbed bool, ok bool)
}

// NullProvider satisfies PostTextProvider without any backing store. Used when
// Redis is disabled (REDIS_URL empty or startup PING failed). Every call
// returns ok=false — the notification consumer falls through to today's
// unenriched behavior for like/repost reasons.
type NullProvider struct{}

func (NullProvider) PostText(context.Context, string) (string, bool, bool) {
	return "", false, false
}
```

- [ ] **Step 1.4: Run the test to verify it passes**

```
cd /home/yolo/Projects/atproto-push-gateway
go test ./internal/posttext -v
```

Expected: `TestNullProviderAlwaysReturnsMiss` PASS.

- [ ] **Step 1.5: Run the full suite**

```
cd /home/yolo/Projects/atproto-push-gateway
go test ./...
```

Expected: all packages PASS.

- [ ] **Step 1.6: Run gofmt**

```
cd /home/yolo/Projects/atproto-push-gateway
gofmt -l ./internal/posttext/
```

Expected: empty output.

- [ ] **Step 1.7: Commit**

```
git add internal/posttext/provider.go internal/posttext/provider_test.go
git commit -m "feat(posttext): add PostTextProvider interface and NullProvider"
```

---

## Task 2: Add `AppViewFetcher` HTTP client

**Files:**
- Create: `internal/posttext/fetch.go`
- Create: `internal/posttext/fetch_test.go`

Implements the AppView `getPosts` client. Uses `httptest.Server` for tests; no network. Stdlib only — no new dependencies.

The `getPosts` response shape per the ATProto lexicon:

```json
{
  "posts": [
    {
      "uri": "at://did:plc:alice/app.bsky.feed.post/abc",
      "cid": "bafyreixyz",
      "record": {
        "$type": "app.bsky.feed.post",
        "text": "hello world",
        "embed": {"$type": "app.bsky.embed.images"},
        "createdAt": "2026-04-11T00:00:00Z"
      }
    }
  ]
}
```

For unknown/deleted posts the AppView returns `{"posts": []}` with HTTP 200.

- [ ] **Step 2.1: Write the failing test**

Create `internal/posttext/fetch_test.go` with this content:

```go
package posttext

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// appViewResponse mirrors the structure the fetcher's tests return.
type appViewResponse struct {
	Posts []appViewPost `json:"posts"`
}

type appViewPost struct {
	URI    string                 `json:"uri"`
	Record map[string]interface{} `json:"record"`
}

func newFetchTestServer(t *testing.T, handler func(uri string) appViewResponse) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/xrpc/app.bsky.feed.getPosts") {
			t.Errorf("unexpected path: %s", r.URL.Path)
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		uris := r.URL.Query()["uris[]"]
		if len(uris) != 1 {
			t.Errorf("expected exactly one uris[] query param, got %d: %v", len(uris), uris)
		}
		resp := handler(uris[0])
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
}

func TestAppViewFetcherPositiveWithoutEmbed(t *testing.T) {
	ts := newFetchTestServer(t, func(uri string) appViewResponse {
		return appViewResponse{Posts: []appViewPost{{
			URI: uri,
			Record: map[string]interface{}{
				"$type": "app.bsky.feed.post",
				"text":  "hello world",
			},
		}}}
	})
	defer ts.Close()

	f := &AppViewFetcher{Client: ts.Client(), BaseURL: ts.URL}
	text, hasEmbed, found, err := f.Fetch(context.Background(), "at://did:plc:alice/app.bsky.feed.post/abc")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !found {
		t.Errorf("found = false, want true")
	}
	if text != "hello world" {
		t.Errorf("text = %q, want %q", text, "hello world")
	}
	if hasEmbed {
		t.Errorf("hasEmbed = true, want false")
	}
}

func TestAppViewFetcherMediaEmbedVariants(t *testing.T) {
	cases := []struct {
		name         string
		embedType    string
		wantHasEmbed bool
	}{
		{"images", "app.bsky.embed.images", true},
		{"video", "app.bsky.embed.video", true},
		{"external", "app.bsky.embed.external", true},
		{"recordWithMedia", "app.bsky.embed.recordWithMedia", true},
		{"record alone (plain quote)", "app.bsky.embed.record", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ts := newFetchTestServer(t, func(uri string) appViewResponse {
				return appViewResponse{Posts: []appViewPost{{
					URI: uri,
					Record: map[string]interface{}{
						"$type": "app.bsky.feed.post",
						"text":  "x",
						"embed": map[string]interface{}{
							"$type": tc.embedType,
						},
					},
				}}}
			})
			defer ts.Close()

			f := &AppViewFetcher{Client: ts.Client(), BaseURL: ts.URL}
			_, hasEmbed, _, err := f.Fetch(context.Background(), "at://did:plc:alice/app.bsky.feed.post/abc")
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if hasEmbed != tc.wantHasEmbed {
				t.Errorf("hasEmbed = %v, want %v", hasEmbed, tc.wantHasEmbed)
			}
		})
	}
}

func TestAppViewFetcherEmptyPostsArray(t *testing.T) {
	ts := newFetchTestServer(t, func(uri string) appViewResponse {
		return appViewResponse{Posts: []appViewPost{}}
	})
	defer ts.Close()

	f := &AppViewFetcher{Client: ts.Client(), BaseURL: ts.URL}
	text, hasEmbed, found, err := f.Fetch(context.Background(), "at://did:plc:nobody/app.bsky.feed.post/gone")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if found {
		t.Errorf("found = true, want false for empty posts array")
	}
	if text != "" || hasEmbed {
		t.Errorf("text/hasEmbed = (%q, %v), want (\"\", false)", text, hasEmbed)
	}
}

func TestAppViewFetcherHTTPError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer ts.Close()

	f := &AppViewFetcher{Client: ts.Client(), BaseURL: ts.URL}
	_, _, _, err := f.Fetch(context.Background(), "at://did:plc:alice/app.bsky.feed.post/abc")
	if err == nil {
		t.Errorf("expected error on HTTP 500, got nil")
	}
}

func TestAppViewFetcherMalformedJSON(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("{not json"))
	}))
	defer ts.Close()

	f := &AppViewFetcher{Client: ts.Client(), BaseURL: ts.URL}
	_, _, _, err := f.Fetch(context.Background(), "at://did:plc:alice/app.bsky.feed.post/abc")
	if err == nil {
		t.Errorf("expected error on malformed JSON, got nil")
	}
}

func TestAppViewFetcherContextCancelled(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(100 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"posts":[]}`))
	}))
	defer ts.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()

	f := &AppViewFetcher{Client: ts.Client(), BaseURL: ts.URL}
	_, _, _, err := f.Fetch(ctx, "at://did:plc:alice/app.bsky.feed.post/abc")
	if err == nil {
		t.Errorf("expected error on context deadline, got nil")
	}
}

func TestAppViewFetcherURIEncoding(t *testing.T) {
	// Subject URIs contain ':' and '/' — make sure they round-trip through
	// query-string encoding verbatim.
	const uri = "at://did:plc:alice/app.bsky.feed.post/3kco5r7xsgb2p"
	var seenURI string
	ts := newFetchTestServer(t, func(u string) appViewResponse {
		seenURI = u
		return appViewResponse{Posts: []appViewPost{{
			URI:    u,
			Record: map[string]interface{}{"text": "x"},
		}}}
	})
	defer ts.Close()

	f := &AppViewFetcher{Client: ts.Client(), BaseURL: ts.URL}
	_, _, _, err := f.Fetch(context.Background(), uri)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if seenURI != uri {
		t.Errorf("server saw uri %q, expected %q", seenURI, uri)
	}
}
```

- [ ] **Step 2.2: Run the tests to verify they fail**

```
cd /home/yolo/Projects/atproto-push-gateway
go test ./internal/posttext -v
```

Expected: build error — `AppViewFetcher` undefined.

- [ ] **Step 2.3: Create the fetcher implementation**

Create `internal/posttext/fetch.go`:

```go
package posttext

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
)

// Fetcher retrieves a post by AT-URI. CachedProvider uses it as the miss-path
// backend. Exported as an interface so CachedProvider's tests can stub it.
type Fetcher interface {
	// Fetch returns (text, hasEmbed, found, err).
	// found=false with err=nil means the post doesn't exist (treated as a
	// hard negative — safe to negative-cache). err != nil means a transient
	// failure (HTTP, parse, network) — callers should not cache these.
	Fetch(ctx context.Context, uri string) (text string, hasEmbed bool, found bool, err error)
}

// DefaultAppViewBaseURL is Bluesky's public read-only AppView.
const DefaultAppViewBaseURL = "https://public.api.bsky.app"

// AppViewFetcher is the production Fetcher. It calls
// {BaseURL}/xrpc/app.bsky.feed.getPosts?uris[]=<uri> and parses the response.
type AppViewFetcher struct {
	Client  *http.Client // required; caller owns lifecycle
	BaseURL string       // defaults to DefaultAppViewBaseURL when empty
}

// getPostsResponse mirrors the relevant subset of the AppView response.
// Other fields (cid, author, indexedAt, ...) are ignored.
type getPostsResponse struct {
	Posts []struct {
		URI    string `json:"uri"`
		Record struct {
			Text  string `json:"text"`
			Embed *struct {
				Type string `json:"$type"`
			} `json:"embed,omitempty"`
		} `json:"record"`
	} `json:"posts"`
}

func (f *AppViewFetcher) Fetch(ctx context.Context, uri string) (string, bool, bool, error) {
	base := f.BaseURL
	if base == "" {
		base = DefaultAppViewBaseURL
	}
	endpoint := base + "/xrpc/app.bsky.feed.getPosts"

	q := url.Values{}
	q.Set("uris[]", uri)
	reqURL := endpoint + "?" + q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return "", false, false, fmt.Errorf("build request: %w", err)
	}
	resp, err := f.Client.Do(req)
	if err != nil {
		return "", false, false, fmt.Errorf("http do: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", false, false, fmt.Errorf("appview status %d", resp.StatusCode)
	}

	var parsed getPostsResponse
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return "", false, false, fmt.Errorf("decode response: %w", err)
	}

	if len(parsed.Posts) == 0 {
		return "", false, false, nil
	}

	post := parsed.Posts[0]
	hasEmbed := false
	if post.Record.Embed != nil {
		switch post.Record.Embed.Type {
		case "app.bsky.embed.images",
			"app.bsky.embed.video",
			"app.bsky.embed.external",
			"app.bsky.embed.recordWithMedia":
			hasEmbed = true
		}
	}
	return post.Record.Text, hasEmbed, true, nil
}
```

**Note on `url.Values.Set` for `uris[]`:** Go's `url.Values` stores keys as strings verbatim. The key `uris[]` round-trips correctly because `[` and `]` are not reserved in the query-string BNF (only in URI paths). This is the same approach the ATProto client libraries use.

- [ ] **Step 2.4: Run the tests to verify they pass**

```
cd /home/yolo/Projects/atproto-push-gateway
go test ./internal/posttext -v
```

Expected: all 7 test functions PASS (TestAppViewFetcherPositiveWithoutEmbed, TestAppViewFetcherMediaEmbedVariants with 5 subtests, TestAppViewFetcherEmptyPostsArray, TestAppViewFetcherHTTPError, TestAppViewFetcherMalformedJSON, TestAppViewFetcherContextCancelled, TestAppViewFetcherURIEncoding).

- [ ] **Step 2.5: Full suite**

```
cd /home/yolo/Projects/atproto-push-gateway
go test ./...
```

Expected: all packages PASS.

- [ ] **Step 2.6: gofmt**

```
cd /home/yolo/Projects/atproto-push-gateway
gofmt -l ./internal/posttext/
```

Expected: empty.

- [ ] **Step 2.7: Commit**

```
git add internal/posttext/fetch.go internal/posttext/fetch_test.go
git commit -m "feat(posttext): add AppViewFetcher for app.bsky.feed.getPosts"
```

---

## Task 3: Add `CachedProvider` with Redis and singleflight

**Files:**
- Create: `internal/posttext/redis.go`
- Create: `internal/posttext/redis_test.go`
- Modify: `go.mod`, `go.sum`

Brings in the three new dependencies, implements `CachedProvider`, and tests it with `miniredis` + a stub `Fetcher`.

- [ ] **Step 3.1: Add runtime dependencies**

```
cd /home/yolo/Projects/atproto-push-gateway
go get github.com/redis/go-redis/v9@latest
go get golang.org/x/sync@latest
```

Expected: `go.mod` gets two new `require` entries. `go.sum` is populated.

- [ ] **Step 3.2: Add the test-only dependency**

```
cd /home/yolo/Projects/atproto-push-gateway
go get -t github.com/alicebob/miniredis/v2@latest
```

The `-t` flag fetches it as a test dependency. It will be added to `go.mod` like any other require but only used by `*_test.go` files, so it doesn't bloat production builds.

- [ ] **Step 3.3: Write the failing tests**

Create `internal/posttext/redis_test.go` with this content:

```go
package posttext

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

// stubFetcher lets redis_test.go drive Fetcher.Fetch behavior without network.
type stubFetcher struct {
	mu    sync.Mutex
	calls atomic.Int64

	text     string
	hasEmbed bool
	found    bool
	err      error

	// delay lets us exercise singleflight's dedup window.
	delay time.Duration
}

func (s *stubFetcher) Fetch(ctx context.Context, uri string) (string, bool, bool, error) {
	s.calls.Add(1)
	if s.delay > 0 {
		select {
		case <-time.After(s.delay):
		case <-ctx.Done():
			return "", false, false, ctx.Err()
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.text, s.hasEmbed, s.found, s.err
}

// newTestProvider returns a CachedProvider wired to a miniredis instance
// and the given stub. t.Cleanup closes both.
func newTestProvider(t *testing.T, stub *stubFetcher) (*CachedProvider, *miniredis.Miniredis) {
	t.Helper()
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = client.Close() })

	p := &CachedProvider{
		client:  client,
		fetcher: stub,
		cfg: Config{
			PositiveTTL: 1 * time.Hour,
			NegativeTTL: 1 * time.Minute,
		},
	}
	return p, mr
}

func TestCachedProviderMissThenFetchThenCached(t *testing.T) {
	stub := &stubFetcher{text: "hello", hasEmbed: false, found: true}
	p, _ := newTestProvider(t, stub)

	// First call: miss → fetch.
	text, hasEmbed, ok := p.PostText(context.Background(), "at://did:plc:alice/app.bsky.feed.post/abc")
	if !ok || text != "hello" || hasEmbed {
		t.Errorf("first call = (%q, %v, %v), want (\"hello\", false, true)", text, hasEmbed, ok)
	}
	if got := stub.calls.Load(); got != 1 {
		t.Fatalf("fetcher called %d times, want 1", got)
	}

	// Second call: hit → no fetch.
	text2, hasEmbed2, ok2 := p.PostText(context.Background(), "at://did:plc:alice/app.bsky.feed.post/abc")
	if !ok2 || text2 != "hello" || hasEmbed2 {
		t.Errorf("second call = (%q, %v, %v), want (\"hello\", false, true)", text2, hasEmbed2, ok2)
	}
	if got := stub.calls.Load(); got != 1 {
		t.Errorf("fetcher called %d times after cache hit, want 1", got)
	}
}

func TestCachedProviderHitPreservesHasEmbed(t *testing.T) {
	stub := &stubFetcher{text: "pic post", hasEmbed: true, found: true}
	p, _ := newTestProvider(t, stub)

	uri := "at://did:plc:alice/app.bsky.feed.post/xyz"
	_, _, _ = p.PostText(context.Background(), uri) // prime cache

	text, hasEmbed, ok := p.PostText(context.Background(), uri)
	if !ok || text != "pic post" || !hasEmbed {
		t.Errorf("cached call = (%q, %v, %v), want (\"pic post\", true, true)", text, hasEmbed, ok)
	}
}

func TestCachedProviderEmptyTextPositive(t *testing.T) {
	// Image-only post: text="" but found=true, hasEmbed=true.
	// Cache stores this as a positive entry (mirrors getPosts).
	stub := &stubFetcher{text: "", hasEmbed: true, found: true}
	p, _ := newTestProvider(t, stub)

	uri := "at://did:plc:alice/app.bsky.feed.post/pic"
	text, hasEmbed, ok := p.PostText(context.Background(), uri)
	if !ok || text != "" || !hasEmbed {
		t.Errorf("first call = (%q, %v, %v), want (\"\", true, true)", text, hasEmbed, ok)
	}

	// Second call hits cache.
	text, hasEmbed, ok = p.PostText(context.Background(), uri)
	if !ok || text != "" || !hasEmbed {
		t.Errorf("cached call = (%q, %v, %v), want (\"\", true, true)", text, hasEmbed, ok)
	}
	if stub.calls.Load() != 1 {
		t.Errorf("fetcher called %d times, want 1", stub.calls.Load())
	}
}

func TestCachedProviderNegativeCacheHit(t *testing.T) {
	stub := &stubFetcher{found: false}
	p, _ := newTestProvider(t, stub)

	uri := "at://did:plc:nobody/app.bsky.feed.post/gone"

	// First call: miss → fetcher says not found → negative cache.
	_, _, ok := p.PostText(context.Background(), uri)
	if ok {
		t.Errorf("first call ok=true, want false for not-found")
	}
	if stub.calls.Load() != 1 {
		t.Errorf("fetcher called %d times, want 1", stub.calls.Load())
	}

	// Second call: negative hit → no fetch.
	_, _, ok = p.PostText(context.Background(), uri)
	if ok {
		t.Errorf("second call ok=true, want false")
	}
	if stub.calls.Load() != 1 {
		t.Errorf("fetcher called %d times after negative cache hit, want 1", stub.calls.Load())
	}
}

func TestCachedProviderSingleflightDedups(t *testing.T) {
	stub := &stubFetcher{text: "hi", found: true, delay: 50 * time.Millisecond}
	p, _ := newTestProvider(t, stub)

	uri := "at://did:plc:alice/app.bsky.feed.post/concurrent"
	const N = 100

	var wg sync.WaitGroup
	wg.Add(N)
	for i := 0; i < N; i++ {
		go func() {
			defer wg.Done()
			_, _, _ = p.PostText(context.Background(), uri)
		}()
	}
	wg.Wait()

	if got := stub.calls.Load(); got != 1 {
		t.Errorf("fetcher called %d times, want 1 (singleflight dedup)", got)
	}
}

func TestCachedProviderTransientErrorNotCached(t *testing.T) {
	stub := &stubFetcher{err: errors.New("boom")}
	p, _ := newTestProvider(t, stub)

	uri := "at://did:plc:alice/app.bsky.feed.post/abc"

	// Two consecutive calls; both should fetch because the error wasn't cached.
	_, _, ok1 := p.PostText(context.Background(), uri)
	_, _, ok2 := p.PostText(context.Background(), uri)
	if ok1 || ok2 {
		t.Errorf("errored calls should return ok=false; got %v, %v", ok1, ok2)
	}
	if got := stub.calls.Load(); got != 2 {
		t.Errorf("fetcher called %d times, want 2 (no caching on transient errors)", got)
	}
}

func TestCachedProviderPositiveTTLExpiry(t *testing.T) {
	stub := &stubFetcher{text: "hello", found: true}
	p, mr := newTestProvider(t, stub)

	uri := "at://did:plc:alice/app.bsky.feed.post/ttl"
	_, _, _ = p.PostText(context.Background(), uri) // prime

	// Advance miniredis past PositiveTTL (1h).
	mr.FastForward(2 * time.Hour)

	_, _, _ = p.PostText(context.Background(), uri)
	if got := stub.calls.Load(); got != 2 {
		t.Errorf("after TTL expiry, fetcher called %d times, want 2", got)
	}
}

func TestCachedProviderNegativeTTLExpiry(t *testing.T) {
	stub := &stubFetcher{found: false}
	p, mr := newTestProvider(t, stub)

	uri := "at://did:plc:nobody/app.bsky.feed.post/gone"
	_, _, _ = p.PostText(context.Background(), uri) // prime negative

	mr.FastForward(2 * time.Minute) // past NegativeTTL

	_, _, _ = p.PostText(context.Background(), uri)
	if got := stub.calls.Load(); got != 2 {
		t.Errorf("after negative TTL expiry, fetcher called %d times, want 2", got)
	}
}

func TestCachedProviderRedisUnreachableFallsThroughToFetcher(t *testing.T) {
	stub := &stubFetcher{text: "hi", found: true}
	p, mr := newTestProvider(t, stub)

	// Kill miniredis. Subsequent Redis operations will return connection errors.
	mr.Close()

	uri := "at://did:plc:alice/app.bsky.feed.post/abc"

	// Even with Redis down, the fetcher should still be called and the
	// result returned to the caller.
	_, _, ok1 := p.PostText(context.Background(), uri)
	_, _, ok2 := p.PostText(context.Background(), uri)
	if !ok1 || !ok2 {
		t.Errorf("calls returned ok=false with Redis down; want ok=true on fetcher success")
	}
	// Without caching, each call re-fetches.
	if got := stub.calls.Load(); got != 2 {
		t.Errorf("fetcher called %d times with Redis down, want 2", got)
	}
}

func TestCachedProviderMalformedCacheValueRefetches(t *testing.T) {
	stub := &stubFetcher{text: "ok", found: true}
	p, mr := newTestProvider(t, stub)

	uri := "at://did:plc:alice/app.bsky.feed.post/garbage"
	// Write garbage directly at the cache key.
	_ = mr.Set(cacheKey(uri), "not json at all {")

	text, _, ok := p.PostText(context.Background(), uri)
	if !ok || text != "ok" {
		t.Errorf("malformed cache → (%q, _, %v), want (\"ok\", _, true)", text, ok)
	}
	if stub.calls.Load() != 1 {
		t.Errorf("fetcher called %d times, want 1 (treat malformed as miss)", stub.calls.Load())
	}
}

func TestNewPingsRedis(t *testing.T) {
	mr := miniredis.RunT(t)

	cfg := Config{
		URL:         fmt.Sprintf("redis://%s/0", mr.Addr()),
		PositiveTTL: 1 * time.Hour,
		NegativeTTL: 1 * time.Minute,
	}

	stub := &stubFetcher{}
	p, err := New(context.Background(), cfg, stub)
	if err != nil {
		t.Fatalf("New returned error against reachable miniredis: %v", err)
	}
	if p == nil {
		t.Fatalf("New returned nil provider")
	}
	defer p.Close()
}

func TestNewFailsWhenRedisUnreachable(t *testing.T) {
	cfg := Config{
		URL:         "redis://127.0.0.1:1/0", // port 1, guaranteed unused
		PositiveTTL: 1 * time.Hour,
		NegativeTTL: 1 * time.Minute,
	}

	stub := &stubFetcher{}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	p, err := New(ctx, cfg, stub)
	if err == nil {
		t.Errorf("New returned nil error for unreachable Redis")
		if p != nil {
			_ = p.Close()
		}
	}
}
```

- [ ] **Step 3.4: Run the tests to confirm they fail**

```
cd /home/yolo/Projects/atproto-push-gateway
go test ./internal/posttext -v
```

Expected: build errors — `CachedProvider`, `Config`, `New`, `cacheKey` all undefined.

- [ ] **Step 3.5: Create the implementation**

Create `internal/posttext/redis.go`:

```go
package posttext

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/redis/go-redis/v9"
	"golang.org/x/sync/singleflight"
)

// Config controls the CachedProvider.
type Config struct {
	// URL is the Redis connection URL (redis://host:port/db).
	URL string
	// PositiveTTL is how long a successful fetch stays cached.
	PositiveTTL time.Duration
	// NegativeTTL is how long "post not found" stays cached. Shorter than
	// PositiveTTL on purpose — a post that was deleted and then reappears
	// (unusual) shouldn't be stuck in negative cache for a whole day.
	NegativeTTL time.Duration
}

// CachedProvider serves PostText from Redis, falling back to a Fetcher on miss.
// Concurrent misses for the same URI are coalesced via singleflight so a popular
// post only fires one AppView call even under heavy load.
type CachedProvider struct {
	client  *redis.Client
	fetcher Fetcher
	group   singleflight.Group
	cfg     Config
}

// New constructs a CachedProvider. It pings Redis once to confirm reachability.
// If the ping fails, it returns an error — the caller should fall back to
// NullProvider. Transient failures during operation (after a successful ping)
// are handled per-call; New does not retry.
func New(ctx context.Context, cfg Config, fetcher Fetcher) (*CachedProvider, error) {
	opts, err := redis.ParseURL(cfg.URL)
	if err != nil {
		return nil, fmt.Errorf("parse REDIS_URL: %w", err)
	}
	// Short per-op timeouts so a dead Redis doesn't stall dispatch. The
	// DialTimeout only applies to the initial connect (pooled thereafter).
	opts.DialTimeout = 2 * time.Second
	opts.ReadTimeout = 500 * time.Millisecond
	opts.WriteTimeout = 500 * time.Millisecond
	opts.PoolSize = 10

	client := redis.NewClient(opts)
	if err := client.Ping(ctx).Err(); err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("redis ping: %w", err)
	}
	return &CachedProvider{
		client:  client,
		fetcher: fetcher,
		cfg:     cfg,
	}, nil
}

// Close releases the Redis client's connection pool.
func (p *CachedProvider) Close() error {
	return p.client.Close()
}

// cacheKey builds the Redis key for a post's cache entry. Versioned so a future
// format change can bump to v2 without colliding with stale keys.
func cacheKey(uri string) string {
	return "posttext:v1:" + uri
}

// cacheEntry is the positive-hit serialization.
type cacheEntry struct {
	Text     string `json:"t"`
	HasEmbed bool   `json:"e"`
}

// negativeValue is the literal string stored in Redis for negative hits.
const negativeValue = "null"

// fetchResult bundles what singleflight passes through.
type fetchResult struct {
	text     string
	hasEmbed bool
	ok       bool
}

func (p *CachedProvider) PostText(ctx context.Context, uri string) (string, bool, bool) {
	key := cacheKey(uri)

	// Cache read.
	val, err := p.client.Get(ctx, key).Result()
	switch {
	case errors.Is(err, redis.Nil):
		// Miss. Fall through to fetch.
	case err != nil:
		// Redis trouble — log at debug and fall through without attempting SET.
		log.Printf("[posttext] redis get error for %s: %v", uri, err)
		return p.fetchWithoutCache(ctx, uri)
	case val == negativeValue:
		return "", false, false
	default:
		var e cacheEntry
		if jsonErr := json.Unmarshal([]byte(val), &e); jsonErr == nil {
			return e.Text, e.HasEmbed, true
		}
		// Malformed cache value — treat as miss, refetch, overwrite.
		log.Printf("[posttext] malformed cache value for %s: %v", uri, jsonErr)
	}

	// Singleflight-dedup the miss path.
	v, _, _ := p.group.Do(uri, func() (any, error) {
		text, hasEmbed, found, fetchErr := p.fetcher.Fetch(ctx, uri)
		if fetchErr != nil {
			log.Printf("[posttext] fetch error for %s: %v", uri, fetchErr)
			// Transient — don't cache.
			return fetchResult{}, nil
		}
		// Cache the result.
		var value string
		var ttl time.Duration
		if found {
			b, _ := json.Marshal(cacheEntry{Text: text, HasEmbed: hasEmbed})
			value, ttl = string(b), p.cfg.PositiveTTL
		} else {
			value, ttl = negativeValue, p.cfg.NegativeTTL
		}
		if setErr := p.client.Set(ctx, key, value, ttl).Err(); setErr != nil {
			log.Printf("[posttext] redis set error for %s: %v", uri, setErr)
		}
		return fetchResult{text: text, hasEmbed: hasEmbed, ok: found}, nil
	})

	r := v.(fetchResult)
	return r.text, r.hasEmbed, r.ok
}

// fetchWithoutCache bypasses Redis entirely. Used when Redis is unreachable.
// Still singleflight-dedups so concurrent misses during a Redis outage don't
// hammer AppView.
func (p *CachedProvider) fetchWithoutCache(ctx context.Context, uri string) (string, bool, bool) {
	v, _, _ := p.group.Do(uri, func() (any, error) {
		text, hasEmbed, found, err := p.fetcher.Fetch(ctx, uri)
		if err != nil {
			log.Printf("[posttext] fetch error (redis down) for %s: %v", uri, err)
			return fetchResult{}, nil
		}
		return fetchResult{text: text, hasEmbed: hasEmbed, ok: found}, nil
	})
	r := v.(fetchResult)
	return r.text, r.hasEmbed, r.ok
}
```

- [ ] **Step 3.6: Run the tests to confirm they pass**

```
cd /home/yolo/Projects/atproto-push-gateway
go test ./internal/posttext -v
```

Expected: all 13 test functions PASS.

- [ ] **Step 3.7: Run the full suite**

```
cd /home/yolo/Projects/atproto-push-gateway
go test ./...
```

Expected: all packages PASS.

- [ ] **Step 3.8: Tidy go.mod**

```
cd /home/yolo/Projects/atproto-push-gateway
go mod tidy
```

This removes any stale `// indirect` markers and organizes the require block.

- [ ] **Step 3.9: gofmt**

```
cd /home/yolo/Projects/atproto-push-gateway
gofmt -l ./internal/posttext/
```

Expected: empty.

- [ ] **Step 3.10: Commit**

```
git add internal/posttext/redis.go internal/posttext/redis_test.go go.mod go.sum
git commit -m "feat(posttext): add CachedProvider with Redis and singleflight

Uses go-redis/v9 + x/sync/singleflight. Concurrent misses on the same
URI are coalesced to a single fetch. Empty-text posts are cached
positively (matching getPosts verbatim). Transient fetcher errors are
never cached. Redis outages degrade gracefully: GET errors fall
through to the fetcher, SET errors are best-effort logged. Startup
PING verifies reachability; if it fails, New returns an error and the
caller falls back to NullProvider."
```

---

## Task 4: Wire `PostTextProvider` into `Consumer`

**Files:**
- Modify: `internal/jetstream/consumer.go`
- Modify: `internal/jetstream/consumer_test.go`

Add a `PostTextProvider` field to `Consumer`. Update `NewConsumer` signature. In `handleLike` and `handleRepost`, call the provider with a timeout-bounded context and pass the result to `sendNotification`.

- [ ] **Step 4.1: Write the failing tests**

Append this to `internal/jetstream/consumer_test.go`:

```go
// fakePostTextProvider records calls and returns canned results.
type fakePostTextProvider struct {
	mu       sync.Mutex
	calls    []string
	text     string
	hasEmbed bool
	ok       bool
}

func (f *fakePostTextProvider) PostText(ctx context.Context, uri string) (string, bool, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, uri)
	return f.text, f.hasEmbed, f.ok
}

func TestHandleLikeCallsPostTextProvider(t *testing.T) {
	// Build a minimal Consumer — we only need the postText field plus store,
	// and we only exercise the code path up to sendNotification. sendNotification
	// short-circuits when IsRegistered(targetDID) is false, which is the default
	// for an empty store.
	fake := &fakePostTextProvider{text: "hello", hasEmbed: false, ok: true}
	tmpStore, err := store.New(":memory:")
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	defer tmpStore.Close()

	c := &Consumer{
		store:        tmpStore,
		postText:     fake,
		fetchTimeout: 1 * time.Second,
	}

	rawLike := json.RawMessage(`{
		"$type": "app.bsky.feed.like",
		"subject": {"uri": "at://did:plc:target/app.bsky.feed.post/xyz", "cid": "bafy"}
	}`)
	c.handleLike("did:plc:actor", "rk1", rawLike)

	fake.mu.Lock()
	defer fake.mu.Unlock()
	if len(fake.calls) != 1 {
		t.Fatalf("fake.calls = %v, want 1 call", fake.calls)
	}
	if fake.calls[0] != "at://did:plc:target/app.bsky.feed.post/xyz" {
		t.Errorf("fake.calls[0] = %q, want subject URI", fake.calls[0])
	}
}

func TestHandleRepostCallsPostTextProvider(t *testing.T) {
	fake := &fakePostTextProvider{text: "rp", hasEmbed: true, ok: true}
	tmpStore, err := store.New(":memory:")
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	defer tmpStore.Close()

	c := &Consumer{
		store:        tmpStore,
		postText:     fake,
		fetchTimeout: 1 * time.Second,
	}

	rawRepost := json.RawMessage(`{
		"$type": "app.bsky.feed.repost",
		"subject": {"uri": "at://did:plc:target/app.bsky.feed.post/xyz", "cid": "bafy"}
	}`)
	c.handleRepost("did:plc:actor", "rk1", rawRepost)

	fake.mu.Lock()
	defer fake.mu.Unlock()
	if len(fake.calls) != 1 {
		t.Fatalf("fake.calls = %v, want 1 call", fake.calls)
	}
}

func TestHandleLikeWithViaFieldCallsProviderOnce(t *testing.T) {
	// like-via-repost fires two notifications (like + like-via-repost), but
	// they share the same subject URI, so the provider should only be
	// consulted once.
	fake := &fakePostTextProvider{text: "x", ok: true}
	tmpStore, err := store.New(":memory:")
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	defer tmpStore.Close()

	c := &Consumer{
		store:        tmpStore,
		postText:     fake,
		fetchTimeout: 1 * time.Second,
	}

	rawLike := json.RawMessage(`{
		"$type": "app.bsky.feed.like",
		"subject": {"uri": "at://did:plc:target/app.bsky.feed.post/xyz", "cid": "bafy"},
		"via": {"uri": "at://did:plc:reposter/app.bsky.feed.repost/rp1", "cid": "bafy2"}
	}`)
	c.handleLike("did:plc:actor", "rk1", rawLike)

	fake.mu.Lock()
	defer fake.mu.Unlock()
	if len(fake.calls) != 1 {
		t.Errorf("like-via-repost called provider %d times, want 1", len(fake.calls))
	}
}
```

Also update the imports at the top of `internal/jetstream/consumer_test.go`. The current import block is:

```go
import (
	"encoding/json"
	"testing"
)
```

Replace with:

```go
import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/dracoblue/atproto-push-gateway/internal/store"
)
```

- [ ] **Step 4.2: Run the tests to confirm they fail**

```
cd /home/yolo/Projects/atproto-push-gateway
go test ./internal/jetstream -v
```

Expected: build errors — `Consumer.postText` undefined.

- [ ] **Step 4.3: Update `Consumer` struct and `NewConsumer`**

In `internal/jetstream/consumer.go`, locate the `Consumer` struct definition (around line 105). It currently starts:

```go
type Consumer struct {
	url             string
	store           *store.Store
	sender          *push.MultiSender
	profileResolver *profile.Resolver
	...
```

Add a `postText` field. The new struct declaration should be:

```go
type Consumer struct {
	url             string
	store           *store.Store
	sender          *push.MultiSender
	profileResolver *profile.Resolver
	postText        posttext.PostTextProvider
	fetchTimeout    time.Duration
	lastCursor      atomic.Int64
	stopCh          chan struct{}
	startCh         chan struct{} // closed when first token registered
	commitCh        chan dispatchItem
	eventsDropped   atomic.Int64

	// Stats
	eventsReceived atomic.Int64
	bytesReceived  atomic.Int64
	pushesSent     atomic.Int64
	pushErrors     atomic.Int64
	matchedEvents  atomic.Int64
}
```

(The existing fields are preserved; only `postText` and `fetchTimeout` are added, placed right after `profileResolver` for logical grouping.)

Update `NewConsumer` (around line 147):

```go
func NewConsumer(
	url string,
	s *store.Store,
	sender *push.MultiSender,
	profileResolver *profile.Resolver,
	postText posttext.PostTextProvider,
	fetchTimeout time.Duration,
) *Consumer {
	c := &Consumer{
		url:             url,
		store:           s,
		sender:          sender,
		profileResolver: profileResolver,
		postText:        postText,
		fetchTimeout:    fetchTimeout,
		stopCh:          make(chan struct{}),
		startCh:         make(chan struct{}),
		commitCh:        make(chan dispatchItem, 1024),
	}
	if s.HasRegisteredDIDs() {
		close(c.startCh)
	}
	return c
}
```

Add the import for the new package. The existing import block in `consumer.go` currently looks like:

```go
	"github.com/dracoblue/atproto-push-gateway/internal/notification"
	"github.com/dracoblue/atproto-push-gateway/internal/profile"
	"github.com/dracoblue/atproto-push-gateway/internal/push"
	"github.com/dracoblue/atproto-push-gateway/internal/store"
```

Add `"github.com/dracoblue/atproto-push-gateway/internal/posttext"` alphabetically:

```go
	"github.com/dracoblue/atproto-push-gateway/internal/notification"
	"github.com/dracoblue/atproto-push-gateway/internal/posttext"
	"github.com/dracoblue/atproto-push-gateway/internal/profile"
	"github.com/dracoblue/atproto-push-gateway/internal/push"
	"github.com/dracoblue/atproto-push-gateway/internal/store"
```

- [ ] **Step 4.4: Update `handleLike` to call the provider**

Replace the body of `handleLike` in `internal/jetstream/consumer.go`. The current body is:

```go
func (c *Consumer) handleLike(actorDID string, rkey string, record json.RawMessage) {
	var like LikeRecord
	if err := json.Unmarshal(record, &like); err != nil {
		return
	}

	targetDID := extractDIDFromURI(like.Subject.URI)
	if targetDID == "" || targetDID == actorDID {
		return
	}

	recordURI := fmt.Sprintf("at://%s/app.bsky.feed.like/%s", actorDID, rkey)
	c.sendNotification(actorDID, targetDID, "like", recordURI, like.Subject.URI, "", false)

	// like-via-repost: notify the reposter if discovered via their repost
	if like.Via != nil {
		reposterDID := extractDIDFromURI(like.Via.URI)
		if reposterDID != "" && reposterDID != actorDID && reposterDID != targetDID {
			c.sendNotification(actorDID, reposterDID, "like-via-repost", recordURI, like.Subject.URI, "", false)
		}
	}
}
```

Replace with:

```go
func (c *Consumer) handleLike(actorDID string, rkey string, record json.RawMessage) {
	var like LikeRecord
	if err := json.Unmarshal(record, &like); err != nil {
		return
	}

	targetDID := extractDIDFromURI(like.Subject.URI)
	if targetDID == "" || targetDID == actorDID {
		return
	}

	postText, hasEmbed := c.fetchSubjectPost(like.Subject.URI)
	recordURI := fmt.Sprintf("at://%s/app.bsky.feed.like/%s", actorDID, rkey)
	c.sendNotification(actorDID, targetDID, "like", recordURI, like.Subject.URI, postText, hasEmbed)

	// like-via-repost: notify the reposter if discovered via their repost.
	// Same subject post, so reuse the already-fetched text.
	if like.Via != nil {
		reposterDID := extractDIDFromURI(like.Via.URI)
		if reposterDID != "" && reposterDID != actorDID && reposterDID != targetDID {
			c.sendNotification(actorDID, reposterDID, "like-via-repost", recordURI, like.Subject.URI, postText, hasEmbed)
		}
	}
}
```

- [ ] **Step 4.5: Update `handleRepost` similarly**

Current body of `handleRepost`:

```go
func (c *Consumer) handleRepost(actorDID string, rkey string, record json.RawMessage) {
	var repost RepostRecord
	if err := json.Unmarshal(record, &repost); err != nil {
		return
	}

	targetDID := extractDIDFromURI(repost.Subject.URI)
	if targetDID == "" || targetDID == actorDID {
		return
	}

	recordURI := fmt.Sprintf("at://%s/app.bsky.feed.repost/%s", actorDID, rkey)
	c.sendNotification(actorDID, targetDID, "repost", recordURI, repost.Subject.URI, "", false)

	// repost-via-repost: notify the original reposter if discovered via their repost
	if repost.Via != nil {
		reposterDID := extractDIDFromURI(repost.Via.URI)
		if reposterDID != "" && reposterDID != actorDID && reposterDID != targetDID {
			c.sendNotification(actorDID, reposterDID, "repost-via-repost", recordURI, repost.Subject.URI, "", false)
		}
	}
}
```

Replace with:

```go
func (c *Consumer) handleRepost(actorDID string, rkey string, record json.RawMessage) {
	var repost RepostRecord
	if err := json.Unmarshal(record, &repost); err != nil {
		return
	}

	targetDID := extractDIDFromURI(repost.Subject.URI)
	if targetDID == "" || targetDID == actorDID {
		return
	}

	postText, hasEmbed := c.fetchSubjectPost(repost.Subject.URI)
	recordURI := fmt.Sprintf("at://%s/app.bsky.feed.repost/%s", actorDID, rkey)
	c.sendNotification(actorDID, targetDID, "repost", recordURI, repost.Subject.URI, postText, hasEmbed)

	// repost-via-repost: notify the original reposter.
	// Same subject post, so reuse the already-fetched text.
	if repost.Via != nil {
		reposterDID := extractDIDFromURI(repost.Via.URI)
		if reposterDID != "" && reposterDID != actorDID && reposterDID != targetDID {
			c.sendNotification(actorDID, reposterDID, "repost-via-repost", recordURI, repost.Subject.URI, postText, hasEmbed)
		}
	}
}
```

- [ ] **Step 4.6: Add the `fetchSubjectPost` helper**

Append this helper method to `internal/jetstream/consumer.go` (place it right after `handleVerificationDelete`, just above `sendNotification`):

```go
// fetchSubjectPost looks up the subject post's text and embed status via the
// configured PostTextProvider. Returns ("", false) on miss or timeout —
// the caller passes these to sendNotification which in turn causes Format
// to produce a ZWSP body. Uses a bounded context so a slow AppView can't
// stall the dispatch worker indefinitely.
func (c *Consumer) fetchSubjectPost(uri string) (string, bool) {
	if c.postText == nil {
		return "", false
	}
	ctx, cancel := context.WithTimeout(context.Background(), c.fetchTimeout)
	defer cancel()
	text, hasEmbed, ok := c.postText.PostText(ctx, uri)
	if !ok {
		return "", false
	}
	return text, hasEmbed
}
```

Add the `"context"` import to the top of `consumer.go` if it's not already present. The current stdlib imports are:

```go
import (
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	...
)
```

Add `"context"` alphabetically:

```go
import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	...
)
```

- [ ] **Step 4.7: Run the tests**

```
cd /home/yolo/Projects/atproto-push-gateway
go test ./internal/jetstream -v
```

Expected: the new tests pass. Existing tests still pass.

Note: `main.go` will fail to build at this point because `NewConsumer`'s signature changed. That's expected and fixed in Task 5. For now `go test` is the gate; `go build ./...` will fail.

- [ ] **Step 4.8: Run package-level build (skipping main)**

```
cd /home/yolo/Projects/atproto-push-gateway
go build ./internal/...
```

Expected: clean build. (`./cmd/server` intentionally out of scope until Task 5.)

- [ ] **Step 4.9: gofmt**

```
cd /home/yolo/Projects/atproto-push-gateway
gofmt -l ./internal/jetstream/
```

Expected: empty.

- [ ] **Step 4.10: Commit**

```
git add internal/jetstream/consumer.go internal/jetstream/consumer_test.go
git commit -m "feat(jetstream): wire PostTextProvider into handleLike/handleRepost

Consumer gains a PostTextProvider and a fetch timeout. handleLike and
handleRepost resolve the subject post's text via the provider (with a
bounded context) and pass it to sendNotification. The -via-repost
variants reuse the same lookup, so one getPosts call covers both
notifications generated per commit."
```

---

## Task 5: Wire env vars into `main.go`

**Files:**
- Modify: `cmd/server/main.go`

Read the new env vars. Build a `CachedProvider` on success, fall back to `NullProvider` otherwise. Pass into `NewConsumer`.

- [ ] **Step 5.1: Add the env-var reads and provider construction**

Open `cmd/server/main.go`. Locate the existing `NewConsumer` call (around line 146):

```go
	// Initialize profile resolver for display names
	profileResolver := profile.NewResolver()

	// Initialize Jetstream consumer
	consumer := jetstream.NewConsumer(jetstreamURL, s, sender, profileResolver)
	go consumer.Run()
```

Replace the two-line block (comment + `consumer := ...`) with:

```go
	// Initialize profile resolver for display names
	profileResolver := profile.NewResolver()

	// Initialize subject-post text provider. If REDIS_URL is empty or the
	// startup ping fails, fall back to NullProvider — like/repost bodies
	// stay empty (today's behavior), but nothing else breaks.
	redisURL := getEnv("REDIS_URL", "redis://redis:6379/0")
	positiveTTL := time.Duration(getEnvInt("REDIS_POST_TTL_SECONDS", 86400)) * time.Second
	negativeTTL := time.Duration(getEnvInt("REDIS_POST_NEGATIVE_TTL_SECONDS", 300)) * time.Second
	fetchTimeout := time.Duration(getEnvInt("POST_FETCH_TIMEOUT_SECONDS", 2)) * time.Second

	var postTextProvider posttext.PostTextProvider = posttext.NullProvider{}
	if redisURL != "" {
		fetcher := &posttext.AppViewFetcher{
			Client: &http.Client{Timeout: fetchTimeout},
		}
		cfg := posttext.Config{
			URL:         redisURL,
			PositiveTTL: positiveTTL,
			NegativeTTL: negativeTTL,
		}
		pingCtx, pingCancel := context.WithTimeout(context.Background(), 2*time.Second)
		cachedProvider, err := posttext.New(pingCtx, cfg, fetcher)
		pingCancel()
		if err != nil {
			log.Printf("  Redis:     disabled (connect failed: %v)", err)
		} else {
			log.Printf("  Redis:     enabled (url=%s, positive=%s, negative=%s)", redisURL, positiveTTL, negativeTTL)
			postTextProvider = cachedProvider
			defer func() { _ = cachedProvider.Close() }()
		}
	} else {
		log.Printf("  Redis:     disabled (REDIS_URL empty)")
	}

	// Initialize Jetstream consumer
	consumer := jetstream.NewConsumer(jetstreamURL, s, sender, profileResolver, postTextProvider, fetchTimeout)
	go consumer.Run()
```

Add the `posttext` import to the import block at the top of `main.go`:

Current:

```go
import (
	"context"
	"encoding/base64"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/dracoblue/atproto-push-gateway/internal/jetstream"
	"github.com/dracoblue/atproto-push-gateway/internal/profile"
	"github.com/dracoblue/atproto-push-gateway/internal/push"
	"github.com/dracoblue/atproto-push-gateway/internal/store"
	"github.com/dracoblue/atproto-push-gateway/internal/xrpc"
)
```

Change to:

```go
import (
	"context"
	"encoding/base64"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/dracoblue/atproto-push-gateway/internal/jetstream"
	"github.com/dracoblue/atproto-push-gateway/internal/posttext"
	"github.com/dracoblue/atproto-push-gateway/internal/profile"
	"github.com/dracoblue/atproto-push-gateway/internal/push"
	"github.com/dracoblue/atproto-push-gateway/internal/store"
	"github.com/dracoblue/atproto-push-gateway/internal/xrpc"
)
```

Add a `getEnvInt` helper next to the existing `getEnv` (near the top of `main.go`):

```go
func getEnvInt(key string, fallback int) int {
	if v := os.Getenv(key); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			log.Printf("invalid %s=%q (not an integer), using default %d", key, v, fallback)
			return fallback
		}
		return n
	}
	return fallback
}
```

- [ ] **Step 5.2: Build to verify `main.go` compiles**

```
cd /home/yolo/Projects/atproto-push-gateway
go build ./...
```

Expected: clean build.

- [ ] **Step 5.3: Run the full test suite**

```
cd /home/yolo/Projects/atproto-push-gateway
go test ./...
```

Expected: all packages PASS.

- [ ] **Step 5.4: Smoke-test with no Redis running**

Start the server with an empty `REDIS_URL` and confirm it logs the disabled message and doesn't try to connect:

```
cd /home/yolo/Projects/atproto-push-gateway
DEV_MODE=true REDIS_URL= PUSH_GATEWAY_DID=did:web:localhost timeout 3 go run ./cmd/server 2>&1 | grep -E "Redis|Starting|Listening" | head -10
```

Expected output includes: `Redis:     disabled (REDIS_URL empty)`.

Then start with the code default pointing at an unreachable host:

```
cd /home/yolo/Projects/atproto-push-gateway
DEV_MODE=true PUSH_GATEWAY_DID=did:web:localhost timeout 5 go run ./cmd/server 2>&1 | grep -E "Redis|Starting|Listening" | head -10
```

Expected output includes a `Redis:     disabled (connect failed: ...)` line — the gateway still starts.

- [ ] **Step 5.5: gofmt**

```
cd /home/yolo/Projects/atproto-push-gateway
gofmt -l ./cmd/server/
```

Expected: empty.

- [ ] **Step 5.6: Commit**

```
git add cmd/server/main.go
git commit -m "feat(server): wire REDIS_URL / POST_FETCH_TIMEOUT config

Reads REDIS_URL, REDIS_POST_TTL_SECONDS, REDIS_POST_NEGATIVE_TTL_SECONDS,
POST_FETCH_TIMEOUT_SECONDS. Constructs a CachedProvider on success;
logs and falls back to NullProvider when Redis is disabled or
unreachable. Default REDIS_URL points at the docker-compose service
hostname so 'docker compose up' works out of the box."
```

---

## Task 6: Add Redis to `docker-compose.yml`

**Files:**
- Modify: `docker-compose.yml`

- [ ] **Step 6.1: Read current `docker-compose.yml`**

Confirm the current file matches what the plan's Section 6 of the spec expects (push-gateway service, push-data volume, no redis service yet).

- [ ] **Step 6.2: Rewrite `docker-compose.yml`**

Replace the entire contents of `docker-compose.yml` with:

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

- [ ] **Step 6.3: Validate compose syntax**

```
cd /home/yolo/Projects/atproto-push-gateway
docker compose config > /dev/null
```

Expected: no output (exit 0). Non-zero exit means YAML is invalid.

- [ ] **Step 6.4: Manual bring-up check (skip if Docker is not available in the environment)**

```
cd /home/yolo/Projects/atproto-push-gateway
docker compose up -d
sleep 5
docker compose logs push-gateway | grep -E "Redis|Listening" | head -5
docker compose down
```

Expected log lines include `Redis:     enabled (url=redis://redis:6379/0, ...)` and `Listening on ...`.

If Docker is not available in the current environment, log that fact in the commit message and proceed — the CI/production build will catch any regression.

- [ ] **Step 6.5: Commit**

```
git add docker-compose.yml
git commit -m "feat(compose): add redis:7-alpine service for post-text cache

Cache is disposable (no volume). Redis is reachable only on the
internal compose network. push-gateway waits for redis health before
starting, so the startup PING doesn't race with Redis boot."
```

---

## Task 7: Documentation updates

**Files:**
- Modify: `README.md`
- Modify: `docs/NOTIFICATIONS.md`

- [ ] **Step 7.1: Update the Supported Events table in `README.md`**

Locate the existing "Supported Events" table in `README.md`. It currently shows for likes/reposts/`-via-repost` reasons a body of `*(empty)*`. Update those four rows so they read:

```markdown
| Like | X liked your post | subject post text (+ 🖼 if media attached), empty if cache misses |
| Repost | X reposted your post | subject post text (+ 🖼 if media attached), empty if cache misses |
| Like via repost | X liked a post you reposted | subject post text (+ 🖼 if media attached), empty if cache misses |
| Repost via repost | X reposted a post you reposted | subject post text (+ 🖼 if media attached), empty if cache misses |
```

All other rows stay the same.

- [ ] **Step 7.2: Update the post-table explanatory paragraph in `README.md`**

The current paragraph reads:

```markdown
Reply/mention/quote bodies carry the actual post text, dynamically truncated with an ellipsis if the push payload would exceed ~3.5 KB. An embed marker (🖼) is appended when the post carries images, video, an external link card, or a record-with-media embed. Notifications without body text use a single zero-width space (U+200B) as the body — invisible on screen, keeps the iOS Notification Service Extension path active.
```

Replace with:

```markdown
Reply/mention/quote bodies carry the triggering post's text directly from the Jetstream commit. Like/repost/like-via-repost/repost-via-repost bodies carry the *subject* post's text, resolved via a Redis-backed cache that falls back to the AppView's `app.bsky.feed.getPosts` on miss. All enriched bodies are dynamically truncated with an ellipsis if the push payload would exceed ~3.5 KB, and an embed marker (🖼) is appended when the post carries images, video, an external link card, or a record-with-media embed. Notifications without body text — including cache misses and transient AppView failures — use a single zero-width space (U+200B) as the body, keeping the iOS Notification Service Extension path active.
```

- [ ] **Step 7.3: Add new env vars to the Configuration table in `README.md`**

Locate the existing Configuration table. Add four new rows to the bottom:

```markdown
| `REDIS_URL` | `redis://redis:6379/0` | Redis connection URL for the post-text cache. Set empty to disable (likes/reposts use empty bodies). |
| `REDIS_POST_TTL_SECONDS` | `86400` | Positive cache TTL (24 hours). |
| `REDIS_POST_NEGATIVE_TTL_SECONDS` | `300` | Negative cache TTL for deleted/unknown posts (5 minutes). |
| `POST_FETCH_TIMEOUT_SECONDS` | `2` | Per-fetch AppView timeout. |
```

- [ ] **Step 7.4: Update the Architecture section in `README.md`**

The current Architecture section includes:

```markdown
- **In-Memory:** Hashmap of registered DIDs + block graph for fast matching
- **Single process, single container, no external services**
```

Change the second bullet to:

```markdown
- **In-Memory:** Hashmap of registered DIDs + block graph for fast matching
- **Redis:** Subject-post text cache for like/repost notifications (optional; falls back to empty bodies when unavailable)
- **Single process, one optional sidecar (Redis via compose)**
```

- [ ] **Step 7.5: Update `docs/NOTIFICATIONS.md` payload examples**

For each of `like`, `repost`, `like-via-repost`, `repost-via-repost`, locate the existing Push Payload JSON block. The current `body` field is `"​"`. Replace with an example enriched body so a reader sees what a cache-hit notification looks like.

**like** — replace the existing `"body": "​",` line with:

```json
  "body": "Great read, thanks for sharing!",
```

**repost** — replace with:

```json
  "body": "This is so important right now.",
```

**like-via-repost** — replace with:

```json
  "body": "Friday thoughts from the team.",
```

**repost-via-repost** — replace with:

```json
  "body": "New blog post up: why Go scheduling matters.",
```

- [ ] **Step 7.6: Add a note about cache misses in `docs/NOTIFICATIONS.md`**

Locate the existing "Title/Body Layout" section at the top of `docs/NOTIFICATIONS.md`. Append this paragraph at the end of the section:

```markdown
Like/repost/like-via-repost/repost-via-repost bodies carry the *subject* post's text, not the liker/reposter's. This text is cached in Redis (key `posttext:v1:<at-uri>`, default TTL 24h). Cache misses trigger a single `app.bsky.feed.getPosts` call to the AppView; concurrent misses for the same post are deduplicated via a singleflight group. When Redis is disabled, unreachable, or the AppView fetch fails, these notifications fall back to a ZWSP body — the user sees the title ("Alice liked your post") but no preview.
```

- [ ] **Step 7.7: Commit**

```
git add README.md docs/NOTIFICATIONS.md
git commit -m "docs(posttext): document subject-post enrichment behavior and config"
```

---

## Final Verification

After Task 7:

- [ ] **Run the full test suite**

```
cd /home/yolo/Projects/atproto-push-gateway
go test ./...
```

Expected: all packages PASS, including `internal/posttext` (13+ tests), `internal/jetstream` (existing + 3 new), `internal/notification` (unchanged).

- [ ] **Run the build**

```
cd /home/yolo/Projects/atproto-push-gateway
go build ./...
```

Expected: clean build.

- [ ] **gofmt on the whole repo**

```
cd /home/yolo/Projects/atproto-push-gateway
gofmt -l .
```

Expected: empty output.

- [ ] **Branch summary**

```
cd /home/yolo/Projects/atproto-push-gateway
git log --oneline better-notifs..HEAD
```

Expected commits (7 task commits plus the spec commit at base):

- `docs(posttext): add subject-post enrichment design spec`
- `feat(posttext): add PostTextProvider interface and NullProvider`
- `feat(posttext): add AppViewFetcher for app.bsky.feed.getPosts`
- `feat(posttext): add CachedProvider with Redis and singleflight`
- `feat(jetstream): wire PostTextProvider into handleLike/handleRepost`
- `feat(server): wire REDIS_URL / POST_FETCH_TIMEOUT config`
- `feat(compose): add redis:7-alpine service for post-text cache`
- `docs(posttext): document subject-post enrichment behavior and config`

- [ ] **End-to-end smoke test (if Docker is available)**

```
cd /home/yolo/Projects/atproto-push-gateway
docker compose up -d
sleep 10
docker compose logs push-gateway | grep -E "Redis|Listening" | head
# Tail the gateway logs; trigger a like on a registered DID manually
# (register via /test/register in DEV_MODE). Confirm the push payload
# (in `docker compose logs -f`) contains a non-empty body for the like.
docker compose down
```

If Docker isn't available in the execution environment, note that in the final review summary.
