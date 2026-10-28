// Package store persists runs, tracks and candidates in SQLite (pure Go,
// modernc.org/sqlite).
package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite" // driver "sqlite"

	"github.com/cmerk2021/slskify/internal/match"
	"github.com/cmerk2021/slskify/internal/spotify"
)

// State is a track's position in the per-track state machine.
type State string

const (
	StatePending     State = "pending"
	StateSearching   State = "searching"
	StateRanked      State = "ranked"
	StateDownloading State = "downloading"
	StateDone        State = "done"
	StateNeedsReview State = "needs_review"
	StateNotFound    State = "not_found"
	StateFailed      State = "failed"
	StateSkipped     State = "skipped"
)

// Run is one invocation against a Spotify source.
type Run struct {
	ID            int64
	SourceURL     string
	Kind          string
	Name          string
	QualityPreset string
	SpeedPreset   string
	CreatedAt     time.Time
	// Skipped lists source items that could not be resolved.
	Skipped []spotify.SkippedItem
}

// RunSummary is a run plus per-state track counts.
type RunSummary struct {
	Run
	Total  int
	Counts map[State]int
}

// Track is a persisted track and its pipeline state.
type Track struct {
	ID    int64
	RunID int64
	Pos   int
	spotify.Track
	State  State
	Query  string // current/last search query (user-editable in review)
	Reason string // human explanation of the current state
	// Chosen identifies the candidate being (or last) downloaded.
	ChosenUser string
	ChosenPath string
	Attempts   int
	// Transfer is the slskd transfer currently tracked, if any.
	TransferID   string
	TransferUser string
	TransferPath string
	// ReviewSeq orders the review queue: assigned (monotonic) whenever the
	// track enters needs_review/not_found, so arrivals append.
	ReviewSeq int64
	AutoCount int
	Passed    int
	UpdatedAt time.Time
}

// Store is a SQLite-backed store.
type Store struct {
	db *sql.DB
}

