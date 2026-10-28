package match

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// Rank rules are a compact way to write quality tiers, one string per
// tier, best first:
//
//	rank = ["mp3 320", "flac", "mp3"]
//
// Each rule is a format part followed by optional modifiers:
//
//	format:   lossless | lossy | any | a format (flac, alac, wav, aiff, ape,
//	          wv, mp3, aac, ogg, opus, wma, mpc, m4a) | several joined by "/"
//	          (e.g. "flac/alac")
//	bitrate:  a minimum in kbps: 256, 256+, >=256, ≥256, 256k, 256kbps
//	mode:     cbr | vbr
//	presets:  v0 (= vbr, ≥220 kbps), v2 (= vbr, ≥170 kbps)

var lossyFormats = []string{"mp3", "aac", "ogg", "opus", "wma", "mpc", "oga"}

var rankFormats = func() map[string]bool {
	m := toSet("m4a")
	for _, f := range losslessFormats {
		m[f] = true
	}
	for _, f := range lossyFormats {
		m[f] = true
	}
	return m
}()

// RankFormats lists the format names accepted in rank rules.
func RankFormats() []string {
	var out []string
	for f := range rankFormats {
		out = append(out, f)
	}
	sort.Strings(out)
	return out
}

// ParseRankRule parses one rank rule into a tier.
func ParseRankRule(rule string) (Tier, error) {
	fields := strings.Fields(strings.ToLower(strings.TrimSpace(rule)))
	if len(fields) == 0 {
		return Tier{}, fmt.Errorf("empty rule")
	}
	var t Tier
	switch head := fields[0]; head {
	case "lossless":
		t.Lossless = bptr(true)
	case "lossy":
		t.Lossless = bptr(false)
	case "any", "*":
	default:
		for _, f := range strings.Split(head, "/") {
			f = strings.TrimPrefix(f, ".")
			switch {
			case f == "m4a":
				t.Formats = append(t.Formats, "aac", "alac")
			case f == "aiff" || f == "aif":
				t.Formats = append(t.Formats, "aiff", "aif")
			case rankFormats[f]:
				t.Formats = append(t.Formats, f)
			default:
				return Tier{}, fmt.Errorf("%q: unknown format %q (use lossless, lossy, any or one of %s)", rule, f, strings.Join(RankFormats(), ", "))
			}
		}
	}
	t.Formats = dedupe(t.Formats)
	setMode := func(m string) error {
		if t.Mode != "" && t.Mode != m {
			return fmt.Errorf("%q: conflicting cbr/vbr", rule)
		}
		t.Mode = m
		return nil
	}
	setBitrate := func(n int) error {
		if t.MinBitrate != 0 && t.MinBitrate != n {
			return fmt.Errorf("%q: more than one bitrate", rule)
		}
		t.MinBitrate = n
		return nil
	}
	for _, f := range fields[1:] {
		var err error
		switch f {
		case "cbr", "vbr":
			err = setMode(f)
		case "v0":
			if err = setMode("vbr"); err == nil {
				err = setBitrate(220)
			}
		case "v2":
			if err = setMode("vbr"); err == nil {
				err = setBitrate(170)
			}
		default:
			n, ok := parseBitrate(f)
			if !ok {
				return Tier{}, fmt.Errorf("%q: unknown modifier %q (use a bitrate like 320, cbr, vbr, v0 or v2)", rule, f)
			}
			err = setBitrate(n)
		}
		if err != nil {
			return Tier{}, err
		}
	}
	if t.MinBitrate > 0 || t.Mode != "" {
		if t.Lossless != nil && *t.Lossless || allLossless(t.Formats) {
			return Tier{}, fmt.Errorf("%q: bitrate and cbr/vbr only apply to lossy formats", rule)
		}
	}
	t.Name = TierRule(t)
	return t, nil
}

func parseBitrate(s string) (int, bool) {
	for _, p := range []string{">=", "≥"} {
		s = strings.TrimPrefix(s, p)
	}
	for _, suf := range []string{"+", "kbps", "kb/s", "k"} {
		s = strings.TrimSuffix(s, suf)
	}
	n, err := strconv.Atoi(s)
	if err != nil || n <= 0 || n > 10000 {
		return 0, false
	}
	return n, true
}

func allLossless(formats []string) bool {
	if len(formats) == 0 {
		return false
	}
	for _, f := range formats {
		if !losslessExts[f] {
			return false
		}
	}
	return true
}

// TierRule renders a tier as a rank rule (the inverse of ParseRankRule).
func TierRule(t Tier) string {
	var parts []string
	switch {
	case len(t.Formats) > 0:
		fs := append([]string{}, t.Formats...)
		for i, f := range fs {
			fs[i] = strings.ToLower(f)
		}
		parts = append(parts, strings.Join(fs, "/"))
	case t.Lossless != nil && *t.Lossless:
		parts = append(parts, "lossless")
	case t.Lossless != nil:
		parts = append(parts, "lossy")
	default:
		parts = append(parts, "any")
	}
	switch {
	case t.Mode == "vbr" && t.MinBitrate == 220:
		parts = append(parts, "v0")
	case t.Mode == "vbr" && t.MinBitrate == 170:
		parts = append(parts, "v2")
	default:
		if t.MinBitrate > 0 {
			parts = append(parts, strconv.Itoa(t.MinBitrate))
		}
		if t.Mode != "" {
			parts = append(parts, t.Mode)
		}
	}
	return strings.Join(parts, " ")
}

