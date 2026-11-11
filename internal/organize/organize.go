// Package organize post-processes finished downloads when slskd runs on
// the same machine as slskify: it finds the file slskd wrote, rewrites its
// tags from the Spotify metadata (Soulseek files often carry inconsistent
// tags) and moves it into a music library as e.g.
// <music>/<album artist>/<album>/<title>.<ext>.
package organize

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/cmerk2021/slskify/internal/spotify"
)

// Metadata is what gets written to a file and used for its path.
type Metadata struct {
	Title       string
	Artists     []string
	AlbumArtist string
	Album       string
	TrackNumber int
	TrackTotal  int
	DiscNumber  int
	Year        int
	Date        string // YYYY, YYYY-MM or YYYY-MM-DD
	Genre       string
	ISRC        string
	Compilation bool
	Cover       []byte
}

// Source supplies Spotify metadata missing from a stored track.
// *spotify.Client implements it.
type Source interface {
	TrackByID(ctx context.Context, id string) (spotify.Track, error)
	ArtistGenres(ctx context.Context, id string) ([]string, error)
	Image(ctx context.Context, url string) ([]byte, error)
}

// Result describes an organized file.
type Result struct {
	Path string
	// Warnings are problems that did not stop the move (e.g. no cover art).
	Warnings []string
}

// Organizer tags and moves finished downloads. It is safe for concurrent
// use.
type Organizer struct {
	downloads, music, template string
	// library is the music library root; it differs from music when files
	// go to a playlist folder.
	library string
	source  Source
	tagger  Tagger
	// LocateTimeout is how long to wait for slskd to move a finished file
	// into the downloads directory.
	LocateTimeout time.Duration
	LocateEvery   time.Duration

	mu      sync.Mutex // serializes picking a free destination name and moving
	cacheMu sync.Mutex
	tracks  map[string]cached[spotify.Track]
	genres  map[string]cached[[]string]
	covers  map[string]cached[[]byte]
}

type cached[T any] struct {
	v   T
	err error
}

// New builds an organizer. downloads is slskd's download directory as seen
// from this machine; music is the library root; template is the path
// template (DefaultTemplate when empty). source may be nil (tags then use
// only the stored metadata); tagger nil means TagLib.
func New(downloads, music, template string, source Source, tagger Tagger) (*Organizer, error) {
	if err := CheckDirs(downloads, music); err != nil {
		return nil, err
	}
	dl, _ := ExpandPath(downloads)
	mu, _ := ExpandPath(music)
	if strings.TrimSpace(template) == "" {
		template = DefaultTemplate
	}
	if err := ValidateTemplate(template); err != nil {
		return nil, err
	}
	if tagger == nil {
		tagger = TagLib{}
	}
	return &Organizer{
		downloads: dl, music: mu, library: mu, template: template, source: source, tagger: tagger,
		LocateTimeout: 30 * time.Second, LocateEvery: time.Second,
		tracks: map[string]cached[spotify.Track]{}, genres: map[string]cached[[]string]{}, covers: map[string]cached[[]byte]{},
	}, nil
}

// MusicDir returns the folder files are moved into.
func (o *Organizer) MusicDir() string { return o.music }

// LibraryDir returns the music library root (where playlist files go).
// It equals MusicDir unless SetLibraryDir changed it.
func (o *Organizer) LibraryDir() string { return o.library }

// SetLibraryDir sets the library root, for an organizer whose MusicDir is
// a folder elsewhere (e.g. a playlist folder). Call before use.
func (o *Organizer) SetLibraryDir(dir string) { o.library = dir }

// Organize finds the finished download of remotePath (size bytes, 0 if
// unknown), rewrites its tags from t and moves it into the library. A
// cancelled ctx aborts before the file is touched.
func (o *Organizer) Organize(ctx context.Context, t spotify.Track, remotePath string, size int64) (Result, error) {
	src, err := o.locate(ctx, remotePath, size)
	if err != nil {
		return Result{}, err
	}
	md, warnings := o.Metadata(ctx, t)
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	// From here on the file is modified; finish even if ctx is cancelled.
	if err := o.tagger.WriteTags(src, md); err != nil {
		warnings = append(warnings, "tags not updated: "+err.Error())
	}
	_, ext := splitExt(src)
	rel, err := RelPath(o.template, md, ext)
	if err != nil {
		return Result{}, err
	}
	dst := filepath.Join(o.music, rel)
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return Result{}, fmt.Errorf("creating %s: %w", filepath.Dir(dst), err)
	}
	o.mu.Lock()
	if src != dst {
		dst = uniquePath(dst)
		err = moveFile(src, dst)
	}
	o.mu.Unlock()
	if err != nil {
		return Result{}, fmt.Errorf("moving to %s: %w", dst, err)
	}
	pruneEmptyDirs(filepath.Dir(src), o.downloads)
	return Result{Path: dst, Warnings: warnings}, nil
}

// locate waits (up to LocateTimeout) for the file to appear: slskd reports
// a transfer as succeeded slightly before it moves the file out of its
// incomplete directory.
func (o *Organizer) locate(ctx context.Context, remotePath string, size int64) (string, error) {
	exclude := ""
	for _, d := range []string{o.library, o.music} {
		if within(d, o.downloads) {
			exclude = d
			break
		}
	}
	deadline := time.Now().Add(o.LocateTimeout)
	for {
		p, err := Locate(o.downloads, remotePath, size, exclude)
		if err == nil || !errors.Is(err, ErrNotFound) || !time.Now().Before(deadline) {
			return p, err
		}
		t := time.NewTimer(o.LocateEvery)
		select {
		case <-ctx.Done():
			t.Stop()
			return "", ctx.Err()
		case <-t.C:
		}
	}
}

