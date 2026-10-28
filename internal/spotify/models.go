// Package spotify is a small, native Spotify Web API client that resolves
// track, album and playlist links into canonical Track models.
package spotify

// Kind is the type of Spotify entity referenced by a URL or URI.
type Kind string

const (
	KindTrack    Kind = "track"
	KindAlbum    Kind = "album"
	KindPlaylist Kind = "playlist"
)

// Track is the canonical, source-independent description of a recording
// that slskify tries to find on Soulseek.
type Track struct {
	SpotifyID   string   `json:"spotify_id"`
	ISRC        string   `json:"isrc,omitempty"`
	Title       string   `json:"title"`
	Artists     []string `json:"artists"`
	Album       string   `json:"album"`
	AlbumArtist string   `json:"album_artist"`
	AlbumType   string   `json:"album_type,omitempty"` // album, single, compilation
	TrackNumber int      `json:"track_number"`
	DiscNumber  int      `json:"disc_number"`
	DurationMs  int      `json:"duration_ms"`
	Year        int      `json:"year,omitempty"`
	Explicit    bool     `json:"explicit"`
}

// PrimaryArtist returns the first credited artist.
func (t Track) PrimaryArtist() string {
	if len(t.Artists) == 0 {
		return ""
	}
	return t.Artists[0]
}

// SkippedItem records an entry of a playlist/album that could not be turned
// into a Track (null track, local file, podcast episode...).
type SkippedItem struct {
	Position int    `json:"position"`
	Name     string `json:"name,omitempty"`
	Reason   string `json:"reason"`
}

// Collection is the resolved result of a URL: one or more tracks plus
// anything that had to be skipped.
type Collection struct {
	Kind    Kind
	ID      string
	Name    string
	Owner   string
	URL     string
	Tracks  []Track
	Skipped []SkippedItem
}

// ---- wire types (subset of the Web API objects we need) ----

type apiArtist struct {
	Name string `json:"name"`
}

type apiAlbumInfo struct {
	ID          string      `json:"id"`
	Name        string      `json:"name"`
	AlbumType   string      `json:"album_type"`
	ReleaseDate string      `json:"release_date"`
	Artists     []apiArtist `json:"artists"`
}

type apiTrack struct {
	ID          string        `json:"id"`
	Name        string        `json:"name"`
	Type        string        `json:"type"` // "track" or "episode"
	IsLocal     bool          `json:"is_local"`
	URI         string        `json:"uri"`
	Artists     []apiArtist   `json:"artists"`
	Album       *apiAlbumInfo `json:"album"`
	TrackNumber int           `json:"track_number"`
	DiscNumber  int           `json:"disc_number"`
	DurationMs  int           `json:"duration_ms"`
	Explicit    bool          `json:"explicit"`
	ExternalIDs struct {
		ISRC string `json:"isrc"`
	} `json:"external_ids"`
}

type apiPaging[T any] struct {
	Items  []T     `json:"items"`
	Next   *string `json:"next"`
	Total  int     `json:"total"`
	Offset int     `json:"offset"`
	Limit  int     `json:"limit"`
}

type apiAlbum struct {
	apiAlbumInfo
	Tracks apiPaging[apiTrack] `json:"tracks"`
}

// apiPlaylistItem covers both the pre-2026 shape ({"track": {...}}) and the
// February 2026 shape ({"item": {...}, "is_local": ...}).
type apiPlaylistItem struct {
	IsLocal bool      `json:"is_local"`
	Item    *apiTrack `json:"item"`
	Track   *apiTrack `json:"track"`
}

func (p apiPlaylistItem) entry() *apiTrack {
	if p.Item != nil {
		return p.Item
	}
	return p.Track
}

type apiPlaylist struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Owner struct {
		ID          string `json:"id"`
		DisplayName string `json:"display_name"`
	} `json:"owner"`
}

func artistNames(as []apiArtist) []string {
	out := make([]string, 0, len(as))
	for _, a := range as {
		if a.Name != "" {
			out = append(out, a.Name)
		}
	}
	return out
}

func yearOf(releaseDate string) int {
	if len(releaseDate) < 4 {
		return 0
	}
	y := 0
	for _, c := range releaseDate[:4] {
		if c < '0' || c > '9' {
			return 0
		}
		y = y*10 + int(c-'0')
	}
	return y
}

// toTrack converts a wire track. Simplified album-track objects carry no
// album, so fallback supplies album info in that case.
func toTrack(t *apiTrack, fallback *apiAlbumInfo) Track {
	alb := t.Album
	if alb == nil {
		alb = fallback
	}
	out := Track{
		SpotifyID:   t.ID,
		ISRC:        t.ExternalIDs.ISRC,
		Title:       t.Name,
		Artists:     artistNames(t.Artists),
		TrackNumber: t.TrackNumber,
		DiscNumber:  t.DiscNumber,
		DurationMs:  t.DurationMs,
		Explicit:    t.Explicit,
	}
	if alb != nil {
		out.Album = alb.Name
		out.AlbumType = alb.AlbumType
		out.Year = yearOf(alb.ReleaseDate)
		if len(alb.Artists) > 0 {
			out.AlbumArtist = alb.Artists[0].Name
		}
	}
	if out.AlbumArtist == "" {
		out.AlbumArtist = out.PrimaryArtist()
	}
	return out
}
