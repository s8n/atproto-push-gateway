package jetstream

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/dracoblue/atproto-push-gateway/internal/push"
	"github.com/dracoblue/atproto-push-gateway/internal/store"
)

func TestExtractDIDFromURI(t *testing.T) {
	tests := []struct {
		name string
		uri  string
		want string
	}{
		{"standard post URI", "at://did:plc:abc123/app.bsky.feed.post/xyz", "did:plc:abc123"},
		{"like URI", "at://did:plc:user456/app.bsky.feed.like/rkey", "did:plc:user456"},
		{"did:web URI", "at://did:web:example.org/app.bsky.feed.post/abc", "did:web:example.org"},
		{"empty string", "", ""},
		{"no at:// prefix", "https://example.com", ""},
		{"at:// with no path", "at://did:plc:abc", "did:plc:abc"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := extractDIDFromURI(tt.uri); got != tt.want {
				t.Errorf("extractDIDFromURI(%q) = %q, want %q", tt.uri, got, tt.want)
			}
		})
	}
}

func TestParseLikeRecord(t *testing.T) {
	raw := json.RawMessage(`{
		"$type": "app.bsky.feed.like",
		"subject": {
			"uri": "at://did:plc:target/app.bsky.feed.post/abc",
			"cid": "bafyreiabc"
		},
		"createdAt": "2026-04-11T00:00:00Z"
	}`)

	var like LikeRecord
	if err := json.Unmarshal(raw, &like); err != nil {
		t.Fatalf("failed to parse like: %v", err)
	}

	targetDID := extractDIDFromURI(like.Subject.URI)
	if targetDID != "did:plc:target" {
		t.Errorf("expected target did:plc:target, got %s", targetDID)
	}
}

func TestParsePostReply(t *testing.T) {
	raw := json.RawMessage(`{
		"$type": "app.bsky.feed.post",
		"text": "Hello!",
		"reply": {
			"parent": {"uri": "at://did:plc:parent/app.bsky.feed.post/xyz", "cid": "bafyxyz"},
			"root": {"uri": "at://did:plc:root/app.bsky.feed.post/abc", "cid": "bafyabc"}
		},
		"createdAt": "2026-04-11T00:00:00Z"
	}`)

	var post PostRecord
	if err := json.Unmarshal(raw, &post); err != nil {
		t.Fatalf("failed to parse post: %v", err)
	}

	if post.Reply == nil {
		t.Fatal("expected reply to be non-nil")
	}

	parentDID := extractDIDFromURI(post.Reply.Parent.URI)
	if parentDID != "did:plc:parent" {
		t.Errorf("expected parent did:plc:parent, got %s", parentDID)
	}
}

func TestParsePostMention(t *testing.T) {
	raw := json.RawMessage(`{
		"$type": "app.bsky.feed.post",
		"text": "Hey @alice!",
		"facets": [{
			"index": {"byteStart": 4, "byteEnd": 10},
			"features": [{
				"$type": "app.bsky.richtext.facet#mention",
				"did": "did:plc:alice"
			}]
		}],
		"createdAt": "2026-04-11T00:00:00Z"
	}`)

	var post PostRecord
	if err := json.Unmarshal(raw, &post); err != nil {
		t.Fatalf("failed to parse post: %v", err)
	}

	if len(post.Facets) != 1 {
		t.Fatalf("expected 1 facet, got %d", len(post.Facets))
	}

	feature := post.Facets[0].Features[0]
	if feature.Type != "app.bsky.richtext.facet#mention" {
		t.Errorf("expected mention facet, got %s", feature.Type)
	}
	if feature.DID != "did:plc:alice" {
		t.Errorf("expected did:plc:alice, got %s", feature.DID)
	}
}

