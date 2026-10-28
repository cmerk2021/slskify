package match

import (
	"fmt"
	"strings"
)

// Explain renders the score breakdown, one signal per line, each prefixed
// with indent.
func (c *Candidate) Explain(indent string) string {
	var b strings.Builder
	for _, s := range c.Breakdown {
		fmt.Fprintf(&b, "%s%-12s %+6.1f  %s\n", indent, s.Name, s.Points, s.Note)
	}
	if len(c.Flags) > 0 {
		fmt.Fprintf(&b, "%sflags: %s\n", indent, strings.Join(c.Flags, ", "))
	}
	return b.String()
}
