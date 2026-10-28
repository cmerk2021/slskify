// Package config loads, merges and saves slskify's configuration.
//
// Precedence (highest first): CLI flags > environment variables > config
// file > built-in defaults.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/BurntSushi/toml"

	"github.com/cmerk2021/slskify/internal/match"
)

// Duration is a time.Duration that reads/writes TOML strings like "5m".
type Duration struct{ time.Duration }

// UnmarshalText parses a Go duration string ("90s", "5m", "1.5s").
func (d *Duration) UnmarshalText(b []byte) error {
	v, err := time.ParseDuration(strings.TrimSpace(string(b)))
	if err != nil {
		return fmt.Errorf("invalid duration %q (use e.g. \"30s\", \"5m\")", string(b))
	}
	d.Duration = v
	return nil
}

// MarshalText formats the duration.
func (d Duration) MarshalText() ([]byte, error) { return []byte(d.Duration.String()), nil }

// D is shorthand for constructing a Duration.
func D(v time.Duration) Duration { return Duration{v} }

// SlskdConfig holds the slskd connection.
type SlskdConfig struct {
	URL    string `toml:"url"`
	APIKey string `toml:"api_key"`
	// MaxConcurrentSearches caps searches open on slskd at once regardless
	// of the speed preset; extra searches queue locally.
	MaxConcurrentSearches int `toml:"max_concurrent_searches"`
}

// SpotifyConfig holds Client Credentials.
type SpotifyConfig struct {
	ClientID     string `toml:"client_id"`
	ClientSecret string `toml:"client_secret"`
}

// SpeedPreset bundles throughput knobs.
type SpeedPreset struct {
	Description         string   `toml:"description"`
	ConcurrentSearches  int      `toml:"concurrent_searches"`
	ConcurrentDownloads int      `toml:"concurrent_downloads"`
	SearchDelay         Duration `toml:"search_delay"`
	SearchTimeout       Duration `toml:"search_timeout"`
	// ResponseLimit / FileLimit bound how much slskd collects per search.
	ResponseLimit int `toml:"response_limit"`
	FileLimit     int `toml:"file_limit"`
	// Warning is shown in the pre-run picker.
	Warning string `toml:"warning"`
}

// DownloadConfig controls transfer handling.
type DownloadConfig struct {
	// QueueTimeout: a transfer still queued remotely after this long is
	// cancelled and the next candidate is tried.
	QueueTimeout Duration `toml:"queue_timeout"`
	// StallTimeout: an in-progress transfer with no new bytes for this long
	// is treated as failed. 0 disables.
	StallTimeout Duration `toml:"stall_timeout"`
	// MaxCandidateAttempts before the track goes to review.
	MaxCandidateAttempts int      `toml:"max_candidate_attempts"`
	PollInterval         Duration `toml:"poll_interval"`
}

// Config is the full configuration.
type Config struct {
	Slskd   SlskdConfig   `toml:"slskd"`
	Spotify SpotifyConfig `toml:"spotify"`
	// QualityPreset / SpeedPreset are the defaults preselected in the TUI.
	QualityPreset  string                         `toml:"quality_preset"`
	SpeedPreset    string                         `toml:"speed_preset"`
	Download       DownloadConfig                 `toml:"download"`
	Match          match.Config                   `toml:"match"`
	QualityPresets map[string]match.QualityPolicy `toml:"quality_presets"`
	SpeedPresets   map[string]SpeedPreset         `toml:"speed_presets"`
}

// DefaultSpeedPresets returns the built-in speed presets.
func DefaultSpeedPresets() map[string]SpeedPreset {
	return map[string]SpeedPreset{
		"slow": {Description: "1 search, 2 downloads, 3s between searches", ConcurrentSearches: 1, ConcurrentDownloads: 2,
			SearchDelay: D(3 * time.Second), SearchTimeout: D(20 * time.Second), ResponseLimit: 100, FileLimit: 10000},
		"medium": {Description: "2 searches, 3 downloads, 1s between searches", ConcurrentSearches: 2, ConcurrentDownloads: 3,
			SearchDelay: D(time.Second), SearchTimeout: D(15 * time.Second), ResponseLimit: 100, FileLimit: 10000},
		"fast": {Description: "6 searches, 8 downloads, no delay", ConcurrentSearches: 6, ConcurrentDownloads: 8,
			SearchDelay: D(0), SearchTimeout: D(15 * time.Second), ResponseLimit: 100, FileLimit: 10000,
			Warning: "heavy search volume may get your client throttled by the Soulseek network"},
	}
}

