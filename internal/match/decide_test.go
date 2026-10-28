package match

import (
	"strings"
	"testing"
)

func file(user, path string, length, bitrate int) File {
	f := File{Username: user, Path: path, Length: length, BitRate: bitrate, Size: 9_000_000, FreeSlot: true, UploadSpeed: 1_000_000}
	if strings.HasSuffix(path, ".flac") {
		f.Size = 30_000_000
		f.BitRate = 0
		f.BitDepth, f.SampleRate = 16, 44100
	}
	return f
}

func evaluate(t *testing.T, cfg Config, tg Target, files []File, failed map[string]string) Decision {
	t.Helper()
	m := NewMatcher(cfg)
	d := m.Evaluate(tg, files, DefaultQualityPresets()["lossless-first"], failed)
	if t.Failed() || testing.Verbose() {
		for i, c := range d.Candidates {
			t.Logf("#%d %s conf=%d tier=%s rej=%q fail=%q\n%s", i+1, c.Path, c.Confidence, c.TierName, c.Rejected, c.Failed, c.Explain("    "))
		}
	}
	return d
}

func TestWrongVersionFlacLosesToCorrectMP3(t *testing.T) {
	files := []File{
		file("a", `@@a\Queen - Bohemian Rhapsody (Live at Wembley).flac`, 357, 0),
		file("b", `@@b\Queen - Bohemian Rhapsody (Instrumental).flac`, 355, 0),
		file("c", `@@c\Queen\A Night at the Opera\11 - Bohemian Rhapsody.mp3`, 355, 320),
	}
	d := evaluate(t, DefaultConfig(), tBohemian, files, nil)
	if d.Outcome != OutcomeAuto || d.Candidates[0].Username != "c" {
		t.Fatalf("want auto c, got %s %s (%s)", d.Outcome, d.Candidates[0].Username, d.Reason)
	}
}

func TestDoubtfulFlacDoesNotBeatCertainMP3(t *testing.T) {
	// The FLAC clears the high threshold but is far less certain than the
	// MP3 (no artist anywhere); confidence_spread keeps it out of the
	// auto group, so quality can't override confidence.
	files := []File{
		file("flac", `@@a\misc\Bohemian Rhapsody.flac`, 355, 0),
		file("mp3", `@@c\Queen\A Night at the Opera\11 - Bohemian Rhapsody.mp3`, 355, 320),
	}
	d := evaluate(t, DefaultConfig(), tBohemian, files, nil)
	if d.Candidates[0].Username != "mp3" {
		t.Fatalf("want mp3 first, got %s", d.Candidates[0].Username)
	}
}

func TestLosslessPreferredAmongConfident(t *testing.T) {
	files := []File{
		file("mp3", `@@c\Queen\A Night at the Opera\11 - Bohemian Rhapsody.mp3`, 355, 320),
		file("flac", `@@a\Queen\A Night at the Opera (1975)\11. Bohemian Rhapsody.flac`, 355, 0),
	}
	d := evaluate(t, DefaultConfig(), tBohemian, files, nil)
	if d.Outcome != OutcomeAuto || d.Candidates[0].Username != "flac" || d.AutoCount != 2 {
		t.Fatalf("want auto flac (2 eligible), got %s %s autocount=%d", d.Outcome, d.Candidates[0].Username, d.AutoCount)
	}
}

func TestUnknownDurationNeverAutoSelects(t *testing.T) {
	files := []File{
		file("a", `@@a\Queen\A Night at the Opera\11 - Bohemian Rhapsody.flac`, 0, 0),
	}
	cfg := DefaultConfig()
	d := evaluate(t, cfg, tBohemian, files, nil)
	if d.Outcome != OutcomeReview {
		t.Fatalf("want review, got %s (%s)", d.Outcome, d.Reason)
	}
	// Even a perfect-looking path with a lowered threshold stays in review.
	cfg.HighThreshold = 60
	d = evaluate(t, cfg, tBohemian, files, nil)
	if d.Outcome != OutcomeReview || d.AutoCount != 0 {
		t.Fatalf("lowered threshold: want review, got %s", d.Outcome)
	}
	// Turning the toggle off allows it once corroborated enough.
	cfg.RequireDurationForAuto = false
	d = evaluate(t, cfg, tBohemian, files, nil)
	if d.Outcome != OutcomeAuto {
		t.Fatalf("toggle off: want auto, got %s (%s)", d.Outcome, d.Reason)
	}
}

