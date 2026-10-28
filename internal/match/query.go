package match

import (
	"strings"
	"unicode"
)

// genericTitleWords make a title alone too vague to search for.
var genericTitleWords = toSet(`intro outro interlude love song track untitled home stay run go up down hello
	you me baby yeah tonight forever again dreams dream fire heart light time life world`)

// QueryText makes s safe for Soulseek search: diacritics folded,
// apostrophes dropped, punctuation and "&"/"+" removed (Soulseek search is
// token based and punctuation-hostile; a leading "-" would even exclude a
// term). Unlike Tokens, no synonyms are applied: the query must match the
// peer's literal words.
func QueryText(s string) string {
	s = Fold(s)
	s = strings.ReplaceAll(s, "'", "")
	var b strings.Builder
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsNumber(r) {
			b.WriteRune(r)
		} else {
			b.WriteByte(' ')
		}
	}
	return strings.Join(strings.Fields(b.String()), " ")
}

// queryKeepDiacritics strips punctuation but keeps original letters, for
// peers whose filenames use them (Soulseek matching is not accent-folding).
func queryKeepDiacritics(s string) string {
	s = strings.ToLower(s)
	var b strings.Builder
	for _, r := range s {
		switch {
		case r == '\'' || r == '\u2019':
		case unicode.IsLetter(r) || unicode.IsNumber(r):
			b.WriteRune(r)
		default:
			b.WriteByte(' ')
		}
	}
	return strings.Join(strings.Fields(b.String()), " ")
}

func isASCII(s string) bool {
	for _, r := range s {
		if r > unicode.MaxASCII {
			return false
		}
	}
	return true
}

// distinctiveTitle reports whether a title alone is a useful query.
func distinctiveTitle(core string) bool {
	toks := contentTokens(Tokens(core))
	if len(toks) == 0 {
		return false
	}
	n := 0
	for _, t := range toks {
		n += len(t)
	}
	if len(toks) == 1 {
		return len(toks[0]) >= 8 && !genericTitleWords[toks[0]]
	}
	generic := 0
	for _, t := range toks {
		if genericTitleWords[t] {
			generic++
		}
	}
	return n >= 10 && generic < len(toks)
}

// Queries generates search queries for t, most specific first:
//
//  1. primary artist + title
//  2. title alone (only if distinctive)
//  3. primary artist + album + title (album releases, album ≠ title)
//  4. primary artist + title with feat./version parentheticals stripped
//  5. diacritic-preserving variant of (1) for non-ASCII metadata
//
// Duplicates are removed.
func (m *Matcher) Queries(t Target) []string {
	artist := t.PrimaryArtist()
	core, _ := m.splitTitle(t.Title)
	if core == "" {
		core = t.Title
	}
	// Title with feat. removed but version suffixes kept: "Song Live".
	withVersion := featTailRe.ReplaceAllString(featParenRe.ReplaceAllString(t.Title, " "), "")

	var out []string
	seen := map[string]bool{}
	add := func(parts ...string) {
		q := QueryText(strings.Join(parts, " "))
		if q == "" || seen[q] {
			return
		}
		seen[q] = true
		out = append(out, q)
	}
	add(artist, withVersion)
	if distinctiveTitle(core) {
		add(withVersion)
	}
	at := strings.ToLower(t.AlbumType)
	if t.Album != "" && at != "single" && at != "compilation" && Normalize(t.Album) != Normalize(core) {
		add(artist, t.Album, core)
	}
	add(artist, core)
	if !isASCII(artist + t.Title) {
		q := queryKeepDiacritics(artist + " " + withVersion)
		if q != "" && !seen[q] {
			seen[q] = true
			out = append(out, q)
		}
	}
	return out
}
