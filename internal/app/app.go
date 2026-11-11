// Package app wires configuration, clients, store and pipeline together.
// It is shared by the TUI and the headless commands and has no UI code.
package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/cmerk2021/slskify/internal/config"
	"github.com/cmerk2021/slskify/internal/match"
	"github.com/cmerk2021/slskify/internal/organize"
	"github.com/cmerk2021/slskify/internal/pipeline"
	"github.com/cmerk2021/slskify/internal/slskd"
	"github.com/cmerk2021/slskify/internal/spotify"
	"github.com/cmerk2021/slskify/internal/store"
)

// App holds the resolved configuration and shared clients.
type App struct {
	Cfg     *config.Config
	CfgPath string
	Store   *store.Store
	Spotify *spotify.Client
	Slskd   *slskd.Client
	// SpotifyAPIURL / SpotifyAccountsURL override the Spotify endpoints
	// (tests and local fakes); empty means the real service.
	SpotifyAPIURL, SpotifyAccountsURL string
}

// New builds clients from cfg. st may be nil (dry-run does not persist).
func New(cfg *config.Config, cfgPath string, st *store.Store) *App {
	a := &App{Cfg: cfg, CfgPath: cfgPath, Store: st}
	a.Reconnect()
	return a
}

// Reconnect rebuilds the API clients from the current credentials (after
// setup changed them).
func (a *App) Reconnect() {
	ts := spotify.NewClientCredentials(a.Cfg.Spotify.ClientID, a.Cfg.Spotify.ClientSecret)
	ts.AccountsURL = a.SpotifyAccountsURL
	a.Spotify = spotify.NewClient(ts)
	if a.SpotifyAPIURL != "" {
		a.Spotify.BaseURL = a.SpotifyAPIURL
	}
	a.Slskd = slskd.NewClient(a.Cfg.Slskd.URL, a.Cfg.Slskd.APIKey, a.Cfg.Slskd.MaxConcurrentSearches)
}

// Matcher builds the matcher from the match config.
func (a *App) Matcher() *match.Matcher { return match.NewMatcher(a.Cfg.Match) }

// Policy returns the named quality policy, falling back to the configured
// default when the name is unknown (e.g. a preset removed since a run).
func (a *App) Policy(name string) (match.QualityPolicy, string) {
	if p, ok := a.Cfg.QualityPresets[name]; ok {
		return p, name
	}
	return a.Cfg.QualityPresets[a.Cfg.QualityPreset], a.Cfg.QualityPreset
}

// Speed returns the named speed preset with the same fallback.
func (a *App) Speed(name string) (config.SpeedPreset, string) {
	if p, ok := a.Cfg.SpeedPresets[name]; ok {
		return p, name
	}
	return a.Cfg.SpeedPresets[a.Cfg.SpeedPreset], a.Cfg.SpeedPreset
}

// RunSettings are the per-run choices made on the pre-run screen.
type RunSettings struct {
	Quality, Speed string
	// Output is store.OutputLibrary or store.OutputPlaylist.
	Output       string
	PlaylistFile bool
}

// CreateRun persists a new run for a resolved collection.
func (a *App) CreateRun(ctx context.Context, c *spotify.Collection, rs RunSettings) (store.Run, error) {
	name := c.Name
	if name == "" {
		name = string(c.Kind) + " " + c.ID
	}
	run, _, err := a.Store.CreateRun(ctx, store.Run{
		SourceURL: c.URL, Kind: string(c.Kind), Name: name,
		QualityPreset: rs.Quality, SpeedPreset: rs.Speed, Output: rs.Output, PlaylistFile: rs.PlaylistFile, Skipped: c.Skipped,
	}, c.Tracks)
	return run, err
}

