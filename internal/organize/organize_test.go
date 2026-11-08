package organize

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"go.senan.xyz/taglib"

	"github.com/cmerk2021/slskify/internal/spotify"
)

func TestRelPath(t *testing.T) {
	md := Metadata{Title: "haunted house", Artists: []string{"Nightly"}, Album: "THE VOID", TrackNumber: 3, Year: 2023}
	cases := []struct {
		tmpl, ext, want string
		md              Metadata
	}{
		{DefaultTemplate, "MP3", "Nightly/THE VOID/haunted house.mp3", md},
		{"{artist}/{year} - {album}/{track} - {title}", "flac", "Nightly/2023 - THE VOID/03 - haunted house.flac", md},
		{"{album_artist}/{album}/{track} - {title}", "mp3", "A_C/Unknown Album/x_y.mp3", Metadata{Title: "x/y", Artists: []string{"A:C"}}},
		{DefaultTemplate, "mp3", "_hidden/Unknown Album/Unknown Title.mp3", Metadata{Artists: []string{".hidden"}}},
		{"{artists}/{title}", "ogg", "A, B/T.ogg", Metadata{Title: "T.", Artists: []string{"A", "B"}}},
		{DefaultTemplate, "mp3", "CON_/a/b.mp3", Metadata{Title: "b", Album: "a", AlbumArtist: "CON"}},
	}
	for _, c := range cases {
		got, err := RelPath(c.tmpl, c.md, c.ext)
		if err != nil {
			t.Fatalf("%s: %v", c.tmpl, err)
		}
		if got != filepath.FromSlash(c.want) {
			t.Errorf("RelPath(%q) = %q, want %q", c.tmpl, got, c.want)
		}
	}
	long, _ := RelPath(DefaultTemplate, Metadata{Title: strings.Repeat("é", 300), Album: "a", AlbumArtist: "b"}, "flac")
	if n := len(filepath.Base(long)); n > maxComponentBytes || !strings.HasSuffix(long, ".flac") {
		t.Errorf("long name not truncated: %d bytes", n)
	}
}

func TestValidateTemplate(t *testing.T) {
	for _, bad := range []string{"", "/abs/{title}", "{album}//{title}", "{nope}", "{title", "title}", "../{title}", "{album}/"} {
		if ValidateTemplate(bad) == nil {
			t.Errorf("ValidateTemplate(%q) = nil, want error", bad)
		}
	}
	for _, good := range []string{DefaultTemplate, "{Artist} - {TITLE}", `{album}\{title}`} {
		if err := ValidateTemplate(good); err != nil {
			t.Errorf("ValidateTemplate(%q) = %v", good, err)
		}
	}
}

func TestGenreName(t *testing.T) {
	for in, want := range map[string]string{"indie pop": "Indie Pop", "k-pop": "K-Pop", "r&b": "R&B", "uk garage": "UK Garage", "edm": "EDM"} {
		if got := GenreName(in); got != want {
			t.Errorf("GenreName(%q) = %q, want %q", in, got, want)
		}
	}
}

func write(t *testing.T, p string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestLocate(t *testing.T) {
	dl := t.TempDir()
	data := []byte("0123456789")
	remote := `@@abc\Music\Nightly - THE VOID\03. haunted house.mp3`

	// Default slskd layout.
	want := filepath.Join(dl, "Nightly - THE VOID", "03. haunted house.mp3")
	write(t, want, data)
	if got, err := Locate(dl, remote, 10, ""); err != nil || got != want {
		t.Fatalf("default layout: %q, %v", got, err)
	}
	os.Remove(want)

	// Sanitized folder name.
	want = filepath.Join(dl, "AC_DC", "x.flac")
	write(t, want, data)
	if got, err := Locate(dl, `music\AC/DC\x.flac`, 0, ""); err != nil || got != want {
		t.Fatalf("sanitized: %q, %v", got, err)
	}

	// Custom subfolder pattern + renamed to avoid a clash: found by scan,
	// matching extension and size, newest wins.
	other := filepath.Join(dl, "user", "a", "03. haunted house_639.mp3")
	write(t, other, data)
	old := filepath.Join(dl, "older", "03. haunted house.mp3")
	write(t, old, data)
	os.Chtimes(old, time.Now().Add(-time.Hour), time.Now().Add(-time.Hour))
	write(t, filepath.Join(dl, "wrongsize", "03. haunted house.mp3"), []byte("x"))
	write(t, filepath.Join(dl, "wrongext", "03. haunted house.flac"), data)
	if got, err := Locate(dl, remote, 10, ""); err != nil || got != other {
		t.Fatalf("scan: %q, %v", got, err)
	}
	// Excluded directory is skipped.
	if got, err := Locate(dl, remote, 10, filepath.Join(dl, "user")); err != nil || got != old {
		t.Fatalf("exclude: %q, %v", got, err)
	}
	if _, err := Locate(dl, `x\missing.mp3`, 10, ""); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing: %v", err)
	}
}

