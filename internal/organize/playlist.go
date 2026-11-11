package organize

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// FlatTemplate names files in playlist output mode: no subfolders.
const FlatTemplate = "{artist} - {title}"

// playlistName makes a playlist name safe as a single file/folder name.
func playlistName(name string, limit int) string {
	if n := SanitizeName(name, limit); n != "" {
		return n
	}
	return "Playlist"
}

// PlaylistFolder is the flat folder a playlist's files go into in
// playlist output mode: <base>/<playlist name>.
func PlaylistFolder(base, name string) string {
	return filepath.Join(base, playlistName(name, maxComponentBytes))
}

// PlaylistFilePath is the playlist file for name in musicDir:
// <music>/<playlist name>.m3u8.
func PlaylistFilePath(musicDir, name string) string {
	return filepath.Join(musicDir, playlistName(name, maxComponentBytes-len(".m3u8"))+".m3u8")
}

// PlaylistEntry is one file in a playlist file.
type PlaylistEntry struct {
	Path       string // absolute path of the file
	Title      string
	Artists    []string
	DurationMs int
}

// WritePlaylist (re)writes an extended M3U playlist (UTF-8, .m3u8) at file
// listing entries in order. Paths are relative to the playlist's folder
// with "/" separators; entries whose file no longer exists are left out.
// The file is replaced atomically.
func WritePlaylist(file, name string, entries []PlaylistEntry) error {
	dir := filepath.Dir(file)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".slskify-playlist-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	w := bufio.NewWriter(tmp)
	fmt.Fprintln(w, "#EXTM3U")
	if n := oneLine(name); n != "" {
		fmt.Fprintln(w, "#PLAYLIST:"+n)
	}
	for _, e := range entries {
		if _, err := os.Stat(e.Path); err != nil {
			continue
		}
		rel, err := filepath.Rel(dir, e.Path)
		if err != nil {
			rel = e.Path
		}
		secs := -1
		if e.DurationMs > 0 {
			secs = (e.DurationMs + 500) / 1000
		}
		label := oneLine(e.Title)
		if a := oneLine(strings.Join(e.Artists, ", ")); a != "" {
			label = a + " - " + label
		}
		fmt.Fprintf(w, "#EXTINF:%d,%s\n%s\n", secs, label, filepath.ToSlash(rel))
	}
	if err := w.Flush(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), file)
}

func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