// NewRunner creates and loads a runner for run (new or resumed).
func (a *App) NewRunner(ctx context.Context, run store.Run) (*pipeline.Runner, error) {
	pol, qn := a.Policy(run.QualityPreset)
	sp, sn := a.Speed(run.SpeedPreset)
	run.QualityPreset, run.SpeedPreset = qn, sn
	opts := pipeline.Options{
		Matcher: a.Matcher(), Policy: pol, Speed: sp, Download: a.Cfg.Download,
	}
	if (run.Output == store.OutputPlaylist || run.PlaylistFile) && !a.Cfg.Organize.Enabled {
		return nil, errors.New("playlist output and playlist files need organizing turned on (ctrl+s on the start screen opens setup)")
	}
	if a.Cfg.Organize.Enabled {
		org, err := a.OrganizerFor(run)
		if err != nil {
			return nil, fmt.Errorf("organize: %w (fix it in setup or set organize.enabled = false)", err)
		}
		opts.Organizer = org
		if run.PlaylistFile {
			opts.PlaylistFile = organize.PlaylistFilePath(org.LibraryDir(), run.Name)
		}
	}
	r := pipeline.New(a.Slskd, a.Store, run, opts)
	if err := r.Load(ctx); err != nil {
		return nil, err
	}
	return r, nil
}

// Organizer builds the post-download organizer from the [organize] config.
func (a *App) Organizer() (*organize.Organizer, error) {
	oc := a.Cfg.Organize
	return organize.New(oc.DownloadsDir, oc.MusicDir, oc.PathTemplate, a.Spotify, nil)
}

// OrganizerFor builds the organizer for run: the library layout from the
// config, or for playlist output a flat folder named after the run.
func (a *App) OrganizerFor(run store.Run) (*organize.Organizer, error) {
	if run.Output != store.OutputPlaylist {
		return a.Organizer()
	}
	oc := a.Cfg.Organize
	dir, err := a.PlaylistDir()
	if err != nil {
		return nil, err
	}
	org, err := organize.New(oc.DownloadsDir, organize.PlaylistFolder(dir, run.Name), organize.FlatTemplate, a.Spotify, nil)
	if err != nil {
		return nil, err
	}
	mu, err := organize.ExpandPath(oc.MusicDir)
	if err != nil {
		return nil, err
	}
	org.SetLibraryDir(mu)
	return org, nil
}

// PlaylistDir is the folder playlist output mode creates per-playlist
// folders in (organize.playlist_dir, or the music directory).
func (a *App) PlaylistDir() (string, error) {
	oc := a.Cfg.Organize
	d := oc.PlaylistDir
	if strings.TrimSpace(d) == "" {
		d = oc.MusicDir
	}
	return organize.ExpandPath(d)
}

// CheckOrganize validates organize settings, including the directories,
// without saving them.
func (a *App) CheckOrganize(oc config.OrganizeConfig) error {
	if !oc.Enabled {
		return nil
	}
	if err := organize.CheckDirs(oc.DownloadsDir, oc.MusicDir); err != nil {
		return err
	}
	return oc.Validate()
}

// ApplyOrganize saves the [organize] section and updates cfg. New runs and
// resumed runs pick it up; a run already in progress keeps its settings.
func (a *App) ApplyOrganize(oc config.OrganizeConfig) error {
	if strings.TrimSpace(oc.PathTemplate) == "" {
		oc.PathTemplate = organize.DefaultTemplate
	}
	if err := config.SaveOrganize(a.CfgPath, oc); err != nil {
		return fmt.Errorf("saving config: %w", err)
	}
	oc.DownloadsDir, oc.MusicDir = strings.TrimSpace(oc.DownloadsDir), strings.TrimSpace(oc.MusicDir)
	oc.PathTemplate, oc.PlaylistDir = strings.TrimSpace(oc.PathTemplate), strings.TrimSpace(oc.PlaylistDir)
	a.Cfg.Organize = oc
	return nil
}

// SlskdDownloadsDir asks slskd for its configured download directory (as
// slskd sees it; inside Docker this differs from the host path).
func (a *App) SlskdDownloadsDir(ctx context.Context, cr config.Credentials) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	return slskd.NewClient(cr.SlskdURL, cr.SlskdAPIKey, 1).DownloadsDir(ctx)
}

