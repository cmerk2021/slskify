package match

import (
	"strings"
	"testing"
)

func TestClassify(t *testing.T) {
	cases := []struct {
		f     File
		fmt   string
		lossl bool
		br    int
		label string
	}{
		{File{Path: "a.flac", BitDepth: 16, SampleRate: 44100}, "flac", true, 0, "FLAC 16/44.1"},
		{File{Path: "a.FLAC"}, "flac", true, 0, "FLAC"},
		{File{Path: "a.mp3", BitRate: 320}, "mp3", false, 320, "MP3 320"},
		{File{Path: "a.mp3", BitRate: 245, VBR: true}, "mp3", false, 245, "MP3 VBR ~245"},
		{File{Path: "a.mp3", Size: 8_000_000, Length: 200}, "mp3", false, 320, "MP3 ~320"},
		{File{Path: "a.mp3"}, "mp3", false, 0, "MP3 ?kbps"},
		{File{Path: "a.m4a", BitRate: 256}, "aac", false, 256, "AAC 256"},
		{File{Path: "a.m4a", BitRate: 900}, "alac", true, 900, "ALAC"},
		{File{Path: "a.wav", BitDepth: 24, SampleRate: 96000}, "wav", true, 0, "WAV 24/96"},
	}
	for _, c := range cases {
		q := Classify(c.f)
		if q.Format != c.fmt || q.Lossless != c.lossl || q.Bitrate != c.br || q.Label != c.label {
			t.Errorf("%s: got %+v", c.f.Path, q)
		}
	}
}

// files used for quality-order tests (all "correct", same confidence).
var qFiles = map[string]File{
	"flac16":   {Path: "a.flac", BitDepth: 16, SampleRate: 44100},
	"flac24":   {Path: "a.flac", BitDepth: 24, SampleRate: 96000},
	"flac?":    {Path: "a.flac"},
	"mp3-320":  {Path: "a.mp3", BitRate: 320},
	"mp3-v0":   {Path: "a.mp3", BitRate: 245, VBR: true},
	"mp3-256":  {Path: "a.mp3", BitRate: 256},
	"mp3-192":  {Path: "a.mp3", BitRate: 192},
	"mp3-128":  {Path: "a.mp3", BitRate: 128},
	"aac-256":  {Path: "a.m4a", BitRate: 256},
	"ogg-320":  {Path: "a.ogg", BitRate: 320},
	"opus-160": {Path: "a.opus", BitRate: 160},
}

func TestQualityOrderPerPreset(t *testing.T) {
	presets := DefaultQualityPresets()
	cases := []struct {
		preset  string
		order   []string // expected best → worst among allowed files
		blocked []string // hard-rejected by the policy
	}{
		{"lossless-first", []string{"flac16", "flac?", "flac24", "mp3-320", "mp3-v0", "mp3-256", "mp3-192"},
			[]string{"mp3-128", "aac-256", "ogg-320", "opus-160"}},
		{"lossless-only", []string{"flac16", "flac?", "flac24"},
			[]string{"mp3-320", "mp3-v0", "aac-256"}},
		{"mp3-best", []string{"mp3-320", "mp3-v0", "mp3-256", "mp3-192"},
			[]string{"flac16", "mp3-128", "aac-256"}},
		{"any-best", []string{"flac16", "flac?", "flac24", "mp3-320", "ogg-320", "aac-256", "mp3-256", "mp3-v0", "mp3-192", "opus-160", "mp3-128"},
			nil},
	}
	m := NewMatcher(DefaultConfig())
	for _, c := range cases {
		t.Run(c.preset, func(t *testing.T) {
			p, ok := presets[c.preset]
			if !ok {
				t.Fatalf("missing preset")
			}
			var cs []*Candidate
			for _, name := range c.order {
				f := qFiles[name]
				f.Username = name
				cand := &Candidate{File: f, Confidence: 95}
				cand.Quality = Classify(f)
				cand.Tier, cand.TierName = p.TierOf(cand.Quality)
				if r := m.hardFilter(withSize(cand), p, 0); r != "" {
					t.Fatalf("%s unexpectedly rejected: %s", name, r)
				}
				cs = append(cs, cand)
			}
			// Reverse, then sort, to make sure sorting does the work.
			for i, j := 0, len(cs)-1; i < j; i, j = i+1, j-1 {
				cs[i], cs[j] = cs[j], cs[i]
			}
			rankByQuality(cs, p, 5)
			var got []string
			for _, c := range cs {
				got = append(got, c.Username)
			}
			if strings.Join(got, " ") != strings.Join(c.order, " ") {
				t.Errorf("order\n got  %v\n want %v", got, c.order)
			}
			for _, name := range c.blocked {
				f, ok := qFiles[name]
				if !ok {
					continue
				}
				cand := &Candidate{File: f}
				cand.Quality = Classify(f)
				if r := m.hardFilter(withSize(cand), p, 0); r == "" {
					t.Errorf("%s should be rejected by %s", name, c.preset)
				}
			}
		})
	}
}

