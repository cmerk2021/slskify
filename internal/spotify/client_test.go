package spotify

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestParseURL(t *testing.T) {
	const id = "4uLU6hMCjMI75M1A2tKUQC"
	cases := []struct {
		in      string
		kind    Kind
		wantErr string
	}{
		{"https://open.spotify.com/track/" + id, KindTrack, ""},
		{"https://open.spotify.com/track/" + id + "?si=abc123", KindTrack, ""},
		{"open.spotify.com/album/" + id, KindAlbum, ""},
		{"https://open.spotify.com/intl-de/playlist/" + id + "?si=x", KindPlaylist, ""},
		{"https://open.spotify.com/embed/playlist/" + id, KindPlaylist, ""},
		{"https://open.spotify.com/user/someone/playlist/" + id, KindPlaylist, ""},
		{"spotify:track:" + id, KindTrack, ""},
		{"spotify:user:someone:playlist:" + id, KindPlaylist, ""},
		{"  spotify:album:" + id + "  ", KindAlbum, ""},
		{"https://open.spotify.com/artist/" + id, "", "artist links are not supported"},
		{"spotify:artist:" + id, "", "artist links are not supported"},
		{"https://open.spotify.com/episode/" + id, "", "podcasts"},
		{"https://spotify.link/abc", "", "shortened"},
		{"https://example.com/track/" + id, "", "not a Spotify link"},
		{"https://open.spotify.com/track/short", "", "invalid Spotify ID"},
		{"", "", "empty"},
	}
	for _, c := range cases {
		k, gotID, err := ParseURL(c.in)
		if c.wantErr != "" {
			if err == nil || !strings.Contains(err.Error(), c.wantErr) {
				t.Errorf("ParseURL(%q) err=%v, want containing %q", c.in, err, c.wantErr)
			}
			continue
		}
		if err != nil || k != c.kind || gotID != id {
			t.Errorf("ParseURL(%q) = %q,%q,%v want %q", c.in, k, gotID, err, c.kind)
		}
	}
}

type staticTokens struct{ invalidated atomic.Int32 }

func (s *staticTokens) Token(context.Context) (string, error) { return "tok", nil }
func (s *staticTokens) Invalidate()                           { s.invalidated.Add(1) }

func newTestClient(srv *httptest.Server) *Client {
	c := NewClient(&staticTokens{})
	c.BaseURL = srv.URL + "/v1"
	c.sleep = func(context.Context, time.Duration) error { return nil }
	return c
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func trackJSON(id, name string, n int) map[string]any {
	return map[string]any{
		"id": id, "name": name, "type": "track", "uri": "spotify:track:" + id,
		"artists":      []map[string]any{{"name": "Artist"}, {"name": "Feat"}},
		"album":        map[string]any{"name": "Album", "album_type": "album", "release_date": "2014-05-01", "artists": []map[string]any{{"name": "Artist"}}},
		"track_number": n, "disc_number": 1, "duration_ms": 200000 + n, "explicit": n%2 == 0,
		"external_ids": map[string]any{"isrc": "ISRC" + id},
	}
}

func TestClientCredentialsCachingAndRefresh(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		id, secret, ok := r.BasicAuth()
		if !ok || id != "id" || secret != "secret" {
			w.WriteHeader(400)
			return
		}
		if r.FormValue("grant_type") != "client_credentials" {
			w.WriteHeader(400)
			return
		}
		writeJSON(w, map[string]any{"access_token": "t" + strconv.Itoa(int(calls.Load())), "expires_in": 3600})
	}))
	defer srv.Close()
	now := time.Unix(0, 0)
	cc := &ClientCredentials{ClientID: "id", ClientSecret: "secret", AccountsURL: srv.URL, now: func() time.Time { return now }}
	ctx := context.Background()
	a, _ := cc.Token(ctx)
	b, _ := cc.Token(ctx)
	if a != b || calls.Load() != 1 {
		t.Fatalf("expected cached token, got %q %q calls=%d", a, b, calls.Load())
	}
	now = now.Add(3550 * time.Second) // within refresh skew
	c, _ := cc.Token(ctx)
	if c == a || calls.Load() != 2 {
		t.Fatalf("expected refresh before expiry")
	}
	bad := &ClientCredentials{ClientID: "id", ClientSecret: "wrong", AccountsURL: srv.URL}
	if _, err := bad.Token(ctx); !IsKind(err, ErrKindAuth) {
		t.Fatalf("expected auth error, got %v", err)
	}
}