// Default returns the built-in configuration.
func Default() *Config {
	return &Config{
		Slskd:         SlskdConfig{URL: "http://localhost:5030", MaxConcurrentSearches: 6},
		QualityPreset: "lossless-first",
		SpeedPreset:   "medium",
		Download: DownloadConfig{
			QueueTimeout:         D(5 * time.Minute),
			StallTimeout:         D(3 * time.Minute),
			MaxCandidateAttempts: 3,
			PollInterval:         D(2 * time.Second),
		},
		Match:          match.DefaultConfig(),
		QualityPresets: match.DefaultQualityPresets(),
		SpeedPresets:   DefaultSpeedPresets(),
	}
}

// Path returns the default config file path
// (os.UserConfigDir()/slskify/config.toml).
func Path() (string, error) {
	d, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "slskify", "config.toml"), nil
}

// DataDir returns slskify's data directory: $XDG_DATA_HOME/slskify (or
// ~/.local/share/slskify) on Linux/BSD, ~/Library/Application
// Support/slskify on macOS, %LocalAppData%\slskify on Windows.
func DataDir() (string, error) {
	switch runtime.GOOS {
	case "windows":
		if d := os.Getenv("LocalAppData"); d != "" {
			return filepath.Join(d, "slskify"), nil
		}
		d, err := os.UserConfigDir()
		return filepath.Join(d, "slskify"), err
	case "darwin", "ios":
		d, err := os.UserConfigDir() // ~/Library/Application Support
		return filepath.Join(d, "slskify"), err
	}
	if d := os.Getenv("XDG_DATA_HOME"); d != "" && filepath.IsAbs(d) {
		return filepath.Join(d, "slskify"), nil
	}
	h, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(h, ".local", "share", "slskify"), nil
}

// DBPath returns the state database path.
func DBPath() (string, error) {
	d, err := DataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "state.db"), nil
}

// Load reads defaults merged with the file at path (missing file = defaults).
// Presets defined in the file are overlaid on built-ins of the same name,
// so a user can override a single knob of "medium" or add new presets.
func Load(path string) (*Config, error) {
	cfg := Default()
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return cfg, nil
	}
	if err != nil {
		return nil, err
	}
	var file struct {
		Slskd          SlskdConfig               `toml:"slskd"`
		Spotify        SpotifyConfig             `toml:"spotify"`
		QualityPreset  string                    `toml:"quality_preset"`
		SpeedPreset    string                    `toml:"speed_preset"`
		Download       toml.Primitive            `toml:"download"`
		Match          toml.Primitive            `toml:"match"`
		QualityPresets map[string]toml.Primitive `toml:"quality_presets"`
		SpeedPresets   map[string]toml.Primitive `toml:"speed_presets"`
	}
	file.Slskd, file.Spotify = cfg.Slskd, cfg.Spotify
	file.QualityPreset, file.SpeedPreset = cfg.QualityPreset, cfg.SpeedPreset
	md, err := toml.Decode(string(b), &file)
	if err != nil {
		return nil, fmt.Errorf("config %s: %w", path, err)
	}
	cfg.Slskd, cfg.Spotify = file.Slskd, file.Spotify
	cfg.QualityPreset, cfg.SpeedPreset = file.QualityPreset, file.SpeedPreset
	if md.IsDefined("download") {
		if err := md.PrimitiveDecode(file.Download, &cfg.Download); err != nil {
			return nil, fmt.Errorf("config [download]: %w", err)
		}
	}
	if md.IsDefined("match") {
		if err := md.PrimitiveDecode(file.Match, &cfg.Match); err != nil {
			return nil, fmt.Errorf("config [match]: %w", err)
		}
	}
	for name, prim := range file.QualityPresets {
		p := cfg.QualityPresets[name]
		hasRank := md.IsDefined("quality_presets", name, "rank")
		if hasRank && md.IsDefined("quality_presets", name, "tiers") {
			return nil, fmt.Errorf("config [quality_presets.%s]: set either rank or tiers, not both", name)
		}
		if hasRank || md.IsDefined("quality_presets", name, "tiers") {
			p.Tiers, p.Rank = nil, nil
		}
		if hasRank && !md.IsDefined("quality_presets", name, "allowed_extensions") {
			p.AllowedExtensions = nil
		}
		if hasRank && !md.IsDefined("quality_presets", name, "description") {
			p.Description = ""
		}
		if err := md.PrimitiveDecode(prim, &p); err != nil {
			return nil, fmt.Errorf("config [quality_presets.%s]: %w", name, err)
		}
		if p, err = p.Compile(); err != nil {
			return nil, fmt.Errorf("config [quality_presets.%s] rank: %w", name, err)
		}
		cfg.QualityPresets[name] = p
	}
	for name, prim := range file.SpeedPresets {
		p := cfg.SpeedPresets[name]
		if err := md.PrimitiveDecode(prim, &p); err != nil {
			return nil, fmt.Errorf("config [speed_presets.%s]: %w", name, err)
		}
		cfg.SpeedPresets[name] = p
	}
	if und := md.Undecoded(); len(und) > 0 {
		var keys []string
		for _, k := range und {
			keys = append(keys, k.String())
		}
		return nil, fmt.Errorf("config %s: unknown keys: %s", path, strings.Join(keys, ", "))
	}
	return cfg, nil
}