func TestUnknownDurationCandidateLosesToKnownOne(t *testing.T) {
	files := []File{
		file("nolen", `@@a\Queen\A Night at the Opera\11 - Bohemian Rhapsody.flac`, 0, 0),
		file("len", `@@c\Queen - Bohemian Rhapsody.mp3`, 355, 320),
	}
	d := evaluate(t, DefaultConfig(), tBohemian, files, nil)
	if d.Outcome != OutcomeAuto || d.Candidates[0].Username != "len" {
		t.Fatalf("want auto len, got %s %s", d.Outcome, d.Candidates[0].Username)
	}
}

func TestDurationOutsideToleranceGoesToReview(t *testing.T) {
	files := []File{file("a", `@@a\Queen\A Night at the Opera\11 - Bohemian Rhapsody.mp3`, 362, 320)}
	d := evaluate(t, DefaultConfig(), tBohemian, files, nil)
	if d.Outcome == OutcomeAuto {
		t.Fatalf("Δ7s must not auto: %s", d.Reason)
	}
}

func TestAmbiguityRouting(t *testing.T) {
	t.Run("different versions close in confidence", func(t *testing.T) {
		// Spotify title has no version, top is plain, competitor is a
		// "remaster" (mild penalty keeps it close).
		files := []File{
			file("plain", `@@a\Queen - Bohemian Rhapsody.mp3`, 355, 320),
			file("remaster", `@@b\Queen - Bohemian Rhapsody (2011 Remaster).flac`, 355, 0),
		}
		d := evaluate(t, DefaultConfig(), tBohemian, files, nil)
		if d.Outcome != OutcomeReview || !strings.Contains(d.Reason, "ambiguous") {
			t.Fatalf("want ambiguous review, got %s (%s)", d.Outcome, d.Reason)
		}
	})
	t.Run("durations differ", func(t *testing.T) {
		files := []File{
			file("a", `@@a\Oasis - Wonderwall.flac`, 259, 0),
			file("b", `@@b\Oasis - Wonderwall.mp3`, 255, 320),
		}
		d := evaluate(t, DefaultConfig(), tWonder, files, nil)
		if d.Outcome != OutcomeReview || !strings.Contains(d.Reason, "durations differ") {
			t.Fatalf("want ambiguous review, got %s (%s)", d.Outcome, d.Reason)
		}
	})
	t.Run("identical copies are not ambiguous", func(t *testing.T) {
		files := []File{
			file("a", `@@a\Oasis\Morning Glory\03 - Wonderwall.flac`, 258, 0),
			file("b", `@@b\Oasis\(What's The Story) Morning Glory\03 Wonderwall.mp3`, 259, 320),
			file("c", `@@c\Oasis - Wonderwall.mp3`, 258, 320),
		}
		d := evaluate(t, DefaultConfig(), tWonder, files, nil)
		if d.Outcome != OutcomeAuto {
			t.Fatalf("want auto, got %s (%s)", d.Outcome, d.Reason)
		}
	})
	t.Run("clear winner over distant different version", func(t *testing.T) {
		files := []File{
			file("a", `@@a\Oasis - Wonderwall.mp3`, 258, 320),
			file("b", `@@b\Oasis - Wonderwall (Live).mp3`, 258, 320),
		}
		d := evaluate(t, DefaultConfig(), tWonder, files, nil)
		if d.Outcome != OutcomeAuto || d.Candidates[0].Username != "a" {
			t.Fatalf("want auto a, got %s (%s)", d.Outcome, d.Reason)
		}
	})
}

