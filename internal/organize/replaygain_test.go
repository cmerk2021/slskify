package organize

import (
	"context"
	"path/filepath"
	"testing"

	"go.senan.xyz/taglib"
)

func TestReplayGainWithRsgain(t *testing.T) {
	if CheckRsgain() != nil {
		t.Skip("rsgain not installed")
	}
	// A leading dash must not be taken for an option.
	p := filepath.Join(t.TempDir(), "-a.mp3")
	write(t, p, mp3Fixture())
	if err := ReplayGain(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	tags, err := taglib.ReadTags(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(tags["REPLAYGAIN_TRACK_GAIN"]) == 0 || len(tags["REPLAYGAIN_ALBUM_GAIN"]) != 0 {
		t.Fatalf("tags %v", tags)
	}
	if err := ReplayGain(context.Background(), filepath.Join(t.TempDir(), "missing.mp3")); err == nil {
		t.Fatal("expected an error for a missing file")
	}
}
