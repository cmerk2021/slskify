package match

// Target is the Spotify track we are looking for (decoupled from the
// spotify package so the matcher stays pure).
type Target struct {
	Title       string
	Artists     []string
	Album       string
	AlbumArtist string
	AlbumType   string // album, single, compilation
	TrackNumber int
	DiscNumber  int
	DurationMs  int
	Explicit    bool
	Year        int
}

// PrimaryArtist returns the first artist.
func (t Target) PrimaryArtist() string {
	if len(t.Artists) == 0 {
		return ""
	}
	return t.Artists[0]
}

// File is one remote file as reported by a slskd search, flattened with
// its peer's info. Numeric fields use 0 for "unknown".
type File struct {
	Username    string `json:"username"`
	Path        string `json:"path"`
	Size        int64  `json:"size"`
	Ext         string `json:"ext,omitempty"`
	BitRate     int    `json:"bitrate,omitempty"`     // kbps
	SampleRate  int    `json:"sample_rate,omitempty"` // Hz
	BitDepth    int    `json:"bit_depth,omitempty"`
	Length      int    `json:"length,omitempty"` // seconds
	VBR         bool   `json:"vbr,omitempty"`
	VBRKnown    bool   `json:"vbr_known,omitempty"`
	Locked      bool   `json:"locked,omitempty"`
	FreeSlot    bool   `json:"free_slot,omitempty"`
	UploadSpeed int64  `json:"upload_speed,omitempty"` // bytes/s
	QueueLength int64  `json:"queue_length,omitempty"`
}

// Key uniquely identifies a remote file.
func (f File) Key() string { return f.Username + "\x00" + f.Path }

// Signal is one line of a score breakdown.
type Signal struct {
	Name   string  `json:"name"`
	Points float64 `json:"points"`
	Note   string  `json:"note,omitempty"`
}

// Candidate flags.
const (
	FlagDurationUnknown   = "duration_unknown"
	FlagDurationEstimated = "duration_estimated"
	FlagVersionMismatch   = "version_mismatch"
	FlagTitleMismatch     = "title_mismatch"
	FlagTitleExtraWords   = "title_extra_words"
	FlagArtistMissing     = "artist_missing"
	FlagNegative          = "negative_signal"
	FlagMultiTrack        = "multi_track"
	FlagBitrateUnknown    = "bitrate_unknown"
	FlagAmbiguous         = "ambiguous"
)

// Candidate is a scored file. Confidence answers "is this the recording?";
// Quality/Tier answer "how good is the file?". They are never combined.
type Candidate struct {
	File
	Confidence int      `json:"confidence"`
	Breakdown  []Signal `json:"breakdown"`
	Flags      []string `json:"flags,omitempty"`
	// Versions lists version tokens detected on the candidate.
	Versions []string `json:"versions,omitempty"`
	// DurationDiff is |candidate - target| in seconds (valid if Length>0).
	DurationDiff float64     `json:"duration_diff,omitempty"`
	AlbumMatch   bool        `json:"album_match,omitempty"`
	Quality      QualityInfo `json:"quality"`
	Tier         int         `json:"tier"`
	TierName     string      `json:"tier_name"`
	// AutoEligible is set by Evaluate for candidates in the auto group.
	AutoEligible bool `json:"auto_eligible,omitempty"`
	// Rejected is the hard-filter reason ("" if it passed).
	Rejected string `json:"rejected,omitempty"`
	// Failed is a download failure reason recorded by the pipeline.
	Failed string `json:"failed,omitempty"`
}

// HasFlag reports whether the candidate carries flag f.
func (c *Candidate) HasFlag(f string) bool {
	for _, x := range c.Flags {
		if x == f {
			return true
		}
	}
	return false
}

func (c *Candidate) addFlag(f string) {
	if !c.HasFlag(f) {
		c.Flags = append(c.Flags, f)
	}
}

func (c *Candidate) add(name string, pts float64, note string) {
	c.Breakdown = append(c.Breakdown, Signal{Name: name, Points: pts, Note: note})
}

// DurationKnown reports whether the peer reported a length.
func (c *Candidate) DurationKnown() bool { return c.Length > 0 }
