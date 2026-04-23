package notification

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/dracoblue/atproto-push-gateway/internal/push"
)

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

func TestFormatTitleTrimsActorWhitespace(t *testing.T) {
	// Bluesky does not forbid leading/trailing whitespace in displayName
	// (real example: did:plc:6ljix2gyn5vvbkiwtkezsij5 has displayName "Rune ").
	// Without trimming, the template's own space produces double-space titles.
	cases := []struct {
		name, display, handle, wantTitle string
	}{
		{"trailing space in display", "Rune ", "runefar.bsky.social", "Rune liked your post"},
		{"leading space in display", " Alice", "alice.bsky.social", "Alice liked your post"},
		{"tab in display", "Alice\t", "alice.bsky.social", "Alice liked your post"},
		{"nbsp in display", "Alice ", "alice.bsky.social", "Alice liked your post"},
		{"newline in display", "Alice\n", "alice.bsky.social", "Alice liked your post"},
		{"whitespace-only display falls back to handle", "   ", "alice.bsky.social", "alice.bsky.social liked your post"},
		{"padded handle when no display", "", " alice.bsky.social ", "alice.bsky.social liked your post"},
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
	if body != "" {
		t.Errorf("body for unknown reason = %q, want empty string", body)
	}
}

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

func TestFormatBodyEmptyTextFallsToEmpty(t *testing.T) {
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
			if body != "" {
				t.Errorf("body = %q, want empty string", body)
			}
		})
	}
}

func TestFormatBodyNonEnrichedReasonsAlwaysEmpty(t *testing.T) {
	cases := []struct {
		name, reason string
	}{
		{"follow", "follow"},
		{"verified", "verified"},
		{"unverified", "unverified"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Even with postText provided, these reasons must not enrich.
			_, body := Format(tc.reason, "Alice", "alice.bsky.social", "ignored", true, 0)
			if body != "" {
				t.Errorf("body = %q, want empty string", body)
			}
		})
	}
}

func TestFormatBodyEnrichesLikeRepostFamily(t *testing.T) {
	cases := []struct {
		name, reason, text string
		hasEmbed           bool
		wantBody           string
	}{
		{"like with text", "like", "Great post!", false, "Great post!"},
		{"like with embed", "like", "Check this", true, "Check this 🖼"},
		{"repost with text", "repost", "Shared this.", false, "Shared this."},
		{"like-via-repost", "like-via-repost", "Via repost", false, "Via repost"},
		{"repost-via-repost", "repost-via-repost", "Chain share", false, "Chain share"},
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

func TestFormatBodyWhitespacePassThrough(t *testing.T) {
	// Whitespace inside post text must be preserved byte-for-byte.
	text := "line one\n\nline\ttwo   trailing"
	_, body := Format("reply", "Alice", "alice.bsky.social", text, false, 0)
	if body != text {
		t.Errorf("body = %q, want %q (whitespace must pass through)", body, text)
	}
}

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

func TestFormatAvailableZeroReturnsEmpty(t *testing.T) {
	// Give Format an impossibly large baseOverhead. Body must fall to empty
	// rather than overflow or panic.
	_, body := Format("reply", "Alice", "alice.bsky.social", "hello", false, 999999)
	if body != "" {
		t.Errorf("body under zero-budget should be empty, got %q", body)
	}
}

// This is the adversarial-input guard. It is the single most important test
// in this package: it verifies that after Format returns, marshaling the
// title+body pair cannot exceed the payload budget minus safety margin,
// regardless of JSON escape expansion.
func TestFormatAdversarialInputNeverExceedsBudget(t *testing.T) {
	adversarial := []string{
		strings.Repeat(`"`, 5000),    // every byte escapes to \" (2x)
		strings.Repeat(`\`, 5000),    // every byte escapes to \\ (2x)
		strings.Repeat("\n", 5000),   // every byte escapes to \n (2x)
		strings.Repeat("\x01", 5000), // every byte escapes to  (6x)
		strings.Repeat("", 5000),    // 2-byte UTF-8 rune that json escapes to  (6 bytes)
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
	for _, input := range adversarial {
		n := push.Notification{
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

	// Adversarial display-name guard: ensure the budget invariant also holds
	// when the title source is long and/or escape-heavy. Today Bluesky caps
	// displayName at 640 chars and handle is shorter, so the invariant holds
	// with real inputs — but if those limits ever change, or if anyone adds
	// escape-heavy title sources, this tripwire fires.
	adversarialNames := []string{
		strings.Repeat("A", 640), // longest realistic displayName
		strings.Repeat(`"`, 640), // worst-case escape expansion within cap
		strings.Repeat(`\`, 640), // worst-case escape expansion within cap
		strings.Repeat("", 640),  // multi-byte + escape-heavy
	}
	for _, name := range adversarialNames {
		n := push.Notification{
			Token:    "ExponentPushToken[xxxxxxxxxxxxxxxxxxxxxx]",
			Platform: "ios",
			Data:     data,
		}
		baseline, err := json.Marshal(n)
		if err != nil {
			t.Fatalf("marshal failed: %v", err)
		}
		baseOverhead := len(baseline)

		title, body := Format("reply", name, "alice.bsky.social", "normal reply text", false, baseOverhead)
		n.Title = title
		n.Body = body
		final, err := json.Marshal(n)
		if err != nil {
			t.Fatalf("final marshal failed: %v", err)
		}
		if len(final) > payloadBudget-safetyMargin {
			t.Errorf("final payload %d > budget %d for displayName of length %d (first 20: %q...)",
				len(final), payloadBudget-safetyMargin, len(name), name[:min(20, len(name))])
		}
	}
}
