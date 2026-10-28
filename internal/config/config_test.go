package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDefaultsValid(t *testing.T) {
	if err := Default().Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestLoadMissingFileGivesDefaults(t *testing.T) {
	cfg, err := Load(filepath.Join(t.TempDir(), "nope.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.SpeedPreset != "medium" || cfg.Match.HighThreshold != 80 || cfg.Download.QueueTimeout.Duration != 5*time.Minute {
		t.Fatalf("unexpected defaults: %+v", cfg)
	}
}

func TestLoadMergesPresets(t *testing.T) {
	path := filepath.Join(t.TempDir(), "c.toml")
	os.WriteFile(path, []byte(`
quality_preset = "mine"
[slskd]
url = "http://nas:5030"
[match]
high_threshold = 85
[download]
queue_timeout = "2m"
[speed_presets.medium]
concurrent_searches = 4
[speed_presets.turtle]
concurrent_searches = 1
concurrent_downloads = 1
search_delay = "10s"
[quality_presets.mine]
allowed_extensions = ["flac"]
[[quality_presets.mine.tiers]]
name = "flac"
formats = ["flac"]
[quality_presets.mp3-best]
min_bitrate = 256
`), 0o600)
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Slskd.URL != "http://nas:5030" || cfg.Slskd.MaxConcurrentSearches != 6 {
		t.Errorf("slskd: %+v", cfg.Slskd)
	}
	if cfg.Match.HighThreshold != 85 || cfg.Match.LowThreshold != 50 || !cfg.Match.RequireDurationForAuto {
		t.Errorf("match not merged: %+v", cfg.Match)
	}
	if cfg.Download.QueueTimeout.Duration != 2*time.Minute || cfg.Download.MaxCandidateAttempts != 3 {
		t.Errorf("download: %+v", cfg.Download)
	}
	med := cfg.SpeedPresets["medium"]
	if med.ConcurrentSearches != 4 || med.ConcurrentDownloads != 3 || med.SearchDelay.Duration != time.Second {
		t.Errorf("medium not overlaid: %+v", med)
	}
	if cfg.SpeedPresets["turtle"].SearchDelay.Duration != 10*time.Second || cfg.SpeedPresets["fast"].ConcurrentSearches != 6 {
		t.Error("custom/builtin speed presets")
	}
	if mp3 := cfg.QualityPresets["mp3-best"]; mp3.MinBitrate != 256 || len(mp3.Tiers) != 4 {
		t.Errorf("mp3-best overlay: %+v", mp3)
	}
	if mine := cfg.QualityPresets["mine"]; len(mine.Tiers) != 1 {
		t.Errorf("mine: %+v", mine)
	}
	if err := cfg.Validate(); err != nil {
		t.Error(err)
	}
	names := cfg.SpeedPresetNames()
	if strings.Join(names, ",") != "slow,medium,fast,turtle" {
		t.Errorf("names %v", names)
	}
}

func TestUnknownKeysRejected(t *testing.T) {
	path := filepath.Join(t.TempDir(), "c.toml")
	os.WriteFile(path, []byte("[match]\nhigh_treshold = 3\n"), 0o600)
	if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "high_treshold") {
		t.Fatalf("want unknown key error, got %v", err)
	}
}

func TestPrecedence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "c.toml")
	os.WriteFile(path, []byte("[slskd]\nurl = \"http://file\"\napi_key = \"filekey\"\n[spotify]\nclient_id = \"fileid\"\n"), 0o600)
	cfg, _ := Load(path)
	env := map[string]string{EnvSlskdURL: "http://env", EnvSpotifyClientID: "envid"}
	cfg.ApplyEnv(func(k string) string { return env[k] })
	cfg.ApplyOverrides(Overrides{SlskdURL: "http://flag"})
	if cfg.Slskd.URL != "http://flag" || cfg.Slskd.APIKey != "filekey" || cfg.Spotify.ClientID != "envid" {
		t.Fatalf("precedence wrong: %+v %+v", cfg.Slskd, cfg.Spotify)
	}
	if m := cfg.MissingCredentials(); len(m) != 1 || m[0] != "Spotify client secret" {
		t.Fatalf("missing: %v", m)
	}
}

func TestSaveCredentialsNewAndExisting(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "config.toml")
	cr := Credentials{"http://h:5030", "k1", "id1", "sec1"}
	if err := SaveCredentials(path, cr); err != nil {
		t.Fatal(err)
	}
	st, _ := os.Stat(path)
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", st.Mode().Perm())
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("reload full defaults: %v", err)
	}
	if cfg.Slskd.APIKey != "k1" || cfg.Spotify.ClientSecret != "sec1" || len(cfg.QualityPresets["lossless-first"].Tiers) != 5 {
		t.Fatalf("roundtrip: %+v", cfg)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	// Existing file: other settings survive.
	os.WriteFile(path, []byte("speed_preset = \"fast\"\n[match]\nhigh_threshold = 90\n"), 0o644)
	if err := SaveCredentials(path, Credentials{"http://x", "k2", "id2", "sec2"}); err != nil {
		t.Fatal(err)
	}
	cfg, err = Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.SpeedPreset != "fast" || cfg.Match.HighThreshold != 90 || cfg.Slskd.APIKey != "k2" {
		t.Fatalf("existing merge: %+v", cfg)
	}
	st, _ = os.Stat(path)
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("mode after update %v", st.Mode().Perm())
	}
}

func TestValidateCatchesBadPreset(t *testing.T) {
	c := Default()
	c.SpeedPreset = "warp"
	if err := c.Validate(); err == nil {
		t.Fatal("want error")
	}
}