// Open opens (creating if needed) the database at path and migrates it.
func Open(path string) (*Store, error) {
	if path != ":memory:" {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return nil, err
		}
	}
	dsn := "file:" + path + "?_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)"
	if path == ":memory:" {
		dsn = "file::memory:?_pragma=foreign_keys(1)"
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	// One connection: SQLite serialises writers anyway, and :memory:
	// databases are per-connection.
	db.SetMaxOpenConns(1)
	s := &Store{db: db}
	if err := s.migrate(context.Background()); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

// migrations are applied in order; never edit an applied one, append.
var migrations = []string{
	// 1: initial schema
	`CREATE TABLE runs (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		source_url TEXT NOT NULL,
		kind TEXT NOT NULL DEFAULT '',
		name TEXT NOT NULL DEFAULT '',
		quality_preset TEXT NOT NULL,
		speed_preset TEXT NOT NULL,
		skipped_json TEXT NOT NULL DEFAULT '[]',
		created_at INTEGER NOT NULL
	);
	CREATE TABLE tracks (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		run_id INTEGER NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
		pos INTEGER NOT NULL,
		spotify_id TEXT NOT NULL DEFAULT '',
		isrc TEXT NOT NULL DEFAULT '',
		meta_json TEXT NOT NULL,
		state TEXT NOT NULL,
		query TEXT NOT NULL DEFAULT '',
		reason TEXT NOT NULL DEFAULT '',
		chosen_user TEXT NOT NULL DEFAULT '',
		chosen_path TEXT NOT NULL DEFAULT '',
		attempts INTEGER NOT NULL DEFAULT 0,
		transfer_id TEXT NOT NULL DEFAULT '',
		transfer_user TEXT NOT NULL DEFAULT '',
		transfer_path TEXT NOT NULL DEFAULT '',
		review_seq INTEGER NOT NULL DEFAULT 0,
		auto_count INTEGER NOT NULL DEFAULT 0,
		passed INTEGER NOT NULL DEFAULT 0,
		updated_at INTEGER NOT NULL
	);
	CREATE INDEX tracks_run ON tracks(run_id, pos);
	CREATE INDEX tracks_spotify_id ON tracks(spotify_id);
	CREATE INDEX tracks_isrc ON tracks(isrc);
	CREATE TABLE candidates (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		track_id INTEGER NOT NULL REFERENCES tracks(id) ON DELETE CASCADE,
		rank INTEGER NOT NULL,
		username TEXT NOT NULL,
		filename TEXT NOT NULL,
		confidence INTEGER NOT NULL,
		failed_reason TEXT NOT NULL DEFAULT '',
		rejected_reason TEXT NOT NULL DEFAULT '',
		data_json TEXT NOT NULL,
		UNIQUE(track_id, username, filename)
	);
	CREATE INDEX candidates_track ON candidates(track_id, rank);`,
}

func (s *Store) migrate(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (version INTEGER PRIMARY KEY, applied_at INTEGER NOT NULL)`); err != nil {
		return err
	}
	var cur int
	if err := s.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(version),0) FROM schema_migrations`).Scan(&cur); err != nil {
		return err
	}
	if cur > len(migrations) {
		return fmt.Errorf("state database is from a newer slskify (schema v%d, this build knows v%d)", cur, len(migrations))
	}
	for v := cur + 1; v <= len(migrations); v++ {
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, migrations[v-1]); err != nil {
			tx.Rollback()
			return fmt.Errorf("migration %d: %w", v, err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations(version, applied_at) VALUES(?,?)`, v, time.Now().Unix()); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

// SchemaVersion returns the applied schema version.
func (s *Store) SchemaVersion(ctx context.Context) (int, error) {
	var v int
	err := s.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(version),0) FROM schema_migrations`).Scan(&v)
	return v, err
}

// CreateRun inserts a run and its tracks (state pending).
func (s *Store) CreateRun(ctx context.Context, r Run, tracks []spotify.Track) (Run, []Track, error) {
	if r.CreatedAt.IsZero() {
		r.CreatedAt = time.Now()
	}
	skipped, _ := json.Marshal(r.Skipped)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return r, nil, err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `INSERT INTO runs(source_url,kind,name,quality_preset,speed_preset,skipped_json,created_at) VALUES(?,?,?,?,?,?,?)`,
		r.SourceURL, r.Kind, r.Name, r.QualityPreset, r.SpeedPreset, string(skipped), r.CreatedAt.Unix())
	if err != nil {
		return r, nil, err
	}
	r.ID, _ = res.LastInsertId()
	out := make([]Track, 0, len(tracks))
	now := time.Now()
	for i, st := range tracks {
		meta, _ := json.Marshal(st)
		res, err := tx.ExecContext(ctx, `INSERT INTO tracks(run_id,pos,spotify_id,isrc,meta_json,state,updated_at) VALUES(?,?,?,?,?,?,?)`,
			r.ID, i, st.SpotifyID, st.ISRC, string(meta), StatePending, now.Unix())
		if err != nil {
			return r, nil, err
		}
		id, _ := res.LastInsertId()
		out = append(out, Track{ID: id, RunID: r.ID, Pos: i, Track: st, State: StatePending, UpdatedAt: now})
	}
	return r, out, tx.Commit()
}

