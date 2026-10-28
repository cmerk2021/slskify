package match

import (
	"fmt"
	"math"
	"regexp"
	"strings"
)

// Matcher scores files against targets. Create once per config.
type Matcher struct {
	cfg   Config
	vts   []compiledVT
	vocab map[string]bool
}

// NewMatcher compiles the configuration.
func NewMatcher(cfg Config) *Matcher {
	vts := compileVersionTokens(cfg.versionTokens())
	return &Matcher{cfg: cfg, vts: vts, vocab: versionVocab(vts)}
}

// Config returns the matcher's configuration.
func (m *Matcher) Config() Config { return m.cfg }

var (
	featParenRe = regexp.MustCompile(`(?i)\s*[\(\[]\s*(?:feat\.?|ft\.?|featuring|with)\s[^\)\]]*[\)\]]`)
	featTailRe  = regexp.MustCompile(`(?i)\s+(?:feat\.?|ft\.?|featuring)\s.*$`)
	parenRe     = regexp.MustCompile(`[\(\[]([^\(\)\[\]]*)[\)\]]`)
	trackishRe  = regexp.MustCompile(`^(?:[a-d]\d{1,2}|cd\d{1,2}|disc\d{1,2}|disk\d{1,2}|\d+(?:st|nd|rd|th)|\d+k|\d+kbps|\d+bit|\d+khz|v\d)$`)
)

// noiseWords commonly appear in filenames without identifying the song.
var noiseWords = toSet(`official audio video music lyric lyrics hq hd hi res hires flac mp3 wav aac m4a alac ogg opus
	kbps kbit cbr vbr web webrip cd cdrip rip vinyl lp ep single album version original mix edit remaster bonus track
	disc disk side digital deluxe edition expanded feat prod produced records recordings bit khz ost soundtrack theme
	release promo www com net org free download final full visualizer explicit clean dirty uncensored censored radio
	clip audiotrack topic vevo mastered master quality high best`)

func toSet(s string) map[string]bool {
	m := map[string]bool{}
	for _, f := range strings.Fields(s) {
		m[f] = true
	}
	return m
}

type prepared struct {
	t                Target
	coreAll          []string // all tokens of the core title
	coreContent      []string // core title without stopwords
	extraToks        []string // distinctive words of version suffixes (remixer, venue)
	fullToks         []string
	artistToks       [][]string // content tokens per credited artist
	artistRaw        [][]string // raw tokens per artist (for phrase removal)
	albumArtistToks  []string
	albumContent     []string
	albumIsTitle     bool
	titleVers        map[string]int // version tokens anywhere in title
	ctxVers          map[string]int // version tokens in version suffixes only
	albumVers        map[string]int
	explained        map[string]bool
	durS             float64
	titleNeg, multiQ bool
}

// splitTitle separates the core title from version suffixes. "Song (Live)",
// "Song - 2011 Remaster" and "Song (X Remix)" have suffixes; "(Don't Fear)
// The Reaper" keeps its parenthetical because it has no version token.
// Featuring credits and neutral suffixes ("Single Version", "From 'Film'")
// are dropped.
func (m *Matcher) splitTitle(title string) (core string, suffixes []string) {
	s := featParenRe.ReplaceAllString(title, " ")
	s = featTailRe.ReplaceAllString(s, "")
	if i := strings.Index(s, " - "); i > 0 {
		head, tail := s[:i], s[i+3:]
		switch {
		case hasVersionToken(tail, m.vts):
			suffixes = append(suffixes, tail)
			s = head
		case isNeutralSuffix(tail):
			s = head
		}
	}
	s = parenRe.ReplaceAllStringFunc(s, func(p string) string {
		inner := parenRe.FindStringSubmatch(p)[1]
		switch {
		case hasVersionToken(inner, m.vts):
			suffixes = append(suffixes, inner)
			return " "
		case isNeutralSuffix(inner):
			return " "
		default:
			return " " + inner + " "
		}
	})
	return strings.TrimSpace(s), suffixes
}

