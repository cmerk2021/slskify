package match

import (
	"strings"
	"testing"
)

func TestParseRankRule(t *testing.T) {
	ok := map[string]string{
		"mp3 320":         "mp3 320",
		"MP3 ≥320 CBR":    "mp3 320 cbr",
		"mp3 >=256":       "mp3 256",
		"mp3 256+":        "mp3 256",
		"mp3 256kbps":     "mp3 256",
		"mp3 v0":          "mp3 v0",
		"mp3 vbr 220":     "mp3 v0",
		"mp3 v2":          "mp3 v2",
		"flac":            "flac",
		".FLAC":           "flac",
		"flac/alac":       "flac/alac",
		"m4a":             "aac/alac",
		"aif":             "aiff/aif",
		"lossless":        "lossless",
		"lossy 192":       "lossy 192",
		"any":             "any",
		"*":               "any",
		"  aac   256  ":   "aac 256",
		"mp3/aac 256 vbr": "mp3/aac 256 vbr",
	}
	for in, want := range ok {
		tier, err := ParseRankRule(in)
		if err != nil {
			t.Errorf("%q: %v", in, err)
			continue
		}
		if tier.Name != want {
			t.Errorf("%q: name %q, want %q", in, tier.Name, want)
		}
		// Round trip.
		again, err := ParseRankRule(TierRule(tier))
		if err != nil || again.Name != tier.Name {
			t.Errorf("%q: round trip %q (%v)", in, again.Name, err)
		}
	}
	for _, bad := range []string{"", "mp4", "flac 320", "lossless vbr", "mp3 cbr vbr", "mp3 320 256", "mp3 fast", "mp3 -5"} {
		if _, err := ParseRankRule(bad); err == nil {
			t.Errorf("%q: expected an error", bad)
		}
	}
}

func TestBuiltinTiersRoundTripThroughRank(t *testing.T) {
	for name, p := range DefaultQualityPresets() {
		r := QualityPolicy{Rank: p.RankRules(), AllowedExtensions: p.AllowedExtensions}
		c, err := r.Compile()
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		for _, f := range qFiles {
			q := Classify(f)
			a, _ := p.TierOf(q)
			b, _ := c.TierOf(q)
			if a != b {
				t.Errorf("%s: %s tier %d vs %d via rank %v", name, f.Path, a, b, r.Rank)
			}
		}
	}
}

// The motivating example: prefer MP3 320, take FLAC when there is none,
// then any other MP3.
func TestRankMP3FirstThenFLAC(t *testing.T) {
	p, err := QualityPolicy{Rank: []string{"mp3 320", "flac", "mp3"}}.Compile()
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(p.AllowedExtensions, ","); got != "mp3,flac" {
		t.Errorf("allowed = %s", got)
	}
	if p.Description != "mp3 320 > flac > mp3" {
		t.Errorf("description = %q", p.Description)
	}
	order := []string{"mp3-320", "flac16", "flac?", "flac24", "mp3-256", "mp3-v0", "mp3-192", "mp3-128"}
	m := NewMatcher(DefaultConfig())
	var cs []*Candidate
	for i := len(order) - 1; i >= 0; i-- {
		f := qFiles[order[i]]
		f.Username = order[i]
		c := &Candidate{File: f, Confidence: 95}
		c.Quality = Classify(f)
		c.Tier, c.TierName = p.TierOf(c.Quality)
		if r := m.hardFilter(withSize(c), p, 0); r != "" {
			t.Fatalf("%s rejected: %s", order[i], r)
		}
		cs = append(cs, c)
	}
	rankByQuality(cs, p, 5)
	var got []string
	for _, c := range cs {
		got = append(got, c.Username)
	}
	if strings.Join(got, " ") != strings.Join(order, " ") {
		t.Errorf("order\n got  %v\n want %v", got, order)
	}
	for _, name := range []string{"aac-256", "ogg-320", "opus-160"} {
		c := &Candidate{File: qFiles[name]}
		c.Quality = Classify(c.File)
		if m.hardFilter(withSize(c), p, 0) == "" {
			t.Errorf("%s should be rejected", name)
		}
	}

	// A floor rejects low lossy bitrates but never lossless.
	p.MinBitrate = 256
	for _, r := range p.Preview() {
		if strings.HasPrefix(r.Label, "MP3 192") || strings.HasPrefix(r.Label, "MP3 128") {
			if r.Tier >= 0 {
				t.Errorf("%s should be below the floor", r.Label)
			}
		}
		if strings.HasPrefix(r.Label, "FLAC") && r.Tier != 1 {
			t.Errorf("%s: tier %d", r.Label, r.Tier)
		}
	}
	if pv := p.Preview(); pv[0].Label != "MP3 320" {
		t.Errorf("preview starts with %q", pv[0].Label)
	}
}

func TestCompileErrors(t *testing.T) {
	if _, err := (QualityPolicy{Rank: []string{"flac", "wat"}}).Compile(); err == nil {
		t.Error("expected error")
	}
	p, err := (QualityPolicy{Rank: []string{"flac"}, AllowedExtensions: []string{"flac", "mp3"}, Description: "x"}).Compile()
	if err != nil || len(p.AllowedExtensions) != 2 || p.Description != "x" {
		t.Errorf("explicit fields overwritten: %+v %v", p, err)
	}
}
