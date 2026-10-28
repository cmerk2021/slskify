package match

import (
	"reflect"
	"testing"
)

func TestFold(t *testing.T) {
	cases := map[string]string{
		"Beyoncé":     "beyonce",
		"Sigur Rós":   "sigur ros",
		"Mötley Crüe": "motley crue",
		"Straße":      "strasse",
		"Ærøskøbing":  "aeroskobing",
		"Björk":       "bjork",
		"Don’t Stop":  "don't stop",
		"ＦＵＬＬＷＩＤＴＨ":   "fullwidth",
	}
	for in, want := range cases {
		if got := Fold(in); got != want {
			t.Errorf("Fold(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestNormalize(t *testing.T) {
	cases := map[string]string{
		"Simon & Garfunkel":           "simon and garfunkel",
		"Florence + the Machine":      "florence and the machine",
		"Guns N' Roses":               "guns and roses",
		"Don't Stop Me Now":           "dont stop me now",
		"R.E.M.":                      "rem",
		"Pt. 2":                       "part 2",
		"Hello,   World!!":            "hello world",
		"Artist_-_Title_(Radio_Edit)": "artist title radio edit",
		"ft. Somebody":                "feat somebody",
		"2011 Remastered Version":     "2011 remaster version",
	}
	for in, want := range cases {
		if got := Normalize(in); got != want {
			t.Errorf("Normalize(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestJaroWinkler(t *testing.T) {
	if s := jaroWinkler("martha", "marhta"); s < 0.95 || s > 0.97 {
		t.Errorf("martha/marhta = %v", s)
	}
	if !tokenEq("beautiful", "beautifull") {
		t.Error("beautiful ≈ beautifull")
	}
	if tokenEq("2011", "2012") {
		t.Error("numbers must match exactly")
	}
	if tokenEq("love", "live") {
		t.Error("short tokens must match exactly")
	}
}

func TestParsePath(t *testing.T) {
	cases := []struct {
		path     string
		dirs     []string
		stripped string
		ext      string
		track    int
		disc     int
	}{
		{`@@abc\Music\Artist\Album (2014)\05 - Title.flac`, []string{"Music", "Artist", "Album (2014)"}, "Title", "flac", 5, 0},
		{`Artist - Title (Live at X) [320].mp3`, nil, "Artist - Title (Live at X) [320]", "mp3", 0, 0},
		{`share/01. title.mp3`, []string{"share"}, "title", "mp3", 1, 0},
		{`x\Artist_-_Title_(Radio_Edit).mp3`, []string{"x"}, "Artist - Title (Radio Edit)", "mp3", 0, 0},
		{`d\1-05 Song Name.flac`, []string{"d"}, "Song Name", "flac", 5, 1},
		{`d\CD2-07 - Song.flac`, []string{"d"}, "Song", "flac", 7, 2},
		{`d\A1 Song.flac`, []string{"d"}, "Song", "flac", 0, 0},
		{`d\Artist - 03 - Song.mp3`, []string{"d"}, "Artist - Song", "mp3", 3, 0},
		{`d\07 Song.mp3`, []string{"d"}, "Song", "mp3", 7, 0},
		{`d\99 Problems.mp3`, []string{"d"}, "Problems", "mp3", 99, 0},
		{`d\1979.mp3`, []string{"d"}, "1979", "mp3", 0, 0},
		{`a/b/c/d/e/f/03) Song.ogg`, []string{"d", "e", "f"}, "Song", "ogg", 3, 0},
		{`d\Song.Name.Without.Ext`, []string{"d"}, "Song.Name.Without", "ext", 0, 0},
	}
	for _, c := range cases {
		p := ParsePath(c.path)
		if !reflect.DeepEqual(p.Dirs, c.dirs) && !(len(p.Dirs) == 0 && len(c.dirs) == 0) {
			t.Errorf("%s: dirs %q want %q", c.path, p.Dirs, c.dirs)
		}
		if p.Stripped != c.stripped || p.Ext != c.ext || p.TrackNum != c.track || p.DiscNum != c.disc {
			t.Errorf("%s: got stripped=%q ext=%q track=%d disc=%d", c.path, p.Stripped, p.Ext, p.TrackNum, p.DiscNum)
		}
	}
}

func TestCoverage(t *testing.T) {
	if c := coverage([]string{"bohemian", "rhapsody"}, Tokens("Queen - Bohemian Rhapsody")); c != 1 {
		t.Errorf("full coverage = %v", c)
	}
	if c := coverage([]string{"bohemian", "rhapsody"}, Tokens("Queen - Rhapsody in Blue")); c != 0.5 {
		t.Errorf("half coverage = %v", c)
	}
}
