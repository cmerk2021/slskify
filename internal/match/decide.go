package match

import (
	"fmt"
	"math"
	"sort"
	"strings"
)

// hardFilter returns a rejection reason, or "" if the candidate may be
// scored and ranked.
func (m *Matcher) hardFilter(c *Candidate, p QualityPolicy, durS float64) string {
	ext := fileExt(c.File)
	switch {
	case c.Locked:
		return "locked"
	case !IsAudioExt(ext):
		if ext == "" {
			return "not audio (no extension)"
		}
		return "not audio (." + ext + ")"
	case !p.Allows(c.File, c.Quality):
		return "format " + c.Quality.Format + " not allowed by policy"
	case !c.Quality.Lossless && p.MinBitrate > 0 && c.Quality.Bitrate > 0 && c.Quality.Bitrate < p.MinBitrate-bitrateSlack:
		return fmt.Sprintf("bitrate %d < %d", c.Quality.Bitrate, p.MinBitrate)
	case m.cfg.MinSizeMB > 0 && float64(c.Size) < m.cfg.MinSizeMB*1e6:
		return fmt.Sprintf("size %.1f MB < %.0f MB", float64(c.Size)/1e6, m.cfg.MinSizeMB)
	case m.cfg.MaxSizeMB > 0 && float64(c.Size) > m.cfg.MaxSizeMB*1e6:
		return fmt.Sprintf("size %.0f MB > %.0f MB", float64(c.Size)/1e6, m.cfg.MaxSizeMB)
	case c.Length > 0 && durS > 0 && math.Abs(float64(c.Length)-durS) > m.cfg.DurationHardRejectS:
		return fmt.Sprintf("duration off by %.0fs", math.Abs(float64(c.Length)-durS))
	}
	return ""
}

// RankLess is the explicit quality ranking used among candidates that
// passed the confidence gate:
//
//  1. quality tier (lower index first)
//  2. match confidence, compared in buckets of ConfidenceBucket points
//  3. in-tier quality: lossless preference (preferred depth/rate first),
//     then higher bitrate
//  4. peer: free upload slot first
//  5. peer: higher upload speed
//  6. peer: shorter queue
//  7. exact confidence, then username/path for determinism
func RankLess(a, b *Candidate, p QualityPolicy, bucket int) bool {
	if a.Tier != b.Tier {
		return a.Tier < b.Tier
	}
	if bucket <= 0 {
		bucket = 1
	}
	if ba, bb := a.Confidence/bucket, b.Confidence/bucket; ba != bb {
		return ba > bb
	}
	if la, lb := p.losslessPref(a.Quality), p.losslessPref(b.Quality); la != lb {
		return la < lb
	}
	if a.Quality.Bitrate != b.Quality.Bitrate {
		return a.Quality.Bitrate > b.Quality.Bitrate
	}
	if a.FreeSlot != b.FreeSlot {
		return a.FreeSlot
	}
	if a.UploadSpeed != b.UploadSpeed {
		return a.UploadSpeed > b.UploadSpeed
	}
	if a.QueueLength != b.QueueLength {
		return a.QueueLength < b.QueueLength
	}
	if a.Confidence != b.Confidence {
		return a.Confidence > b.Confidence
	}
	if a.Username != b.Username {
		return a.Username < b.Username
	}
	return a.Path < b.Path
}

func rankByQuality(cs []*Candidate, p QualityPolicy, bucket int) {
	sort.SliceStable(cs, func(i, j int) bool { return RankLess(cs[i], cs[j], p, bucket) })
}

func rankByConfidence(cs []*Candidate) {
	sort.SliceStable(cs, func(i, j int) bool {
		if cs[i].Confidence != cs[j].Confidence {
			return cs[i].Confidence > cs[j].Confidence
		}
		return cs[i].Key() < cs[j].Key()
	})
}

// Outcome is the per-track decision.
type Outcome string

const (
	OutcomeAuto     Outcome = "auto"
	OutcomeReview   Outcome = "review"
	OutcomeNotFound Outcome = "not_found"
)

// Decision is the result of Evaluate.
//
// Candidates is ordered for display and fallback:
//
//	[0:AutoCount)       auto-eligible, quality-ranked (download fallback order)
//	[AutoCount:Passed)  other candidates that passed hard filters: confident
//	                    ones quality-ranked, then failed ones, then
//	                    low-confidence ones by confidence
//	[Passed:]           hard-rejected candidates by confidence (context only)
type Decision struct {
	Outcome    Outcome
	Reason     string
	Candidates []*Candidate
	AutoCount  int
	Passed     int
}

