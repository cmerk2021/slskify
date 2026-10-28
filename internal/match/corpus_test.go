package match

import (
	"testing"

	"github.com/BurntSushi/toml"
)

type corpusCase struct {
	Note        string   `toml:"note"`
	Title       string   `toml:"title"`
	Artists     []string `toml:"artists"`
	Album       string   `toml:"album"`
	AlbumType   string   `toml:"album_type"`
	TrackNumber int      `toml:"track_number"`
	DurationS   int      `toml:"duration_s"`
	Explicit    bool     `toml:"explicit"`
	Path        string   `toml:"path"`
	LengthS     int      `toml:"length_s"`
	Bitrate     int      `toml:"bitrate"`
	SizeMB      float64  `toml:"size_mb"`
	Expect      string   `toml:"expect"`
	MinConf     int      `toml:"min_conf"`
	MaxConf     int      `toml:"max_conf"`
}

// TestCorpus runs testdata/corpus.toml. Extend it with real cases.
func TestCorpus(t *testing.T) {
	var corpus struct {
		Case []corpusCase `toml:"case"`
	}
	if _, err := toml.DecodeFile("testdata/corpus.toml", &corpus); err != nil {
		t.Fatal(err)
	}
	if len(corpus.Case) == 0 {
		t.Fatal("empty corpus")
	}
	cfg := DefaultConfig()
	m := NewMatcher(cfg)
	for _, c := range corpus.Case {
		t.Run(c.Note, func(t *testing.T) {
			at := c.AlbumType
			if at == "" {
				at = "album"
			}
			size := c.SizeMB
			if size == 0 {
				size = 8
			}
			tg := Target{Title: c.Title, Artists: c.Artists, Album: c.Album, AlbumType: at,
				TrackNumber: c.TrackNumber, DurationMs: c.DurationS * 1000, Explicit: c.Explicit}
			f := File{Username: "u", Path: c.Path, Length: c.LengthS, BitRate: c.Bitrate, Size: int64(size * 1e6)}
			cand := m.Score(tg, f)
			lo, hi := 0, 100
			switch c.Expect {
			case "match":
				lo = cfg.HighThreshold
			case "review":
				lo, hi = cfg.LowThreshold, cfg.HighThreshold-1
			case "reject":
				hi = cfg.LowThreshold - 1
			default:
				t.Fatalf("bad expect %q", c.Expect)
			}
			if c.MinConf > 0 {
				lo = c.MinConf
			}
			if c.MaxConf > 0 {
				hi = c.MaxConf
			}
			if cand.Confidence < lo || cand.Confidence > hi {
				t.Errorf("%s: confidence %d, want [%d,%d]\n%s", c.Path, cand.Confidence, lo, hi, cand.Explain("    "))
			}
		})
	}
}
