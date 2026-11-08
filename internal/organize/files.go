package organize

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"
)

// maxWalkEntries bounds the fallback scan of the downloads directory.
const maxWalkEntries = 200_000

// ErrNotFound means the finished download could not be found locally.
var ErrNotFound = errors.New("downloaded file not found")

// remoteParts splits a Soulseek path (backslash separated, but peers vary)
// into its parent folder name and file name.
func remoteParts(remote string) (parent, base string) {
	parts := strings.FieldsFunc(remote, func(r rune) bool { return r == '\\' || r == '/' })
	if len(parts) == 0 {
		return "", ""
	}
	base = parts[len(parts)-1]
	if len(parts) > 1 {
		parent = parts[len(parts)-2]
	}
	return parent, base
}

// nameKey reduces a file stem to lowercase letters and digits, so names
// survive slskd's sanitizing (invalid characters → "_").
func nameKey(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func splitExt(name string) (stem, ext string) {
	ext = filepath.Ext(name)
	return strings.TrimSuffix(name, ext), strings.ToLower(strings.TrimPrefix(ext, "."))
}

// Locate finds the local copy of a finished slskd download. slskd stores a
// file as <downloads>/<remote parent folder>/<file name> by default, but
// the subfolder pattern is configurable and an existing file can make
// slskd rename the new one, so when the usual places miss, the downloads
// directory is scanned for a file with the same extension, a matching name
// and (when known) the exact size. exclude (e.g. the music directory when
// it lives inside the downloads directory) is skipped during the scan.
func Locate(downloads, remote string, size int64, exclude string) (string, error) {
	parent, base := remoteParts(remote)
	if base == "" {
		return "", fmt.Errorf("%w: empty remote path", ErrNotFound)
	}
	ok := func(p string) bool {
		fi, err := os.Stat(p)
		return err == nil && fi.Mode().IsRegular() && (size <= 0 || fi.Size() == size)
	}
	san := sanitizeValue(base)
	for _, p := range []string{
		filepath.Join(downloads, sanitizeValue(parent), san),
		filepath.Join(downloads, parent, base),
		filepath.Join(downloads, san),
	} {
		if ok(p) {
			return p, nil
		}
	}

	stem, ext := splitExt(base)
	key := nameKey(stem)
	var best string
	var bestMod time.Time
	n := 0
	err := filepath.WalkDir(downloads, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if p == downloads {
				return err
			}
			return nil // unreadable subfolder: skip it
		}
		if n++; n > maxWalkEntries {
			return fs.SkipAll
		}
		if d.IsDir() {
			if exclude != "" && p == exclude {
				return fs.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		s, e := splitExt(d.Name())
		if e != ext {
			return nil
		}
		k := nameKey(s)
		if size > 0 {
			// slskd may append a suffix to avoid overwriting an existing file.
			if key != "" && !strings.HasPrefix(k, key) {
				return nil
			}
		} else if k != key {
			return nil
		}
		fi, err := d.Info()
		if err != nil || (size > 0 && fi.Size() != size) {
			return nil
		}
		if best == "" || fi.ModTime().After(bestMod) {
			best, bestMod = p, fi.ModTime()
		}
		return nil
	})
	if err != nil {
		return "", fmt.Errorf("scanning %s: %w", downloads, err)
	}
	if best == "" {
		return "", fmt.Errorf("%w: %q is not in %s", ErrNotFound, base, downloads)
	}
	return best, nil
}

// uniquePath returns p, or "name (2).ext", "name (3).ext"… if p exists.
func uniquePath(p string) string {
	if _, err := os.Lstat(p); errors.Is(err, fs.ErrNotExist) {
		return p
	}
	dir, name := filepath.Split(p)
	ext := filepath.Ext(name)
	stem := strings.TrimSuffix(name, ext)
	for i := 2; ; i++ {
		c := filepath.Join(dir, fmt.Sprintf("%s (%d)%s", stem, i, ext))
		if _, err := os.Lstat(c); errors.Is(err, fs.ErrNotExist) {
			return c
		}
	}
}

// moveFile renames src to dst, copying across filesystems when needed.
func moveFile(src, dst string) error {
	if err := os.Rename(src, dst); err == nil {
		return nil
	}
	// Most likely a cross-device move (e.g. downloads and music on
	// different disks): copy to a temp file next to dst, then rename.
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	fi, err := in.Stat()
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(dst), ".slskify-*"+filepath.Ext(dst))
	if err != nil {
		return err
	}
	cleanup := func() { tmp.Close(); os.Remove(tmp.Name()) }
	if _, err := io.Copy(tmp, in); err != nil {
		cleanup()
		return err
	}
	if err := tmp.Sync(); err != nil {
		cleanup()
		return err
	}
	if err := tmp.Chmod(fi.Mode().Perm() | 0o600); err != nil {
		cleanup()
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	if err := os.Rename(tmp.Name(), dst); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	in.Close()
	return os.Remove(src)
}

// pruneEmptyDirs removes dir and its parents while they are empty and
// strictly inside root (root itself is never removed).
func pruneEmptyDirs(dir, root string) {
	for within(dir, root) {
		if os.Remove(dir) != nil {
			return
		}
		dir = filepath.Dir(dir)
	}
}

// within reports whether p is strictly inside root.
func within(p, root string) bool {
	rel, err := filepath.Rel(root, p)
	return err == nil && rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}
