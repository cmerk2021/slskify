package match

import (
	"fmt"
	"strings"
)

// QualityInfo describes a file's audio quality as far as search metadata
// allows. Spoofed/upscaled files cannot be detected from this.
type QualityInfo struct {
	Format           string `json:"format"`
	Lossless         bool   `json:"lossless"`
	Bitrate          int    `json:"bitrate,omitempty"` // kbps (average for VBR)
	BitrateEstimated bool   `json:"bitrate_estimated,omitempty"`
	VBR              bool   `json:"vbr,omitempty"`
	BitDepth         int    `json:"bit_depth,omitempty"`
	SampleRate       int    `json:"sample_rate,omitempty"`
	Label            string `json:"label"`
}

var losslessExts = toSet("flac wav aiff aif alac ape wv dsf dff")
var lossyExts = toSet("mp3 aac ogg opus wma mpc m4a mp4 oga")

// IsAudioExt reports whether ext is a known audio extension.
func IsAudioExt(ext string) bool {
	ext = strings.ToLower(strings.TrimPrefix(ext, "."))
	return losslessExts[ext] || lossyExts[ext]
}

func fileExt(f File) string {
	ext := strings.ToLower(strings.TrimPrefix(f.Ext, "."))
	if ext == "" {
		ext = ParsePath(f.Path).Ext
	}
	return ext
}

// m4aIsLossless guesses whether an .m4a holds ALAC or AAC. The container
// is the same, and search metadata has no codec field, so we rely on the
// reported bitrate (AAC tops out at 320-512 kbps, ALAC is typically
// 600-1400+) or a reported bit depth (only set for lossless files).
func m4aIsLossless(f File) bool {
	return f.BitRate > 500 || f.BitDepth > 0
}

// Classify derives QualityInfo from search metadata.
func Classify(f File) QualityInfo {
	ext := fileExt(f)
	q := QualityInfo{Format: ext, BitDepth: f.BitDepth, SampleRate: f.SampleRate, VBR: f.VBR}
	switch {
	case ext == "m4a" && m4aIsLossless(f):
		q.Format, q.Lossless = "alac", true
	case ext == "m4a":
		q.Format = "aac"
	case losslessExts[ext]:
		q.Lossless = true
	}
	q.Bitrate = f.BitRate
	if !q.Lossless && q.Bitrate <= 0 && f.Length > 0 && f.Size > 0 {
		q.Bitrate = int(f.Size * 8 / int64(f.Length) / 1000)
		q.BitrateEstimated = true
	}
	name := strings.ToUpper(q.Format)
	switch {
	case q.Lossless && q.BitDepth > 0 && q.SampleRate > 0:
		q.Label = fmt.Sprintf("%s %d/%s", name, q.BitDepth, khz(q.SampleRate))
	case q.Lossless:
		q.Label = name
	case q.Bitrate <= 0:
		q.Label = name + " ?kbps"
	case q.VBR:
		q.Label = fmt.Sprintf("%s VBR ~%d", name, q.Bitrate)
	case q.BitrateEstimated:
		q.Label = fmt.Sprintf("%s ~%d", name, q.Bitrate)
	default:
		q.Label = fmt.Sprintf("%s %d", name, q.Bitrate)
	}
	return q
}

func khz(hz int) string {
	if hz%1000 == 0 {
		return fmt.Sprintf("%d", hz/1000)
	}
	return fmt.Sprintf("%.1f", float64(hz)/1000)
}

// Tier is one rung of a quality policy. A file belongs to the first tier
// whose every set condition holds.
type Tier struct {
	Name string `toml:"name" json:"name"`
	// Lossless, when set, requires lossless (true) or lossy (false).
	Lossless *bool `toml:"lossless,omitempty" json:"lossless,omitempty"`
	// Formats restricts to these formats (flac, alac, mp3, aac, ...).
	Formats []string `toml:"formats,omitempty" json:"formats,omitempty"`
	// MinBitrate (kbps) for lossy files; unknown bitrates never satisfy it.
	MinBitrate int `toml:"min_bitrate,omitempty" json:"min_bitrate,omitempty"`
	// Mode is "", "cbr" or "vbr".
	Mode string `toml:"mode,omitempty" json:"mode,omitempty"`
}

// QualityPolicy is a named preset: ordered tiers plus knobs.
type QualityPolicy struct {
	Description       string   `toml:"description" json:"description"`
	AllowedExtensions []string `toml:"allowed_extensions" json:"allowed_extensions"`
	// MinBitrate is the lossy floor (kbps); known bitrates below it are
	// hard-rejected.
	MinBitrate int `toml:"min_bitrate" json:"min_bitrate"`
	// PreferBitDepth / PreferSampleRate: preferred lossless format
	// (default 16-bit / 44.1 kHz ranks above hi-res).
	PreferBitDepth   int `toml:"prefer_bit_depth" json:"prefer_bit_depth"`
	PreferSampleRate int `toml:"prefer_sample_rate" json:"prefer_sample_rate"`
	// Rank is the compact form of Tiers (see ParseRankRule), best first,
	// e.g. ["mp3 320", "flac", "mp3"]. When set, Compile replaces Tiers
	// with it and derives AllowedExtensions if those are empty.
	Rank  []string `toml:"rank,omitempty" json:"rank,omitempty"`
	Tiers []Tier   `toml:"tiers" json:"tiers"`
}

