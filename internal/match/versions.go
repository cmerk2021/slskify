package match

import "strings"

// VersionToken describes a "this is a different version" marker such as
// remix, live or karaoke. Patterns are matched as whole-token phrases on
// normalized text.
type VersionToken struct {
	Name     string   `toml:"name" json:"name"`
	Patterns []string `toml:"patterns" json:"patterns"`
	// Penalty applied when the candidate and the original disagree.
	Penalty float64 `toml:"penalty" json:"penalty"`
	// DirWeight scales the penalty when the token only appears in the
	// candidate's parent folders (e.g. "Karaoke Hits Vol. 3"). 0 → 0.5.
	DirWeight float64 `toml:"dir_weight" json:"dir_weight"`
}

// DefaultVersionTokens is the built-in, config-extensible list.
func DefaultVersionTokens() []VersionToken {
	// h: a different version of the same recording (remix, live, ...).
	// x: never the original recording (karaoke, sped up, cover, ...).
	h, x := 35.0, 50.0
	return []VersionToken{
		{Name: "remix", Patterns: []string{"remix", "remixes", "remixed", "rmx", "rework", "bootleg"}, Penalty: h},
		{Name: "live", Patterns: []string{"live", "unplugged", "in concert"}, Penalty: h},
		{Name: "acoustic", Patterns: []string{"acoustic"}, Penalty: h},
		{Name: "instrumental", Patterns: []string{"instrumental", "instrumentals", "inst", "off vocal"}, Penalty: x, DirWeight: 1},
		{Name: "karaoke", Patterns: []string{"karaoke", "backing track", "in the style of", "made famous by", "originally performed", "sing along", "singalong"}, Penalty: x, DirWeight: 1},
		{Name: "a cappella", Patterns: []string{"a cappella", "a capella", "acapella", "acappella"}, Penalty: x},
		{Name: "demo", Patterns: []string{"demo", "demos"}, Penalty: h},
		{Name: "radio edit", Patterns: []string{"radio edit", "radio version", "radio mix", "single edit"}, Penalty: h},
		{Name: "extended", Patterns: []string{"extended", "club mix", "12 inch", "12inch"}, Penalty: h},
		{Name: "sped up", Patterns: []string{"sped up", "speed up", "spedup", "nightcore"}, Penalty: x, DirWeight: 1},
		{Name: "slowed", Patterns: []string{"slowed"}, Penalty: x, DirWeight: 1},
		{Name: "reverb", Patterns: []string{"reverb"}, Penalty: h},
		{Name: "8d", Patterns: []string{"8d"}, Penalty: x},
		{Name: "cover", Patterns: []string{"cover", "covers", "covered by"}, Penalty: x, DirWeight: 0.8},
		{Name: "tribute", Patterns: []string{"tribute"}, Penalty: x, DirWeight: 1},
		{Name: "mashup", Patterns: []string{"mashup", "mash up", "medley"}, Penalty: h},
		{Name: "re-recorded", Patterns: []string{"taylors version", "re recorded", "rerecorded", "re record"}, Penalty: h},
		{Name: "remaster", Patterns: []string{"remaster"}, Penalty: 5},
		{Name: "mono", Patterns: []string{"mono"}, Penalty: 15},
		{Name: "stereo", Patterns: []string{"stereo"}, Penalty: 8},
		{Name: "clean", Patterns: []string{"clean", "clean version"}, Penalty: h},
		{Name: "explicit", Patterns: []string{"explicit", "dirty"}, Penalty: 5},
	}
}

type compiledVT struct {
	VersionToken
	phrases [][]string
}

func compileVersionTokens(vts []VersionToken) []compiledVT {
	out := make([]compiledVT, 0, len(vts))
	for _, vt := range vts {
		c := compiledVT{VersionToken: vt}
		if c.DirWeight == 0 {
			c.DirWeight = 0.5
		}
		for _, p := range vt.Patterns {
			if toks := Tokens(p); len(toks) > 0 {
				c.phrases = append(c.phrases, toks)
			}
		}
		out = append(out, c)
	}
	return out
}

// countVersions counts each version token in toks. Overlapping patterns of
// one token (e.g. "clean" and "clean version") count once per position.
func countVersions(toks []string, vts []compiledVT) map[string]int {
	out := map[string]int{}
	for _, vt := range vts {
		best := 0
		for _, ph := range vt.phrases {
			if n := countPhrase(toks, ph); n > best {
				best = n
			}
		}
		if best > 0 {
			out[vt.Name] = best
		}
	}
	return out
}

// versionVocab returns every token appearing in any version pattern.
func versionVocab(vts []compiledVT) map[string]bool {
	m := map[string]bool{}
	for _, vt := range vts {
		for _, ph := range vt.phrases {
			for _, t := range ph {
				if !stopwords[t] {
					m[t] = true
				}
			}
		}
	}
	return m
}

// removePhrase deletes every contiguous occurrence of phrase from toks.
// Used to drop artist names before version detection, so a band called
// "Live" or "Mono" doesn't look like a version marker.
func removePhrase(toks, phrase []string) []string {
	if len(phrase) == 0 {
		return toks
	}
	out := make([]string, 0, len(toks))
	for i := 0; i < len(toks); {
		if i+len(phrase) <= len(toks) {
			ok := true
			for j, p := range phrase {
				if toks[i+j] != p {
					ok = false
					break
				}
			}
			if ok {
				i += len(phrase)
				continue
			}
		}
		out = append(out, toks[i])
		i++
	}
	return out
}

func hasVersionToken(text string, vts []compiledVT) bool {
	return len(countVersions(Tokens(text), vts)) > 0
}

// neutralSuffixes are title suffixes that name the standard version.
var neutralSuffixes = []string{
	"single version", "album version", "original mix", "original version", "main version",
	"bonus track", "original", "lp version",
}

func isNeutralSuffix(s string) bool {
	n := Normalize(s)
	for _, x := range neutralSuffixes {
		if n == x {
			return true
		}
	}
	return strings.HasPrefix(n, "from ") || strings.HasPrefix(n, "theme from ") ||
		strings.Contains(n, "soundtrack") || strings.HasPrefix(n, "music from ")
}
