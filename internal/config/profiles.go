package config

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/BurntSushi/toml"

	"github.com/cmerk2021/slskify/internal/match"
)

var presetNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,39}$`)

// ValidatePresetName checks a user-chosen preset name.
func ValidatePresetName(name string) error {
	if !presetNameRe.MatchString(name) {
		return errors.New("name must be 1-40 characters: lowercase letters, digits, - or _ (starting with a letter or digit)")
	}
	return nil
}

// QualityProfile is the user-editable part of a quality preset.
type QualityProfile struct {
	Description string
	Rank        []string
	MinBitrate  int
}

// Policy compiles the profile into a policy, starting from base (so an
// override of a built-in keeps its other knobs).
func (qp QualityProfile) Policy(base match.QualityPolicy) (match.QualityPolicy, error) {
	if len(qp.Rank) == 0 {
		return base, errors.New("add at least one rank rule")
	}
	p := base
	p.Description, p.Rank, p.MinBitrate = strings.TrimSpace(qp.Description), qp.Rank, qp.MinBitrate
	p.Tiers, p.AllowedExtensions = nil, nil
	return p.Compile()
}

// SaveQualityPreset writes [quality_presets.<name>] in rank form to the
// config file at path, replacing any previous definition of that name and
// leaving everything else untouched. (Comments are not preserved.)
func SaveQualityPreset(path, name string, qp QualityProfile) error {
	if err := ValidatePresetName(name); err != nil {
		return err
	}
	if _, err := qp.Policy(match.QualityPolicy{}); err != nil {
		return err
	}
	return editFile(path, func(m map[string]any) {
		presets, ok := m["quality_presets"].(map[string]any)
		if !ok {
			presets = map[string]any{}
			m["quality_presets"] = presets
		}
		entry, ok := presets[name].(map[string]any)
		if !ok {
			entry = map[string]any{}
		}
		delete(entry, "tiers")
		delete(entry, "allowed_extensions")
		delete(entry, "description")
		entry["rank"], entry["min_bitrate"] = qp.Rank, qp.MinBitrate
		if d := strings.TrimSpace(qp.Description); d != "" {
			entry["description"] = d
		}
		presets[name] = entry
	})
}

// DeleteQualityPreset removes [quality_presets.<name>] from the config file.
// For a built-in name this restores the built-in definition.
func DeleteQualityPreset(path, name string) error {
	if _, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return editFile(path, func(m map[string]any) {
		if presets, ok := m["quality_presets"].(map[string]any); ok {
			delete(presets, name)
			if len(presets) == 0 {
				delete(m, "quality_presets")
			}
		}
	})
}

// editFile applies f to the config file decoded as a generic map (an empty
// map if the file doesn't exist) and writes it back with mode 0600.
func editFile(path string, f func(map[string]any)) error {
	m := map[string]any{}
	existing, err := os.ReadFile(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
	case err != nil:
		return err
	default:
		if _, err := toml.Decode(string(existing), &m); err != nil {
			return fmt.Errorf("config %s: %w", path, err)
		}
	}
	f(m)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(m); err != nil {
		return err
	}
	return writePrivate(path, buf.Bytes())
}