// Evaluate scores, filters, ranks and decides. failed maps File.Key() to a
// download-failure reason; such candidates are never auto-selected.
func (m *Matcher) Evaluate(t Target, files []File, p QualityPolicy, failed map[string]string) Decision {
	prep := m.prepare(t)
	var passed, rejected []*Candidate
	seen := map[string]bool{}
	for _, f := range files {
		if seen[f.Key()] {
			continue
		}
		seen[f.Key()] = true
		c := m.score(prep, f)
		c.Quality = Classify(c.File)
		c.Tier, c.TierName = p.TierOf(c.Quality)
		c.Failed = failed[f.Key()]
		if r := m.hardFilter(c, p, prep.durS); r != "" {
			c.Rejected = r
			rejected = append(rejected, c)
			continue
		}
		passed = append(passed, c)
	}

	cfg := m.cfg
	durationOK := func(c *Candidate) bool {
		if c.DurationKnown() {
			return c.DurationDiff <= cfg.DurationToleranceS
		}
		return !cfg.RequireDurationForAuto
	}
	var eligible []*Candidate
	bestEl := -1
	for _, c := range passed {
		if c.Failed == "" && c.Confidence >= cfg.HighThreshold && durationOK(c) {
			eligible = append(eligible, c)
			bestEl = max(bestEl, c.Confidence)
		}
	}
	var groupA, groupB, groupF, groupC []*Candidate
	inA := map[*Candidate]bool{}
	for _, c := range eligible {
		if c.Confidence >= bestEl-cfg.ConfidenceSpread {
			c.AutoEligible = true
			groupA = append(groupA, c)
			inA[c] = true
		}
	}
	for _, c := range passed {
		switch {
		case inA[c]:
		case c.Failed != "":
			groupF = append(groupF, c)
		case c.Confidence >= cfg.LowThreshold:
			groupB = append(groupB, c)
		default:
			groupC = append(groupC, c)
		}
	}
	rankByQuality(groupA, p, cfg.ConfidenceBucket)
	rankByQuality(groupB, p, cfg.ConfidenceBucket)
	rankByQuality(groupF, p, cfg.ConfidenceBucket)
	rankByConfidence(groupC)
	rankByConfidence(rejected)

	d := Decision{AutoCount: len(groupA), Passed: len(passed)}
	d.Candidates = append(d.Candidates, groupA...)
	d.Candidates = append(d.Candidates, groupB...)
	d.Candidates = append(d.Candidates, groupF...)
	d.Candidates = append(d.Candidates, groupC...)
	d.Candidates = append(d.Candidates, rejected...)

	bestPassed := -1
	var bestC *Candidate
	for _, c := range passed {
		if c.Failed == "" && c.Confidence > bestPassed {
			bestPassed, bestC = c.Confidence, c
		}
	}
	switch {
	case len(files) == 0:
		d.Outcome, d.Reason = OutcomeNotFound, "no search results"
	case len(passed) == 0:
		d.Outcome, d.Reason = OutcomeNotFound, fmt.Sprintf("no files passed hard filters (%d rejected)", len(rejected))
	case bestC == nil:
		d.Outcome, d.Reason = OutcomeReview, "every remaining candidate failed to download"
	case bestPassed < cfg.LowThreshold:
		d.Outcome, d.Reason = OutcomeNotFound, fmt.Sprintf("best confidence %d < %d", bestPassed, cfg.LowThreshold)
	case len(groupA) == 0:
		d.Outcome = OutcomeReview
		switch {
		case bestPassed >= cfg.HighThreshold && !bestC.DurationKnown():
			d.Reason = fmt.Sprintf("best match (%d) has no duration", bestPassed)
		case bestPassed >= cfg.HighThreshold:
			d.Reason = fmt.Sprintf("best match (%d) duration off by %.0fs", bestPassed, bestC.DurationDiff)
		default:
			d.Reason = fmt.Sprintf("best confidence %d below auto threshold %d", bestPassed, cfg.HighThreshold)
		}
	default:
		top := groupA[0]
		if why, other := m.ambiguity(top, append(append([]*Candidate{}, groupA[1:]...), groupB...)); why != "" {
			top.addFlag(FlagAmbiguous)
			d.Outcome = OutcomeReview
			d.Reason = fmt.Sprintf("ambiguous: top (%d) vs %s (%d): %s", top.Confidence, shortName(other), other.Confidence, why)
		} else {
			d.Outcome, d.Reason = OutcomeAuto, fmt.Sprintf("confidence %d, %s", top.Confidence, top.Quality.Label)
		}
	}
	return d
}

// ambiguity finds a close competitor that differs meaningfully from top.
func (m *Matcher) ambiguity(top *Candidate, others []*Candidate) (string, *Candidate) {
	for _, o := range others {
		if o.Confidence < top.Confidence-m.cfg.AmbiguityMargin {
			continue
		}
		if why := m.differs(top, o); why != "" {
			return why, o
		}
	}
	return "", nil
}

// differs reports how two candidates differ meaningfully: different
// version tokens, durations more than AmbiguityDurationS apart, or the
// competitor comes from the Spotify album folder while top does not and
// their durations can't confirm they're the same recording.
func (m *Matcher) differs(a, b *Candidate) string {
	if !sameSet(a.Versions, b.Versions) {
		return fmt.Sprintf("different version tokens [%s] vs [%s]", strings.Join(a.Versions, ","), strings.Join(b.Versions, ","))
	}
	bothKnown := a.Length > 0 && b.Length > 0
	if bothKnown && math.Abs(float64(a.Length-b.Length)) > m.cfg.AmbiguityDurationS {
		return fmt.Sprintf("durations differ by %ds", abs(a.Length-b.Length))
	}
	if !a.AlbumMatch && b.AlbumMatch && (!bothKnown || abs(a.Length-b.Length) > 1) {
		return "different albums"
	}
	return ""
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

func sameSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	m := map[string]bool{}
	for _, x := range a {
		m[x] = true
	}
	for _, x := range b {
		if !m[x] {
			return false
		}
	}
	return true
}

func shortName(c *Candidate) string {
	parts := splitPath(c.Path)
	if len(parts) == 0 {
		return c.Path
	}
	return parts[len(parts)-1]
}
