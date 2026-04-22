# Richer Notifications Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Enrich reply/mention/quote push notifications with the actual post text, drawing the text from the Jetstream commit we already parse. Shift the title/body semantics so the title carries the action ("Alice replied to your post") and the body carries post content. Bodies are dynamically truncated to fit within a conservative push-payload budget, with JSON-escape awareness so adversarial post text can't overflow the APNs/FCM limit.

**Architecture:** A new `internal/notification` package owns a pure `Format(reason, display, handle, text, hasEmbed, baseOverhead) -> (title, body)` function. The consumer computes a `baseOverhead` from a throwaway `push.Notification` marshal with empty title+body, then calls `Format`. The old inline formatter (`formatNotification`, `reasonTitles`, `reasonBodyTemplates`) is deleted. Likes/reposts/follows get empty (ZWSP) bodies — their enrichment is deferred until Redis-backed subject-post caching lands.

**Tech Stack:** Go 1.25, standard library only (`encoding/json`, `unicode/utf8`, `strings`, `fmt`). No new dependencies.

**Spec:** `docs/superpowers/specs/2026-04-22-richer-notifications-design.md`

---

## Project Context (read before every task)

`atproto-push-gateway` is a self-hosted push-notification gateway for ATProto. Relevant paths for this plan:

```
internal/jetstream/consumer.go        WebSocket consumer, parses commits, dispatches notifications
internal/jetstream/consumer_test.go   unit tests (parser-level, no sendNotification coverage today)
internal/push/push.go                 push.Notification struct and MultiSender
internal/notification/                (NEW) formatter package
docs/NOTIFICATIONS.md                 public doc describing payload shape per reason
README.md                             public doc with Supported Events table
```

Module path: `github.com/dracoblue/atproto-push-gateway`.

**Running the tests:**
```
cd /home/yolo/Projects/atproto-push-gateway
go test ./...
```

**Running one package's tests:**
```
cd /home/yolo/Projects/atproto-push-gateway
go test ./internal/notification -v
```

**Building:**
```
cd /home/yolo/Projects/atproto-push-gateway
go build ./...
```

---

## File Structure

| File | Status | Responsibility |
|---|---|---|
| `internal/notification/formatter.go` | Create | Pure `Format` function; title templates; truncation constants |
| `internal/notification/formatter_test.go` | Create | Table-driven tests for titles, body rules, truncation, JSON escape guard |
| `internal/jetstream/consumer.go` | Modify | Delete inline formatter; add `extractPostContent` helper; update `sendNotification` signature and callers |
| `internal/jetstream/consumer_test.go` | Modify | Add tests for `extractPostContent` |
| `docs/NOTIFICATIONS.md` | Modify | Update Push Payload examples to show new title/body |
| `README.md` | Modify | Update Supported Events table to show new format |

---

## Task 1: Scaffold `internal/notification` package with title-only `Format`

**Files:**
- Create: `internal/notification/formatter.go`
- Create: `internal/notification/formatter_test.go`

This task builds the package skeleton and the title-rendering half of `Format`. Body is always ZWSP in this task. Body enrichment arrives in Task 2.

- [ ] **Step 1.1: Write the failing tests**

Create `internal/notification/formatter_test.go` with this content:

```go
package notification

import "testing"

func TestFormatTitlesKnownReasons(t *testing.T) {
	cases := []struct {
		name, reason, display, handle, wantTitle string
	}{
		{"like with display", "like", "Alice", "alice.bsky.social", "Alice liked your post"},
		{"repost with display", "repost", "Alice", "alice.bsky.social", "Alice reposted your post"},
		{"reply with display", "reply", "Alice", "alice.bsky.social", "Alice replied to your post"},
		{"mention with display", "mention", "Alice", "alice.bsky.social", "Alice mentioned you"},
		{"quote with display", "quote", "Alice", "alice.bsky.social", "Alice quoted your post"},
		{"follow with display", "follow", "Alice", "alice.bsky.social", "Alice followed you"},
		{"like-via-repost", "like-via-repost", "Alice", "alice.bsky.social", "Alice liked a post you reposted"},
		{"repost-via-repost", "repost-via-repost", "Alice", "alice.bsky.social", "Alice reposted a post you reposted"},
		{"verified ignores actor", "verified", "Alice", "alice.bsky.social", "Your account has been verified"},
		{"unverified ignores actor", "unverified", "Alice", "alice.bsky.social", "Your account verification was removed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			title, _ := Format(tc.reason, tc.display, tc.handle, "", false, 0)
			if title != tc.wantTitle {
				t.Errorf("title = %q, want %q", title, tc.wantTitle)
			}
		})
	}
}

func TestFormatTitleActorFallback(t *testing.T) {
	cases := []struct {
		name, display, handle, wantTitle string
	}{
		{"display wins", "Alice", "alice.bsky.social", "Alice liked your post"},
		{"handle when no display", "", "alice.bsky.social", "alice.bsky.social liked your post"},
		{"someone when no display or handle", "", "", "Someone liked your post"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			title, _ := Format("like", tc.display, tc.handle, "", false, 0)
			if title != tc.wantTitle {
				t.Errorf("title = %q, want %q", title, tc.wantTitle)
			}
		})
	}
}

func TestFormatUnknownReason(t *testing.T) {
	title, body := Format("never-heard-of-it", "Alice", "alice.bsky.social", "some text", true, 0)
	if title != "Notification" {
		t.Errorf("title for unknown reason = %q, want %q", title, "Notification")
	}
	if body != "​" {
		t.Errorf("body for unknown reason = %q, want ZWSP", body)
	}
}

func TestFormatBodyIsZWSPUntilEnrichmentLands(t *testing.T) {
	// Task 2 replaces this with enrichment tests.
	_, body := Format("reply", "Alice", "alice.bsky.social", "some text", false, 0)
	if body != "​" {
		t.Errorf("body = %q, want ZWSP", body)
	}
}
```

- [ ] **Step 1.2: Run the tests to confirm they fail**

```
cd /home/yolo/Projects/atproto-push-gateway
go test ./internal/notification -v
```