func bptr(b bool) *bool { return &b }

// bitrateSlack tolerates peers reporting 319/318 for 320 kbps CBR.
const bitrateSlack = 2

// Allows reports whether the policy accepts the file's format.
func (p QualityPolicy) Allows(f File, q QualityInfo) bool {
	ext := fileExt(f)
	for _, a := range p.AllowedExtensions {
		a = strings.ToLower(strings.TrimPrefix(a, "."))
		if a == q.Format || (a == ext && ext != "m4a") || (a == "m4a" && ext == "m4a") {
			return true
		}
	}
	return false
}

// TierOf returns the tier index (lower is better) and its name. Files
// matching no tier get len(Tiers) and "other".
func (p QualityPolicy) TierOf(q QualityInfo) (int, string) {
	for i, t := range p.Tiers {
		if t.Lossless != nil && *t.Lossless != q.Lossless {
			continue
		}
		if len(t.Formats) > 0 {
			ok := false
			for _, f := range t.Formats {
				if strings.EqualFold(f, q.Format) {
					ok = true
				}
			}
			if !ok {
				continue
			}
		}
		if t.MinBitrate > 0 && !q.Lossless && (q.Bitrate <= 0 || q.Bitrate < t.MinBitrate-bitrateSlack) {
			continue
		}
		if t.Mode == "cbr" && q.VBR || t.Mode == "vbr" && !q.VBR {
			continue
		}
		return i, t.Name
	}
	return len(p.Tiers), "other"
}

// losslessPref ranks lossless files within a tier: 0 = preferred
// depth/rate, 1 = unknown, 2 = higher resolution, 3 = lower resolution.
func (p QualityPolicy) losslessPref(q QualityInfo) int {
	if !q.Lossless {
		return 0
	}
	if q.BitDepth <= 0 && q.SampleRate <= 0 {
		return 1
	}
	pd, pr := p.PreferBitDepth, p.PreferSampleRate
	if pd <= 0 {
		pd = 16
	}
	if pr <= 0 {
		pr = 44100
	}
	d, r := q.BitDepth, q.SampleRate
	if (d == 0 || d == pd) && (r == 0 || r == pr) {
		return 0
	}
	if (d != 0 && d < pd) || (r != 0 && r < pr) {
		return 3
	}
	return 2
}

var losslessFormats = []string{"flac", "alac", "wav", "aiff", "aif", "ape", "wv"}

// DefaultQualityPresets returns the built-in presets.
func DefaultQualityPresets() map[string]QualityPolicy {
	lossless := Tier{Name: "lossless", Lossless: bptr(true)}
	return map[string]QualityPolicy{
		"lossless-first": {
			Description:       "FLAC/ALAC/WAV/AIFF, then MP3 320 > V0 > 256 > ≥192",
			AllowedExtensions: append(append([]string{}, losslessFormats...), "mp3"),
			MinBitrate:        192, PreferBitDepth: 16, PreferSampleRate: 44100,
			Tiers: []Tier{
				lossless,
				{Name: "mp3 320 cbr", Formats: []string{"mp3"}, MinBitrate: 320, Mode: "cbr"},
				{Name: "mp3 v0/vbr high", Formats: []string{"mp3"}, MinBitrate: 220, Mode: "vbr"},
				{Name: "mp3 256", Formats: []string{"mp3"}, MinBitrate: 256},
				{Name: "lossy ≥ floor", Lossless: bptr(false)},
			},
		},
		"lossless-only": {
			Description:       "Only FLAC/ALAC/WAV/AIFF; 16/44.1 preferred",
			AllowedExtensions: append([]string{}, losslessFormats...),
			PreferBitDepth:    16, PreferSampleRate: 44100,
			Tiers: []Tier{lossless},
		},
		"mp3-best": {
			Description:       "MP3 only: 320 CBR > V0 > 256 > ≥192",
			AllowedExtensions: []string{"mp3"},
			MinBitrate:        192,
			Tiers: []Tier{
				{Name: "mp3 320 cbr", Formats: []string{"mp3"}, MinBitrate: 320, Mode: "cbr"},
				{Name: "mp3 v0/vbr high", Formats: []string{"mp3"}, MinBitrate: 220, Mode: "vbr"},
				{Name: "mp3 256", Formats: []string{"mp3"}, MinBitrate: 256},
				{Name: "mp3 ≥ floor", Formats: []string{"mp3"}},
			},
		},
		"any-best": {
			Description:       "Any format: lossless > lossy, then by bitrate (≥128)",
			AllowedExtensions: append(append([]string{}, losslessFormats...), "mp3", "m4a", "aac", "ogg", "opus"),
			MinBitrate:        128, PreferBitDepth: 16, PreferSampleRate: 44100,
			Tiers: []Tier{
				lossless,
				{Name: "lossy ≥300", Lossless: bptr(false), MinBitrate: 300},
				{Name: "lossy ≥240", Lossless: bptr(false), MinBitrate: 240},
				{Name: "lossy ≥190", Lossless: bptr(false), MinBitrate: 190},
				{Name: "lossy ≥160", Lossless: bptr(false), MinBitrate: 160},
				{Name: "lossy", Lossless: bptr(false)},
			},
		},
	}
}
