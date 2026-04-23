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
//
// An empty body is returned as "". Platform-specific senders decide how
// to handle an empty body on the wire (iOS needs a zero-width space to
// render as title-only; Android accepts an empty string).
func Format(reason, actorDisplayName, actorHandle, postText string, hasEmbed bool, baseOverhead int) (title, body string) {
	title = renderTitle(reason, actorDisplayName, actorHandle)
	if !isEnrichedReason(reason) || postText == "" {
		return title, ""
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
	case "reply", "mention", "quote",
		"like", "repost", "like-via-repost", "repost-via-repost":
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
		return ""
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
	return ""
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
