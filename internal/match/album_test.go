package match

import "testing"

func TestAlbumQuery(t *testing.T) {
	m := NewMatcher(DefaultConfig())
	cases := []struct {
		t    Target
		want string
	}{
		{Target{Album: "THE VOID", AlbumArtist: "Nightly", Artists: []string{"Nightly"}}, "nightly the void"},
		{Target{Album: "Abbey Road (Remastered 2019)", Artists: []string{"The Beatles"}}, "the beatles abbey road"},
		{Target{Album: "Now That's What I Call Music 50", AlbumArtist: "Various Artists", AlbumType: "compilation"}, "now thats what i call music 50"},
		{Target{Album: "Hits", AlbumArtist: "Various Artists", AlbumType: "compilation"}, ""},
		{Target{Artists: []string{"X"}}, ""},
	}
	for _, c := range cases {
		if got := m.AlbumQuery(c.t); got != c.want {
			t.Errorf("AlbumQuery(%q) = %q, want %q", c.t.Album, got, c.want)
		}
	}
}

func TestAlbumKey(t *testing.T) {
	a := Target{Album: "THE VOID", AlbumArtist: "Nightly", Artists: []string{"Nightly", "Guest"}}
	b := Target{Album: "the void", Artists: []string{"Nightly"}}
	if AlbumKey(a) == "" || AlbumKey(a) != AlbumKey(b) {
		t.Fatalf("keys %q %q", AlbumKey(a), AlbumKey(b))
	}
	if AlbumKey(Target{Artists: []string{"x"}}) != "" {
		t.Fatal("no album should give empty key")
	}
}

func TestFolderKey(t *testing.T) {
	a := FolderKey(File{Username: "u", Path: `@@u\music\Nightly\THE VOID\01 - A.flac`})
	b := FolderKey(File{Username: "u", Path: `@@u\music\Nightly\THE VOID\CD2\05 - B.flac`})
	c := FolderKey(File{Username: "v", Path: `@@u\music\Nightly\THE VOID\01 - A.flac`})
	if a != b || a == c {
		t.Fatalf("keys %q %q %q", a, b, c)
	}
}

func TestFolderPreferenceWithinTier(t *testing.T) {
	m := NewMatcher(DefaultConfig())
	tg := Target{Title: "Wonderwall", Artists: []string{"Oasis"}, Album: "Morning Glory", DurationMs: 258000}
	fast := File{Username: "fast", Path: `@@f\Oasis - Wonderwall.flac`, Size: 30e6, Length: 258, FreeSlot: true, UploadSpeed: 9e6}
	album := File{Username: "album", Path: `@@a\Oasis\Morning Glory\03 - Wonderwall.flac`, Size: 30e6, Length: 258, UploadSpeed: 1e5}
	mp3 := File{Username: "mp3", Path: `@@m\Oasis\Morning Glory\03 - Wonderwall.mp3`, Size: 9e6, Length: 258, BitRate: 320}
	p := DefaultQualityPresets()["lossless-first"]

	d := m.Evaluate(tg, []File{fast, album}, p, nil)
	if d.Candidates[0].Username != "fast" {
		t.Fatalf("without preference want fast peer first, got %s", d.Candidates[0].Username)
	}
	folders := map[string]int{FolderKey(album): 5, FolderKey(mp3): 9}
	d = m.EvaluateWithFolders(tg, []File{fast, album, mp3}, p, nil, folders)
	if d.Outcome != OutcomeAuto || d.Candidates[0].Username != "album" {
		t.Fatalf("want album folder first, got %s (%s)", d.Candidates[0].Username, d.Reason)
	}
	// Quality tier still comes first: the bigger MP3 folder does not beat FLAC.
	if d.Candidates[len(d.Candidates)-1].Username != "mp3" {
		t.Fatalf("mp3 should rank last: %s", d.Candidates[len(d.Candidates)-1].Username)
	}
}

func TestFolderCoverage(t *testing.T) {
	c := func(u, p string) *Candidate { return &Candidate{File: File{Username: u, Path: p}} }
	ds := []Decision{
		{AutoCount: 2, Candidates: []*Candidate{c("a", `x\Album\01.flac`), c("b", `y\01.flac`)}},
		{AutoCount: 1, Candidates: []*Candidate{c("a", `x\Album\02.flac`), c("b", `y\02.flac`)}},
	}
	got := FolderCoverage(ds)
	if len(got) != 1 || got[FolderKey(File{Username: "a", Path: `x\Album\01.flac`})] != 2 {
		t.Fatalf("coverage %v", got)
	}
}
