package organize

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// RsgainBinary is the rsgain executable looked up on PATH.
const RsgainBinary = "rsgain"

// CheckRsgain reports whether rsgain is installed and on PATH.
func CheckRsgain() error {
	if _, err := exec.LookPath(RsgainBinary); err != nil {
		return fmt.Errorf("%s not found on PATH (install rsgain or turn ReplayGain off)", RsgainBinary)
	}
	return nil
}

// ReplayGain scans one file with rsgain and writes ReplayGain 2.0 track
// gain/peak tags (clipping protection for positive gains). Only the given
// file is scanned, never the rest of the library.
func ReplayGain(ctx context.Context, file string) error {
	cmd := exec.CommandContext(ctx, RsgainBinary, "custom", "--tagmode=i", "--clip-mode=p", "--quiet", "--", file)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Run(); err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return CheckRsgain()
		}
		if msg := strings.TrimSpace(out.String()); msg != "" {
			return fmt.Errorf("rsgain: %w: %s", err, lastLine(msg))
		}
		return fmt.Errorf("rsgain: %w", err)
	}
	return nil
}

func lastLine(s string) string {
	if i := strings.LastIndexByte(s, '\n'); i >= 0 {
		return strings.TrimSpace(s[i+1:])
	}
	return s
}