// UpdateRunPresets changes the presets of a run (e.g. chosen on resume).
func (s *Store) UpdateRunPresets(ctx context.Context, runID int64, quality, speed string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE runs SET quality_preset=?, speed_preset=? WHERE id=?`, quality, speed, runID)
	return err
}

// ErrNotFound is returned for missing rows.
var ErrNotFound = errors.New("not found")

// GetRun loads one run.
func (s *Store) GetRun(ctx context.Context, id int64) (Run, error) {
	row := s.db.QueryRowContext(ctx, `SELECT id,source_url,kind,name,quality_preset,speed_preset,skipped_json,created_at FROM runs WHERE id=?`, id)
	r, err := scanRun(row)
	if errors.Is(err, sql.ErrNoRows) {
		return r, ErrNotFound
	}
	return r, err
}

type scanner interface{ Scan(...any) error }

func scanRun(sc scanner) (Run, error) {
	var r Run
	var skipped string
	var created int64
	if err := sc.Scan(&r.ID, &r.SourceURL, &r.Kind, &r.Name, &r.QualityPreset, &r.SpeedPreset, &skipped, &created); err != nil {
		return r, err
	}
	r.CreatedAt = time.Unix(created, 0)
	_ = json.Unmarshal([]byte(skipped), &r.Skipped)
	return r, nil
}

// ListRuns returns runs, newest first, with state counts.
func (s *Store) ListRuns(ctx context.Context) ([]RunSummary, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,source_url,kind,name,quality_preset,speed_preset,skipped_json,created_at FROM runs ORDER BY created_at DESC, id DESC`)
	if err != nil {
		return nil, err
	}
	var out []RunSummary
	idx := map[int64]int{}
	for rows.Next() {
		r, err := scanRun(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		idx[r.ID] = len(out)
		out = append(out, RunSummary{Run: r, Counts: map[State]int{}})
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	crow, err := s.db.QueryContext(ctx, `SELECT run_id, state, COUNT(*) FROM tracks GROUP BY run_id, state`)
	if err != nil {
		return nil, err
	}
	defer crow.Close()
	for crow.Next() {
		var id int64
		var st State
		var n int
		if err := crow.Scan(&id, &st, &n); err != nil {
			return nil, err
		}
		if i, ok := idx[id]; ok {
			out[i].Counts[st] = n
			out[i].Total += n
		}
	}
	return out, crow.Err()
}

// DeleteRun removes a run and everything under it.
func (s *Store) DeleteRun(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM runs WHERE id=?`, id)
	return err
}

const trackCols = `id,run_id,pos,meta_json,state,query,reason,chosen_user,chosen_path,attempts,transfer_id,transfer_user,transfer_path,review_seq,auto_count,passed,updated_at`

func scanTrack(sc scanner) (Track, error) {
	var t Track
	var meta string
	var updated int64
	err := sc.Scan(&t.ID, &t.RunID, &t.Pos, &meta, &t.State, &t.Query, &t.Reason, &t.ChosenUser, &t.ChosenPath,
		&t.Attempts, &t.TransferID, &t.TransferUser, &t.TransferPath, &t.ReviewSeq, &t.AutoCount, &t.Passed, &updated)
	if err != nil {
		return t, err
	}
	if err := json.Unmarshal([]byte(meta), &t.Track); err != nil {
		return t, fmt.Errorf("track %d metadata: %w", t.ID, err)
	}
	t.UpdatedAt = time.Unix(updated, 0)
	return t, nil
}

// Tracks returns a run's tracks in source order.
func (s *Store) Tracks(ctx context.Context, runID int64) ([]Track, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+trackCols+` FROM tracks WHERE run_id=? ORDER BY pos`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Track
	for rows.Next() {
		t, err := scanTrack(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// UpdateTrack persists a track's mutable fields.
func (s *Store) UpdateTrack(ctx context.Context, t Track) error {
	_, err := s.db.ExecContext(ctx, `UPDATE tracks SET state=?,query=?,reason=?,chosen_user=?,chosen_path=?,attempts=?,
		transfer_id=?,transfer_user=?,transfer_path=?,review_seq=?,auto_count=?,passed=?,updated_at=? WHERE id=?`,
		t.State, t.Query, t.Reason, t.ChosenUser, t.ChosenPath, t.Attempts, t.TransferID, t.TransferUser, t.TransferPath,
		t.ReviewSeq, t.AutoCount, t.Passed, time.Now().Unix(), t.ID)
	return err
}

// SaveCandidates replaces a track's candidates (in display/rank order).
func (s *Store) SaveCandidates(ctx context.Context, trackID int64, cs []*match.Candidate) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM candidates WHERE track_id=?`, trackID); err != nil {
		return err
	}
	stmt, err := tx.PrepareContext(ctx, `INSERT INTO candidates(track_id,rank,username,filename,confidence,failed_reason,rejected_reason,data_json) VALUES(?,?,?,?,?,?,?,?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for i, c := range cs {
		data, err := json.Marshal(c)
		if err != nil {
			return err
		}
		if _, err := stmt.ExecContext(ctx, trackID, i, c.Username, c.Path, c.Confidence, c.Failed, c.Rejected, string(data)); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// Candidates loads a track's candidates in rank order.
func (s *Store) Candidates(ctx context.Context, trackID int64) ([]*match.Candidate, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT data_json, failed_reason FROM candidates WHERE track_id=? ORDER BY rank`, trackID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*match.Candidate
	for rows.Next() {
		var data, failed string
		if err := rows.Scan(&data, &failed); err != nil {
			return nil, err
		}
		c := &match.Candidate{}
		if err := json.Unmarshal([]byte(data), c); err != nil {
			return nil, err
		}
		c.Failed = failed
		out = append(out, c)
	}
	return out, rows.Err()
}

// MaxReviewSeq returns the highest review sequence in a run.
func (s *Store) MaxReviewSeq(ctx context.Context, runID int64) (int64, error) {
	var v int64
	err := s.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(review_seq),0) FROM tracks WHERE run_id=?`, runID).Scan(&v)
	return v, err
}
