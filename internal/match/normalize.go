// Package match decides which Soulseek file (if any) is the Spotify track.
//
// It is pure (no I/O) and split into stages:
//
//	normalize/parse → hard filters → confidence score (0-100, with a
//	breakdown) → confidence gate → quality ranking → decision.
//
// "Is this the right recording?" (confidence) and "how good is the file?"
// (quality) are deliberately kept separate and never blended into one score.
package match

import (
	"regexp"
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

// specialFolds covers letters that NFKD does not decompose into base+mark.
var specialFolds = map[rune]string{
	'ß': "ss", 'æ': "ae", 'Æ': "ae", 'ø': "o", 'Ø': "o", 'œ': "oe", 'Œ': "oe",
	'ł': "l", 'Ł': "l", 'đ': "d", 'Đ': "d", 'ð': "d", 'Ð': "d", 'þ': "th", 'Þ': "th",
	'ı': "i", 'ħ': "h", '\u2019': "'", '\u2018': "'", '`': "'", '´': "'",
}

// Fold lowercases and removes diacritics ("Beyoncé" → "beyonce",
// "Sigur Rós" → "sigur ros", "Mötley Crüe" → "motley crue").
func Fold(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range norm.NFKD.String(s) {
		if unicode.Is(unicode.Mn, r) {
			continue
		}
		if f, ok := specialFolds[r]; ok {
			b.WriteString(f)
			continue
		}
		b.WriteRune(unicode.ToLower(r))
	}
	return b.String()
}

var (
	// R.E.M. / P.O.D / U.S.A. → rem / pod / usa
	acronymRe = regexp.MustCompile(`\b(?:[a-z0-9]\.){1,}[a-z0-9]\b\.?`)
	// " + " used as "and" (Florence + the Machine)
	plusAndRe = regexp.MustCompile(`\s\+\s`)
)

// tokenSynonyms canonicalises common variants so both sides compare equal.
var tokenSynonyms = map[string]string{
	"pt": "part", "vol": "volume", "ft": "feat", "featuring": "feat", "n": "and",
	"remastered": "remaster", "remasterd": "remaster", "w": "with",
}

// Normalize returns a canonical, space-separated, punctuation-free form.
// "&" and " + " become "and", apostrophes are dropped (don't → dont),
// dotted acronyms are joined, everything else non-alphanumeric is a space.
func Normalize(s string) string {
	return strings.Join(Tokens(s), " ")
}

// Tokens returns the normalized tokens of s.
func Tokens(s string) []string {
	s = Fold(s)
	s = strings.ReplaceAll(s, "&", " and ")
	s = plusAndRe.ReplaceAllString(s, " and ")
	s = strings.ReplaceAll(s, "'", "")
	s = acronymRe.ReplaceAllStringFunc(s, func(m string) string { return strings.ReplaceAll(m, ".", "") })
	fields := strings.FieldsFunc(s, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsNumber(r) })
	for i, f := range fields {
		if syn, ok := tokenSynonyms[f]; ok {
			fields[i] = syn
		}
	}
	return fields
}

// stopwords carry no identifying information for title/artist coverage.
var stopwords = map[string]bool{
	"the": true, "a": true, "an": true, "and": true, "of": true, "at": true, "in": true,
	"on": true, "to": true, "for": true, "by": true, "with": true, "from": true, "le": true,
	"la": true, "les": true, "el": true, "los": true, "die": true, "der": true, "das": true,
	"de": true, "y": true, "et": true, "und": true, "is": true, "it": true, "my": true,
}

// contentTokens drops stopwords; if that leaves nothing (e.g. "The The"),
// the original tokens are returned.
func contentTokens(toks []string) []string {
	out := make([]string, 0, len(toks))
	for _, t := range toks {
		if !stopwords[t] {
			out = append(out, t)
		}
	}
	if len(out) == 0 {
		return toks
	}
	return out
}

func isNumeric(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// tokenEq compares tokens with tolerance for small typos on longer words.
func tokenEq(a, b string) bool {
	if a == b {
		return true
	}
	if isNumeric(a) || isNumeric(b) {
		return false
	}
	if len(a) >= 5 && len(b) >= 5 {
		return jaroWinkler(a, b) >= 0.93
	}
	return false
}

// jaroWinkler similarity in [0,1].
func jaroWinkler(s1, s2 string) float64 {
	a, b := []rune(s1), []rune(s2)
	if len(a) == 0 && len(b) == 0 {
		return 1
	}
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	window := max(len(a), len(b))/2 - 1
	if window < 0 {
		window = 0
	}
	am := make([]bool, len(a))
	bm := make([]bool, len(b))
	matches := 0
	for i := range a {
		lo, hi := max(0, i-window), min(len(b), i+window+1)
		for j := lo; j < hi; j++ {
			if !bm[j] && a[i] == b[j] {
				am[i], bm[j] = true, true
				matches++
				break
			}
		}
	}
	if matches == 0 {
		return 0
	}
	t, k := 0, 0
	for i := range a {
		if !am[i] {
			continue
		}
		for !bm[k] {
			k++
		}
		if a[i] != b[k] {
			t++
		}
		k++
	}
	m := float64(matches)
	jaro := (m/float64(len(a)) + m/float64(len(b)) + (m-float64(t)/2)/m) / 3
	prefix := 0
	for i := 0; i < min(4, len(a), len(b)) && a[i] == b[i]; i++ {
		prefix++
	}
	return jaro + float64(prefix)*0.1*(1-jaro)
}

// coverage returns the fraction of want tokens found in have, consuming
// matches so duplicates must appear as many times as wanted.
func coverage(want, have []string) float64 {
	if len(want) == 0 {
		return 0
	}
	used := make([]bool, len(have))
	hit := 0
	for _, w := range want {
		for j, h := range have {
			if !used[j] && tokenEq(w, h) {
				used[j] = true
				hit++
				break
			}
		}
	}
	return float64(hit) / float64(len(want))
}

// removeTokens returns have minus one matched occurrence of each token in
// remove.
func removeTokens(have, remove []string) []string {
	used := make([]bool, len(have))
	for _, r := range remove {
		for j, h := range have {
			if !used[j] && tokenEq(r, h) {
				used[j] = true
				break
			}
		}
	}
	out := make([]string, 0, len(have))
	for j, h := range have {
		if !used[j] {
			out = append(out, h)
		}
	}
	return out
}

func containsTok(toks []string, t string) bool {
	for _, x := range toks {
		if tokenEq(x, t) {
			return true
		}
	}
	return false
}

// countPhrase counts non-overlapping occurrences of phrase in toks.
func countPhrase(toks, phrase []string) int {
	if len(phrase) == 0 || len(phrase) > len(toks) {
		return 0
	}
	n := 0
	for i := 0; i+len(phrase) <= len(toks); {
		ok := true
		for j, p := range phrase {
			if toks[i+j] != p {
				ok = false
				break
			}
		}
		if ok {
			n++
			i += len(phrase)
		} else {
			i++
		}
	}
	return n
}
