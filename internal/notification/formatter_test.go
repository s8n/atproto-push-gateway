package notification

import (
	"encoding/json"
	"strings"
	"testing"
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

func TestFormatUnknownReason(t *testing.T) {
	title, body := Format("never-heard-of-it", "Alice", "alice.bsky.social", "some text", true, 0)
	if title != "Notification" {
		t.Errorf("title for unknown reason = %q, want %q", title, "Notification")
	}
	if body != "​" {
		t.Errorf("body for unknown reason = %q, want ZWSP", body)
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
		strings.Repeat(`"`, 5000),    // every byte escapes to \" (2x)
		strings.Repeat(`\`, 5000),    // every byte escapes to \\ (2x)
		strings.Repeat("\n", 5000),   // every byte escapes to \n (2x)
		strings.Repeat("\x01", 5000), // every byte escapes to  (6x)
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
