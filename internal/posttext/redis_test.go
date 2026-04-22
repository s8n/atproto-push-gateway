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