// tierExtensions lists the file extensions/formats a tier can match.
func tierExtensions(t Tier) []string {
	switch {
	case len(t.Formats) > 0:
		return t.Formats
	case t.Lossless != nil && *t.Lossless:
		return losslessFormats
	case t.Lossless != nil:
		return append(append([]string{}, lossyFormats...), "m4a")
	}
	return append(append(append([]string{}, losslessFormats...), lossyFormats...), "m4a")
}

// Compile turns Rank into Tiers (and, when unset, AllowedExtensions and
// Description). Policies without Rank are returned unchanged.
func (p QualityPolicy) Compile() (QualityPolicy, error) {
	if len(p.Rank) == 0 {
		return p, nil
	}
	tiers := make([]Tier, 0, len(p.Rank))
	var exts []string
	seen := map[string]bool{}
	for _, r := range p.Rank {
		t, err := ParseRankRule(r)
		if err != nil {
			return p, err
		}
		tiers = append(tiers, t)
		for _, e := range tierExtensions(t) {
			if !seen[e] {
				seen[e] = true
				exts = append(exts, e)
			}
		}
	}
	p.Tiers = tiers
	if len(p.AllowedExtensions) == 0 {
		p.AllowedExtensions = exts
	}
	if strings.TrimSpace(p.Description) == "" {
		p.Description = p.RankSummary()
	}
	return p, nil
}

// RankRules returns the policy's tiers as rank rules.
func (p QualityPolicy) RankRules() []string {
	if len(p.Rank) > 0 {
		return append([]string{}, p.Rank...)
	}
	out := make([]string, len(p.Tiers))
	for i, t := range p.Tiers {
		out[i] = TierRule(t)
	}
	return out
}

// RankSummary renders the ranking as "a > b > c".
func (p QualityPolicy) RankSummary() string {
	rules := p.RankRules()
	for i, r := range rules {
		if t, err := ParseRankRule(r); err == nil {
			rules[i] = t.Name
		}
	}
	return strings.Join(rules, " > ")
}

func dedupe(xs []string) []string {
	if xs == nil {
		return nil
	}
	seen := map[string]bool{}
	out := xs[:0]
	for _, x := range xs {
		if !seen[x] {
			seen[x] = true
			out = append(out, x)
		}
	}
	return out
}

// PreviewRow shows how a policy treats one sample file.
type PreviewRow struct {
	Label    string
	Tier     int // -1 when rejected
	TierName string
	Rejected string
}

var previewFiles = []File{
	{Path: "a.flac", BitDepth: 16, SampleRate: 44100},
	{Path: "a.flac", BitDepth: 24, SampleRate: 96000},
	{Path: "a.m4a", BitRate: 900, BitDepth: 16, SampleRate: 44100},
	{Path: "a.wav", BitDepth: 16, SampleRate: 44100},
	{Path: "a.mp3", BitRate: 320},
	{Path: "a.mp3", BitRate: 245, VBR: true},
	{Path: "a.mp3", BitRate: 256},
	{Path: "a.mp3", BitRate: 192},
	{Path: "a.mp3", BitRate: 128},
	{Path: "a.m4a", BitRate: 256},
	{Path: "a.ogg", BitRate: 320},
	{Path: "a.opus", BitRate: 160},
}

// Preview classifies a fixed set of sample files under the policy, sorted
// best first (rejected last), to show what a ranking does.
func (p QualityPolicy) Preview() []PreviewRow {
	type row struct {
		PreviewRow
		pref, br, idx int
	}
	var rows []row
	for i, f := range previewFiles {
		q := Classify(f)
		r := row{PreviewRow: PreviewRow{Label: q.Label, Tier: -1}, idx: i, pref: p.losslessPref(q), br: q.Bitrate}
		switch {
		case !p.Allows(f, q):
			r.Rejected = "format not allowed"
		case !q.Lossless && p.MinBitrate > 0 && q.Bitrate > 0 && q.Bitrate < p.MinBitrate-bitrateSlack:
			r.Rejected = fmt.Sprintf("below %d kbps floor", p.MinBitrate)
		default:
			r.Tier, r.TierName = p.TierOf(q)
		}
		rows = append(rows, r)
	}
	sort.SliceStable(rows, func(i, j int) bool {
		a, b := rows[i], rows[j]
		if (a.Tier < 0) != (b.Tier < 0) {
			return b.Tier < 0
		}
		if a.Tier != b.Tier {
			return a.Tier < b.Tier
		}
		if a.pref != b.pref {
			return a.pref < b.pref
		}
		return a.br > b.br
	})
	out := make([]PreviewRow, len(rows))
	for i, r := range rows {
		out[i] = r.PreviewRow
	}
	return out
}