func TestPlaylistPaginationAndSkips(t *testing.T) {
	const pid = "1111111111111111111111"
	srv := httptest.NewServer(nil)
	defer srv.Close()
	srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/playlists/" + pid:
			writeJSON(w, map[string]any{"id": pid, "name": "My List", "owner": map[string]any{"display_name": "me"}})
		case "/v1/playlists/" + pid + "/items":
			off, _ := strconv.Atoi(r.URL.Query().Get("offset"))
			if off == 0 {
				next := srv.URL + "/v1/playlists/" + pid + "/items?offset=3&limit=3"
				writeJSON(w, map[string]any{"offset": 0, "limit": 3, "total": 6, "next": next, "items": []any{
					map[string]any{"item": trackJSON("aaaaaaaaaaaaaaaaaaaaa1", "One", 1)},
					map[string]any{"item": nil},
					map[string]any{"is_local": true, "item": map[string]any{"name": "Local thing", "uri": "spotify:local:x"}},
				}})
				return
			}
			writeJSON(w, map[string]any{"offset": 3, "limit": 3, "total": 6, "next": nil, "items": []any{
				map[string]any{"item": map[string]any{"id": "ep", "name": "Pod", "type": "episode", "uri": "spotify:episode:ep"}},
				map[string]any{"track": trackJSON("aaaaaaaaaaaaaaaaaaaaa2", "Two", 2)}, // legacy shape
				map[string]any{"item": trackJSON("aaaaaaaaaaaaaaaaaaaaa3", "Three", 3)},
			}})
		default:
			http.NotFound(w, r)
		}
	})
	c := newTestClient(srv)
	col, err := c.Resolve(context.Background(), "https://open.spotify.com/playlist/"+pid)
	if err != nil {
		t.Fatal(err)
	}
	if col.Name != "My List" || len(col.Tracks) != 3 || len(col.Skipped) != 3 {
		t.Fatalf("got name=%q tracks=%d skipped=%+v", col.Name, len(col.Tracks), col.Skipped)
	}
	if col.Tracks[1].Title != "Two" || col.Tracks[0].Artists[1] != "Feat" || col.Tracks[0].Year != 2014 || col.Tracks[0].ISRC == "" {
		t.Fatalf("bad track mapping: %+v", col.Tracks)
	}
	reasons := []string{col.Skipped[0].Reason, col.Skipped[1].Reason, col.Skipped[2].Reason}
	if !strings.Contains(reasons[0], "unavailable") || reasons[1] != "local file" || reasons[2] != "podcast episode" {
		t.Fatalf("skip reasons: %v", reasons)
	}
}

func TestPlaylistLegacyTracksFallback(t *testing.T) {
	const pid = "2222222222222222222222"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/playlists/" + pid:
			writeJSON(w, map[string]any{"id": pid, "name": "Old"})
		case "/v1/playlists/" + pid + "/tracks":
			writeJSON(w, map[string]any{"items": []any{map[string]any{"track": trackJSON("aaaaaaaaaaaaaaaaaaaaa1", "One", 1)}}})
		default:
			w.WriteHeader(404)
			_, _ = w.Write([]byte(`{"error":{"status":404,"message":"Not found."}}`))
		}
	}))
	defer srv.Close()
	col, err := newTestClient(srv).Resolve(context.Background(), "spotify:playlist:"+pid)
	if err != nil || len(col.Tracks) != 1 {
		t.Fatalf("err=%v col=%+v", err, col)
	}
}

func TestAlbumPaginationAndISRCEnrichmentFallback(t *testing.T) {
	const aid = "3333333333333333333333"
	var batchCalls, singleCalls atomic.Int32
	srv := httptest.NewServer(nil)
	defer srv.Close()
	simple := func(id string, n int) map[string]any {
		m := trackJSON(id, "T"+strconv.Itoa(n), n)
		delete(m, "album")
		delete(m, "external_ids")
		return m
	}
	srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v1/albums/"+aid:
			next := srv.URL + "/v1/albums/" + aid + "/tracks?offset=2&limit=2"
			writeJSON(w, map[string]any{"id": aid, "name": "LP", "album_type": "album", "release_date": "1999",
				"artists": []any{map[string]any{"name": "Band"}},
				"tracks":  map[string]any{"offset": 0, "limit": 2, "next": next, "items": []any{simple("bbbbbbbbbbbbbbbbbbbbb1", 1), simple("bbbbbbbbbbbbbbbbbbbbb2", 2)}}})
		case r.URL.Path == "/v1/albums/"+aid+"/tracks":
			writeJSON(w, map[string]any{"offset": 2, "limit": 2, "next": nil, "items": []any{simple("bbbbbbbbbbbbbbbbbbbbb3", 3)}})
		case r.URL.Path == "/v1/tracks":
			batchCalls.Add(1)
			w.WriteHeader(403) // removed for dev-mode apps
			_, _ = w.Write([]byte(`{"error":{"status":403,"message":"Forbidden"}}`))
		case strings.HasPrefix(r.URL.Path, "/v1/tracks/"):
			singleCalls.Add(1)
			id := strings.TrimPrefix(r.URL.Path, "/v1/tracks/")
			writeJSON(w, trackJSON(id, "x", 1))
		default:
			http.NotFound(w, r)
		}
	})
	col, err := newTestClient(srv).Resolve(context.Background(), "https://open.spotify.com/album/"+aid)
	if err != nil {
		t.Fatal(err)
	}
	if len(col.Tracks) != 3 || col.Name != "Band - LP" {
		t.Fatalf("got %+v", col)
	}
	for _, tr := range col.Tracks {
		if tr.ISRC != "ISRC"+tr.SpotifyID || tr.Album != "LP" || tr.Year != 1999 || tr.AlbumArtist != "Band" {
			t.Fatalf("bad enrichment: %+v", tr)
		}
	}
	if batchCalls.Load() != 1 || singleCalls.Load() != 3 {
		t.Fatalf("batch=%d single=%d", batchCalls.Load(), singleCalls.Load())
	}
}

