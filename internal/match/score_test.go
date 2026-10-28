package match

import (
	"strings"
	"testing"
)

func tgt(title string, artists []string, album string, track int, durS int) Target {
	return Target{Title: title, Artists: artists, Album: album, AlbumType: "album", TrackNumber: track, DurationMs: durS * 1000}
}

var (
	tBohemian = tgt("Bohemian Rhapsody", []string{"Queen"}, "A Night at the Opera", 11, 355)
	tWonder   = tgt("Wonderwall", []string{"Oasis"}, "(What's The Story) Morning Glory?", 3, 258)
	tLiveFor  = tgt("Live Forever", []string{"Oasis"}, "Definitely Maybe", 3, 276)
	tCrazy    = tgt("Crazy In Love (feat. Jay-Z)", []string{"Beyoncé", "JAY-Z"}, "Dangerously In Love", 1, 236)
	tHurt     = tgt("Hurt", []string{"Johnny Cash"}, "American IV: The Man Comes Around", 2, 218)
	tBlind    = tgt("Blinding Lights", []string{"The Weeknd"}, "After Hours", 9, 200)
	tSounds   = tgt("The Sound of Silence", []string{"Simon & Garfunkel"}, "Sounds of Silence", 1, 185)
	tHoppi    = tgt("Hoppípolla", []string{"Sigur Rós"}, "Takk...", 2, 268)
	tLiveSong = tgt("Wish You Were Here - Live at Wembley", []string{"Pink Floyd"}, "Live Collection", 4, 330)
	tRemix    = tgt("Titanium (David Guetta Remix)", []string{"Sia"}, "Titanium Remixes", 2, 245)
	tRadio    = tgt("Levels - Radio Edit", []string{"Avicii"}, "Levels", 1, 199)
)

type scoreCase struct {
	name     string
	t        Target
	path     string
	length   int
	min, max int
	flags    []string // must be present
	noFlags  []string // must be absent
}