func TestMoveAndUnique(t *testing.T) {
	d := t.TempDir()
	src := filepath.Join(d, "a.mp3")
	write(t, src, []byte("a"))
	dst := filepath.Join(d, "b.mp3")
	write(t, dst, []byte("b"))
	u := uniquePath(dst)
	if filepath.Base(u) != "b (2).mp3" {
		t.Fatalf("uniquePath = %s", u)
	}
	if err := moveFile(src, u); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(u); string(b) != "a" {
		t.Fatalf("moved content %q", b)
	}
	if _, err := os.Stat(src); !os.IsNotExist(err) {
		t.Fatal("source still exists")
	}
}

func TestCheckDirs(t *testing.T) {
	d := t.TempDir()
	if err := CheckDirs(filepath.Join(d, "nope"), d); err == nil || !strings.Contains(err.Error(), "remote slskd") {
		t.Fatalf("missing downloads: %v", err)
	}
	f := filepath.Join(d, "file")
	write(t, f, nil)
	if err := CheckDirs(d, f); err == nil {
		t.Fatal("music file accepted")
	}
	if err := CheckDirs(d, filepath.Join(d, "new")); err != nil {
		t.Fatal(err)
	}
	if err := CheckDirs("", d); err == nil {
		t.Fatal("empty downloads accepted")
	}
}

type fakeTagger struct {
	mu   sync.Mutex
	got  map[string]Metadata
	fail error
}

func (f *fakeTagger) WriteTags(p string, md Metadata) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.got == nil {
		f.got = map[string]Metadata{}
	}
	f.got[p] = md
	return f.fail
}

type fakeSource struct {
	mu                   sync.Mutex
	track                spotify.Track
	genres               []string
	img                  []byte
	trackN, genreN, imgN int
	trackErr, genreErr   error
}

func (f *fakeSource) TrackByID(ctx context.Context, id string) (spotify.Track, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.trackN++
	return f.track, f.trackErr
}

func (f *fakeSource) ArtistGenres(ctx context.Context, id string) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.genreN++
	return f.genres, f.genreErr
}

func (f *fakeSource) Image(ctx context.Context, url string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.imgN++
	return f.img, nil
}

func TestOrganize(t *testing.T) {
	root := t.TempDir()
	dl, music := filepath.Join(root, "downloads"), filepath.Join(root, "Music")
	os.Mkdir(dl, 0o755)
	stored := spotify.Track{SpotifyID: "t1", Title: "haunted house", Artists: []string{"Nightly"}, Album: "THE VOID", TrackNumber: 3}
	src := &fakeSource{
		track:  spotify.Track{SpotifyID: "t1", Title: "x", AlbumArtist: "Nightly", ArtistIDs: []string{"a1"}, ReleaseDate: "2023-05-05", Year: 2023, TotalTracks: 10, CoverURL: "http://img"},
		genres: []string{"indie pop", "rock"}, img: []byte("JPEG"),
	}
	tg := &fakeTagger{}
	o, err := New(dl, music, "", src, tg)
	if err != nil {
		t.Fatal(err)
	}
	o.LocateTimeout, o.LocateEvery = 2*time.Second, 10*time.Millisecond

	// The file shows up shortly after slskd reports success.
	p := filepath.Join(dl, "Nightly - THE VOID", "03 Haunted House [320].MP3")
	go func() {
		time.Sleep(50 * time.Millisecond)
		write(t, p, []byte("audio"))
	}()
	res, err := o.Organize(context.Background(), stored, `@@x\Nightly - THE VOID\03 Haunted House [320].MP3`, 5)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(music, "Nightly", "THE VOID", "haunted house.mp3")
	if res.Path != want || len(res.Warnings) != 0 {
		t.Fatalf("result %+v, want %s", res, want)
	}
	if b, _ := os.ReadFile(want); string(b) != "audio" {
		t.Fatalf("content %q", b)
	}
	if _, err := os.Stat(filepath.Dir(p)); !os.IsNotExist(err) {
		t.Error("empty download folder not pruned")
	}
	if _, err := os.Stat(dl); err != nil {
		t.Error("downloads root removed")
	}
	md := tg.got[p]
	if md.Title != "haunted house" || md.Genre != "Indie Pop" || md.Date != "2023-05-05" || md.TrackTotal != 10 ||
		md.AlbumArtist != "Nightly" || !bytes.Equal(md.Cover, []byte("JPEG")) {
		t.Fatalf("metadata %+v", md)
	}

	// Second copy: unique name; fetches are cached; tag failure is a warning.
	tg.fail = errors.New("unsupported")
	write(t, filepath.Join(dl, "f", "b.mp3"), []byte("audio"))
	res, err = o.Organize(context.Background(), stored, `f\b.mp3`, 5)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(res.Path) != "haunted house (2).mp3" || len(res.Warnings) != 1 {
		t.Fatalf("second: %+v", res)
	}
	if src.trackN != 1 || src.genreN != 1 || src.imgN != 1 {
		t.Fatalf("fetch counts %d %d %d", src.trackN, src.genreN, src.imgN)
	}

	// Not found: error after the timeout, or immediately on cancel.
	o.LocateTimeout = 30 * time.Millisecond
	if _, err := o.Organize(context.Background(), stored, `x\gone.mp3`, 5); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing: %v", err)
	}
	o.LocateTimeout = time.Minute
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(20 * time.Millisecond); cancel() }()
	if _, err := o.Organize(ctx, stored, `x\gone.mp3`, 5); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel: %v", err)
	}
}

