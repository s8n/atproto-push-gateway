# Richer Push Notifications — Design

**Goal:** When the gateway has the text of a post that triggered a notification (reply / mention / quote), surface it in the push notification body so the recipient sees meaningful content on the lockscreen instead of a generic "Alice replied to your post".

**Non-goal (this PR):** Subject-post text for likes/reposts. That requires fetching the subject post from the AppView and caching it (Redis). Planned as a follow-up; this change is deliberately scoped so the follow-up is additive.

**Client (cope.works) context:** The iOS NSE and the Android background handler do **not** currently rewrite `title`/`body`. They pass the server-sent strings through unchanged. So enrichment must happen server-side to be visible — placing extra data in the `data` dictionary alone would produce no visible change today. This PR chooses server-side body enrichment for that reason.

---

## Scope

| Reason | Enriched in this PR? | Source of text |
|---|---|---|
| `reply` | yes | `record.text` from the Jetstream commit |
| `mention` | yes | `record.text` from the Jetstream commit |
| `quote` | yes | `record.text` from the Jetstream commit |
| `like` | no | (future: fetch subject post + cache) |
| `repost` | no | (future: fetch subject post + cache) |
| `like-via-repost` | no | (future) |
| `repost-via-repost` | no | (future) |
| `follow` | no | n/a |
| `verified` / `unverified` | no | n/a |

The three enriched reasons all carry their own text in the Jetstream event we've already parsed — zero new I/O, no rate-limit exposure, no cache required.

---

## Notification layout change

The title/body semantics shift:

| | Current | New |
|---|---|---|
| **Title** | generic category (`"New reply"`) | actor-centric action (`"Alice replied to your post"`) |
| **Body** | actor-centric action (`"Alice replied to your post"`) | post text when available; otherwise ZWSP |

The old body string becomes the new title. The body slot is repurposed for the post content. For non-enriched reasons (like / repost / follow / etc.) the body slot becomes a single zero-width space (ZWSP, `U+200B`).

### Why ZWSP and not empty body

iOS APNs with `mutable-content: 1` (which the gateway sets today to trigger the NSE) plus an empty `alert.body` has been observed to behave inconsistently across iOS versions — in some cases the NSE does not fire. A single zero-width space is visually indistinguishable from empty on screen but reliably keeps the NSE path active. The cost is one extra UTF-8 character (3 bytes).

### Title templates

```
like              -> "%s liked your post"
repost            -> "%s reposted your post"
reply             -> "%s replied to your post"
mention           -> "%s mentioned you"
quote             -> "%s quoted your post"
follow            -> "%s followed you"
like-via-repost   -> "%s liked a post you reposted"
repost-via-repost -> "%s reposted a post you reposted"
verified          -> "Your account has been verified"
unverified        -> "Your account verification was removed"
```

Actor substitution precedence (unchanged): `actorDisplayName` → `actorHandle` → `"Someone"`. `verified` / `unverified` take no substitution.

Unknown/future reasons fall back to a literal title `"Notification"` and ZWSP body. Never panics.

### Body rules

```
if reason ∈ {reply, mention, quote} AND postText != "":
    body = postText                   # whitespace passed through byte-for-byte
    if hasEmbed:
        body += " 🖼"                 # single trailing marker
    body = truncate(body, overhead)   # see Dynamic truncation below
else:
    body = "​"
```

### Embed detection for the 🖼 marker

A reply/mention/quote post "has a media embed" if `record.embed` is present and its `$type` is one of:

- `app.bsky.embed.images`
- `app.bsky.embed.video`
- `app.bsky.embed.external`
- `app.bsky.embed.recordWithMedia`

`app.bsky.embed.record` alone (a plain quote with no media) does **not** earn a marker. The quote relationship is already signaled by the `quote` reason's title; marking every quote post with 🖼 would be misleading.

### Whitespace

Post text is passed through verbatim. Newlines, tabs, multiple spaces, trailing whitespace — all preserved. Rationale: the author wrote it that way; lockscreens handle it fine; normalization loses intent.

---

## Dynamic truncation

Fixed char limits are replaced with a budget-driven truncator: as much post text as fits in the push payload, no more.

### Constants

```go
const (
    payloadBudget = 3584  // target envelope size; leaves headroom inside APNs 4096 / FCM 4096
    safetyMargin  = 256   // JSON envelope overhead added by Expo/APNs/FCM wrappers
    ellipsis      = "…"       // U+2026, 3 bytes UTF-8
    embedMarker   = " 🖼"     // space + U+1F5BC, 5 bytes UTF-8
    zwsp          = "​"  // U+200B, 3 bytes UTF-8
)
```

### Inputs

- `text` — proposed body (post text, whitespace preserved)
- `hasEmbed` — whether to append the 🖼 marker
- `overhead` — bytes consumed by everything else in the final push message: title, all `data` fields, token, JSON scaffolding, quotes, commas, keys. Computed once by the caller and passed in.

### Overhead computation