func TestScore(t *testing.T) {
	m := NewMatcher(DefaultConfig())
	cases := []scoreCase{
		// --- correct matches, messy paths ---
		{name: "classic album folder", t: tBohemian, path: `@@abc\Music\Queen\A Night at the Opera (1975)\11 - Bohemian Rhapsody.flac`, length: 355, min: 95},
		{name: "artist - title, no folder info", t: tBohemian, path: `@@x\Downloads\Queen - Bohemian Rhapsody.mp3`, length: 356, min: 90},
		{name: "track-number only filename", t: tBohemian, path: `@@x\Queen\A Night At The Opera\11. bohemian rhapsody.mp3`, length: 354, min: 90},
		{name: "underscores", t: tWonder, path: `@@q\mp3\Oasis_-_Wonderwall.mp3`, length: 258, min: 90},
		{name: "bitrate tag in brackets", t: tWonder, path: `@@q\Oasis - Wonderwall [320].mp3`, length: 258, min: 90, noFlags: []string{FlagTitleExtraWords}},
		{name: "unknown duration caps confidence", t: tWonder, path: `@@q\Oasis\Morning Glory\03 - Wonderwall.mp3`, length: 0, min: 75, max: 85, flags: []string{FlagDurationUnknown}},
		{name: "diacritics folded", t: tHoppi, path: `@@s\Sigur Ros\Takk\02 - Hoppipolla.flac`, length: 268, min: 95},
		{name: "diacritics kept", t: tHoppi, path: `@@s\Sigur Rós\Takk...\02 Hoppípolla.flac`, length: 268, min: 95},
		{name: "ampersand vs and", t: tSounds, path: `@@s\Simon and Garfunkel - The Sound of Silence.mp3`, length: 186, min: 90},
		{name: "the prefix missing", t: tBlind, path: `@@s\Weeknd\After Hours\09 Blinding Lights.flac`, length: 200, min: 85},
		{name: "featured artist in filename", t: tCrazy, path: `@@s\Beyonce - Crazy In Love (ft. Jay-Z).mp3`, length: 236, min: 90},
		{name: "featured artist missing in filename", t: tCrazy, path: `@@s\Beyonce\Dangerously in Love\01 Crazy in Love.mp3`, length: 236, min: 90},
		{name: "artist only in folder", t: tHurt, path: `@@s\Music\Johnny Cash\American IV\02 - Hurt.mp3`, length: 218, min: 90},
		{name: "remaster mild penalty", t: tBohemian, path: `@@s\Queen - Bohemian Rhapsody (2011 Remaster).mp3`, length: 355, min: 85, max: 96, noFlags: []string{FlagVersionMismatch}},
		{name: "duration off 4s slides", t: tWonder, path: `@@s\Oasis - Wonderwall.mp3`, length: 262, min: 80, max: 95},
		{name: "track number mismatch no penalty", t: tHurt, path: `@@s\Best Of Johnny Cash\14 - Hurt.mp3`, length: 218, min: 90},

		// --- titles that contain version tokens ---
		{name: "title contains live (real song)", t: tLiveFor, path: `@@s\Oasis\Definitely Maybe\03 - Live Forever.flac`, length: 276, min: 95, noFlags: []string{FlagVersionMismatch}},
		{name: "live version of song containing live", t: tLiveFor, path: `@@s\Oasis - Live Forever (Live at Knebworth).mp3`, length: 280, max: 65, flags: []string{FlagVersionMismatch}},
		{name: "spotify title is live, file is live", t: tLiveSong, path: `@@s\Pink Floyd\Live Collection\04 - Wish You Were Here (Live at Wembley).flac`, length: 330, min: 90, noFlags: []string{FlagVersionMismatch}},
		{name: "spotify title is live, file is studio", t: tLiveSong, path: `@@s\Pink Floyd\Wish You Were Here\05 - Wish You Were Here.flac`, length: 334, max: 70, flags: []string{FlagVersionMismatch}},
		{name: "spotify title is remix, file is remix", t: tRemix, path: `@@s\Sia - Titanium (David Guetta Remix).mp3`, length: 245, min: 90},
		{name: "spotify title is remix, file is different remix", t: tRemix, path: `@@s\Sia - Titanium (Alesso Remix).mp3`, length: 245, max: 79},
		{name: "spotify radio edit, file radio edit underscores", t: tRadio, path: `@@s\Avicii_-_Levels_(Radio_Edit).mp3`, length: 199, min: 90},
		{name: "spotify radio edit, file original", t: tRadio, path: `@@s\Avicii - Levels.mp3`, length: 199, max: 70, flags: []string{FlagVersionMismatch}},

		// --- wrong versions must lose ---
		{name: "remix", t: tBlind, path: `@@s\The Weeknd - Blinding Lights (Chromatics Remix).mp3`, length: 202, max: 65, flags: []string{FlagVersionMismatch}},
		{name: "karaoke", t: tWonder, path: `@@s\Karaoke Hits\Wonderwall (In the Style of Oasis) [Karaoke Version].mp3`, length: 258, max: 50, flags: []string{FlagVersionMismatch}},
		{name: "karaoke folder only", t: tWonder, path: `@@s\Karaoke Hits Vol 3\07 - Oasis - Wonderwall.mp3`, length: 258, max: 65, flags: []string{FlagVersionMismatch}},
		{name: "instrumental", t: tBohemian, path: `@@s\Queen - Bohemian Rhapsody (Instrumental).flac`, length: 355, max: 65, flags: []string{FlagVersionMismatch}},
		{name: "live", t: tWonder, path: `@@s\Oasis - Wonderwall (Live at Maine Road).mp3`, length: 258, max: 65, flags: []string{FlagVersionMismatch}},
		{name: "sped up", t: tBlind, path: `@@s\The Weeknd - Blinding Lights (Sped Up).mp3`, length: 160, max: 50},
		{name: "acoustic", t: tWonder, path: `@@s\Oasis - Wonderwall (Acoustic).mp3`, length: 258, max: 65, flags: []string{FlagVersionMismatch}},
		{name: "cover by other artist", t: tWonder, path: `@@s\Ryan Adams - Wonderwall.mp3`, length: 250, max: 65, flags: []string{FlagArtistMissing}},
		{name: "live album folder", t: tWonder, path: `@@s\Oasis\Familiar to Millions (Live)\Wonderwall.mp3`, length: 258, max: 85},

		// --- wrong songs / junk ---
		{name: "different song same artist", t: tWonder, path: `@@s\Oasis\Morning Glory\02 - Roll With It.mp3`, length: 239, max: 50, flags: []string{FlagTitleMismatch}},
		{name: "extra words: different song containing title", t: tHurt, path: `@@s\Johnny Cash - Hurt So Bad Tonight.mp3`, length: 218, max: 75, flags: []string{FlagTitleExtraWords}},
		{name: "sample", t: tBlind, path: `@@s\The Weeknd - Blinding Lights (sample).mp3`, length: 0, max: 40, flags: []string{FlagNegative}},
		{name: "full album rip", t: tBohemian, path: `@@s\Queen - A Night at the Opera (Full Album).mp3`, length: 0, max: 30, flags: []string{FlagMultiTrack}},
		{name: "estimated album-length flac", t: tHurt, path: `@@s\Johnny Cash - American IV.flac`, length: 0, max: 40},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := File{Username: "u", Path: c.path, Size: 8_000_000, Length: c.length}
			if strings.Contains(c.name, "estimated album-length") {
				f.Size = 300_000_000
			}
			cand := m.Score(c.t, f)
			if cand.Confidence < c.min || (c.max > 0 && cand.Confidence > c.max) {
				t.Errorf("confidence %d not in [%d,%d]\n%s", cand.Confidence, c.min, c.max, fmtBreakdown(cand))
			}
			for _, fl := range c.flags {
				if !cand.HasFlag(fl) {
					t.Errorf("missing flag %s (flags %v)\n%s", fl, cand.Flags, fmtBreakdown(cand))
				}
			}
			for _, fl := range c.noFlags {
				if cand.HasFlag(fl) {
					t.Errorf("unexpected flag %s\n%s", fl, fmtBreakdown(cand))
				}
			}
		})
	}
}

func fmtBreakdown(c *Candidate) string { return c.Explain("    ") }
