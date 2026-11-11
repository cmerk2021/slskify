package organize

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/cmerk2021/slskify/internal/spotify"
)

func TestPlaylistPaths(t *testing.T) {
	if got := PlaylistFolder("/m", "My: Mix/2"); got != filepath.Join("/m", "My_ Mix_2") {
		t.Fatalf("folder %q", got)
	}
	if got := PlaylistFilePath("/m", "Road Trip"); got != filepath.Join("/m", "Road Trip.m3u8") {
		t.Fatalf("file %q", got)
	}
	if got := PlaylistFilePath("/m", " .. "); got != filepath.Join("/m", "_.m3u8") {
		t.Fatalf("unsafe name %q", got)
	}
	if got := PlaylistFolder("/m", ""); got != filepath.Join("/m", "Playlist") {
		t.Fatalf("empty name %q", got)
	}
}

func TestWritePlaylist(t *testing.T) {
	music := t.TempDir()
	a := filepath.Join(music, "Nightly", "THE VOID", "haunted house.mp3")
	b := filepath.Join(music, "Mix", "Oasis - Wonderwall.flac")
	write(t, a, []byte("x"))
	write(t, b, []byte("x"))
	file := PlaylistFilePath(music, "Mix")
	entries := []PlaylistEntry{
		{Path: b, Title: "Wonderwall", Artists: []string{"Oasis"}, DurationMs: 258_400},
		{Path: filepath.Join(music, "gone.mp3"), Title: "Gone"},
		{Path: a, Title: "haunted house", Artists: []string{"Nightly", "X"}},
	}
	if err := WritePlaylist(file, "Mix", entries); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(file)
	want := "#EXTM3U\n#PLAYLIST:Mix\n" +
		"#EXTINF:258,Oasis - Wonderwall\nMix/Oasis - Wonderwall.flac\n" +
		"#EXTINF:-1,Nightly, X - haunted house\nNightly/THE VOID/haunted house.mp3\n"
	if string(got) != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
	// Rewritten in place; no temp files left behind.
	if err := WritePlaylist(file, "Mix", entries[:1]); err != nil {
		t.Fatal(err)
	}
	ents, _ := os.ReadDir(music)
	if len(ents) != 3 {
		t.Fatalf("music dir has %d entries", len(ents))
	}
}

func TestOrganizeFlatPlaylistFolder(t *testing.T) {
	root := t.TempDir()
	music := filepath.Join(root, "Music")
	o, err := New(root, PlaylistFolder(music, "Road Trip"), FlatTemplate, nil, &fakeTagger{})
	if err != nil {
		t.Fatal(err)
	}
	o.SetLibraryDir(music)
	write(t, filepath.Join(root, "d", "Some Album", "07 x.flac"), []byte("x"))
	tr := spotify.Track{Title: "Song", Artists: []string{"A", "B"}, Album: "Some Album", AlbumArtist: "Various"}
	res, err := o.Organize(context.Background(), tr, `d\Some Album\07 x.flac`, 0)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(music, "Road Trip", "A - Song.flac"); res.Path != want {
		t.Fatalf("path %s, want %s", res.Path, want)
	}
	if o.LibraryDir() != music {
		t.Fatalf("library %s", o.LibraryDir())
	}
}