The caller (`consumer.go`) builds a throwaway `push.Notification` with `Body: ""` and all other final fields filled in, JSON-marshals it, measures `len(json)`, and passes that number to the truncator. The throwaway marshal already contains `"body":""` — the `"body":` key plus two empty quotes. When the real body is inserted, those empty quotes stay and only the encoded content between them changes. The `jsonEncodedLen(s)` helper subtracts 2 for the surrounding quotes so it reports exactly the delta vs. the empty-body baseline.

### JSON escape awareness

The truncator compares the *JSON-encoded* length of the candidate body against `available`, not the raw byte length. Post text is escape-encoded on its way into JSON: backslashes double, double-quotes become `\"`, newlines become `\n`, etc. A raw text byte length of `N` can produce a JSON-encoded length up to `2N` (pathological case of all `"` or `\`). Typical prose is within 1–2% of the raw length. Raw-length comparison would let an adversarial post push the final payload over the APNs/FCM limit.

### Algorithm

```
available = payloadBudget - safetyMargin - overhead

candidate = text
if hasEmbed:
    candidate += " 🖼"

# Fast path: whole candidate fits.
if jsonEncodedLen(candidate) <= available:
    return candidate

# Room for any content at all?
suffixLen = jsonEncodedLen(ellipsis + (embedMarker if hasEmbed else ""))
if suffixLen >= available:
    return zwsp

# Walk back until truncated text + suffix fits.
# Seed at the optimistic byte position assuming 1:1 encoding.
cut = min(len(text), available - suffixLen)
loop:
    cut = truncateToUTF8Boundary(text, cut)
    candidate = text[:cut] + ellipsis + (embedMarker if hasEmbed else "")
    if jsonEncodedLen(candidate) <= available:
        return candidate
    cut -= 1
    if cut <= 0:
        return zwsp
```

Where `jsonEncodedLen(s)` is `len(x) - 2` with `x, _ := json.Marshal(s)` — the encoded form without its surrounding quotes (those two bytes are already counted in `overhead`'s `"body":""` segment).

### UTF-8 boundary cut

`truncateToUTF8Boundary(s, cut)` walks back one byte at a time from position `cut` using `utf8.DecodeLastRuneInString`, stopping at the first position where the last rune is valid (not `utf8.RuneError`). Standard library only, no external dependencies.

**Grapheme clusters are not preserved.** A post ending in a multi-codepoint emoji (e.g. `👨‍👩‍👧`) may be cut mid-sequence, leaving only part of the cluster before the ellipsis. The ellipsis makes truncation visible; the tradeoff (simplicity, no `golang.org/x/text/unicode/norm` dependency) is acceptable.

### Walk-back cost

Post text is bounded by Bluesky's 300-grapheme (≤3000-byte) limit. The walk-back loop runs at most once per byte, each iteration doing one small `json.Marshal`. Worst case: ~3000 iterations of a ~3 KB marshal. Measured at well under 1 ms per notification on commodity hardware, dominated by the AppView/APNs network call that follows. Not a hot-path concern.

### Why 3584 + 256 is conservative

Expo wraps our payload in `{"to": "...", ...}` (~100 bytes). APNs wraps again with the `aps` alert structure (~60 bytes). FCM's `message.android.data` similarly. 256 bytes covers the largest of these with margin. If a future profile has an extraordinarily long handle or display name, the truncator silently shrinks the body — worst case: body becomes ZWSP, notification still delivers.

---

## Code organization

### New package: `internal/notification/`

```
internal/notification/
├── formatter.go
└── formatter_test.go
```

**`formatter.go`** exports:

```go
// Format renders the user-facing title and body for a notification.
// Pure; no I/O. overhead is the byte size of everything else in the
// final push payload (title, data fields, token, JSON scaffolding).
func Format(
    reason string,
    actorDisplayName string,
    actorHandle string,
    postText string,
    hasEmbed bool,
    overhead int,
) (title string, body string)
```

and the constants block above. Title templates live here as a package-level `map[string]string`. The function is trivially table-testable.

### Changes to `internal/jetstream/consumer.go`

- The inline `formatNotification`, `reasonTitles`, and `reasonBodyTemplates` maps are **deleted**.
- `sendNotification` grows two parameters: `postText string, hasEmbed bool`.
- `handlePost` extracts `post.Text` and the embed `$type` from the already-parsed `PostRecord` and passes them through for `reply`, `mention`, and `quote`. Quote's embed-type check excludes `app.bsky.embed.record` per the rule above.
- `handleLike`, `handleRepost`, `handleFollow`, `handleVerificationCreate`, `handleVerificationDelete`, and the `-via-repost` paths pass `"", false`.
- Before calling `notification.Format`, `sendNotification` builds a throwaway `push.Notification` with `Body: ""` and the final `Data` map, marshals it to measure `overhead`, then calls `Format`.

### No other changes

- `push/`, `store/`, `xrpc/`, `profile/`, `did/` are untouched.
- No new dependencies.
- No new configuration, environment variables, or runtime flags.
- No changes to the wire format (`data` dictionary fields are identical to today).

### `PostTextProvider` interface — deferred

An earlier design draft introduced a `PostTextProvider` interface as a seam for the future Redis-backed subject-post lookup. It has been **cut from this PR** — the sole implementation today would be a stub (`always returns ok=false`) with no consumer. When the Redis work starts, the interface and `CachedProvider` implementation will be introduced together in a single coherent change. Nothing in this PR's structure prevents that.

---

## Data flow

### Path A — reply / mention / quote (text comes from the triggering commit)

```
Jetstream commit (app.bsky.feed.post with reply/embed.record/facet#mention)
    │
    ▼
consumer.handlePost() parses PostRecord (already does this today)
    │   extracts: record.text, record.embed (for 🖼 marker)
    ▼
consumer.sendNotification(..., postText, hasEmbed)
    │
    ▼
compute overhead (marshal push.Notification with Body="")
    │
    ▼
notification.Format(reason, actor, postText, hasEmbed, overhead)
    │
    ▼
push.Notification{Title, Body, Data}  → sender
```

### Path B — like / repost / follow / verified / unverified

```
Jetstream commit
    │
    ▼
consumer.handleLike / handleRepost / handleFollow / ...
    │
    ▼
consumer.sendNotification(..., "", false)
    │
    ▼
notification.Format(reason, actor, "", false, overhead)
    │   body = ZWSP; title carries the action
    ▼
push.Notification{Title, Body=ZWSP, Data}  → sender
```

---

## Error handling

Almost nothing new — the feature is pure text manipulation over already-parsed data.

- **Malformed `PostRecord`** — already handled by existing `json.Unmarshal` error path; function returns early, no notification sent. No change.
- **Post text is empty string (`""`)** — treated as "no text available"; body falls to ZWSP. Same code path as like/follow.
- **Invalid UTF-8 in post text** — should not happen (Jetstream guarantees valid UTF-8), but if it does, `utf8.DecodeLastRune` returns `RuneError` and the walk-back loop eventually reaches a valid boundary or cuts to empty. Worst case: ZWSP body. No panic.
- **`json.Marshal` fails when computing overhead** — cannot happen for `push.Notification`'s shape (no chans/funcs/custom marshalers), but defensively: on marshal error, log at `[jetstream]` warning level and send the notification with `body = ZWSP` (skip enrichment). Preserves delivery.
- **Unknown reason** — title falls back to `"Notification"`, body to ZWSP. Never panics.

No new logging categories. Existing `[jetstream]` / `[push]` logs cover this.

---

## Testing

Unit tests only; the feature has no I/O surface.

### `internal/notification/formatter_test.go`

- Title rendering for every reason, with/without `displayName`, with only `handle`, with neither (→ `"Someone"`).
- `verified` / `unverified` do not substitute actor name.
- Unknown reason → title `"Notification"`, body ZWSP.
- Body = ZWSP when `postText == ""` regardless of `hasEmbed`.
- Body = text (no ellipsis) when `postText + marker` fits within budget.
- Body = text + `" 🖼"` when embed present and text + marker fit.
- Body = text + `"…"` when truncated, no embed.
- Body = text + `"…"` + `" 🖼"` when truncated with embed.
- Truncation lands on UTF-8 codepoint boundary — multi-byte tests with `"café"`, `"日本語"`.
- Post text ending mid-rune (forced invalid UTF-8 at tail) — no panic, produces valid UTF-8.
- `available <= 0` path → body = ZWSP, no panic.
- Whitespace pass-through — newlines, tabs, multiple spaces preserved byte-for-byte.
- JSON-escape-heavy post text (all `"` characters, all `\` characters, mixed control chars) — after formatting, a round-trip marshal of `push.Notification{Title, Body, Data}` yields JSON of length `<= payloadBudget - safetyMargin`. This is the anti-adversarial guard.

### `internal/jetstream/consumer_test.go` (additions)

- Reply with post text → captured `Notification.Body` equals the text (no truncation in test inputs).
- Reply with post text + image embed → body ends with `" 🖼"`.
- Reply with post text + `app.bsky.embed.record` only → body does **not** end with `" 🖼"`.
- Mention with post text → body equals the text.
- Quote with post text + image via `recordWithMedia` → body ends with `" 🖼"`.
- Like → body equals `"​"`.
- Follow → body equals `"​"`.
- Title for reply includes actor display name (`"Alice replied to your post"`).

### Verification gate

```
cd /home/yolo/Projects/atproto-push-gateway
go test ./...
```

All existing tests must continue to pass. The consumer tests that previously asserted the old title/body format will be updated to assert the new format as part of this change.

---

## Follow-up (out of scope)

When the subject-post enrichment for likes/reposts is implemented:

1. Introduce `notification.PostTextProvider` interface.
2. Introduce `CachedProvider` backed by Redis with AppView `getPosts` on miss, TTL + negative caching.
3. Wire `CachedProvider` into `Consumer` via `NewConsumer`.
4. Update `handleLike`, `handleRepost`, `handleLikeViaRepost`, `handleRepostViaRepost` to call `PostText(subjectURI)` and pass the result to `sendNotification`.
5. No change to `notification.Format` or this PR's test matrix.