func (m *Matcher) prepare(t Target) *prepared {
	p := &prepared{t: t, durS: float64(t.DurationMs) / 1000, explained: map[string]bool{}}
	core, suffixes := m.splitTitle(t.Title)
	p.coreAll = Tokens(core)
	if len(p.coreAll) == 0 {
		p.coreAll = Tokens(t.Title)
	}
	p.coreContent = contentTokens(p.coreAll)
	p.fullToks = Tokens(t.Title)
	p.titleVers = countVersions(p.fullToks, m.vts)
	p.ctxVers = map[string]int{}
	for _, s := range suffixes {
		toks := Tokens(s)
		for k, v := range countVersions(toks, m.vts) {
			p.ctxVers[k] += v
		}
		for _, tok := range contentTokens(toks) {
			if !m.vocab[tok] && !isNumeric(tok) && !noiseWords[tok] && !stopwords[tok] && !containsTok(p.coreAll, tok) {
				p.extraToks = append(p.extraToks, tok)
			}
		}
	}
	for _, a := range t.Artists {
		raw := Tokens(a)
		if len(raw) == 0 {
			continue
		}
		p.artistRaw = append(p.artistRaw, raw)
		p.artistToks = append(p.artistToks, contentTokens(raw))
		for _, tok := range raw {
			p.explained[tok] = true
		}
	}
	// Featured artists hidden in the title, e.g. "Song (feat. X)".
	for _, tok := range p.fullToks {
		p.explained[tok] = true
	}
	if aa := Tokens(t.AlbumArtist); len(aa) > 0 && Normalize(t.AlbumArtist) != "various artists" {
		p.albumArtistToks = contentTokens(aa)
		for _, tok := range aa {
			p.explained[tok] = true
		}
	}
	albumCore := strings.TrimSpace(parenRe.ReplaceAllString(t.Album, " "))
	if i := strings.Index(albumCore, " - "); i > 0 {
		albumCore = albumCore[:i]
	}
	if albumCore == "" {
		albumCore = t.Album
	}
	p.albumContent = contentTokens(Tokens(albumCore))
	p.albumIsTitle = Normalize(albumCore) == Normalize(core)
	albumToks := Tokens(t.Album)
	p.albumVers = countVersions(albumToks, m.vts)
	for _, tok := range albumToks {
		p.explained[tok] = true
	}
	for tok := range m.vocab {
		p.explained[tok] = true
	}
	p.titleNeg = hasAny(p.fullToks, negativeWords)
	return p
}

var negativeWords = [][]string{{"sample"}, {"samples"}, {"preview"}, {"previews"}, {"snippet"}, {"excerpt"}, {"teaser"}, {"ringtone"}, {"ringtones"}}
var multiTrackPhrases = [][]string{{"full", "album"}, {"complete", "album"}, {"full", "lp"}, {"full", "ep"}, {"album", "mix"},
	{"continuous", "mix"}, {"megamix"}, {"dj", "mix"}, {"mixed", "by"}, {"full", "concert"}, {"full", "set"}}

func hasAny(toks []string, phrases [][]string) bool {
	for _, ph := range phrases {
		if countPhrase(toks, ph) > 0 {
			return true
		}
	}
	return false
}

// Score computes the match confidence of f for t. Hard filters are not
// applied here (see Evaluate); this is pure "is it the recording?" scoring.
func (m *Matcher) Score(t Target, f File) *Candidate {
	return m.score(m.prepare(t), f)
}

func (m *Matcher) score(p *prepared, f File) *Candidate {
	c := &Candidate{File: f}
	pp := ParsePath(f.Path)
	if c.Ext == "" {
		c.Ext = pp.Ext
	}
	c.Ext = strings.ToLower(strings.TrimPrefix(c.Ext, "."))

	m.scoreTitle(p, pp, c)
	m.scoreArtist(p, pp, c)
	m.scoreDuration(p, c)
	m.scoreAlbum(p, pp, c)
	m.scoreTrackNumber(p, pp, c)
	m.scoreVersions(p, pp, c)
	m.scoreNegatives(p, pp, c)

	total := 0.0
	for _, s := range c.Breakdown {
		total += s.Points
	}
	// Without the title there is no match, however well artist and
	// duration line up (same artist, similar length is common).
	if c.HasFlag(FlagTitleMismatch) && total > TitleMismatchCap {
		c.add("title-gate", -round1(total-TitleMismatchCap), fmt.Sprintf("capped at %.0f: title not found", TitleMismatchCap))
		total = TitleMismatchCap
	}
	c.Confidence = int(math.Round(math.Max(0, math.Min(100, total))))
	return c
}