func withSize(c *Candidate) *Candidate { c.Size = 8_000_000; return c }

func TestRankTieBreakers(t *testing.T) {
	p := DefaultQualityPresets()["lossless-first"]
	mk := func(user string, conf int, free bool, speed, queue int64) *Candidate {
		c := &Candidate{File: File{Username: user, Path: "x.mp3", BitRate: 320, FreeSlot: free, UploadSpeed: speed, QueueLength: queue}, Confidence: conf}
		c.Quality = Classify(c.File)
		c.Tier, _ = p.TierOf(c.Quality)
		return c
	}
	cs := []*Candidate{
		mk("slow-free", 96, true, 100, 0),
		mk("busy", 97, false, 9999, 0),
		mk("fast-free", 95, true, 5000, 3),
		mk("fast-free-short", 95, true, 5000, 1),
		mk("lower-bucket", 90, true, 99999, 0),
	}
	rankByQuality(cs, p, 5)
	want := "fast-free-short fast-free slow-free busy lower-bucket"
	var got []string
	for _, c := range cs {
		got = append(got, c.Username)
	}
	if strings.Join(got, " ") != want {
		t.Errorf("got %v want %s", got, want)
	}
}

func TestHardFilters(t *testing.T) {
	m := NewMatcher(DefaultConfig())
	p := DefaultQualityPresets()["lossless-first"]
	cases := []struct {
		f    File
		want string
	}{
		{File{Path: "a.mp3", BitRate: 320, Size: 8e6, Locked: true}, "locked"},
		{File{Path: "a.jpg", Size: 8e6}, "not audio"},
		{File{Path: "a.cue", Size: 8e6}, "not audio"},
		{File{Path: "a.m4a", BitRate: 256, Size: 8e6}, "not allowed"},
		{File{Path: "a.mp3", BitRate: 128, Size: 8e6}, "bitrate"},
		{File{Path: "a.mp3", BitRate: 320, Size: 500_000}, "size"},
		{File{Path: "a.flac", Size: 400e6}, "size"},
		{File{Path: "a.mp3", BitRate: 320, Size: 8e6, Length: 215}, "duration"},
		{File{Path: "a.mp3", BitRate: 320, Size: 8e6, Length: 205}, ""},
		{File{Path: "a.mp3", BitRate: 319, Size: 8e6}, ""},
		{File{Path: "a.mp3", Size: 8e6}, ""}, // unknown bitrate passes
	}
	for _, c := range cases {
		cand := &Candidate{File: c.f}
		cand.Quality = Classify(c.f)
		got := m.hardFilter(cand, p, 200)
		if (c.want == "") != (got == "") || !strings.Contains(got, c.want) {
			t.Errorf("%+v: got %q want %q", c.f, got, c.want)
		}
	}
}
