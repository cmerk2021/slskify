package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadRankPresets(t *testing.T) {
	path := filepath.Join(t.TempDir(), "c.toml")
	os.WriteFile(path, []byte(`
quality_preset = "mp3-first"
[quality_presets.mp3-first]
rank = ["mp3 320", "flac", "mp3"]
min_bitrate = 192
[quality_presets.lossless-first]
rank = ["flac", "mp3 v0"]
prefer_bit_depth = 24
`), 0o600)
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	p := cfg.QualityPresets["mp3-first"]
	if len(p.Tiers) != 3 || p.Tiers[0].MinBitrate != 320 || p.MinBitrate != 192 || p.Description != "mp3 320 > flac > mp3" {
		t.Errorf("mp3-first: %+v", p)
	}
	if strings.Join(p.AllowedExtensions, ",") != "mp3,flac" {
		t.Errorf("allowed: %v", p.AllowedExtensions)
	}
	lf := cfg.QualityPresets["lossless-first"]
	if len(lf.Tiers) != 2 || strings.Join(lf.AllowedExtensions, ",") != "flac,mp3" ||
		lf.PreferBitDepth != 24 || lf.MinBitrate != 192 || lf.Description != "flac > mp3 v0" {
		t.Errorf("built-in override: %+v", lf)
	}
}

func TestLoadRankErrors(t *testing.T) {
	for _, body := range []string{
		"[quality_presets.x]\nrank = [\"flac\", \"bogus\"]\n",
		"[quality_presets.x]\nrank = [\"flac\"]\n[[quality_presets.x.tiers]]\nname = \"a\"\n",
	} {
		path := filepath.Join(t.TempDir(), "c.toml")
		os.WriteFile(path, []byte(body), 0o600)
		if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "quality_presets.x") {
			t.Errorf("%q: err = %v", body, err)
		}
	}
}

func TestSaveAndDeleteQualityPreset(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "c.toml")
	// Missing file: created with only the preset.
	if err := SaveQualityPreset(path, "mp3-first", QualityProfile{Rank: []string{"mp3 320", "flac", "mp3"}, MinBitrate: 192}); err != nil {
		t.Fatal(err)
	}
	if st, _ := os.Stat(path); st.Mode().Perm() != 0o600 {
		t.Errorf("mode %v", st.Mode())
	}
	if err := SaveCredentials(path, Credentials{SlskdURL: "http://x", SlskdAPIKey: "k", SpotifyClientID: "i", SpotifyClientSecret: "s"}); err != nil {
		t.Fatal(err)
	}
	// Overriding a built-in keeps unrelated keys of that entry.
	os.WriteFile(path, append(must(os.ReadFile(path)), []byte("\n[quality_presets.mp3-best]\nprefer_bit_depth = 24\nallowed_extensions = [\"mp3\"]\n")...), 0o600)
	if err := SaveQualityPreset(path, "mp3-best", QualityProfile{Rank: []string{"mp3 320 cbr", "mp3"}, Description: "  mine "}); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Slskd.APIKey != "k" {
		t.Error("credentials lost")
	}
	if p := cfg.QualityPresets["mp3-first"]; len(p.Tiers) != 3 || p.MinBitrate != 192 {
		t.Errorf("mp3-first: %+v", p)
	}
	if p := cfg.QualityPresets["mp3-best"]; len(p.Tiers) != 2 || p.PreferBitDepth != 24 || p.Description != "mine" || p.MinBitrate != 0 {
		t.Errorf("mp3-best: %+v", p)
	}
	for _, n := range []string{"mp3-first", "mp3-best"} {
		if err := DeleteQualityPreset(path, n); err != nil {
			t.Fatal(err)
		}
	}
	cfg, err = Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := cfg.QualityPresets["mp3-first"]; ok {
		t.Error("mp3-first not deleted")
	}
	if p := cfg.QualityPresets["mp3-best"]; len(p.Tiers) != 4 || p.MinBitrate != 192 {
		t.Errorf("mp3-best not restored: %+v", p)
	}
	if strings.Contains(string(must(os.ReadFile(path))), "quality_presets") {
		t.Error("empty quality_presets table left behind")
	}
}

func TestSaveQualityPresetValidation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "c.toml")
	for name, qp := range map[string]QualityProfile{
		"Bad Name": {Rank: []string{"flac"}},
		"":         {Rank: []string{"flac"}},
		"empty":    {},
		"bad-rule": {Rank: []string{"flac 320"}},
	} {
		if err := SaveQualityPreset(path, name, qp); err == nil {
			t.Errorf("%q: expected error", name)
		}
	}
	if _, err := os.Stat(path); err == nil {
		t.Error("file written despite errors")
	}
}

func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}