// Env variable names.
const (
	EnvSlskdURL            = "SLSKD_URL"
	EnvSlskdAPIKey         = "SLSKD_API_KEY"
	EnvSpotifyClientID     = "SPOTIFY_CLIENT_ID"
	EnvSpotifyClientSecret = "SPOTIFY_CLIENT_SECRET"
)

// ApplyEnv overlays environment variables using getenv (os.Getenv if nil).
func (c *Config) ApplyEnv(getenv func(string) string) {
	if getenv == nil {
		getenv = os.Getenv
	}
	set := func(dst *string, key string) {
		if v := strings.TrimSpace(getenv(key)); v != "" {
			*dst = v
		}
	}
	set(&c.Slskd.URL, EnvSlskdURL)
	set(&c.Slskd.APIKey, EnvSlskdAPIKey)
	set(&c.Spotify.ClientID, EnvSpotifyClientID)
	set(&c.Spotify.ClientSecret, EnvSpotifyClientSecret)
}

// Overrides are CLI flag values; empty strings mean "not set".
type Overrides struct {
	SlskdURL, SlskdAPIKey                string
	SpotifyClientID, SpotifyClientSecret string
	Quality, Speed                       string
}

// ApplyOverrides overlays CLI flags.
func (c *Config) ApplyOverrides(o Overrides) {
	set := func(dst *string, v string) {
		if v = strings.TrimSpace(v); v != "" {
			*dst = v
		}
	}
	set(&c.Slskd.URL, o.SlskdURL)
	set(&c.Slskd.APIKey, o.SlskdAPIKey)
	set(&c.Spotify.ClientID, o.SpotifyClientID)
	set(&c.Spotify.ClientSecret, o.SpotifyClientSecret)
	set(&c.QualityPreset, o.Quality)
	set(&c.SpeedPreset, o.Speed)
}

// Resolve loads the file at path (default location if empty), then applies
// env vars and flags.
func Resolve(path string, o Overrides) (*Config, string, error) {
	if path == "" {
		p, err := Path()
		if err != nil {
			return nil, "", err
		}
		path = p
	}
	cfg, err := Load(path)
	if err != nil {
		return nil, path, err
	}
	cfg.ApplyEnv(nil)
	cfg.ApplyOverrides(o)
	return cfg, path, cfg.Validate()
}

// MissingCredentials lists credentials that are not configured.
func (c *Config) MissingCredentials() []string {
	var out []string
	if c.Slskd.URL == "" {
		out = append(out, "slskd URL")
	}
	if c.Slskd.APIKey == "" {
		out = append(out, "slskd API key")
	}
	if c.Spotify.ClientID == "" {
		out = append(out, "Spotify client ID")
	}
	if c.Spotify.ClientSecret == "" {
		out = append(out, "Spotify client secret")
	}
	return out
}

