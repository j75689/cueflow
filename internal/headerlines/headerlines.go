// Package headerlines parses "Name: value" lines, the format of secret header settings (an MCP connector's
// secret_headers, a runner's CUEFLOW_RUNNER_HEADERS). The values are secrets, so no error quotes a line.
package headerlines

import (
	"errors"
	"fmt"
	"strings"
)

// Header is one parsed line.
type Header struct{ Name, Value string }

// ErrMalformed is a line that is not "Name: value".
var ErrMalformed = errors.New(`want "Header-Name: value"`)

// Parse reads one header per line, skipping blank lines and lines starting with "#", in order.
func Parse(s string) ([]Header, error) {
	var out []Header
	for i, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		h, err := ParseLine(line)
		if err != nil {
			return nil, fmt.Errorf("line %d: %w", i+1, err)
		}
		out = append(out, h)
	}
	return out, nil
}

// ParseLine splits a single "Name: value", trimming both.
func ParseLine(s string) (Header, error) {
	name, value, ok := strings.Cut(s, ":")
	if name = strings.TrimSpace(name); !ok || name == "" {
		return Header{}, ErrMalformed
	}
	return Header{name, strings.TrimSpace(value)}, nil
}