func TestParsePostQuote(t *testing.T) {
	raw := json.RawMessage(`{
		"$type": "app.bsky.feed.post",
		"text": "Check this out",
		"embed": {
			"$type": "app.bsky.embed.record",
			"record": {
				"uri": "at://did:plc:quoted/app.bsky.feed.post/abc",
				"cid": "bafyabc"
			}
		},
		"createdAt": "2026-04-11T00:00:00Z"
	}`)

	var post PostRecord
	if err := json.Unmarshal(raw, &post); err != nil {
		t.Fatalf("failed to parse post: %v", err)
	}

	if post.Embed == nil {
		t.Fatal("expected embed to be non-nil")
	}
	if post.Embed.Type != "app.bsky.embed.record" {
		t.Errorf("expected embed type app.bsky.embed.record, got %s", post.Embed.Type)
	}

	quotedDID := extractDIDFromURI(post.Embed.Record.URI)
	if quotedDID != "did:plc:quoted" {
		t.Errorf("expected did:plc:quoted, got %s", quotedDID)
	}
}

func TestParseFollowRecord(t *testing.T) {
	raw := json.RawMessage(`{
		"$type": "app.bsky.graph.follow",
		"subject": "did:plc:target",
		"createdAt": "2026-04-11T00:00:00Z"
	}`)

	var follow FollowRecord
	if err := json.Unmarshal(raw, &follow); err != nil {
		t.Fatalf("failed to parse follow: %v", err)
	}

	if follow.Subject != "did:plc:target" {
		t.Errorf("expected did:plc:target, got %s", follow.Subject)
	}
}

func TestParseBlockRecord(t *testing.T) {
	raw := json.RawMessage(`{
		"$type": "app.bsky.graph.block",
		"subject": "did:plc:blocked",
		"createdAt": "2026-04-11T00:00:00Z"
	}`)

	var block BlockRecord
	if err := json.Unmarshal(raw, &block); err != nil {
		t.Fatalf("failed to parse block: %v", err)
	}

	if block.Subject != "did:plc:blocked" {
		t.Errorf("expected did:plc:blocked, got %s", block.Subject)
	}
}

func TestParseJetstreamEvent(t *testing.T) {
	raw := `{
		"did": "did:plc:actor",
		"time_us": 1712800000000000,
		"kind": "commit",
		"commit": {
			"rev": "abc",
			"operation": "create",
			"collection": "app.bsky.feed.like",
			"rkey": "xyz",
			"record": {
				"$type": "app.bsky.feed.like",
				"subject": {
					"uri": "at://did:plc:target/app.bsky.feed.post/123",
					"cid": "bafytest"
				}
			}
		}
	}`

	var event Event
	if err := json.Unmarshal([]byte(raw), &event); err != nil {
		t.Fatalf("failed to parse event: %v", err)
	}

	if event.DID != "did:plc:actor" {
		t.Errorf("expected actor did:plc:actor, got %s", event.DID)
	}
	if event.Kind != "commit" {
		t.Errorf("expected kind commit, got %s", event.Kind)
	}
	if event.Commit == nil {
		t.Fatal("expected commit to be non-nil")
	}
	if event.Commit.Collection != "app.bsky.feed.like" {
		t.Errorf("expected collection app.bsky.feed.like, got %s", event.Commit.Collection)
	}
	if event.Commit.Operation != "create" {
		t.Errorf("expected operation create, got %s", event.Commit.Operation)
	}
}

func TestParseTextOnlyPost(t *testing.T) {
	raw := json.RawMessage(`{
		"$type": "app.bsky.feed.post",
		"text": "Just a text post",
		"createdAt": "2026-04-11T00:00:00Z"
	}`)

	var post PostRecord
	if err := json.Unmarshal(raw, &post); err != nil {
		t.Fatalf("failed to parse post: %v", err)
	}

	if post.Reply != nil {
		t.Error("expected reply to be nil for non-reply post")
	}
	if post.Embed != nil {
		t.Error("expected embed to be nil for text-only post")
	}
	if len(post.Facets) != 0 {
		t.Errorf("expected 0 facets, got %d", len(post.Facets))
	}
}

