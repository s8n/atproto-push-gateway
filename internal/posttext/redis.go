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
		} else {
			// Malformed cache value — treat as miss, refetch, overwrite.
			log.Printf("[posttext] malformed cache value for %s: %v", uri, jsonErr)
		}
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
