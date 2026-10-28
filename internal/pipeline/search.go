// Package pipeline runs tracks through search → rank → download with
// bounded concurrency, persisting every transition. It has no UI
// dependency: frontends observe it through Snapshot/Updates and drive
// review through Pick/Skip/Research.
package pipeline

import (
	"context"
	"fmt"
	"strings"

	"github.com/cmerk2021/slskify/internal/match"
	"github.com/cmerk2021/slskify/internal/slskd"
	"github.com/cmerk2021/slskify/internal/spotify"
	"github.com/cmerk2021/slskify/internal/store"
)

// SpotifyClient resolves Spotify links.
type SpotifyClient interface {
	Resolve(ctx context.Context, url string) (*spotify.Collection, error)
}

// SlskdClient is the subset of the slskd API the pipeline needs.
type SlskdClient interface {
	Search(ctx context.Context, text string, opts slskd.SearchOptions) ([]slskd.Response, error)
	Enqueue(ctx context.Context, username, filename string, size int64) (string, error)
	Downloads(ctx context.Context) ([]slskd.Transfer, error)
	CancelDownload(ctx context.Context, username, id string, remove bool) error
}

// Store is the persistence the pipeline needs.
type Store interface {
	CreateRun(ctx context.Context, r store.Run, tracks []spotify.Track) (store.Run, []store.Track, error)
	Tracks(ctx context.Context, runID int64) ([]store.Track, error)
	UpdateTrack(ctx context.Context, t store.Track) error
	SaveCandidates(ctx context.Context, trackID int64, cs []*match.Candidate) error
	Candidates(ctx context.Context, trackID int64) ([]*match.Candidate, error)
	MaxReviewSeq(ctx context.Context, runID int64) (int64, error)
}

// TargetOf converts a Spotify track into the matcher's target.
func TargetOf(t spotify.Track) match.Target {
	return match.Target{
		Title: t.Title, Artists: t.Artists, Album: t.Album, AlbumArtist: t.AlbumArtist, AlbumType: t.AlbumType,
		TrackNumber: t.TrackNumber, DiscNumber: t.DiscNumber, DurationMs: t.DurationMs, Explicit: t.Explicit, Year: t.Year,
	}
}

// FilesFromResponses flattens slskd responses into matcher files. Locked
// files are included (flagged) so hard filters can report them.
func FilesFromResponses(rs []slskd.Response) []match.File {
	var out []match.File
	conv := func(r slskd.Response, f slskd.File, locked bool) match.File {
		mf := match.File{
			Username: r.Username, Path: f.Filename, Size: f.Size, Ext: strings.TrimPrefix(strings.ToLower(f.Extension), "."),
			Locked: locked || f.IsLocked, FreeSlot: r.HasFreeUploadSlot, UploadSpeed: r.UploadSpeed, QueueLength: r.QueueLength,
		}
		if f.BitRate != nil {
			mf.BitRate = *f.BitRate
		}
		if f.SampleRate != nil {
			mf.SampleRate = *f.SampleRate
		}
		if f.BitDepth != nil {
			mf.BitDepth = *f.BitDepth
		}
		if f.Length != nil {
			mf.Length = *f.Length
		}
		if f.IsVariableBitRate != nil {
			mf.VBR, mf.VBRKnown = *f.IsVariableBitRate, true
		}
		return mf
	}
	for _, r := range rs {
		for _, f := range r.Files {
			out = append(out, conv(r, f, false))
		}
		for _, f := range r.LockedFiles {
			out = append(out, conv(r, f, true))
		}
	}
	return out
}

// SearchResult is the outcome of searching one track.
type SearchResult struct {
	Queries  []string // queries actually run
	Files    []match.File
	Decision match.Decision
}

// Searcher runs the multi-query search for one track. It is shared by the
// runner and the headless dry-run command.
type Searcher struct {
	Slskd   SlskdClient
	Matcher *match.Matcher
	Policy  match.QualityPolicy
	Options slskd.SearchOptions
	// BeforeSearch is called before each slskd search (rate limiting).
	BeforeSearch func(ctx context.Context) error
	// OnQuery is called when a query starts (progress display).
	OnQuery func(q string)
}

// Search runs queries in order (or just custom when non-empty), merging and
// de-duplicating results by (username, path), and stops early once a
// candidate is auto-eligible.
func (s *Searcher) Search(ctx context.Context, t match.Target, custom string) (SearchResult, error) {
	queries := s.Matcher.Queries(t)
	if q := strings.TrimSpace(custom); q != "" {
		queries = []string{match.QueryText(q)}
	}
	var res SearchResult
	seen := map[string]bool{}
	var lastErr error
	ok := 0
	for _, q := range queries {
		if q == "" {
			continue
		}
		if s.BeforeSearch != nil {
			if err := s.BeforeSearch(ctx); err != nil {
				return res, err
			}
		}
		if s.OnQuery != nil {
			s.OnQuery(q)
		}
		res.Queries = append(res.Queries, q)
		rs, err := s.Slskd.Search(ctx, q, s.Options)
		if err != nil {
			if ctx.Err() != nil {
				return res, ctx.Err()
			}
			lastErr = err
			continue
		}
		ok++
		for _, f := range FilesFromResponses(rs) {
			if !seen[f.Key()] {
				seen[f.Key()] = true
				res.Files = append(res.Files, f)
			}
		}
		res.Decision = s.Matcher.Evaluate(t, res.Files, s.Policy, nil)
		if res.Decision.Outcome == match.OutcomeAuto {
			break
		}
	}
	if ok == 0 && lastErr != nil {
		return res, fmt.Errorf("search failed: %w", lastErr)
	}
	if ok == 0 {
		res.Decision = s.Matcher.Evaluate(t, nil, s.Policy, nil)
	}
	return res, nil
}
