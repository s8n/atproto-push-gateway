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