Expected: compile error — package `notification` has no `Format` function. Fail is correct.

- [ ] **Step 1.3: Create the formatter implementation**

Create `internal/notification/formatter.go` with this content:

```go
// Package notification renders push-notification titles and bodies.
// The functions here are pure: no I/O, no state, trivially testable.
package notification

import "fmt"

const zwsp = "​"

var titleTemplates = map[string]string{
	"like":              "%s liked your post",
	"repost":            "%s reposted your post",
	"reply":             "%s replied to your post",
	"mention":           "%s mentioned you",
	"quote":             "%s quoted your post",
	"follow":            "%s followed you",
	"like-via-repost":   "%s liked a post you reposted",
	"repost-via-repost": "%s reposted a post you reposted",
	"verified":          "Your account has been verified",
	"unverified":        "Your account verification was removed",
}

// Format renders the user-facing title and body for a push notification.
// Pure; no I/O. baseOverhead is the byte size of the final push payload
// with Title and Body both set to empty strings — the caller measures it
// via json.Marshal of the throwaway push.Notification.
func Format(reason, actorDisplayName, actorHandle, postText string, hasEmbed bool, baseOverhead int) (title, body string) {
	title = renderTitle(reason, actorDisplayName, actorHandle)
	body = zwsp
	return
}

func renderTitle(reason, actorDisplayName, actorHandle string) string {
	tmpl, ok := titleTemplates[reason]
	if !ok {
		return "Notification"
	}
	if reason == "verified" || reason == "unverified" {
		return tmpl
	}
	actor := actorDisplayName
	if actor == "" {
		actor = actorHandle
	}
	if actor == "" {
		actor = "Someone"
	}
	return fmt.Sprintf(tmpl, actor)
}
```

- [ ] **Step 1.4: Run the tests to confirm they pass**

```
cd /home/yolo/Projects/atproto-push-gateway
go test ./internal/notification -v
```

Expected: all tests PASS.

- [ ] **Step 1.5: Run the full suite to confirm nothing else broke**

```
cd /home/yolo/Projects/atproto-push-gateway
go test ./...
```

Expected: all tests PASS.

- [ ] **Step 1.6: Commit**

```
git add internal/notification/formatter.go internal/notification/formatter_test.go
git commit -m "feat(notification): add formatter package with title rendering"
```

---

## Task 2: Add body enrichment (reply/mention/quote with text, no truncation yet)

**Files:**
- Modify: `internal/notification/formatter.go`
- Modify: `internal/notification/formatter_test.go`

Adds the enriched body path. Truncation is deferred to Task 3 — in this task, full text and marker are appended unconditionally.

- [ ] **Step 2.1: Replace the Task-1 body test with enrichment tests**

In `internal/notification/formatter_test.go`, delete `TestFormatBodyIsZWSPUntilEnrichmentLands` and add these tests at the end of the file:

```go
func TestFormatBodyEnrichesReplyMentionQuote(t *testing.T) {
	cases := []struct {
		name, reason, text string
		hasEmbed           bool
		wantBody           string
	}{
		{"reply with text", "reply", "Hello there", false, "Hello there"},
		{"mention with text", "mention", "cc @alice", false, "cc @alice"},
		{"quote with text", "quote", "lol", false, "lol"},
		{"reply with embed", "reply", "check this", true, "check this 🖼"},
		{"mention with embed", "mention", "see @bob", true, "see @bob 🖼"},
		{"quote with embed", "quote", "lol", true, "lol 🖼"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, body := Format(tc.reason, "Alice", "alice.bsky.social", tc.text, tc.hasEmbed, 0)
			if body != tc.wantBody {
				t.Errorf("body = %q, want %q", body, tc.wantBody)
			}
		})
	}
}

func TestFormatBodyEmptyTextFallsToZWSP(t *testing.T) {
	cases := []struct {
		name, reason string
		hasEmbed     bool
	}{
		{"reply empty no embed", "reply", false},
		{"reply empty with embed", "reply", true},
		{"mention empty", "mention", false},
		{"quote empty", "quote", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, body := Format(tc.reason, "Alice", "alice.bsky.social", "", tc.hasEmbed, 0)
			if body != "​" {
				t.Errorf("body = %q, want ZWSP", body)
			}
		})
	}
}

func TestFormatBodyNonEnrichedReasonsAlwaysZWSP(t *testing.T) {
	cases := []struct {
		name, reason string
	}{
		{"like", "like"},
		{"repost", "repost"},
		{"follow", "follow"},
		{"like-via-repost", "like-via-repost"},
		{"repost-via-repost", "repost-via-repost"},
		{"verified", "verified"},
		{"unverified", "unverified"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Even with postText provided, these reasons must not enrich.
			_, body := Format(tc.reason, "Alice", "alice.bsky.social", "ignored", true, 0)
			if body != "​" {
				t.Errorf("body = %q, want ZWSP", body)
			}
		})
	}
}

func TestFormatBodyWhitespacePassThrough(t *testing.T) {
	// Whitespace inside post text must be preserved byte-for-byte.
	text := "line one\n\nline\ttwo   trailing"
	_, body := Format("reply", "Alice", "alice.bsky.social", text, false, 0)
	if body != text {
		t.Errorf("body = %q, want %q (whitespace must pass through)", body, text)
	}
}
```

- [ ] **Step 2.2: Run the tests to confirm the new ones fail**

```
cd /home/yolo/Projects/atproto-push-gateway
go test ./internal/notification -v
```

Expected: the four new tests FAIL (body is still ZWSP). Existing title tests PASS.

- [ ] **Step 2.3: Update `Format` to add body enrichment**

Replace the body of `Format` in `internal/notification/formatter.go`:

```go
const embedMarker = " 🖼"

// Format renders the user-facing title and body for a push notification.
// Pure; no I/O. baseOverhead is the byte size of the final push payload
// with Title and Body both set to empty strings — the caller measures it
// via json.Marshal of the throwaway push.Notification.
func Format(reason, actorDisplayName, actorHandle, postText string, hasEmbed bool, baseOverhead int) (title, body string) {
	title = renderTitle(reason, actorDisplayName, actorHandle)
	body = renderBody(reason, postText, hasEmbed)
	return
}

func renderBody(reason, postText string, hasEmbed bool) string {
	if !isEnrichedReason(reason) || postText == "" {
		return zwsp
	}
	if hasEmbed {
		return postText + embedMarker
	}
	return postText
}

func isEnrichedReason(reason string) bool {
	switch reason {
	case "reply", "mention", "quote":
		return true
	}
	return false
}
```

- [ ] **Step 2.4: Run the tests to confirm they pass**

```
cd /home/yolo/Projects/atproto-push-gateway
go test ./internal/notification -v
```

Expected: all tests PASS.

- [ ] **Step 2.5: Commit**

```
git add internal/notification/formatter.go internal/notification/formatter_test.go
git commit -m "feat(notification): enrich reply/mention/quote body with post text"
```

---

## Task 3: Add dynamic truncation with JSON escape awareness

**Files:**
- Modify: `internal/notification/formatter.go`
- Modify: `internal/notification/formatter_test.go`

