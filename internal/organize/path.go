package organize

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"
)

// DefaultTemplate places files as <album artist>/<album>/<title>.<ext>.
const DefaultTemplate = "{album_artist}/{album}/{title}"

// placeholders maps template names to metadata values.
var placeholders = map[string]func(Metadata) string{
	"title":        func(m Metadata) string { return m.Title },
	"artist":       func(m Metadata) string { return first(m.Artists) },
	"artists":      func(m Metadata) string { return strings.Join(m.Artists, ", ") },
	"album_artist": func(m Metadata) string { return firstNonEmpty(m.AlbumArtist, first(m.Artists)) },
	"album":        func(m Metadata) string { return m.Album },
	"track":        func(m Metadata) string { return padNum(m.TrackNumber) },
	"disc":         func(m Metadata) string { return numOrEmpty(m.DiscNumber) },
	"year":         func(m Metadata) string { return numOrEmpty(m.Year) },
}

// PlaceholderNames lists the supported template placeholders.
func PlaceholderNames() []string {
	return []string{"album_artist", "artist", "artists", "album", "title", "track", "disc", "year"}
}

func first(s []string) string {
	if len(s) == 0 {
		return ""
	}
	return s[0]
}

func firstNonEmpty(vs ...string) string {
	for _, v := range vs {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func padNum(n int) string {
	if n <= 0 {
		return ""
	}
	return fmt.Sprintf("%02d", n)
}

func numOrEmpty(n int) string {
	if n <= 0 {
		return ""
	}
	return strconv.Itoa(n)
}

type segment struct {
	lit string
	ph  string // placeholder name; empty for literal text
}

// parseTemplate splits a template into path components of segments.
func parseTemplate(tmpl string) ([][]segment, error) {
	tmpl = strings.TrimSpace(strings.ReplaceAll(tmpl, `\`, "/"))
	if tmpl == "" {
		return nil, errors.New("path template is empty")
	}
	if strings.HasPrefix(tmpl, "/") || filepath.IsAbs(tmpl) {
		return nil, errors.New("path template must be relative to the music directory")
	}
	var comps [][]segment
	for _, part := range strings.Split(tmpl, "/") {
		if strings.TrimSpace(part) == "" {
			return nil, errors.New(`path template has an empty folder name ("//" or a trailing "/")`)
		}
		if t := strings.TrimSpace(part); t == "." || t == ".." {
			return nil, errors.New(`path template must not contain "." or ".." folders`)
		}
		var segs []segment
		rest := part
		for rest != "" {
			open := strings.IndexByte(rest, '{')
			end := strings.IndexByte(rest, '}')
			if end >= 0 && (open < 0 || end < open) {
				return nil, errors.New(`path template has an unmatched "}"`)
			}
			if open < 0 {
				segs = append(segs, segment{lit: rest})
				break
			}
			if end < 0 {
				return nil, errors.New(`path template has an unmatched "{"`)
			}
			if open > 0 {
				segs = append(segs, segment{lit: rest[:open]})
			}
			name := strings.ToLower(strings.TrimSpace(rest[open+1 : end]))
			if _, ok := placeholders[name]; !ok {
				return nil, fmt.Errorf("unknown placeholder {%s} in path template (use {%s})", name, strings.Join(PlaceholderNames(), "}, {"))
			}
			segs = append(segs, segment{ph: name})
			rest = rest[end+1:]
		}
		comps = append(comps, segs)
	}
	return comps, nil
}

// ValidateTemplate checks a path template.
func ValidateTemplate(tmpl string) error {
	_, err := parseTemplate(tmpl)
	return err
}

// RelPath renders the template for md and returns the path (relative to
// the music directory) with ext appended to the file name. Every component
// is sanitized so it is a safe single file or folder name.
func RelPath(tmpl string, md Metadata, ext string) (string, error) {
	comps, err := parseTemplate(tmpl)
	if err != nil {
		return "", err
	}
	ext = strings.ToLower(strings.TrimPrefix(ext, "."))
	parts := make([]string, len(comps))
	for i, segs := range comps {
		var b strings.Builder
		missing := false
		for _, s := range segs {
			if s.ph == "" {
				b.WriteString(s.lit)
				continue
			}
			v := placeholders[s.ph](md)
			if strings.TrimSpace(v) == "" {
				missing = true
			}
			b.WriteString(sanitizeValue(v))
		}
		name := b.String()
		if missing {
			// "{track} - {title}" without a track number: drop the dangling separator.
			name = strings.Trim(name, " -_.")
		}
		limit := maxComponentBytes
		if i == len(comps)-1 && ext != "" {
			limit -= len(ext) + 1
		}
		if name = SanitizeName(name, limit); name == "" {
			name = unknownFor(segs, i == len(comps)-1)
		}
		parts[i] = name
	}
	if ext != "" {
		parts[len(parts)-1] += "." + ext
	}
	return filepath.Join(parts...), nil
}

// unknownFor names a component whose placeholders were all empty.
func unknownFor(segs []segment, last bool) string {
	for _, s := range segs {
		switch s.ph {
		case "album":
			return "Unknown Album"
		case "artist", "artists", "album_artist":
			return "Unknown Artist"
		case "title":
			return "Unknown Title"
		}
	}
	if last {
		return "Unknown Title"
	}
	return "Unknown"
}

const maxComponentBytes = 200

// sanitizeValue makes a metadata value safe inside a name: path separators
// and characters that are invalid on common filesystems become "_".
func sanitizeValue(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r < 0x20 || r == 0x7f:
			return -1
		case strings.ContainsRune(`/\:*?"<>|`, r):
			return '_'
		}
		return r
	}, s)
}

var reservedNames = map[string]bool{
	"CON": true, "PRN": true, "AUX": true, "NUL": true,
	"COM1": true, "COM2": true, "COM3": true, "COM4": true, "COM5": true, "COM6": true, "COM7": true, "COM8": true, "COM9": true,
	"LPT1": true, "LPT2": true, "LPT3": true, "LPT4": true, "LPT5": true, "LPT6": true, "LPT7": true, "LPT8": true, "LPT9": true,
}

// SanitizeName turns s into a safe single path component of at most limit
// bytes: no separators or control characters, no leading dot (no hidden
// files, "." or ".."), no trailing dots/spaces, and collapsed whitespace.
func SanitizeName(s string, limit int) string {
	s = strings.Join(strings.Fields(sanitizeValue(s)), " ")
	if strings.HasPrefix(s, ".") {
		s = "_" + s[1:]
	}
	if len(s) > limit {
		cut := limit
		for cut > 0 && !utf8.RuneStart(s[cut]) {
			cut--
		}
		s = s[:cut]
	}
	s = strings.TrimRight(s, ". ")
	if reservedNames[strings.ToUpper(s)] {
		s += "_"
	}
	return s
}

// ExpandPath expands a leading "~" and makes the path absolute.
func ExpandPath(p string) (string, error) {
	p = strings.TrimSpace(p)
	if p == "" {
		return "", nil
	}
	if p == "~" || strings.HasPrefix(p, "~/") || strings.HasPrefix(p, `~\`) {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		p = filepath.Join(home, p[1:])
	}
	return filepath.Abs(p)
}

// CheckDirs verifies that downloads (slskd's download directory) exists on
// this machine and that music is usable as the library root.
func CheckDirs(downloads, music string) error {
	dl, err := ExpandPath(downloads)
	if err != nil {
		return err
	}
	mu, err := ExpandPath(music)
	if err != nil {
		return err
	}
	if dl == "" {
		return errors.New("set the slskd downloads directory")
	}
	if mu == "" {
		return errors.New("set the music directory")
	}
	fi, err := os.Stat(dl)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return fmt.Errorf("slskd downloads directory %s does not exist on this machine (organizing needs slskd to run here; turn it off for a remote slskd)", dl)
	case err != nil:
		return fmt.Errorf("slskd downloads directory: %w", err)
	case !fi.IsDir():
		return fmt.Errorf("slskd downloads directory %s is not a directory", dl)
	}
	if fi, err := os.Stat(mu); err == nil && !fi.IsDir() {
		return fmt.Errorf("music directory %s is not a directory", mu)
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("music directory: %w", err)
	}
	return nil
}