func TestExtractPostContent(t *testing.T) {
	cases := []struct {
		name         string
		rawRecord    string
		wantText     string
		wantHasEmbed bool
	}{
		{
			name:         "text only",
			rawRecord:    `{"$type":"app.bsky.feed.post","text":"hello","createdAt":"2026-04-11T00:00:00Z"}`,
			wantText:     "hello",
			wantHasEmbed: false,
		},
		{
			name:         "embed images",
			rawRecord:    `{"$type":"app.bsky.feed.post","text":"look","embed":{"$type":"app.bsky.embed.images","images":[]},"createdAt":"2026-04-11T00:00:00Z"}`,
			wantText:     "look",
			wantHasEmbed: true,
		},
		{
			name:         "embed video",
			rawRecord:    `{"$type":"app.bsky.feed.post","text":"watch","embed":{"$type":"app.bsky.embed.video"},"createdAt":"2026-04-11T00:00:00Z"}`,
			wantText:     "watch",
			wantHasEmbed: true,
		},
		{
			name:         "embed external",
			rawRecord:    `{"$type":"app.bsky.feed.post","text":"click","embed":{"$type":"app.bsky.embed.external"},"createdAt":"2026-04-11T00:00:00Z"}`,
			wantText:     "click",
			wantHasEmbed: true,
		},
		{
			name:         "embed recordWithMedia",
			rawRecord:    `{"$type":"app.bsky.feed.post","text":"quote plus pic","embed":{"$type":"app.bsky.embed.recordWithMedia"},"createdAt":"2026-04-11T00:00:00Z"}`,
			wantText:     "quote plus pic",
			wantHasEmbed: true,
		},
		{
			name:         "embed record only (plain quote)",
			rawRecord:    `{"$type":"app.bsky.feed.post","text":"quote","embed":{"$type":"app.bsky.embed.record","record":{"uri":"at://did:plc:x/app.bsky.feed.post/abc","cid":"bafy"}},"createdAt":"2026-04-11T00:00:00Z"}`,
			wantText:     "quote",
			wantHasEmbed: false,
		},
		{
			name:         "empty text with embed images",
			rawRecord:    `{"$type":"app.bsky.feed.post","text":"","embed":{"$type":"app.bsky.embed.images"},"createdAt":"2026-04-11T00:00:00Z"}`,
			wantText:     "",
			wantHasEmbed: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var post PostRecord
			if err := json.Unmarshal([]byte(tc.rawRecord), &post); err != nil {
				t.Fatalf("parse: %v", err)
			}
			text, hasEmbed := extractPostContent(&post)
			if text != tc.wantText {
				t.Errorf("text = %q, want %q", text, tc.wantText)
			}
			if hasEmbed != tc.wantHasEmbed {
				t.Errorf("hasEmbed = %v, want %v", hasEmbed, tc.wantHasEmbed)
			}
		})
	}
}

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
	// handleLike must consult the PostText provider when a registered user
	// is the post author (the potential notification target).
	fake := &fakePostTextProvider{text: "hello", hasEmbed: false, ok: true}
	tmpStore, err := store.New(":memory:")
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	defer tmpStore.Close()
	if err := tmpStore.RegisterToken("did:plc:target", "ios", "ExponentPushToken[t1]", "org.example"); err != nil {
		t.Fatalf("RegisterToken: %v", err)
	}

	c := &Consumer{
		store:        tmpStore,
		sender:       &captureSender{},
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
	if err := tmpStore.RegisterToken("did:plc:target", "ios", "ExponentPushToken[t1]", "org.example"); err != nil {
		t.Fatalf("RegisterToken: %v", err)
	}

	c := &Consumer{
		store:        tmpStore,
		sender:       &captureSender{},
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
	if err := tmpStore.RegisterToken("did:plc:target", "ios", "ExponentPushToken[t1]", "org.example"); err != nil {
		t.Fatalf("RegisterToken: %v", err)
	}
	if err := tmpStore.RegisterToken("did:plc:reposter", "ios", "ExponentPushToken[t2]", "org.example"); err != nil {
		t.Fatalf("RegisterToken: %v", err)
	}

	c := &Consumer{
		store:        tmpStore,
		sender:       &captureSender{},
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

func TestHandleLikeSkipsFetchWhenNoRecipientRegistered(t *testing.T) {
	// The firehose carries every like on the network. When neither the
	// post author nor the reposter (for like-via-repost) is registered,
	// handleLike must not call the PostText provider at all — otherwise
	// we pound the AppView for notifications we'll never send.
	fake := &fakePostTextProvider{text: "x", ok: true}
	tmpStore, err := store.New(":memory:")
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	defer tmpStore.Close()

	c := &Consumer{
		store:        tmpStore,
		sender:       &captureSender{},
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
	if len(fake.calls) != 0 {
		t.Errorf("provider was called %d times for unregistered recipients, want 0", len(fake.calls))
	}
}

func TestHandleRepostSkipsFetchWhenNoRecipientRegistered(t *testing.T) {
	fake := &fakePostTextProvider{text: "x", ok: true}
	tmpStore, err := store.New(":memory:")
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	defer tmpStore.Close()

	c := &Consumer{
		store:        tmpStore,
		sender:       &captureSender{},
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
	if len(fake.calls) != 0 {
		t.Errorf("provider was called %d times for unregistered recipient, want 0", len(fake.calls))
	}
}

// ctxCheckingProvider records whether the context passed to PostText had
// already expired by the time the provider saw it.
type ctxCheckingProvider struct {
	mu            sync.Mutex
	sawExpiredCtx bool
	callsObserved int
}

func (p *ctxCheckingProvider) PostText(ctx context.Context, uri string) (string, bool, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.callsObserved++
	if ctx.Err() != nil {
		p.sawExpiredCtx = true
	}
	return "text", false, true
}

func TestHandleLikeZeroFetchTimeoutUsesDefault(t *testing.T) {
	// If fetchTimeout is 0 (uninitialized), fetchSubjectPost must NOT pass an
	// already-expired context to the provider — that would silently break
	// subject-post enrichment for callers who forgot to set the timeout.
	prov := &ctxCheckingProvider{}
	tmpStore, err := store.New(":memory:")
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	defer tmpStore.Close()
	if err := tmpStore.RegisterToken("did:plc:target", "ios", "ExponentPushToken[t1]", "org.example"); err != nil {
		t.Fatalf("RegisterToken: %v", err)
	}

	c := &Consumer{
		store:        tmpStore,
		sender:       &captureSender{},
		postText:     prov,
		fetchTimeout: 0, // the case we're defending against
	}

	rawLike := json.RawMessage(`{
		"$type": "app.bsky.feed.like",
		"subject": {"uri": "at://did:plc:target/app.bsky.feed.post/xyz", "cid": "bafy"}
	}`)
	c.handleLike("did:plc:actor", "rk1", rawLike)

	prov.mu.Lock()
	defer prov.mu.Unlock()
	if prov.callsObserved == 0 {
		t.Fatal("provider was never called")
	}
	if prov.sawExpiredCtx {
		t.Errorf("provider saw an already-expired context; zero fetchTimeout must fall back to the default")
	}
}

// captureSender records all notifications sent to it.
type captureSender struct {
	mu   sync.Mutex
	sent []push.Notification
}

func (c *captureSender) Send(n push.Notification) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sent = append(c.sent, n)
	return nil
}

func TestHandleLikeEnrichesBodyWithSubjectPostText(t *testing.T) {
	// End-to-end: handleLike runs provider → Format → sender.Send. The
	// captured notification's Body must be the subject post's text (not
	// ZWSP).
	sender := &captureSender{}
	fakeProv := &fakePostTextProvider{
		text:     "this is the liked post",
		hasEmbed: false,
		ok:       true,
	}
	tmpStore, err := store.New(":memory:")
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	defer tmpStore.Close()

	// Register a token for the target DID so sendNotification doesn't
	// short-circuit on IsRegistered.
	if err := tmpStore.RegisterToken("did:plc:target", "ios", "ExponentPushToken[xxxx]", "org.example"); err != nil {
		t.Fatalf("RegisterToken: %v", err)
	}

	c := &Consumer{
		store:        tmpStore,
		sender:       sender,
		postText:     fakeProv,
		fetchTimeout: 1 * time.Second,
	}

	rawLike := json.RawMessage(`{
		"$type": "app.bsky.feed.like",
		"subject": {"uri": "at://did:plc:target/app.bsky.feed.post/xyz", "cid": "bafy"}
	}`)
	c.handleLike("did:plc:actor", "rk1", rawLike)

	sender.mu.Lock()
	defer sender.mu.Unlock()
	if len(sender.sent) != 1 {
		t.Fatalf("sender received %d notifications, want 1", len(sender.sent))
	}
	n := sender.sent[0]
	if n.Body != "this is the liked post" {
		t.Errorf("Body = %q, want %q", n.Body, "this is the liked post")
	}
	if n.Title == "" {
		t.Errorf("Title is empty")
	}
}