func (m *Matcher) scoreTitle(p *prepared, pp Parsed, c *Candidate) {
	cov := math.Max(coverage(p.coreContent, pp.strippedToks), coverage(p.coreContent, pp.baseToks))
	if cov < 0.5 {
		c.add("title", 0, fmt.Sprintf("only %.0f%% of title words found", cov*100))
		c.addFlag(FlagTitleMismatch)
		return
	}
	pts := PtsTitle * cov * cov
	notes := []string{fmt.Sprintf("%.0f%% of title words", cov*100)}
	if len(p.extraToks) > 0 {
		ec := coverage(p.extraToks, append(append([]string{}, pp.strippedToks...), pp.dirToks...))
		pts *= 0.6 + 0.4*ec
		if ec < 1 {
			notes = append(notes, fmt.Sprintf("version detail %q %.0f%% found", strings.Join(p.extraToks, " "), ec*100))
		}
	}
	dirSet := map[string]bool{}
	for _, d := range pp.dirToks {
		dirSet[d] = true
	}
	var unexplained []string
	for _, tok := range removeTokens(pp.plainToks, p.coreAll) {
		if p.explained[tok] || dirSet[tok] || stopwords[tok] || noiseWords[tok] || isNumeric(tok) ||
			len([]rune(tok)) == 1 || trackishRe.MatchString(tok) {
			continue
		}
		unexplained = append(unexplained, tok)
	}
	if n := len(unexplained); n > 0 {
		// Extra words matter more for short titles: "Hello Goodbye" is a
		// different song from "Hello"; one stray word next to a five-word
		// title is more likely an uploader tag.
		ratio := float64(n) / float64(max(1, len(p.coreAll)))
		pts *= 1 / (1 + 0.7*float64(n) + 0.8*ratio)
		notes = append(notes, "unexpected words: "+strings.Join(unexplained, " "))
		c.addFlag(FlagTitleExtraWords)
	}
	c.add("title", round1(pts), strings.Join(notes, "; "))
}

func (m *Matcher) scoreArtist(p *prepared, pp Parsed, c *Candidate) {
	fileRest := removeTokens(pp.strippedToks, p.coreAll)
	best, note := 0.0, ""
	consider := func(toks []string, weight float64, who string) {
		if len(toks) == 0 {
			return
		}
		fc, dc := coverage(toks, fileRest), coverage(toks, pp.dirToks)
		var pts float64
		var where string
		switch {
		case fc == 1:
			pts, where = PtsArtist*weight, "in filename"
		case dc == 1:
			pts, where = PtsArtist*0.9*weight, "in folder"
		case math.Max(fc, dc) >= 0.5:
			pts, where = PtsArtist*0.5*math.Max(fc, dc)*weight, "partially"
		}
		if pts > best {
			best, note = pts, who+" "+where
		}
	}
	for i, at := range p.artistToks {
		w, who := 1.0, "primary artist"
		if i > 0 {
			w, who = 0.6, "featured artist"
		}
		consider(at, w, who)
	}
	consider(p.albumArtistToks, 0.8, "album artist")
	if best == 0 {
		c.addFlag(FlagArtistMissing)
		note = "no credited artist found"
	}
	c.add("artist", round1(best), note)
}

func (m *Matcher) scoreDuration(p *prepared, c *Candidate) {
	if c.Length <= 0 || p.durS <= 0 {
		c.addFlag(FlagDurationUnknown)
		c.add("duration", 0, "unknown (no bonus)")
		return
	}
	d := math.Abs(float64(c.Length) - p.durS)
	c.DurationDiff = d
	full, tol, hard := m.cfg.DurationFullS, m.cfg.DurationToleranceS, m.cfg.DurationHardRejectS
	var pts float64
	switch {
	case d <= full:
		pts = PtsDuration
	case d <= tol:
		pts = PtsDuration - (d-full)/(tol-full)*(PtsDuration-10)
	case d <= hard:
		pts = 10 - (d-tol)/(hard-tol)*20
	default:
		pts = -10
	}
	c.add("duration", round1(pts), fmt.Sprintf("Δ%.0fs", d))
}

func (m *Matcher) scoreAlbum(p *prepared, pp Parsed, c *Candidate) {
	if len(p.albumContent) == 0 || p.albumIsTitle || len(pp.dirToks) == 0 {
		return
	}
	cov := coverage(p.albumContent, pp.dirToks)
	switch {
	case cov >= 0.8:
		c.AlbumMatch = true
		c.add("album", PtsAlbum, "folder matches album")
	case cov >= 0.5:
		c.add("album", 2, "folder partially matches album")
	}
}

func (m *Matcher) scoreTrackNumber(p *prepared, pp Parsed, c *Candidate) {
	if pp.TrackNum <= 0 || p.t.TrackNumber <= 0 {
		return
	}
	if pp.TrackNum == p.t.TrackNumber {
		c.add("track#", PtsTrackNumber, fmt.Sprintf("%02d matches", pp.TrackNum))
	} else {
		c.add("track#", 0, fmt.Sprintf("%02d ≠ %02d (no penalty)", pp.TrackNum, p.t.TrackNumber))
	}
}

