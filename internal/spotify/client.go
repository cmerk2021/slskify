package spotify

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// DefaultBaseURL is the Spotify Web API base URL.
const DefaultBaseURL = "https://api.spotify.com/v1"

// Client is a minimal Spotify Web API client.
type Client struct {
	BaseURL string
	HTTP    *http.Client
	Tokens  TokenSource
	// MaxRetries bounds retries for 429/5xx responses.
	MaxRetries int
	// MaxRetryAfter: a 429 asking us to wait longer than this is treated as a
	// quota exhaustion and returned as an error instead of sleeping (Spotify
	// development-mode apps have been observed returning Retry-After ~86400).
	MaxRetryAfter time.Duration
	sleep         func(ctx context.Context, d time.Duration) error
}

// NewClient builds a client with sensible defaults.
func NewClient(ts TokenSource) *Client {
	return &Client{BaseURL: DefaultBaseURL, Tokens: ts, HTTP: &http.Client{Timeout: 30 * time.Second}}
}

func (c *Client) base() string {
	if c.BaseURL == "" {
		return DefaultBaseURL
	}
	return strings.TrimRight(c.BaseURL, "/")
}

func (c *Client) doSleep(ctx context.Context, d time.Duration) error {
	if c.sleep != nil {
		return c.sleep(ctx, d)
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// get performs a GET against an API path (or absolute URL under BaseURL),
// handling auth, 401 refresh, 429 Retry-After and 5xx backoff.
func (c *Client) get(ctx context.Context, pathOrURL string, q url.Values, out any) error {
	u := pathOrURL
	if !strings.HasPrefix(u, "http://") && !strings.HasPrefix(u, "https://") {
		u = c.base() + pathOrURL
	}
	if len(q) > 0 {
		sep := "?"
		if strings.Contains(u, "?") {
			sep = "&"
		}
		u += sep + q.Encode()
	}
	maxRetries := c.MaxRetries
	if maxRetries == 0 {
		maxRetries = 4
	}
	maxWait := c.MaxRetryAfter
	if maxWait == 0 {
		maxWait = 2 * time.Minute
	}
	hc := c.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: 30 * time.Second}
	}
	refreshed := false
	for attempt := 0; ; attempt++ {
		tok, err := c.Tokens.Token(ctx)
		if err != nil {
			return err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+tok)
		req.Header.Set("Accept", "application/json")
		resp, err := hc.Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if attempt < maxRetries {
				if serr := c.doSleep(ctx, backoff(attempt)); serr != nil {
					return serr
				}
				continue
			}
			return &Error{Kind: ErrKindNetwork, Message: "could not reach the Spotify API", Cause: err}
		}
		body, rerr := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
		resp.Body.Close()
		if rerr != nil {
			return &Error{Kind: ErrKindNetwork, Message: "reading Spotify response failed", Cause: rerr}
		}
		switch {
		case resp.StatusCode == http.StatusOK:
			if out == nil {
				return nil
			}
			if err := json.Unmarshal(body, out); err != nil {
				return fmt.Errorf("decoding Spotify response: %w", err)
			}
			return nil
		case resp.StatusCode == http.StatusUnauthorized && !refreshed:
			refreshed = true
			c.Tokens.Invalidate()
			continue
		case resp.StatusCode == http.StatusTooManyRequests:
			wait, ok := retryAfter(resp.Header.Get("Retry-After"))
			if !ok {
				wait = backoff(attempt)
			}
			if wait > maxWait {
				return &Error{Kind: ErrKindRateLimited, Status: 429,
					Message: fmt.Sprintf("Spotify rate limit reached (asked to wait %s)", wait.Round(time.Second)),
					Hint:    "development-mode apps have a daily quota; try again later"}
			}
			if attempt >= maxRetries {
				return &Error{Kind: ErrKindRateLimited, Status: 429, Message: "Spotify kept rate-limiting requests", Hint: "wait a minute and try again"}
			}
			if err := c.doSleep(ctx, wait); err != nil {
				return err
			}
			continue
		case resp.StatusCode >= 500:
			if attempt < maxRetries {
				if err := c.doSleep(ctx, backoff(attempt)); err != nil {
					return err
				}
				continue
			}
			return &Error{Kind: ErrKindOther, Status: resp.StatusCode, Message: "Spotify is having problems (server error)", Hint: "try again later"}
		default:
			return classifyStatus(resp.StatusCode, body)
		}
	}
}

func backoff(attempt int) time.Duration {
	d := time.Second << attempt
	if d > 30*time.Second {
		d = 30 * time.Second
	}
	return d
}