func TestRetryOn429AndServerErrors(t *testing.T) {
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch n.Add(1) {
		case 1:
			w.Header().Set("Retry-After", "2")
			w.WriteHeader(429)
		case 2:
			w.WriteHeader(503)
		default:
			writeJSON(w, trackJSON("4uLU6hMCjMI75M1A2tKUQC", "Song", 1))
		}
	}))
	defer srv.Close()
	c := newTestClient(srv)
	var slept []time.Duration
	c.sleep = func(_ context.Context, d time.Duration) error { slept = append(slept, d); return nil }
	col, err := c.Resolve(context.Background(), "spotify:track:4uLU6hMCjMI75M1A2tKUQC")
	if err != nil || col.Tracks[0].Title != "Song" {
		t.Fatalf("err=%v", err)
	}
	if len(slept) != 2 || slept[0] != 2*time.Second {
		t.Fatalf("slept=%v", slept)
	}
}

func TestLongRetryAfterIsQuotaError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "86400")
		w.WriteHeader(429)
	}))
	defer srv.Close()
	_, err := newTestClient(srv).Resolve(context.Background(), "spotify:track:4uLU6hMCjMI75M1A2tKUQC")
	if !IsKind(err, ErrKindRateLimited) || strings.Contains(err.Error(), "429") {
		t.Fatalf("got %v", err)
	}
}

func Test401RefreshesTokenOnce(t *testing.T) {
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if n.Add(1) == 1 {
			w.WriteHeader(401)
			return
		}
		writeJSON(w, trackJSON("4uLU6hMCjMI75M1A2tKUQC", "Song", 1))
	}))
	defer srv.Close()
	c := newTestClient(srv)
	if _, err := c.Resolve(context.Background(), "spotify:track:4uLU6hMCjMI75M1A2tKUQC"); err != nil {
		t.Fatal(err)
	}
	if c.Tokens.(*staticTokens).invalidated.Load() != 1 {
		t.Fatal("token not invalidated")
	}
}

func TestErrorMapping(t *testing.T) {
	cases := []struct {
		name, url string
		status    int
		body      string
		kind      ErrKind
		contains  string
	}{
		{"editorial 404", "https://open.spotify.com/playlist/37i9dQZF1DXcBWIGoYBM5M", 404, `{"error":{"status":404,"message":"Resource not found"}}`, ErrKindNotFound, "editorial"},
		{"private 404", "https://open.spotify.com/playlist/4444444444444444444444", 404, `{"error":{"status":404,"message":"Resource not found"}}`, ErrKindNotFound, "make the playlist public"},
		{"private 403", "https://open.spotify.com/playlist/4444444444444444444444", 403, `{"error":{"status":403,"message":"Forbidden"}}`, ErrKindForbidden, "copy its tracks"},
		{"premium", "https://open.spotify.com/track/4uLU6hMCjMI75M1A2tKUQC", 403, `Active premium subscription required for the owner of the app.`, ErrKindPremiumRequired, "Premium"},
		{"track 404", "https://open.spotify.com/track/4uLU6hMCjMI75M1A2tKUQC", 404, ``, ErrKindNotFound, "could not find this track"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = fmt.Fprint(w, tc.body)
			}))
			defer srv.Close()
			_, err := newTestClient(srv).Resolve(context.Background(), tc.url)
			if !IsKind(err, tc.kind) || !strings.Contains(err.Error(), tc.contains) {
				t.Fatalf("got %v", err)
			}
			if strings.Contains(err.Error(), strconv.Itoa(tc.status)) {
				t.Fatalf("raw status leaked into message: %v", err)
			}
		})
	}
}
