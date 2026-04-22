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