// scoreVersions compares version tokens. Counting (rather than presence)
// handles titles that contain a token as a real word: "Live Forever" has
// one "live"; "Live Forever (Live)" has two.
func (m *Matcher) scoreVersions(p *prepared, pp Parsed, c *Candidate) {
	fileToks := pp.strippedToks
	dirToks := pp.dirToks
	for _, a := range p.artistRaw {
		fileToks = removePhrase(fileToks, a)
		dirToks = removePhrase(dirToks, a)
	}
	fileV := countVersions(fileToks, m.vts)
	dirV := countVersions(dirToks, m.vts)
	for _, vt := range m.vts {
		name := vt.Name
		orig, ctx, inAlbum := p.titleVers[name], p.ctxVers[name], p.albumVers[name] > 0
		cf, cd := fileV[name], dirV[name]
		if cf > 0 || cd > 0 {
			c.Versions = append(c.Versions, name)
		}
		pen := vt.Penalty
		switch name {
		case "clean":
			if !p.t.Explicit {
				pen *= 0.25
			}
		case "explicit":
			if p.t.Explicit {
				pen = 0
			}
		}
		switch {
		case cf > orig && !inAlbum:
			if pen > 0 {
				c.add("version", -pen, fmt.Sprintf("candidate is %q, original is not", name))
				if pen >= 15 {
					c.addFlag(FlagVersionMismatch)
				}
			}
		case cf == 0 && cd > 0 && orig == 0 && !inAlbum:
			if dp := pen * vt.DirWeight; dp > 0 {
				c.add("version", -round1(dp), fmt.Sprintf("folder suggests %q", name))
				if dp >= 15 {
					c.addFlag(FlagVersionMismatch)
				}
			}
		case ctx > 0 && cf < orig && cd == 0 && name != "explicit":
			c.add("version", -vt.Penalty, fmt.Sprintf("original is %q, candidate is not", name))
			if vt.Penalty >= 15 {
				c.addFlag(FlagVersionMismatch)
			}
		}
	}
}

func (m *Matcher) scoreNegatives(p *prepared, pp Parsed, c *Candidate) {
	if !p.titleNeg && (hasAny(pp.baseToks, negativeWords) || hasAny(pp.dirToks, negativeWords)) {
		c.add("negative", -PenNegative, "sample/preview/snippet")
		c.addFlag(FlagNegative)
	}
	if hasAny(pp.baseToks, multiTrackPhrases) && !hasAny(p.fullToks, multiTrackPhrases) {
		c.add("multi-track", -PenMultiTrack, "looks like a full album or mix")
		c.addFlag(FlagMultiTrack)
	}
	if c.Ext == "cue" {
		c.add("multi-track", -PenMultiTrack, "cue sheet")
		c.addFlag(FlagMultiTrack)
	}
	if c.Length > 0 || p.durS <= 0 {
		return
	}
	// Without a reported length, estimate duration from size and bitrate to
	// catch full-album rips and clips. Informational only: an estimate never
	// makes a candidate auto-eligible.
	est, ok := estimateDuration(c.File)
	if !ok {
		return
	}
	c.addFlag(FlagDurationEstimated)
	switch {
	case est > 2*p.durS+60:
		c.add("multi-track", -PenMultiTrack, fmt.Sprintf("estimated %.0fs vs %.0fs expected", est, p.durS))
		c.addFlag(FlagMultiTrack)
	case !Classify(c.File).Lossless && est < 0.4*p.durS:
		c.add("negative", -30, fmt.Sprintf("estimated %.0fs, far shorter than %.0fs", est, p.durS))
		c.addFlag(FlagNegative)
	}
}

// estimateDuration estimates seconds from size. For lossy files with a
// bitrate this is close for CBR. For lossless, the PCM rate is an upper
// bound on bitrate, so the result is a lower bound on duration.
func estimateDuration(f File) (float64, bool) {
	if f.Size <= 0 {
		return 0, false
	}
	q := Classify(f)
	if q.Lossless {
		depth, rate := f.BitDepth, f.SampleRate
		if depth <= 0 {
			depth = 16
		}
		if rate <= 0 {
			rate = 44100
		}
		bps := float64(depth * rate * 2)
		return float64(f.Size*8) / bps, true
	}
	if f.BitRate > 0 {
		return float64(f.Size*8) / float64(f.BitRate*1000), true
	}
	return 0, false
}

func round1(f float64) float64 { return math.Round(f*10) / 10 }
