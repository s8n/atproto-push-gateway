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
