package posttext

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// appViewResponse mirrors the structure the fetcher's tests return.
type appViewResponse struct {
	Posts []appViewPost `json:"posts"`
}

type appViewPost struct {
	URI    string                 `json:"uri"`
	Record map[string]interface{} `json:"record"`
}

func newFetchTestServer(t *testing.T, handler func(uri string) appViewResponse) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/xrpc/app.bsky.feed.getPosts") {
			t.Errorf("unexpected path: %s", r.URL.Path)
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		uris := r.URL.Query()["uris[]"]
		if len(uris) != 1 {
			t.Errorf("expected exactly one uris[] query param, got %d: %v", len(uris), uris)
		}
		resp := handler(uris[0])
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
}

func TestAppViewFetcherPositiveWithoutEmbed(t *testing.T) {
	ts := newFetchTestServer(t, func(uri string) appViewResponse {
		return appViewResponse{Posts: []appViewPost{{
			URI: uri,
			Record: map[string]interface{}{
				"$type": "app.bsky.feed.post",
				"text":  "hello world",
			},
		}}}
	})
	defer ts.Close()

	f := &AppViewFetcher{Client: ts.Client(), BaseURL: ts.URL}
	text, hasEmbed, found, err := f.Fetch(context.Background(), "at://did:plc:alice/app.bsky.feed.post/abc")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !found {
		t.Errorf("found = false, want true")
	}
	if text != "hello world" {
		t.Errorf("text = %q, want %q", text, "hello world")
	}
	if hasEmbed {
		t.Errorf("hasEmbed = true, want false")
	}
}

func TestAppViewFetcherMediaEmbedVariants(t *testing.T) {
	cases := []struct {
		name         string
		embedType    string
		wantHasEmbed bool
	}{
		{"images", "app.bsky.embed.images", true},
		{"video", "app.bsky.embed.video", true},
		{"external", "app.bsky.embed.external", true},
		{"recordWithMedia", "app.bsky.embed.recordWithMedia", true},
		{"record alone (plain quote)", "app.bsky.embed.record", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ts := newFetchTestServer(t, func(uri string) appViewResponse {
				return appViewResponse{Posts: []appViewPost{{
					URI: uri,
					Record: map[string]interface{}{
						"$type": "app.bsky.feed.post",
						"text":  "x",
						"embed": map[string]interface{}{
							"$type": tc.embedType,
						},
					},
				}}}
			})
			defer ts.Close()

			f := &AppViewFetcher{Client: ts.Client(), BaseURL: ts.URL}
			_, hasEmbed, _, err := f.Fetch(context.Background(), "at://did:plc:alice/app.bsky.feed.post/abc")
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if hasEmbed != tc.wantHasEmbed {
				t.Errorf("hasEmbed = %v, want %v", hasEmbed, tc.wantHasEmbed)
			}
		})
	}
}

func TestAppViewFetcherEmptyPostsArray(t *testing.T) {
	ts := newFetchTestServer(t, func(uri string) appViewResponse {
		return appViewResponse{Posts: []appViewPost{}}
	})
	defer ts.Close()

	f := &AppViewFetcher{Client: ts.Client(), BaseURL: ts.URL}
	text, hasEmbed, found, err := f.Fetch(context.Background(), "at://did:plc:nobody/app.bsky.feed.post/gone")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if found {
		t.Errorf("found = true, want false for empty posts array")
	}
	if text != "" || hasEmbed {
		t.Errorf("text/hasEmbed = (%q, %v), want (\"\", false)", text, hasEmbed)
	}
}

func TestAppViewFetcherHTTPError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer ts.Close()

	f := &AppViewFetcher{Client: ts.Client(), BaseURL: ts.URL}
	_, _, _, err := f.Fetch(context.Background(), "at://did:plc:alice/app.bsky.feed.post/abc")
	if err == nil {
		t.Errorf("expected error on HTTP 500, got nil")
	}
}

func TestAppViewFetcherMalformedJSON(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("{not json"))
	}))
	defer ts.Close()

	f := &AppViewFetcher{Client: ts.Client(), BaseURL: ts.URL}
	_, _, _, err := f.Fetch(context.Background(), "at://did:plc:alice/app.bsky.feed.post/abc")
	if err == nil {
		t.Errorf("expected error on malformed JSON, got nil")
	}
}

func TestAppViewFetcherContextCancelled(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(100 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"posts":[]}`))
	}))
	defer ts.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()

	f := &AppViewFetcher{Client: ts.Client(), BaseURL: ts.URL}
	_, _, _, err := f.Fetch(ctx, "at://did:plc:alice/app.bsky.feed.post/abc")
	if err == nil {
		t.Errorf("expected error on context deadline, got nil")
	}
}

func TestAppViewFetcherURIEncoding(t *testing.T) {
	// Subject URIs contain ':' and '/' — make sure they round-trip through
	// query-string encoding verbatim.
	const uri = "at://did:plc:alice/app.bsky.feed.post/3kco5r7xsgb2p"
	var seenURI string
	ts := newFetchTestServer(t, func(u string) appViewResponse {
		seenURI = u
		return appViewResponse{Posts: []appViewPost{{
			URI:    u,
			Record: map[string]interface{}{"text": "x"},
		}}}
	})
	defer ts.Close()

	f := &AppViewFetcher{Client: ts.Client(), BaseURL: ts.URL}
	_, _, _, err := f.Fetch(context.Background(), uri)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if seenURI != uri {
		t.Errorf("server saw uri %q, expected %q", seenURI, uri)
	}
}
