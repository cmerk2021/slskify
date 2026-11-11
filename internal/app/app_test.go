package app

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cmerk2021/slskify/internal/config"
	"github.com/cmerk2021/slskify/internal/spotify"
	"github.com/cmerk2021/slskify/internal/store"
)

func TestRunOutputSettings(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	dl, music := filepath.Join(root, "dl"), filepath.Join(root, "Music")
	os.Mkdir(dl, 0o755)
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	cfg := config.Default()
	a := New(cfg, filepath.Join(root, "c.toml"), st)

	coll := &spotify.Collection{Kind: spotify.KindPlaylist, Name: "Road Trip", Tracks: []spotify.Track{{Title: "x"}}}
	run, err := a.CreateRun(ctx, coll, RunSettings{Quality: "lossless-first", Speed: "medium", Output: store.OutputPlaylist, PlaylistFile: true})
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := st.GetRun(ctx, run.ID); got.Output != store.OutputPlaylist || !got.PlaylistFile {
		t.Fatalf("stored %+v", got)
	}
	if _, err := a.NewRunner(ctx, run); err == nil || !strings.Contains(err.Error(), "organizing") {
		t.Fatalf("playlist output without organizing: %v", err)
	}

	cfg.Organize = config.OrganizeConfig{Enabled: true, DownloadsDir: dl, MusicDir: music}
	org, err := a.OrganizerFor(run)
	if err != nil {
		t.Fatal(err)
	}
	if org.MusicDir() != filepath.Join(music, "Road Trip") || org.LibraryDir() != music {
		t.Fatalf("playlist organizer dirs %s %s", org.MusicDir(), org.LibraryDir())
	}
	cfg.Organize.PlaylistDir = filepath.Join(root, "Lists")
	if org, _ = a.OrganizerFor(run); org.MusicDir() != filepath.Join(root, "Lists", "Road Trip") {
		t.Fatalf("playlist_dir ignored: %s", org.MusicDir())
	}
	run.Output = store.OutputLibrary
	if org, _ = a.OrganizerFor(run); org.MusicDir() != music {
		t.Fatalf("library organizer %s", org.MusicDir())
	}
	if _, err := a.NewRunner(ctx, run); err != nil {
		t.Fatal(err)
	}
}
