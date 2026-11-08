package organize

import (
	"fmt"
	"strconv"
	"strings"

	"go.senan.xyz/taglib"
)

// Tagger writes metadata into an audio file in place.
type Tagger interface {
	WriteTags(path string, md Metadata) error
}

// TagLib writes tags with TagLib (compiled to WebAssembly, no CGO). It
// handles MP3 (ID3v2), FLAC, Ogg Vorbis/Opus, MP4/M4A (AAC, ALAC), WAV,
// AIFF, WMA, APE, WavPack and more.
type TagLib struct{}

// keepPrefixes are existing tags that describe the audio itself rather
// than what it is, so they stay valid and are kept.
var keepPrefixes = []string{"REPLAYGAIN_", "R128_"}

// WriteTags replaces the file's tags with md. Everything else (the
// inconsistent tags Soulseek files often carry: comments, URLs, rip
// credits…) is removed, except loudness tags (ReplayGain/R128). When md has
// cover art, it replaces any embedded images.
func (TagLib) WriteTags(path string, md Metadata) error {
	tags := md.tagMap()
	if old, err := taglib.ReadTags(path); err == nil {
		for k, v := range old {
			for _, p := range keepPrefixes {
				if strings.HasPrefix(strings.ToUpper(k), p) {
					tags[k] = v
				}
			}
		}
	} else {
		return fmt.Errorf("reading tags: %w", err)
	}
	if err := taglib.WriteTags(path, tags, taglib.Clear); err != nil {
		return fmt.Errorf("writing tags: %w", err)
	}
	if len(md.Cover) == 0 {
		return nil
	}
	props, err := taglib.ReadProperties(path)
	if err != nil {
		return fmt.Errorf("reading properties: %w", err)
	}
	for i := len(props.Images) - 1; i >= 1; i-- {
		if err := taglib.WriteImageOptions(path, nil, i, "", "", ""); err != nil {
			return fmt.Errorf("removing old cover art: %w", err)
		}
	}
	if err := taglib.WriteImageOptions(path, md.Cover, 0, "Front Cover", "", imageMIME(md.Cover)); err != nil {
		return fmt.Errorf("writing cover art: %w", err)
	}
	return nil
}

func (md Metadata) tagMap() map[string][]string {
	t := map[string][]string{}
	set := func(k, v string) {
		if v = strings.TrimSpace(v); v != "" {
			t[k] = []string{v}
		}
	}
	set(taglib.Title, md.Title)
	set(taglib.Artist, strings.Join(md.Artists, ", "))
	if len(md.Artists) > 1 {
		t[taglib.Artists] = append([]string(nil), md.Artists...)
	}
	set(taglib.AlbumArtist, md.AlbumArtist)
	set(taglib.Album, md.Album)
	if md.TrackNumber > 0 {
		v := strconv.Itoa(md.TrackNumber)
		if md.TrackTotal >= md.TrackNumber {
			v += "/" + strconv.Itoa(md.TrackTotal)
		}
		set(taglib.TrackNumber, v)
	}
	if md.DiscNumber > 0 {
		set(taglib.DiscNumber, strconv.Itoa(md.DiscNumber))
	}
	switch {
	case md.Date != "":
		set(taglib.Date, md.Date)
	case md.Year > 0:
		set(taglib.Date, strconv.Itoa(md.Year))
	}
	set(taglib.Genre, md.Genre)
	set(taglib.ISRC, md.ISRC)
	if md.Compilation {
		set(taglib.Compilation, "1")
	}
	return t
}

func imageMIME(b []byte) string {
	switch {
	case len(b) >= 8 && string(b[:8]) == "\x89PNG\r\n\x1a\n":
		return "image/png"
	case len(b) >= 12 && string(b[:4]) == "RIFF" && string(b[8:12]) == "WEBP":
		return "image/webp"
	}
	return "image/jpeg"
}