// Metadata builds the tags for t, fetching what the stored track lacks
// (full album info for runs saved by older versions, genre, cover art).
// Fetch failures become warnings; the stored fields are always used.
func (o *Organizer) Metadata(ctx context.Context, t spotify.Track) (Metadata, []string) {
	var warnings []string
	if o.source != nil && t.SpotifyID != "" && (t.CoverURL == "" || len(t.ArtistIDs) == 0 || t.ReleaseDate == "" || t.TotalTracks == 0) {
		full, err := cachedFetch(o, o.tracks, t.SpotifyID, func() (spotify.Track, error) { return o.source.TrackByID(ctx, t.SpotifyID) })
		if err != nil {
			warnings = append(warnings, "Spotify track details: "+err.Error())
		} else {
			t = merge(t, full)
		}
	}
	md := Metadata{
		Title: t.Title, Artists: t.Artists, AlbumArtist: firstNonEmpty(t.AlbumArtist, t.PrimaryArtist()), Album: t.Album,
		TrackNumber: t.TrackNumber, TrackTotal: t.TotalTracks, DiscNumber: t.DiscNumber,
		Year: t.Year, Date: t.ReleaseDate, ISRC: t.ISRC, Compilation: t.AlbumType == "compilation",
	}
	if o.source == nil {
		return md, warnings
	}
	if len(t.ArtistIDs) > 0 && t.ArtistIDs[0] != "" {
		id := t.ArtistIDs[0]
		g, err := cachedFetch(o, o.genres, id, func() ([]string, error) { return o.source.ArtistGenres(ctx, id) })
		if err != nil {
			warnings = append(warnings, "Spotify genre: "+err.Error())
		} else if len(g) > 0 {
			md.Genre = GenreName(g[0])
		}
	}
	if t.CoverURL != "" {
		img, err := cachedFetch(o, o.covers, t.CoverURL, func() ([]byte, error) { return o.source.Image(ctx, t.CoverURL) })
		if err != nil {
			warnings = append(warnings, "cover art: "+err.Error())
		} else {
			md.Cover = img
		}
	} else {
		warnings = append(warnings, "no cover art on Spotify")
	}
	return md, warnings
}

// cachedFetch memoizes fetches per organizer. Errors caused by a cancelled
// context are not cached.
func cachedFetch[T any](o *Organizer, m map[string]cached[T], key string, f func() (T, error)) (T, error) {
	o.cacheMu.Lock()
	c, ok := m[key]
	o.cacheMu.Unlock()
	if ok {
		return c.v, c.err
	}
	v, err := f()
	if err != nil && (errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)) {
		return v, err
	}
	o.cacheMu.Lock()
	m[key] = cached[T]{v, err}
	o.cacheMu.Unlock()
	return v, err
}

// merge fills fields of t that are empty from full.
func merge(t, full spotify.Track) spotify.Track {
	str := func(dst *string, v string) {
		if *dst == "" {
			*dst = v
		}
	}
	num := func(dst *int, v int) {
		if *dst == 0 {
			*dst = v
		}
	}
	str(&t.Title, full.Title)
	str(&t.Album, full.Album)
	str(&t.AlbumArtist, full.AlbumArtist)
	str(&t.AlbumType, full.AlbumType)
	str(&t.AlbumID, full.AlbumID)
	str(&t.ReleaseDate, full.ReleaseDate)
	str(&t.CoverURL, full.CoverURL)
	str(&t.ISRC, full.ISRC)
	num(&t.TrackNumber, full.TrackNumber)
	num(&t.DiscNumber, full.DiscNumber)
	num(&t.Year, full.Year)
	num(&t.TotalTracks, full.TotalTracks)
	if len(t.Artists) == 0 {
		t.Artists = full.Artists
	}
	if len(t.ArtistIDs) == 0 {
		t.ArtistIDs = full.ArtistIDs
	}
	return t
}

var genreAcronyms = map[string]string{"r&b": "R&B", "edm": "EDM", "idm": "IDM", "ebm": "EBM", "uk": "UK", "us": "US", "mpb": "MPB", "aor": "AOR", "dnb": "DnB"}

// GenreName title-cases a Spotify genre ("indie pop" → "Indie Pop",
// "k-pop" → "K-Pop", "r&b" → "R&B").
func GenreName(g string) string {
	words := strings.Fields(strings.TrimSpace(g))
	for i, w := range words {
		if a, ok := genreAcronyms[strings.ToLower(w)]; ok {
			words[i] = a
			continue
		}
		parts := strings.Split(w, "-")
		for j, p := range parts {
			if a, ok := genreAcronyms[strings.ToLower(p)]; ok {
				parts[j] = a
			} else if p != "" {
				r := []rune(p)
				parts[j] = strings.ToUpper(string(r[0])) + string(r[1:])
			}
		}
		words[i] = strings.Join(parts, "-")
	}
	return strings.Join(words, " ")
}