// Validate checks internal consistency.
func (c *Config) Validate() error {
	var errs []string
	if _, ok := c.QualityPresets[c.QualityPreset]; !ok {
		errs = append(errs, fmt.Sprintf("unknown quality preset %q (have %s)", c.QualityPreset, strings.Join(c.QualityPresetNames(), ", ")))
	}
	if _, ok := c.SpeedPresets[c.SpeedPreset]; !ok {
		errs = append(errs, fmt.Sprintf("unknown speed preset %q (have %s)", c.SpeedPreset, strings.Join(c.SpeedPresetNames(), ", ")))
	}
	m := c.Match
	if m.LowThreshold < 0 || m.HighThreshold > 100 || m.LowThreshold > m.HighThreshold {
		errs = append(errs, "match thresholds must satisfy 0 <= low_threshold <= high_threshold <= 100")
	}
	if m.DurationFullS > m.DurationToleranceS || m.DurationToleranceS > m.DurationHardRejectS {
		errs = append(errs, "match durations must satisfy duration_full_s <= duration_tolerance_s <= duration_hard_reject_s")
	}
	for name, p := range c.SpeedPresets {
		if p.ConcurrentSearches < 1 || p.ConcurrentDownloads < 1 {
			errs = append(errs, fmt.Sprintf("speed preset %q needs concurrent_searches and concurrent_downloads >= 1", name))
		}
	}
	for name, p := range c.QualityPresets {
		if len(p.AllowedExtensions) == 0 {
			errs = append(errs, fmt.Sprintf("quality preset %q has no allowed_extensions", name))
		}
	}
	if c.Download.MaxCandidateAttempts < 1 {
		errs = append(errs, "download.max_candidate_attempts must be >= 1")
	}
	if len(errs) > 0 {
		return errors.New("invalid config: " + strings.Join(errs, "; "))
	}
	return nil
}

var builtinQualityPresets = []string{"lossless-first", "lossless-only", "mp3-best", "any-best"}

// IsBuiltinQualityPreset reports whether name is one of the built-in presets.
func IsBuiltinQualityPreset(name string) bool {
	for _, n := range builtinQualityPresets {
		if n == name {
			return true
		}
	}
	return false
}

// QualityPresetNames returns preset names, built-ins first in a stable order.
func (c *Config) QualityPresetNames() []string {
	return orderedNames(c.QualityPresets, builtinQualityPresets)
}

// SpeedPresetNames returns speed preset names (slow, medium, fast first).
func (c *Config) SpeedPresetNames() []string {
	return orderedNames(c.SpeedPresets, []string{"slow", "medium", "fast"})
}

func orderedNames[V any](m map[string]V, builtin []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, n := range builtin {
		if _, ok := m[n]; ok {
			out = append(out, n)
			seen[n] = true
		}
	}
	var rest []string
	for n := range m {
		if !seen[n] {
			rest = append(rest, n)
		}
	}
	sort.Strings(rest)
	return append(out, rest...)
}

// Credentials are the values collected by the setup screen.
type Credentials struct {
	SlskdURL, SlskdAPIKey                string
	SpotifyClientID, SpotifyClientSecret string
}

// SaveCredentials writes credentials to path with mode 0600. A new file
// gets the full default configuration (as a reference to edit); an
// existing file keeps its other settings and only the credential keys
// change. (Comments in an existing file are not preserved.)
func SaveCredentials(path string, cr Credentials) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	var buf bytes.Buffer
	existing, err := os.ReadFile(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		cfg := Default()
		cfg.Slskd.URL, cfg.Slskd.APIKey = cr.SlskdURL, cr.SlskdAPIKey
		cfg.Spotify.ClientID, cfg.Spotify.ClientSecret = cr.SpotifyClientID, cr.SpotifyClientSecret
		buf.WriteString("# slskify configuration. See README for every option.\n# Contains secrets: keep this file private (mode 0600).\n\n")
		if err := toml.NewEncoder(&buf).Encode(cfg); err != nil {
			return err
		}
	case err != nil:
		return err
	default:
		var m map[string]any
		if _, err := toml.Decode(string(existing), &m); err != nil {
			return fmt.Errorf("config %s: %w", path, err)
		}
		if m == nil {
			m = map[string]any{}
		}
		section := func(name string) map[string]any {
			s, ok := m[name].(map[string]any)
			if !ok {
				s = map[string]any{}
				m[name] = s
			}
			return s
		}
		section("slskd")["url"] = cr.SlskdURL
		section("slskd")["api_key"] = cr.SlskdAPIKey
		section("spotify")["client_id"] = cr.SpotifyClientID
		section("spotify")["client_secret"] = cr.SpotifyClientSecret
		if err := toml.NewEncoder(&buf).Encode(m); err != nil {
			return err
		}
	}
	return writePrivate(path, buf.Bytes())
}

// writePrivate atomically writes data with mode 0600.
func writePrivate(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".config-*.toml")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return err
	}
	return os.Chmod(path, 0o600)
}

// Mask hides a secret for display: "abcd…" style without revealing length.
func Mask(s string) string {
	if s == "" {
		return "(not set)"
	}
	return "••••••••"
}
