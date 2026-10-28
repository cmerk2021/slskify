package match

import (
	"regexp"
	"strconv"
	"strings"
)

// Parsed is the structural breakdown of a remote Soulseek path such as
// `@@abc\Music\Artist\Album (2014)\05 - Title.flac`.
type Parsed struct {
	Path string
	// Dirs holds up to the last three parent folders, nearest last.
	Dirs []string
	// Base is the file name without extension.
	Base string
	Ext  string
	// TrackNum / DiscNum are parsed from a leading prefix; 0 = unknown.
	TrackNum int
	DiscNum  int
	// Stripped is Base without the leading track/disc prefix.
	Stripped string

	baseToks     []string // tokens of Base (includes track prefix)
	strippedToks []string // tokens of Stripped
	plainToks    []string // tokens of Stripped outside [...] / {...} brackets
	dirToks      []string // tokens of Dirs
}

var (
	discTrackRe  = regexp.MustCompile(`(?i)^(?:cd|disc|disk)\s*(\d{1,2})\s*[-_. ]\s*(\d{1,3})(?:\s*[-_.)\]]+\s*|\s+)`)
	dashedDiscRe = regexp.MustCompile(`^(\d)[-.](\d{2})(?:\s*[-_.)\]]+\s*|\s+)`)
	trackSepRe   = regexp.MustCompile(`^(\d{1,3})\s*[-_.)\]]+\s*`)
	trackSpaceRe = regexp.MustCompile(`^(\d{2,3})\s+`)
	vinylRe      = regexp.MustCompile(`^([A-Da-d])(\d{1,2})(?:\s*[-_.)\]]+\s*|\s+)`)
	bracketRe    = regexp.MustCompile(`\[[^\]]*\]|\{[^}]*\}`)
	knownExtRe   = regexp.MustCompile(`^[A-Za-z0-9]{1,5}$`)
)

// splitPath splits on both Windows and Unix separators.
func splitPath(p string) []string {
	parts := strings.FieldsFunc(p, func(r rune) bool { return r == '\\' || r == '/' })
	return parts
}

// ParsePath parses a full remote path.
func ParsePath(p string) Parsed {
	parts := splitPath(p)
	out := Parsed{Path: p}
	if len(parts) == 0 {
		return out
	}
	name := parts[len(parts)-1]
	dirs := parts[:len(parts)-1]
	// Drop the share root marker (e.g. "@@abcde") — it is noise.
	if len(dirs) > 0 && strings.HasPrefix(dirs[0], "@@") {
		dirs = dirs[1:]
	}
	if len(dirs) > 3 {
		dirs = dirs[len(dirs)-3:]
	}
	out.Dirs = append([]string(nil), dirs...)
	if i := strings.LastIndexByte(name, '.'); i > 0 && knownExtRe.MatchString(name[i+1:]) {
		out.Base, out.Ext = name[:i], strings.ToLower(name[i+1:])
	} else {
		out.Base = name
	}
	out.TrackNum, out.DiscNum, out.Stripped = stripTrackPrefix(strings.TrimSpace(out.Base))
	out.baseToks = Tokens(out.Base)
	out.strippedToks = Tokens(out.Stripped)
	out.plainToks = Tokens(bracketRe.ReplaceAllString(out.Stripped, " "))
	for _, d := range out.Dirs {
		out.dirToks = append(out.dirToks, Tokens(d)...)
	}
	return out
}

// stripTrackPrefix removes leading track numbers: "05 - ", "05.", "05_",
// "05 ", "1-05 ", "CD2-05 ", "A1 " (vinyl side). It also recognises
// "Artist - 05 - Title" where the number is the second dash segment.
func stripTrackPrefix(base string) (track, disc int, rest string) {
	s := strings.ReplaceAll(base, "_", " ")
	if m := discTrackRe.FindStringSubmatch(s); m != nil {
		disc, _ = strconv.Atoi(m[1])
		track, _ = strconv.Atoi(m[2])
		return track, disc, strings.TrimSpace(s[len(m[0]):])
	}
	if m := dashedDiscRe.FindStringSubmatch(s); m != nil {
		disc, _ = strconv.Atoi(m[1])
		track, _ = strconv.Atoi(m[2])
		return track, disc, strings.TrimSpace(s[len(m[0]):])
	}
	if m := trackSepRe.FindStringSubmatch(s); m != nil && len(m[0]) < len(s) {
		track, _ = strconv.Atoi(m[1])
		return track, 0, strings.TrimSpace(s[len(m[0]):])
	}
	if m := trackSpaceRe.FindStringSubmatch(s); m != nil && len(m[0]) < len(s) {
		track, _ = strconv.Atoi(m[1])
		return track, 0, strings.TrimSpace(s[len(m[0]):])
	}
	if m := vinylRe.FindStringSubmatch(s); m != nil && len(m[0]) < len(s) {
		return 0, 0, strings.TrimSpace(s[len(m[0]):])
	}
	// "Artist - 05 - Title"
	segs := strings.Split(s, " - ")
	if len(segs) >= 3 {
		for i := 1; i < len(segs)-1; i++ {
			seg := strings.TrimSpace(segs[i])
			if len(seg) <= 3 && isNumeric(seg) {
				track, _ = strconv.Atoi(seg)
				return track, 0, strings.Join(append(append([]string{}, segs[:i]...), segs[i+1:]...), " - ")
			}
		}
	}
	return 0, 0, s
}