// ConnResult is the outcome of a live connection test.
type ConnResult struct {
	Slskd, Spotify error
}

// OK reports whether both connections succeeded.
func (c ConnResult) OK() bool { return c.Slskd == nil && c.Spotify == nil }

// TestConnections checks both services concurrently with the given
// credentials (without touching the saved config).
func (a *App) TestConnections(ctx context.Context, cr config.Credentials) ConnResult {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	var res ConnResult
	done := make(chan struct{})
	go func() {
		defer close(done)
		if strings.TrimSpace(cr.SlskdURL) == "" || strings.TrimSpace(cr.SlskdAPIKey) == "" {
			res.Slskd = errors.New("URL and API key are required")
			return
		}
		res.Slskd = slskd.NewClient(cr.SlskdURL, cr.SlskdAPIKey, 1).Ping(ctx)
	}()
	if strings.TrimSpace(cr.SpotifyClientID) == "" || strings.TrimSpace(cr.SpotifyClientSecret) == "" {
		res.Spotify = errors.New("client ID and secret are required")
	} else {
		ts := spotify.NewClientCredentials(cr.SpotifyClientID, cr.SpotifyClientSecret)
		ts.AccountsURL = a.SpotifyAccountsURL
		c := spotify.NewClient(ts)
		if a.SpotifyAPIURL != "" {
			c.BaseURL = a.SpotifyAPIURL
		}
		res.Spotify = c.Ping(ctx)
	}
	<-done
	return res
}

// ApplyCredentials saves credentials to the config file and updates cfg.
func (a *App) ApplyCredentials(cr config.Credentials) error {
	if err := config.SaveCredentials(a.CfgPath, cr); err != nil {
		return fmt.Errorf("saving config: %w", err)
	}
	a.Cfg.Slskd.URL, a.Cfg.Slskd.APIKey = cr.SlskdURL, cr.SlskdAPIKey
	a.Cfg.Spotify.ClientID, a.Cfg.Spotify.ClientSecret = cr.SpotifyClientID, cr.SpotifyClientSecret
	a.Reconnect()
	return nil
}

// Credentials returns the active credentials.
func (a *App) Credentials() config.Credentials {
	return config.Credentials{
		SlskdURL: a.Cfg.Slskd.URL, SlskdAPIKey: a.Cfg.Slskd.APIKey,
		SpotifyClientID: a.Cfg.Spotify.ClientID, SpotifyClientSecret: a.Cfg.Spotify.ClientSecret,
	}
}

// SaveQualityPreset compiles qp, writes it to the config file as
// [quality_presets.<name>] and makes it available immediately.
func (a *App) SaveQualityPreset(name string, qp config.QualityProfile) error {
	base, ok := a.Cfg.QualityPresets[name]
	if !ok {
		base = match.QualityPolicy{}
	}
	pol, err := qp.Policy(base)
	if err != nil {
		return err
	}
	if err := config.SaveQualityPreset(a.CfgPath, name, qp); err != nil {
		return fmt.Errorf("saving config: %w", err)
	}
	a.Cfg.QualityPresets[name] = pol
	return nil
}

// DeleteQualityPreset removes a custom preset from the config file. For a
// built-in name it removes the override and restores the built-in.
func (a *App) DeleteQualityPreset(name string) error {
	builtin := config.IsBuiltinQualityPreset(name)
	if !builtin && name == a.Cfg.QualityPreset {
		return fmt.Errorf("%q is the default quality_preset in %s; change that first", name, a.CfgPath)
	}
	if err := config.DeleteQualityPreset(a.CfgPath, name); err != nil {
		return fmt.Errorf("saving config: %w", err)
	}
	if builtin {
		a.Cfg.QualityPresets[name] = match.DefaultQualityPresets()[name]
	} else {
		delete(a.Cfg.QualityPresets, name)
	}
	return nil
}