func TestOrganizeWithoutSource(t *testing.T) {
	root := t.TempDir()
	tg := &fakeTagger{}
	o, err := New(root, filepath.Join(root, "lib"), "{artist} - {title}", nil, tg)
	if err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(root, "d", "s.flac"), []byte("x"))
	res, err := o.Organize(context.Background(), spotify.Track{Title: "S", Artists: []string{"A"}}, `d\s.flac`, 0)
	if err != nil || res.Path != filepath.Join(root, "lib", "A - S.flac") {
		t.Fatalf("%+v %v", res, err)
	}
}

// mp3Fixture builds a minimal MPEG-1 Layer III stream (silent frames).
func mp3Fixture() []byte {
	frame := make([]byte, 417) // 128 kbps, 44.1 kHz
	copy(frame, []byte{0xFF, 0xFB, 0x90, 0x64})
	return bytes.Repeat(frame, 40)
}

func TestTagLibMP3(t *testing.T) {
	p := filepath.Join(t.TempDir(), "a.mp3")
	write(t, p, mp3Fixture())
	if err := taglib.WriteTags(p, map[string][]string{"COMMENT": {"ripped by xyz"}, "REPLAYGAIN_TRACK_GAIN": {"-6.5 dB"}, taglib.Title: {"wrong"}}, taglib.Clear); err != nil {
		t.Fatalf("seeding tags: %v", err)
	}
	png := append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, 32)...)
	md := Metadata{Title: "haunted house", Artists: []string{"Nightly", "Guest"}, AlbumArtist: "Nightly", Album: "THE VOID",
		TrackNumber: 3, TrackTotal: 10, DiscNumber: 1, Date: "2023-05-05", Genre: "Indie Pop", ISRC: "USX", Cover: png}
	if err := (TagLib{}).WriteTags(p, md); err != nil {
		t.Fatal(err)
	}
	tags, err := taglib.ReadTags(p)
	if err != nil {
		t.Fatal(err)
	}
	check := func(k, want string) {
		if got := strings.Join(tags[k], "|"); got != want {
			t.Errorf("%s = %q, want %q", k, got, want)
		}
	}
	check(taglib.Title, "haunted house")
	check(taglib.Artist, "Nightly, Guest")
	check(taglib.AlbumArtist, "Nightly")
	check(taglib.Album, "THE VOID")
	check(taglib.TrackNumber, "3/10")
	check(taglib.Date, "2023-05-05")
	check(taglib.Genre, "Indie Pop")
	check("REPLAYGAIN_TRACK_GAIN", "-6.5 dB")
	check("COMMENT", "")
	props, err := taglib.ReadProperties(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(props.Images) != 1 || props.Images[0].MIMEType != "image/png" {
		t.Fatalf("images %+v", props.Images)
	}
	img, err := taglib.ReadImage(p)
	if err != nil || !bytes.Equal(img, png) {
		t.Fatalf("cover not stored: %v", err)
	}
}