func TestNotFound(t *testing.T) {
	d := evaluate(t, DefaultConfig(), tWonder, nil, nil)
	if d.Outcome != OutcomeNotFound {
		t.Fatal("empty → not found")
	}
	files := []File{
		file("a", `@@a\Oasis\Morning Glory\02 - Roll With It.mp3`, 255, 320),
		file("b", `@@b\Oasis - Wonderwall.mp3`, 258, 96), // below floor
		file("c", `@@c\Oasis - Wonderwall.jpg`, 0, 0),
	}
	d = evaluate(t, DefaultConfig(), tWonder, files, nil)
	if d.Outcome != OutcomeNotFound {
		t.Fatalf("want not found, got %s (%s)", d.Outcome, d.Reason)
	}
	// Rejected candidates are kept for context, after passing ones.
	if len(d.Candidates) != 3 || d.Passed != 1 || d.Candidates[1].Rejected == "" {
		t.Fatalf("rejected candidates should be kept: passed=%d", d.Passed)
	}
}

func TestMidConfidenceGoesToReview(t *testing.T) {
	files := []File{file("a", `@@a\misc\Wonderwall (Live at Maine Road).mp3`, 258, 320)}
	d := evaluate(t, DefaultConfig(), tWonder, files, nil)
	if d.Outcome == OutcomeAuto {
		t.Fatalf("live version must not auto: %s", d.Reason)
	}
}

func TestFailedCandidatesNeverAuto(t *testing.T) {
	a := file("a", `@@a\Oasis\Morning Glory\03 - Wonderwall.flac`, 258, 0)
	b := file("b", `@@b\Oasis - Wonderwall.mp3`, 258, 320)
	d := evaluate(t, DefaultConfig(), tWonder, []File{a, b}, map[string]string{a.Key(): "rejected"})
	if d.Outcome != OutcomeAuto || d.Candidates[0].Username != "b" {
		t.Fatalf("want auto b, got %s %s", d.Outcome, d.Candidates[0].Username)
	}
	if d.Candidates[1].Failed != "rejected" {
		t.Fatal("failed candidate should be kept with reason")
	}
	d = evaluate(t, DefaultConfig(), tWonder, []File{a, b}, map[string]string{a.Key(): "x", b.Key(): "y"})
	if d.Outcome != OutcomeReview {
		t.Fatalf("all failed → review, got %s", d.Outcome)
	}
}

func TestDedupe(t *testing.T) {
	f := file("a", `@@a\Oasis - Wonderwall.mp3`, 258, 320)
	d := evaluate(t, DefaultConfig(), tWonder, []File{f, f}, nil)
	if len(d.Candidates) != 1 {
		t.Fatalf("want 1 candidate, got %d", len(d.Candidates))
	}
}

func TestQueries(t *testing.T) {
	m := NewMatcher(DefaultConfig())
	cases := []struct {
		t    Target
		want []string
	}{
		{tBohemian, []string{"queen bohemian rhapsody", "bohemian rhapsody", "queen a night at the opera bohemian rhapsody"}},
		{tHurt, []string{"johnny cash hurt", "johnny cash american iv the man comes around hurt"}},
		{tCrazy, []string{"beyonce crazy in love", "beyonce dangerously in love crazy in love", "beyoncé crazy in love"}},
		{tRemix, []string{"sia titanium david guetta remix", "titanium david guetta remix", "sia titanium remixes titanium", "sia titanium"}},
		{Target{Title: "Don't Stop Me Now - Remastered 2011", Artists: []string{"Queen"}, AlbumType: "single", Album: "x"},
			[]string{"queen dont stop me now remastered 2011", "dont stop me now remastered 2011", "queen dont stop me now"}},
	}
	for _, c := range cases {
		got := m.Queries(c.t)
		if strings.Join(got, "|") != strings.Join(c.want, "|") {
			t.Errorf("%s:\n got  %q\n want %q", c.t.Title, got, c.want)
		}
	}
}
