package match

import (
	"regexp"
	"sort"
	"strings"
)

// discDirRe matches per-disc subfolders ("CD1", "Disc 2 - Bonus") so that
// a multi-disc album counts as one folder.
var discDirRe = regexp.MustCompile(`(?i)^(?:cd|disc|disk)\s*\d{1,2}\b`)

// AlbumKey groups tracks that belong to the same release. Tracks without an
// album return "".
func AlbumKey(t Target) string {
	album := Normalize(t.Album)
	if album == "" {
		return ""
	}
	artist := t.AlbumArtist
	if artist == "" {
		artist = t.PrimaryArtist()
	}
	return Normalize(artist) + "\x00" + album
}

// albumCore strips edition parentheticals and " - ..." suffixes:
// "Abbey Road (Remastered) - Deluxe" → "Abbey Road". Peers rarely name
// their folders after Spotify's edition labels.
func albumCore(album string) string {
	core := strings.TrimSpace(parenRe.ReplaceAllString(album, " "))
	if i := strings.Index(core, " - "); i > 0 {
		core = core[:i]
	}
	if strings.TrimSpace(core) == "" {
		core = album
	}
	return core
}

// AlbumQuery is a single search meant to find whole album folders:
// "album-artist album". Compilations and Various Artists releases search
// the album name alone, and only when it is distinctive. Returns "" when no
// useful album query exists.
func (m *Matcher) AlbumQuery(t Target) string {
	core := albumCore(t.Album)
	if Normalize(core) == "" {
		return ""
	}
	artist := t.AlbumArtist
	if artist == "" {
		artist = t.PrimaryArtist()
	}
	if strings.ToLower(t.AlbumType) == "compilation" || Normalize(artist) == "various artists" || Normalize(artist) == "" {
		if !distinctiveTitle(core) {
			return ""
		}
		return QueryText(core)
	}
	return QueryText(artist + " " + core)
}

// FolderKey identifies the peer folder a file lives in (username + parent
// folder; per-disc subfolders collapse into the album folder).
func FolderKey(f File) string {
	parts := splitPath(f.Path)
	if len(parts) < 2 {
		return f.Username + "\x00"
	}
	dirs := parts[:len(parts)-1]
	if len(dirs) > 1 && discDirRe.MatchString(strings.TrimSpace(dirs[len(dirs)-1])) {
		dirs = dirs[:len(dirs)-1]
	}
	return f.Username + "\x00" + strings.Join(dirs, "/")
}

// FolderCoverage counts, per peer folder, how many of the given decisions
// have an auto-eligible candidate in that folder. Only folders that cover
// at least two tracks are returned: they hold (part of) the album.
func FolderCoverage(ds []Decision) map[string]int {
	out := map[string]int{}
	for _, d := range ds {
		seen := map[string]bool{}
		for _, c := range d.Candidates[:d.AutoCount] {
			k := FolderKey(c.File)
			if !seen[k] {
				seen[k] = true
				out[k]++
			}
		}
	}
	for k, n := range out {
		if n < 2 {
			delete(out, k)
		}
	}
	return out
}

// rankByQualityFolders is rankByQuality, except that within one quality
// tier candidates from folders covering more of the album come first, so a
// release is fetched from as few peers/folders as possible.
func rankByQualityFolders(cs []*Candidate, p QualityPolicy, bucket int, folders map[string]int) {
	if len(folders) == 0 {
		rankByQuality(cs, p, bucket)
		return
	}
	sort.SliceStable(cs, func(i, j int) bool {
		a, b := cs[i], cs[j]
		if a.Tier == b.Tier {
			if fa, fb := folders[FolderKey(a.File)], folders[FolderKey(b.File)]; fa != fb {
				return fa > fb
			}
		}
		return RankLess(a, b, p, bucket)
	})
}
