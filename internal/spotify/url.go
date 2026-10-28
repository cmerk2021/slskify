package spotify

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

// ErrArtistUnsupported is returned for artist links, which v1 does not handle.
var ErrArtistUnsupported = errors.New("artist links are not supported yet: paste a track, album or playlist link instead")

var idRe = regexp.MustCompile(`^[A-Za-z0-9]{22}$`)

// ParseURL accepts open.spotify.com URLs (with optional intl-xx prefix,
// embed paths and query strings) and spotify: URIs for tracks, albums and
// playlists.
func ParseURL(raw string) (Kind, string, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return "", "", errors.New("empty Spotify link")
	}
	var parts []string
	if strings.HasPrefix(strings.ToLower(s), "spotify:") {
		parts = strings.Split(s, ":")[1:]
		// legacy form: spotify:user:<user>:playlist:<id>
		if len(parts) >= 4 && parts[0] == "user" {
			parts = parts[2:]
		}
	} else {
		if !strings.Contains(s, "://") {
			s = "https://" + s
		}
		u, err := url.Parse(s)
		if err != nil {
			return "", "", fmt.Errorf("not a valid Spotify link: %q", raw)
		}
		host := strings.ToLower(u.Hostname())
		switch host {
		case "open.spotify.com", "play.spotify.com":
		case "spotify.link", "spotify.app.link":
			return "", "", errors.New("shortened spotify.link URLs are not supported: open the link in a browser and paste the full open.spotify.com URL")
		default:
			return "", "", fmt.Errorf("not a Spotify link: %q", raw)
		}
		for _, p := range strings.Split(strings.Trim(u.Path, "/"), "/") {
			if p != "" {
				parts = append(parts, p)
			}
		}
		if len(parts) > 0 && strings.HasPrefix(parts[0], "intl-") {
			parts = parts[1:]
		}
		if len(parts) > 0 && parts[0] == "embed" {
			parts = parts[1:]
		}
		if len(parts) >= 4 && parts[0] == "user" {
			parts = parts[2:]
		}
	}
	if len(parts) < 2 {
		return "", "", fmt.Errorf("could not find a track, album or playlist in %q", raw)
	}
	kind, id := strings.ToLower(parts[0]), parts[1]
	switch kind {
	case "artist":
		return "", "", ErrArtistUnsupported
	case "track", "album", "playlist":
	case "episode", "show":
		return "", "", errors.New("podcasts are not supported: paste a track, album or playlist link")
	default:
		return "", "", fmt.Errorf("unsupported Spotify link type %q", kind)
	}
	if !idRe.MatchString(id) {
		return "", "", fmt.Errorf("invalid Spotify ID %q", id)
	}
	return Kind(kind), id, nil
}

// IsEditorialPlaylistID reports whether id looks like a Spotify-owned
// editorial/algorithmic playlist. These IDs start with "37i9dQZF1" (e.g.
// "Today's Top Hits", Discover Weekly, Daily Mix). This is a heuristic based
// on observed IDs, not a documented guarantee.
func IsEditorialPlaylistID(id string) bool {
	return strings.HasPrefix(id, "37i9dQZ")
}