Adds budget-driven truncation. The key invariant is: after `Format` returns, the JSON-encoded `title + body` cannot cause the final payload to exceed `payloadBudget - safetyMargin`, even for adversarial input (all `"` or `\`).

- [ ] **Step 3.1: Add truncation tests**

In `internal/notification/formatter_test.go`, replace the existing import line `import "testing"` at the top of the file with this import block:

```go
import (
	"encoding/json"
	"strings"
	"testing"
)
```

Then append these test functions at the end of the file:

```go
func TestFormatTruncatesLongText(t *testing.T) {
	text := strings.Repeat("a", 5000)
	_, body := Format("reply", "Alice", "alice.bsky.social", text, false, 0)
	if !strings.HasSuffix(body, "…") {
		t.Errorf("long body should end with ellipsis, got last rune in %q", body)
	}
	if len(body) >= len(text) {
		t.Errorf("body should be shorter than input, got %d >= %d", len(body), len(text))
	}
}

func TestFormatTruncatesKeepsEmbedMarker(t *testing.T) {
	text := strings.Repeat("a", 5000)
	_, body := Format("reply", "Alice", "alice.bsky.social", text, true, 0)
	if !strings.HasSuffix(body, " 🖼") {
		t.Errorf("body with embed must end with embed marker, got tail %q", body[max(0, len(body)-20):])
	}
	if !strings.Contains(body, "…") {
		t.Errorf("truncated body should contain ellipsis, got %q", body[max(0, len(body)-20):])
	}
}

func TestFormatShortTextNotTruncated(t *testing.T) {
	_, body := Format("reply", "Alice", "alice.bsky.social", "short", false, 0)
	if body != "short" {
		t.Errorf("short body should pass through, got %q", body)
	}
}

func TestFormatUTF8BoundaryRespected(t *testing.T) {
	// Japanese post: each char is 3 bytes UTF-8; truncation must not split one.
	text := strings.Repeat("日", 2000) // 6000 bytes
	_, body := Format("reply", "Alice", "alice.bsky.social", text, false, 0)
	// Body (stripped of ellipsis if present) must be a multiple of 3 bytes
	// since every char in input is 3 bytes.
	trimmed := strings.TrimSuffix(body, "…")
	for i, r := range trimmed {
		if r == '�' {
			t.Errorf("truncation split a UTF-8 codepoint at byte %d: %q", i, body)
			break
		}
	}
}

func TestFormatAvailableZeroReturnsZWSP(t *testing.T) {
	// Give Format an impossibly large baseOverhead. Body must fall to ZWSP
	// rather than overflow or panic.
	_, body := Format("reply", "Alice", "alice.bsky.social", "hello", false, 999999)
	if body != "​" {
		t.Errorf("body under zero-budget should be ZWSP, got %q", body)
	}
}

// This is the adversarial-input guard. It is the single most important test
// in this package: it verifies that after Format returns, marshaling the
// title+body pair cannot exceed the payload budget minus safety margin,
// regardless of JSON escape expansion.
func TestFormatAdversarialInputNeverExceedsBudget(t *testing.T) {
	adversarial := []string{
		strings.Repeat(`"`, 5000), // every byte escapes to \" (2x)
		strings.Repeat(`\`, 5000), // every byte escapes to \\ (2x)
		strings.Repeat("\n", 5000), // every byte escapes to \n (2x)
		strings.Repeat("\x01", 5000), // every byte escapes to  (6x)
	}
	// Simulate the consumer's throwaway marshal. Data map carries a representative
	// set of fields that the real sendNotification populates.
	data := map[string]string{
		"reason":           "reply",
		"uri":              "at://did:plc:abcdefghijklmnop/app.bsky.feed.post/3kco5r9xyz",
		"subject":          "at://did:plc:qrstuvwxyz012345/app.bsky.feed.post/abc123",
		"recipientDid":     "did:plc:qrstuvwxyz012345",
		"actorDid":         "did:plc:abcdefghijklmnop",
		"actorDisplayName": "Alice",
		"actorHandle":      "alice.bsky.social",
	}
	type pushNotif struct {
		Token    string            `json:"token"`
		Platform string            `json:"platform"`
		Title    string            `json:"title"`
		Body     string            `json:"body"`
		Data     map[string]string `json:"data,omitempty"`
	}
	for _, input := range adversarial {
		n := pushNotif{
			Token:    "ExponentPushToken[xxxxxxxxxxxxxxxxxxxxxx]",
			Platform: "ios",
			Data:     data,
		}
		baseline, err := json.Marshal(n)
		if err != nil {
			t.Fatalf("marshal failed: %v", err)
		}
		baseOverhead := len(baseline)

		title, body := Format("reply", "Alice", "alice.bsky.social", input, true, baseOverhead)
		n.Title = title
		n.Body = body
		final, err := json.Marshal(n)
		if err != nil {
			t.Fatalf("final marshal failed: %v", err)
		}
		if len(final) > payloadBudget-safetyMargin {
			t.Errorf("final payload %d > budget %d for input %q...", len(final), payloadBudget-safetyMargin, input[:20])
		}
	}
}
```

**Note on the `max` builtin:** Go 1.21+ has a built-in `max`. This codebase is on Go 1.25 (per `go.mod`), so `max` is available.

- [ ] **Step 3.2: Run the tests to confirm they fail**

```
cd /home/yolo/Projects/atproto-push-gateway
go test ./internal/notification -v
```

Expected: some new tests FAIL — `TestFormatTruncatesLongText` fails because the current body is just `text + marker` unconditionally. `TestFormatAdversarialInputNeverExceedsBudget` fails for the same reason. Build errors reference unknown identifiers `payloadBudget`, `safetyMargin` — this is expected because the tests reference constants that Task 3 will add.

- [ ] **Step 3.3: Implement truncation in formatter.go**

Replace the entire content of `internal/notification/formatter.go` with:

```go
// Package notification renders push-notification titles and bodies.
// The functions here are pure: no I/O, no state, trivially testable.
package notification

import (
	"encoding/json"
	"fmt"
	"unicode/utf8"
)

const (
	// payloadBudget is our target envelope size. Leaves headroom inside
	// APNs (4096 bytes) and FCM (4096 bytes data payload).
	payloadBudget = 3584
	// safetyMargin covers JSON envelope overhead added by Expo/APNs/FCM
	// wrappers around our push.Notification payload.
	safetyMargin = 256

	ellipsis    = "…"
	embedMarker = " 🖼"
	zwsp        = "​"
)

var titleTemplates = map[string]string{
	"like":              "%s liked your post",
	"repost":            "%s reposted your post",
	"reply":             "%s replied to your post",
	"mention":           "%s mentioned you",
	"quote":             "%s quoted your post",
	"follow":            "%s followed you",
	"like-via-repost":   "%s liked a post you reposted",
	"repost-via-repost": "%s reposted a post you reposted",
	"verified":          "Your account has been verified",
	"unverified":        "Your account verification was removed",
}

// Format renders the user-facing title and body for a push notification.
// Pure; no I/O. baseOverhead is the byte size of the final push payload
// with Title and Body both set to empty strings — the caller measures it
// via json.Marshal of the throwaway push.Notification. Format computes
// the title's own contribution internally and truncates the body so the
// final marshaled payload stays within payloadBudget - safetyMargin.
func Format(reason, actorDisplayName, actorHandle, postText string, hasEmbed bool, baseOverhead int) (title, body string) {
	title = renderTitle(reason, actorDisplayName, actorHandle)
	if !isEnrichedReason(reason) || postText == "" {
		return title, zwsp
	}
	available := payloadBudget - safetyMargin - baseOverhead - jsonEncodedLen(title)
	body = renderBody(postText, hasEmbed, available)
	return
}

func renderTitle(reason, actorDisplayName, actorHandle string) string {
	tmpl, ok := titleTemplates[reason]
	if !ok {
		return "Notification"
	}
	if reason == "verified" || reason == "unverified" {
		return tmpl
	}
	actor := actorDisplayName
	if actor == "" {
		actor = actorHandle
	}
	if actor == "" {
		actor = "Someone"
	}
	return fmt.Sprintf(tmpl, actor)
}

func isEnrichedReason(reason string) bool {
	switch reason {
	case "reply", "mention", "quote":
		return true
	}
	return false
}

// renderBody returns the truncated body content. available is the number of
// JSON-encoded bytes the body may contribute (not counting the surrounding
// quotes, which are already accounted for in baseOverhead's "body":"" slot).
func renderBody(postText string, hasEmbed bool, available int) string {
	candidate := postText
	if hasEmbed {
		candidate += embedMarker
	}
	if jsonEncodedLen(candidate) <= available {
		return candidate
	}

	suffix := ellipsis
	if hasEmbed {
		suffix += embedMarker
	}
	if jsonEncodedLen(suffix) >= available {
		return zwsp
	}

	// Walk back until truncated text + suffix fits.
	cut := len(postText)
	if optimistic := available - jsonEncodedLen(suffix); optimistic < cut {
		cut = optimistic
	}
	for cut > 0 {
		cut = truncateToUTF8Boundary(postText, cut)
		candidate = postText[:cut] + suffix
		if jsonEncodedLen(candidate) <= available {
			return candidate
		}
		cut--
	}
	return zwsp
}

// jsonEncodedLen returns the JSON-encoded byte length of s, excluding the
// two surrounding quote characters (which are already counted in the
// caller's baseOverhead measurement).
func jsonEncodedLen(s string) int {
	b, err := json.Marshal(s)
	if err != nil {
		// json.Marshal on a string cannot fail in practice; be defensive.
		return len(s) + 2
	}
	return len(b) - 2
}

// truncateToUTF8Boundary returns the largest n' <= n such that s[:n'] ends on
// a valid UTF-8 codepoint boundary. Walks back one byte at a time from n,
// decoding the last rune. Returns 0 if no valid prefix exists.
func truncateToUTF8Boundary(s string, n int) int {
	if n > len(s) {
		n = len(s)
	}
	for n > 0 {
		r, size := utf8.DecodeLastRuneInString(s[:n])
		if r != utf8.RuneError || size > 1 {
			return n
		}
		n--
	}
	return 0
}
```

- [ ] **Step 3.4: Run the tests to confirm they pass**

```
cd /home/yolo/Projects/atproto-push-gateway
go test ./internal/notification -v
```

Expected: all tests PASS.

- [ ] **Step 3.5: Run the full suite**

```
cd /home/yolo/Projects/atproto-push-gateway
go test ./...
```

Expected: all tests PASS. (Consumer still uses its inline formatter; the notification package is not yet wired in.)

- [ ] **Step 3.6: Commit**

```
git add internal/notification/formatter.go internal/notification/formatter_test.go
git commit -m "feat(notification): add dynamic truncation with JSON escape awareness"
```

---

## Task 4: Add `extractPostContent` helper in consumer package

**Files:**
- Modify: `internal/jetstream/consumer.go`
- Modify: `internal/jetstream/consumer_test.go`

A pure helper that inspects a parsed `PostRecord` and returns `(text, hasEmbed)`. Unit tested without any sender mock. Called by `handlePost` in Task 5.

- [ ] **Step 4.1: Write the failing tests**

Append to `internal/jetstream/consumer_test.go`:

```go
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
			name: "embed images",
			rawRecord: `{"$type":"app.bsky.feed.post","text":"look","embed":{"$type":"app.bsky.embed.images","images":[]},"createdAt":"2026-04-11T00:00:00Z"}`,
			wantText:     "look",
			wantHasEmbed: true,
		},
		{
			name: "embed video",
			rawRecord: `{"$type":"app.bsky.feed.post","text":"watch","embed":{"$type":"app.bsky.embed.video"},"createdAt":"2026-04-11T00:00:00Z"}`,
			wantText:     "watch",
			wantHasEmbed: true,
		},
		{
			name: "embed external",
			rawRecord: `{"$type":"app.bsky.feed.post","text":"click","embed":{"$type":"app.bsky.embed.external"},"createdAt":"2026-04-11T00:00:00Z"}`,
			wantText:     "click",
			wantHasEmbed: true,
		},
		{
			name: "embed recordWithMedia",
			rawRecord: `{"$type":"app.bsky.feed.post","text":"quote plus pic","embed":{"$type":"app.bsky.embed.recordWithMedia"},"createdAt":"2026-04-11T00:00:00Z"}`,
			wantText:     "quote plus pic",
			wantHasEmbed: true,
		},
		{
			name: "embed record only (plain quote)",
			rawRecord: `{"$type":"app.bsky.feed.post","text":"quote","embed":{"$type":"app.bsky.embed.record","record":{"uri":"at://did:plc:x/app.bsky.feed.post/abc","cid":"bafy"}},"createdAt":"2026-04-11T00:00:00Z"}`,
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
```

- [ ] **Step 4.2: Run the tests to confirm they fail**

```
cd /home/yolo/Projects/atproto-push-gateway
go test ./internal/jetstream -v -run TestExtractPostContent
```

Expected: compile error — `extractPostContent` undefined.

- [ ] **Step 4.3: Add the helper to consumer.go**

Append to `internal/jetstream/consumer.go`, right after the `extractDIDFromURI` function (around line 421):

```go
// extractPostContent returns the text and media-embed status of a parsed
// feed post for use in notification enrichment. A "media embed" is one of
// images, video, external, or recordWithMedia; a plain record embed (quote
// without media) does not qualify.
func extractPostContent(post *PostRecord) (text string, hasEmbed bool) {
	if post == nil {
		return "", false
	}
	text = post.Text
	if post.Embed == nil {
		return text, false
	}
	switch post.Embed.Type {
	case "app.bsky.embed.images",
		"app.bsky.embed.video",
		"app.bsky.embed.external",
		"app.bsky.embed.recordWithMedia":
		hasEmbed = true
	}
	return text, hasEmbed
}
```

- [ ] **Step 4.4: Run the tests to confirm they pass**

```
cd /home/yolo/Projects/atproto-push-gateway
go test ./internal/jetstream -v -run TestExtractPostContent
```

Expected: all cases PASS.

- [ ] **Step 4.5: Run the full suite**

```
cd /home/yolo/Projects/atproto-push-gateway
go test ./...
```

Expected: all tests PASS.

- [ ] **Step 4.6: Commit**

```
git add internal/jetstream/consumer.go internal/jetstream/consumer_test.go
git commit -m "feat(jetstream): add extractPostContent helper for notification enrichment"
```

---

## Task 5: Rewire `sendNotification` to use `notification.Format`

**Files:**
- Modify: `internal/jetstream/consumer.go`

Delete the old inline formatter (`formatNotification`, `reasonTitles`, `reasonBodyTemplates`). Update `sendNotification` to accept `postText` and `hasEmbed`, compute `baseOverhead` from a throwaway marshal, and delegate to `notification.Format`. Update each call site in `handlePost`, `handleLike`, `handleRepost`, `handleFollow`, `handleVerificationCreate`, and `handleVerificationDelete`.

- [ ] **Step 5.1: Verify the current state before changes**

```
cd /home/yolo/Projects/atproto-push-gateway
go test ./...
```

Expected: PASS. This is the baseline.

- [ ] **Step 5.2: Delete the inline formatter and update `sendNotification`**

In `internal/jetstream/consumer.go`:

**Remove** the following blocks from the file:

```go
// reasonTitles maps notification reasons to English titles.
// Clients can use the data fields to format localized text instead.
var reasonTitles = map[string]string{ ... }

// reasonBodyTemplates maps notification reasons to English body templates.
var reasonBodyTemplates = map[string]string{ ... }

func formatNotification(reason, actorDisplayName, actorHandle string) (string, string) { ... }
```

**Replace** the existing `sendNotification` function with:

```go
func (c *Consumer) sendNotification(actorDID, targetDID, reason, recordURI, subjectURI, postText string, hasEmbed bool) {
	if !c.store.IsRegistered(targetDID) {
		return
	}

	if c.store.IsBlocked(actorDID, targetDID) {
		log.Printf("[jetstream] suppressed %s notification: blocked (%s -> %s)", reason, actorDID, targetDID)
		return
	}

	tokens, err := c.store.GetTokensForDID(targetDID)
	if err != nil {
		log.Printf("[jetstream] error getting tokens for %s: %v", targetDID, err)
		return
	}

	c.matchedEvents.Add(1)

	// Resolve actorDID to display name + handle for title rendering
	actorDisplayName := ""
	actorHandle := ""
	if c.profileResolver != nil {
		actorDisplayName, actorHandle = c.profileResolver.ResolveProfile(actorDID)
	}

	for _, token := range tokens {
		data := map[string]string{
			"reason":           reason,
			"uri":              recordURI,
			"recipientDid":     targetDID,
			"actorDid":         actorDID,
			"actorDisplayName": actorDisplayName,
			"actorHandle":      actorHandle,
		}
		if subjectURI != "" {
			data["subject"] = subjectURI
		}

		// Measure baseOverhead: the final push.Notification with Title and Body
		// both empty, marshaled to JSON.
		baseline := push.Notification{
			Token:    token.PushToken,
			Platform: token.Platform,
			Data:     data,
		}
		baseOverhead := 2048 // conservative fallback if Marshal fails
		if baselineJSON, err := json.Marshal(baseline); err == nil {
			baseOverhead = len(baselineJSON)
		} else {
			log.Printf("[jetstream] warning: baseOverhead marshal failed: %v", err)
		}

		title, body := notification.Format(reason, actorDisplayName, actorHandle, postText, hasEmbed, baseOverhead)

		n := push.Notification{
			Token:    token.PushToken,
			Platform: token.Platform,
			Title:    title,
			Body:     body,
			Data:     data,
		}

		if err := c.sender.Send(n); err != nil {
			c.pushErrors.Add(1)
			if errors.Is(err, push.ErrTokenInvalid) {
				log.Printf("[jetstream] removing invalid token for %s: %v", targetDID, err)
				if uerr := c.store.UnregisterToken(token.ActorDID, token.Platform, token.PushToken, token.AppID); uerr != nil {
					log.Printf("[jetstream] error removing invalid token: %v", uerr)
				}
			} else {
				log.Printf("[jetstream] push error for %s: %v", targetDID, err)
			}
		} else {
			c.pushesSent.Add(1)
		}
	}
}
```

**Add** the `notification` import at the top of `internal/jetstream/consumer.go`. The existing import block looks like:

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

	"github.com/gorilla/websocket"
	"github.com/klauspost/compress/zstd"

	"github.com/dracoblue/atproto-push-gateway/internal/profile"
	"github.com/dracoblue/atproto-push-gateway/internal/push"
	"github.com/dracoblue/atproto-push-gateway/internal/store"
)
```

Add `"github.com/dracoblue/atproto-push-gateway/internal/notification"` to the project-import group:

```go
	"github.com/dracoblue/atproto-push-gateway/internal/notification"
	"github.com/dracoblue/atproto-push-gateway/internal/profile"
	"github.com/dracoblue/atproto-push-gateway/internal/push"
	"github.com/dracoblue/atproto-push-gateway/internal/store"
```

- [ ] **Step 5.3: Update all callers of `sendNotification` to pass the new parameters**

In `internal/jetstream/consumer.go`, update each `sendNotification` call site:

**In `handleLike`:**

Replace:
```go
c.sendNotification(actorDID, targetDID, "like", recordURI, like.Subject.URI)
```
with:
```go
c.sendNotification(actorDID, targetDID, "like", recordURI, like.Subject.URI, "", false)
```

Replace:
```go
c.sendNotification(actorDID, reposterDID, "like-via-repost", recordURI, like.Subject.URI)
```
with:
```go
c.sendNotification(actorDID, reposterDID, "like-via-repost", recordURI, like.Subject.URI, "", false)
```

**In `handleRepost`:**

Replace:
```go
c.sendNotification(actorDID, targetDID, "repost", recordURI, repost.Subject.URI)
```
with:
```go
c.sendNotification(actorDID, targetDID, "repost", recordURI, repost.Subject.URI, "", false)
```

Replace:
```go
c.sendNotification(actorDID, reposterDID, "repost-via-repost", recordURI, repost.Subject.URI)
```
with:
```go
c.sendNotification(actorDID, reposterDID, "repost-via-repost", recordURI, repost.Subject.URI, "", false)
```

**In `handlePost`:**

Replace the entire function body (everything from `var post PostRecord` to the closing brace) with:

```go
	var post PostRecord
	if err := json.Unmarshal(record, &post); err != nil {
		return
	}

	postURI := fmt.Sprintf("at://%s/app.bsky.feed.post/%s", actorDID, rkey)
	postText, hasEmbed := extractPostContent(&post)

	if post.Reply != nil {
		targetDID := extractDIDFromURI(post.Reply.Parent.URI)
		if targetDID != "" && targetDID != actorDID {
			c.sendNotification(actorDID, targetDID, "reply", postURI, post.Reply.Parent.URI, postText, hasEmbed)
		}
	}

	if post.Embed != nil && post.Embed.Type == "app.bsky.embed.record" && post.Embed.Record != nil {
		targetDID := extractDIDFromURI(post.Embed.Record.URI)
		if targetDID != "" && targetDID != actorDID {
			c.sendNotification(actorDID, targetDID, "quote", postURI, post.Embed.Record.URI, postText, hasEmbed)
		}
	}

	for _, facet := range post.Facets {
		for _, feature := range facet.Features {
			if feature.Type == "app.bsky.richtext.facet#mention" && feature.DID != "" && feature.DID != actorDID {
				c.sendNotification(actorDID, feature.DID, "mention", postURI, "", postText, hasEmbed)
			}
		}
	}
}
```

Note: for the `quote` branch, `hasEmbed` will always be `false` because `extractPostContent` returns `false` for `app.bsky.embed.record` (plain quote). This is intentional per the spec — plain quotes don't earn a marker.

**In `handleFollow`:**

Replace:
```go
c.sendNotification(actorDID, follow.Subject, "follow", recordURI, "")
```
with:
```go
c.sendNotification(actorDID, follow.Subject, "follow", recordURI, "", "", false)
```

**In `handleVerificationCreate`:**

Replace:
```go
c.sendNotification(verifierDID, verification.Subject, "verified", recordURI, "")
```
with:
```go
c.sendNotification(verifierDID, verification.Subject, "verified", recordURI, "", "", false)
```

**In `handleVerificationDelete`:**

Replace:
```go
c.sendNotification(verifierDID, subjectDID, "unverified", recordURI, "")
```
with:
```go
c.sendNotification(verifierDID, subjectDID, "unverified", recordURI, "", "", false)
```

- [ ] **Step 5.4: Run the full test suite**

```
cd /home/yolo/Projects/atproto-push-gateway
go test ./...
```

Expected: all tests PASS. The existing consumer tests are parser-level only and don't assert on notification bodies, so they continue to pass unchanged.

- [ ] **Step 5.5: Build to confirm no stray references**

```
cd /home/yolo/Projects/atproto-push-gateway
go build ./...
```

Expected: clean build, no errors or warnings.

- [ ] **Step 5.6: Commit**

```
git add internal/jetstream/consumer.go
git commit -m "feat(jetstream): wire notification.Format into sendNotification

Deletes the inline formatter (reasonTitles, reasonBodyTemplates,
formatNotification). Title/body rendering is now done by the
notification package with dynamic truncation. Reply/mention/quote
notifications gain the triggering post's text; all other reasons
use ZWSP bodies per the design spec."
```

---

## Task 6: Update public docs

**Files:**
- Modify: `README.md`
- Modify: `docs/NOTIFICATIONS.md`

Update the Supported Events table, the Push Payload example, and a handful of per-reason examples so the docs match what the code actually sends.

- [ ] **Step 6.1: Update the Supported Events table in README.md**

In `README.md`, replace the existing table (around line 36–48):

```markdown
| Event | Default Title | Default Body |
|---|---|---|
| Like | New like | X liked your post |
| Repost | New repost | X reposted your post |
| Reply | New reply | X replied to your post |
| Mention | New mention | X mentioned you |
| Quote | New quote | X quoted your post |
| Follow | New follower | X followed you |
| Like via repost | New like | X liked a post you reposted |
| Repost via repost | New repost | X reposted a post you reposted |
| Verified | Verified | Your account has been verified |
| Unverified | Verification removed | Your account verification was removed |
```

with:

```markdown
| Event | Title | Body |
|---|---|---|
| Like | X liked your post | *(empty)* |
| Repost | X reposted your post | *(empty)* |
| Reply | X replied to your post | post text (+ 🖼 if media attached) |
| Mention | X mentioned you | post text (+ 🖼 if media attached) |
| Quote | X quoted your post | post text (+ 🖼 if media attached) |
| Follow | X followed you | *(empty)* |
| Like via repost | X liked a post you reposted | *(empty)* |
| Repost via repost | X reposted a post you reposted | *(empty)* |
| Verified | Your account has been verified | *(empty)* |
| Unverified | Your account verification was removed | *(empty)* |

Reply/mention/quote bodies carry the actual post text, dynamically truncated with an ellipsis if the push payload would exceed ~3.5 KB. An embed marker (🖼) is appended when the post carries images, video, an external link card, or a record-with-media embed. Notifications without body text use a single zero-width space (U+200B) as the body — invisible on screen, keeps the iOS Notification Service Extension path active.
```

- [ ] **Step 6.2: Update the Push Payload example in README.md**

Still in `README.md`, find the JSON example (around line 54):

```json
{
  "to": "ExponentPushToken[...]",
  "title": "New like",
  "body": "Alice liked your post",
  ...
}
```

Replace it with:

```json
{
  "to": "ExponentPushToken[...]",
  "title": "Alice replied to your post",
  "body": "That's a really interesting point about 🤔",
  "sound": "default",
  "mutableContent": true,
  "data": {
    "reason": "reply",
    "uri": "at://did:plc:alice/app.bsky.feed.post/3kco5r9xyz",
    "subject": "at://did:plc:bob/app.bsky.feed.post/abc123",
    "recipientDid": "did:plc:bob",
    "actorDid": "did:plc:alice",
    "actorDisplayName": "Alice",
    "actorHandle": "alice.bsky.social"
  }
}
```

- [ ] **Step 6.3: Update the per-reason examples in docs/NOTIFICATIONS.md**

In `docs/NOTIFICATIONS.md`, update every `Push Payload` block to reflect the new title/body. For reasons that are **not** enriched (like, repost, follow, verified, unverified, like-via-repost, repost-via-repost), the payload's `body` field becomes `"​"` (ZWSP) and the `title` absorbs what used to be in the body.

Replace the existing `like` Push Payload:

```json
{
  "to": "<push-token>",
  "data": { ... }
}
```

with:

```json
{
  "to": "<push-token>",
  "title": "Alice liked your post",
  "body": "\u200B",
  "sound": "default",
  "mutableContent": true,
  "data": {
    "reason": "like",
    "uri": "at://did:plc:alice/app.bsky.feed.like/3kco5r7xsgb2p",
    "subject": "at://did:plc:bob/app.bsky.feed.post/abc123",
    "recipientDid": "did:plc:bob",
    "actorDid": "did:plc:alice",
    "actorDisplayName": "Alice",
    "actorHandle": "alice.bsky.social"
  }
}
```

For the `reply` Push Payload, replace with:

```json
{
  "to": "<push-token>",
  "title": "Alice replied to your post",
  "body": "Great post!",
  "sound": "default",
  "mutableContent": true,
  "data": {
    "reason": "reply",
    "uri": "at://did:plc:alice/app.bsky.feed.post/3kco5r9xyz",
    "subject": "at://did:plc:bob/app.bsky.feed.post/abc123",
    "recipientDid": "did:plc:bob",
    "actorDid": "did:plc:alice",
    "actorDisplayName": "Alice",
    "actorHandle": "alice.bsky.social"
  }
}
```

For the `mention` Push Payload, replace with:

```json
{
  "to": "<push-token>",
  "title": "Alice mentioned you",
  "body": "Hey @bob check this out",
  "sound": "default",
  "mutableContent": true,
  "data": {
    "reason": "mention",
    "uri": "at://did:plc:alice/app.bsky.feed.post/3kco5radef",
    "recipientDid": "did:plc:bob",
    "actorDid": "did:plc:alice",
    "actorDisplayName": "Alice",
    "actorHandle": "alice.bsky.social"
  }
}
```

For the `quote` Push Payload, replace with:

```json
{
  "to": "<push-token>",
  "title": "Alice quoted your post",
  "body": "This is so true!",
  "sound": "default",
  "mutableContent": true,
  "data": {
    "reason": "quote",
    "uri": "at://did:plc:alice/app.bsky.feed.post/3kco5rbghi",
    "subject": "at://did:plc:bob/app.bsky.feed.post/abc123",
    "recipientDid": "did:plc:bob",
    "actorDid": "did:plc:alice",
    "actorDisplayName": "Alice",
    "actorHandle": "alice.bsky.social"
  }
}
```

For the `repost` Push Payload, replace with (title absorbs, body ZWSP):

```json
{
  "to": "<push-token>",
  "title": "Alice reposted your post",
  "body": "\u200B",
  "sound": "default",
  "mutableContent": true,
  "data": {
    "reason": "repost",
    "uri": "at://did:plc:alice/app.bsky.feed.repost/3kco5r8abc",
    "subject": "at://did:plc:bob/app.bsky.feed.post/abc123",
    "recipientDid": "did:plc:bob",
    "actorDid": "did:plc:alice",
    "actorDisplayName": "Alice",
    "actorHandle": "alice.bsky.social"
  }
}
```

For `follow`, replace with:

```json
{
  "to": "<push-token>",
  "title": "Alice followed you",
  "body": "\u200B",
  "sound": "default",
  "mutableContent": true,
  "data": {
    "reason": "follow",
    "uri": "at://did:plc:alice/app.bsky.graph.follow/3kco5rcjkl",
    "recipientDid": "did:plc:bob",
    "actorDid": "did:plc:alice",
    "actorDisplayName": "Alice",
    "actorHandle": "alice.bsky.social"
  }
}
```

For `like-via-repost`:

```json
{
  "to": "<push-token>",
  "title": "Alice liked a post you reposted",
  "body": "\u200B",
  "sound": "default",
  "mutableContent": true,
  "data": {
    "reason": "like-via-repost",
    "uri": "at://did:plc:alice/app.bsky.feed.like/3l3qo2vuowo2b",
    "subject": "at://did:plc:bob/app.bsky.feed.post/postid123",
    "recipientDid": "did:plc:carol",
    "actorDid": "did:plc:alice",
    "actorDisplayName": "Alice",
    "actorHandle": "alice.bsky.social"
  }
}
```

For `repost-via-repost`:

```json
{
  "to": "<push-token>",
  "title": "Dave reposted a post you reposted",
  "body": "\u200B",
  "sound": "default",
  "mutableContent": true,
  "data": {
    "reason": "repost-via-repost",
    "uri": "at://did:plc:dave/app.bsky.feed.repost/3l3qo2vuxyz2c",
    "subject": "at://did:plc:bob/app.bsky.feed.post/postid123",
    "recipientDid": "did:plc:carol",
    "actorDid": "did:plc:dave",
    "actorDisplayName": "Dave",
    "actorHandle": "dave.bsky.social"
  }
}
```

For `verified`:

```json
{
  "to": "<push-token>",
  "title": "Your account has been verified",
  "body": "\u200B",
  "sound": "default",
  "mutableContent": true,
  "data": {
    "reason": "verified",
    "uri": "at://did:plc:verifier-authority/app.bsky.graph.verification/3l3qo2vvvvv2c",
    "recipientDid": "did:plc:bob",
    "actorDid": "did:plc:verifier-authority",
    "actorDisplayName": "Bluesky Verification",
    "actorHandle": "verification.bsky.app"
  }
}
```

For `unverified`:

```json
{
  "to": "<push-token>",
  "title": "Your account verification was removed",
  "body": "\u200B",
  "sound": "default",
  "mutableContent": true,
  "data": {
    "reason": "unverified",
    "uri": "at://did:plc:verifier-authority/app.bsky.graph.verification/3l3qo2vvvvv2c",
    "recipientDid": "did:plc:bob",
    "actorDid": "did:plc:verifier-authority",
    "actorDisplayName": "Bluesky Verification",
    "actorHandle": "verification.bsky.app"
  }
}
```

- [ ] **Step 6.4: Add a short note about enrichment at the top of docs/NOTIFICATIONS.md**

In `docs/NOTIFICATIONS.md`, right after the first-line intro paragraph (after `... with example Jetstream events and resulting push payloads.`), add:

```markdown
## Title/Body Layout

The gateway renders an actor-centric **title** ("Alice replied to your post") and, for reply/mention/quote reasons, places the triggering post's text in the **body**. Bodies are dynamically truncated so the final JSON payload stays within ~3.5 KB (well under the APNs/FCM 4 KB limit), with an ellipsis appended when truncated and a trailing 🖼 marker when the post carries a media embed (`app.bsky.embed.images`, `app.bsky.embed.video`, `app.bsky.embed.external`, or `app.bsky.embed.recordWithMedia`). Plain quote embeds (`app.bsky.embed.record` without media) do not earn a marker — the "quoted your post" title already conveys the relationship.

Notifications that carry no post text (likes, reposts, follows, verified/unverified, and the `-via-repost` variants) use a single zero-width space (U+200B) as the body. This is invisible on screen and keeps iOS's Notification Service Extension path active, which gets finicky with truly empty bodies.
```

- [ ] **Step 6.5: Commit**

```
git add README.md docs/NOTIFICATIONS.md
git commit -m "docs(notifications): update payload examples for richer-body format"
```

---

## Final Verification

After Task 6:

- [ ] **Run the full test suite**

```
cd /home/yolo/Projects/atproto-push-gateway
go test ./...
```

Expected: all tests PASS.

- [ ] **Run the build**

```
cd /home/yolo/Projects/atproto-push-gateway
go build ./...
```

Expected: clean build.

- [ ] **Check the git log for the feature branch**

```
cd /home/yolo/Projects/atproto-push-gateway
git log --oneline main..HEAD
```

Expected to see (in order):
- `feat(notification): add formatter package with title rendering`
- `feat(notification): enrich reply/mention/quote body with post text`
- `feat(notification): add dynamic truncation with JSON escape awareness`
- `feat(jetstream): add extractPostContent helper for notification enrichment`
- `feat(jetstream): wire notification.Format into sendNotification`
- `docs(notifications): update payload examples for richer-body format`

Plus the two pre-existing spec commits at the base of the branch.