// retryAfter parses a Retry-After header (delta-seconds or HTTP date).
func retryAfter(h string) (time.Duration, bool) {
	h = strings.TrimSpace(h)
	if h == "" {
		return 0, false
	}
	if n, err := strconv.Atoi(h); err == nil && n >= 0 {
		return time.Duration(n) * time.Second, true
	}
	if t, err := http.ParseTime(h); err == nil {
		d := time.Until(t)
		if d < 0 {
			d = 0
		}
		return d, true
	}
	return 0, false
}

func classifyStatus(status int, body []byte) error {
	var eb struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	_ = json.Unmarshal(body, &eb)
	msg := eb.Error.Message
	if msg == "" {
		msg = strings.TrimSpace(string(body))
	}
	lower := strings.ToLower(msg)
	switch {
	case strings.Contains(lower, "premium"):
		return &Error{Kind: ErrKindPremiumRequired, Status: status,
			Message: "Spotify requires the owner of this developer app to have an active Premium subscription",
			Hint:    "development-mode apps stop working without Premium; it can take a few hours to recover after resubscribing"}
	case status == http.StatusUnauthorized:
		return &Error{Kind: ErrKindAuth, Status: status, Message: "Spotify rejected the access token", Hint: "check your client ID/secret with `slskify setup`"}
	case status == http.StatusForbidden:
		return &Error{Kind: ErrKindForbidden, Status: status, Message: "Spotify refused access to this resource", Hint: "it may be private or restricted for this app"}
	case status == http.StatusNotFound:
		return &Error{Kind: ErrKindNotFound, Status: status, Message: "Spotify returned not found"}
	default:
		return &Error{Kind: ErrKindOther, Status: status, Message: fmt.Sprintf("unexpected Spotify response: %s", truncate(msg, 160))}
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// Ping verifies credentials end-to-end: token issuance plus one metadata
// call (which is where the Premium-required restriction shows up).
func (c *Client) Ping(ctx context.Context) error {
	if _, err := c.Tokens.Token(ctx); err != nil {
		return err
	}
	// A well-known, long-lived track.
	var t apiTrack
	return c.get(ctx, "/tracks/4uLU6hMCjMI75M1A2tKUQC", nil, &t)
}

// Resolve turns a URL/URI into a Collection of tracks.
func (c *Client) Resolve(ctx context.Context, raw string) (*Collection, error) {
	kind, id, err := ParseURL(raw)
	if err != nil {
		return nil, err
	}
	var col *Collection
	switch kind {
	case KindTrack:
		col, err = c.track(ctx, id)
	case KindAlbum:
		col, err = c.album(ctx, id)
	case KindPlaylist:
		col, err = c.playlist(ctx, id)
	}
	if err != nil {
		return nil, err
	}
	col.URL = strings.TrimSpace(raw)
	return col, nil
}

func (c *Client) track(ctx context.Context, id string) (*Collection, error) {
	var t apiTrack
	if err := c.get(ctx, "/tracks/"+id, nil, &t); err != nil {
		return nil, mapEntityError(err, KindTrack)
	}
	tr := toTrack(&t, nil)
	return &Collection{Kind: KindTrack, ID: id, Name: tr.PrimaryArtist() + " - " + tr.Title, Tracks: []Track{tr}}, nil
}

func (c *Client) album(ctx context.Context, id string) (*Collection, error) {
	var a apiAlbum
	if err := c.get(ctx, "/albums/"+id, nil, &a); err != nil {
		return nil, mapEntityError(err, KindAlbum)
	}
	info := a.apiAlbumInfo
	col := &Collection{Kind: KindAlbum, ID: id, Name: info.Name}
	if len(info.Artists) > 0 {
		col.Name = info.Artists[0].Name + " - " + info.Name
		col.Owner = info.Artists[0].Name
	}
	page := a.Tracks
	pos := 0
	for {
		for i := range page.Items {
			it := &page.Items[i]
			pos++
			if it.ID == "" {
				col.Skipped = append(col.Skipped, SkippedItem{Position: pos, Name: it.Name, Reason: "unavailable track"})
				continue
			}
			col.Tracks = append(col.Tracks, toTrack(it, &info))
		}
		if page.Next == nil || *page.Next == "" {
			break
		}
		next := c.nextURL(*page.Next, "/albums/"+id+"/tracks", page.Offset+len(page.Items), 50)
		page = apiPaging[apiTrack]{}
		if err := c.get(ctx, next, nil, &page); err != nil {
			return nil, mapEntityError(err, KindAlbum)
		}
		if len(page.Items) == 0 {
			break
		}
	}
	c.enrichISRC(ctx, col.Tracks)
	return col, nil
}

// nextURL follows a pagination "next" link only if it points at our own
// base URL; otherwise it rebuilds the request from offset/limit.
func (c *Client) nextURL(next, path string, offset, limit int) string {
	if strings.HasPrefix(next, c.base()+"/") {
		return next
	}
	return fmt.Sprintf("%s%s?offset=%d&limit=%d", c.base(), path, offset, limit)
}

// enrichISRC fills in ISRCs for simplified album tracks.
//
// Historically this used the batch endpoint GET /tracks?ids= (max 50). That
// endpoint was removed for development-mode apps in the February 2026 Web
// API changes, so on 403/404 we fall back to one GET /tracks/{id} per track.
// Enrichment is best-effort: ISRC is stored for future dedupe but is not
// used for matching, so failures are ignored.
func (c *Client) enrichISRC(ctx context.Context, tracks []Track) {
	idx := map[string][]int{}
	var ids []string
	for i, t := range tracks {
		if t.ISRC == "" && t.SpotifyID != "" {
			if _, seen := idx[t.SpotifyID]; !seen {
				ids = append(ids, t.SpotifyID)
			}
			idx[t.SpotifyID] = append(idx[t.SpotifyID], i)
		}
	}
	apply := func(t *apiTrack) {
		if t == nil {
			return
		}
		for _, i := range idx[t.ID] {
			tracks[i].ISRC = t.ExternalIDs.ISRC
		}
	}
	batchOK := true
	for start := 0; start < len(ids) && batchOK; start += 50 {
		end := min(start+50, len(ids))
		var resp struct {
			Tracks []*apiTrack `json:"tracks"`
		}
		err := c.get(ctx, "/tracks", url.Values{"ids": {strings.Join(ids[start:end], ",")}}, &resp)
		if err != nil {
			if IsKind(err, ErrKindForbidden) || IsKind(err, ErrKindNotFound) || isGone(err) {
				batchOK = false
				break
			}
			return
		}
		for _, t := range resp.Tracks {
			apply(t)
		}
	}
	if batchOK {
		return
	}
	for _, id := range ids {
		if tracks[idx[id][0]].ISRC != "" {
			continue
		}
		var t apiTrack
		if err := c.get(ctx, "/tracks/"+id, nil, &t); err != nil {
			return
		}
		apply(&t)
	}
}

func isGone(err error) bool {
	var e *Error
	return errors.As(err, &e) && e.Status == http.StatusGone
}

func (c *Client) playlist(ctx context.Context, id string) (*Collection, error) {
	var p apiPlaylist
	if err := c.get(ctx, "/playlists/"+id, url.Values{"fields": {"id,name,owner(id,display_name)"}}, &p); err != nil {
		return nil, mapPlaylistError(err, id)
	}
	col := &Collection{Kind: KindPlaylist, ID: id, Name: p.Name, Owner: p.Owner.DisplayName}
	// Since Feb 2026 playlist contents are read from /items; /tracks is the
	// legacy path, kept as a fallback for API deployments that still serve it.
	itemsPath := "/playlists/" + id + "/items"
	var page apiPaging[apiPlaylistItem]
	err := c.get(ctx, itemsPath, url.Values{"limit": {"50"}, "offset": {"0"}, "additional_types": {"track"}}, &page)
	if err != nil && IsKind(err, ErrKindNotFound) {
		itemsPath = "/playlists/" + id + "/tracks"
		page = apiPaging[apiPlaylistItem]{}
		err = c.get(ctx, itemsPath, url.Values{"limit": {"50"}, "offset": {"0"}, "additional_types": {"track"}}, &page)
	}
	if err != nil {
		return nil, mapPlaylistError(err, id)
	}
	pos := 0
	for {
		for _, it := range page.Items {
			pos++
			e := it.entry()
			switch {
			case e == nil:
				col.Skipped = append(col.Skipped, SkippedItem{Position: pos, Reason: "unavailable (removed or region-locked)"})
			case it.IsLocal || e.IsLocal || strings.HasPrefix(e.URI, "spotify:local:"):
				col.Skipped = append(col.Skipped, SkippedItem{Position: pos, Name: e.Name, Reason: "local file"})
			case e.Type == "episode" || strings.HasPrefix(e.URI, "spotify:episode:"):
				col.Skipped = append(col.Skipped, SkippedItem{Position: pos, Name: e.Name, Reason: "podcast episode"})
			case e.ID == "":
				col.Skipped = append(col.Skipped, SkippedItem{Position: pos, Name: e.Name, Reason: "unavailable track"})
			default:
				col.Tracks = append(col.Tracks, toTrack(e, nil))
			}
		}
		if page.Next == nil || *page.Next == "" || len(page.Items) == 0 {
			break
		}
		next := c.nextURL(*page.Next, itemsPath, page.Offset+len(page.Items), 50)
		page = apiPaging[apiPlaylistItem]{}
		if err := c.get(ctx, next, nil, &page); err != nil {
			return nil, mapPlaylistError(err, id)
		}
	}
	return col, nil
}
