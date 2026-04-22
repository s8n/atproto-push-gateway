package posttext

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
)

// Fetcher retrieves a post by AT-URI. CachedProvider uses it as the miss-path
// backend. Exported as an interface so CachedProvider's tests can stub it.
type Fetcher interface {
	// Fetch returns (text, hasEmbed, found, err).
	// found=false with err=nil means the post doesn't exist (treated as a
	// hard negative — safe to negative-cache). err != nil means a transient
	// failure (HTTP, parse, network) — callers should not cache these.
	Fetch(ctx context.Context, uri string) (text string, hasEmbed bool, found bool, err error)
}

// DefaultAppViewBaseURL is Bluesky's public read-only AppView.
const DefaultAppViewBaseURL = "https://public.api.bsky.app"

// AppViewFetcher is the production Fetcher. It calls
// {BaseURL}/xrpc/app.bsky.feed.getPosts?uris[]=<uri> and parses the response.
type AppViewFetcher struct {
	Client  *http.Client // required; caller owns lifecycle
	BaseURL string       // defaults to DefaultAppViewBaseURL when empty
}

// getPostsResponse mirrors the relevant subset of the AppView response.
// Other fields (cid, author, indexedAt, ...) are ignored.
type getPostsResponse struct {
	Posts []struct {
		URI    string `json:"uri"`
		Record struct {
			Text  string `json:"text"`
			Embed *struct {
				Type string `json:"$type"`
			} `json:"embed,omitempty"`
		} `json:"record"`
	} `json:"posts"`
}

func (f *AppViewFetcher) Fetch(ctx context.Context, uri string) (string, bool, bool, error) {
	base := f.BaseURL
	if base == "" {
		base = DefaultAppViewBaseURL
	}
	endpoint := base + "/xrpc/app.bsky.feed.getPosts"

	q := url.Values{}
	q.Set("uris[]", uri)
	reqURL := endpoint + "?" + q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return "", false, false, fmt.Errorf("build request: %w", err)
	}
	resp, err := f.Client.Do(req)
	if err != nil {
		return "", false, false, fmt.Errorf("http do: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", false, false, fmt.Errorf("appview status %d", resp.StatusCode)
	}

	var parsed getPostsResponse
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return "", false, false, fmt.Errorf("decode response: %w", err)
	}

	if len(parsed.Posts) == 0 {
		return "", false, false, nil
	}

	post := parsed.Posts[0]
	hasEmbed := false
	if post.Record.Embed != nil {
		switch post.Record.Embed.Type {
		case "app.bsky.embed.images",
			"app.bsky.embed.video",
			"app.bsky.embed.external",
			"app.bsky.embed.recordWithMedia":
			hasEmbed = true
		}
	}
	return post.Record.Text, hasEmbed, true, nil
}
