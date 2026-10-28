package match

// Config holds every matching threshold and tolerance. All values are
// configurable from config.toml ([match] section).
type Config struct {
	// HighThreshold: minimum confidence for automatic download.
	HighThreshold int `toml:"high_threshold"`
	// LowThreshold: below this a candidate is not considered a match at all.
	LowThreshold int `toml:"low_threshold"`
	// DurationFullS: |Δduration| within this earns full duration points.
	DurationFullS float64 `toml:"duration_full_s"`
	// DurationToleranceS: points slide down to here; this is also the
	// tolerance required for auto-download.
	DurationToleranceS float64 `toml:"duration_tolerance_s"`
	// DurationHardRejectS: known durations off by more are hard-rejected.
	DurationHardRejectS float64 `toml:"duration_hard_reject_s"`
	// RequireDurationForAuto: unknown-duration candidates never auto-download.
	RequireDurationForAuto bool `toml:"require_duration_for_auto"`
	// AmbiguityMargin: competitors within this many points of the top
	// candidate that differ meaningfully force review.
	AmbiguityMargin int `toml:"ambiguity_margin"`
	// AmbiguityDurationS: durations differing more than this are "different".
	AmbiguityDurationS float64 `toml:"ambiguity_duration_s"`
	// ConfidenceSpread: quality ranking may only prefer a candidate over a
	// more confident one if it is within this many points of the best
	// auto-eligible confidence. Keeps a pristine FLAC of a doubtful match
	// from beating a near-certain MP3.
	ConfidenceSpread int `toml:"confidence_spread"`
	// ConfidenceBucket: confidences are compared in buckets of this size
	// when ranking within a quality tier (so 96 vs 97 doesn't override
	// peer quality).
	ConfidenceBucket int `toml:"confidence_bucket"`
	// MinSizeMB / MaxSizeMB hard-filter file sizes.
	MinSizeMB float64 `toml:"min_size_mb"`
	MaxSizeMB float64 `toml:"max_size_mb"`
	// VersionTokens replaces the built-in list when non-empty.
	VersionTokens []VersionToken `toml:"version_tokens,omitempty"`
}

// DefaultConfig returns the documented defaults.
func DefaultConfig() Config {
	return Config{
		HighThreshold:          80,
		LowThreshold:           50,
		DurationFullS:          2,
		DurationToleranceS:     5,
		DurationHardRejectS:    10,
		RequireDurationForAuto: true,
		AmbiguityMargin:        8,
		AmbiguityDurationS:     3,
		ConfidenceSpread:       12,
		ConfidenceBucket:       5,
		MinSizeMB:              1,
		MaxSizeMB:              300,
	}
}

func (c Config) versionTokens() []VersionToken {
	if len(c.VersionTokens) > 0 {
		return c.VersionTokens
	}
	return DefaultVersionTokens()
}

// Point budget. Title + artist + duration = 100 for a perfect, fully
// corroborated match; album and track number are small bonuses (the total
// is clamped to 100). Without a duration the maximum is 75 + bonuses, so
// unknown-duration files need album/track corroboration to reach 80 even
// when require_duration_for_auto is off.
const (
	PtsTitle       = 45.0
	PtsArtist      = 30.0
	PtsDuration    = 25.0
	PtsAlbum       = 5.0
	PtsTrackNumber = 3.0
	PenNegative    = 40.0
	PenMultiTrack  = 50.0
	// TitleMismatchCap bounds confidence when the title isn't found.
	TitleMismatchCap = 40.0
)
