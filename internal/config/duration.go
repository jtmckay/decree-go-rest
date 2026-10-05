package config

import (
	"fmt"
	"math"
	"strconv"
	"time"

	"gopkg.in/yaml.v3"
)

// Duration is a duration in decree's format: a whole number followed by
// s, m, h or d, such as 2s, 60s, 5m or 1d.
type Duration time.Duration

// ParseDuration parses decree's duration format. It is the only duration
// parser in decree-api.
func ParseDuration(s string) (time.Duration, error) {
	if len(s) < 2 {
		return 0, fmt.Errorf("invalid duration %q: want a whole number and s, m, h or d", s)
	}
	var unit time.Duration
	switch s[len(s)-1] {
	case 's':
		unit = time.Second
	case 'm':
		unit = time.Minute
	case 'h':
		unit = time.Hour
	case 'd':
		unit = 24 * time.Hour
	default:
		return 0, fmt.Errorf("invalid duration %q: want a whole number and s, m, h or d", s)
	}
	digits := s[:len(s)-1]
	for _, c := range digits {
		if c < '0' || c > '9' {
			return 0, fmt.Errorf("invalid duration %q: want a whole number and s, m, h or d", s)
		}
	}
	n, err := strconv.ParseInt(digits, 10, 64)
	if err != nil || n > math.MaxInt64/int64(unit) {
		return 0, fmt.Errorf("invalid duration %q: out of range", s)
	}
	return time.Duration(n) * unit, nil
}

// UnmarshalYAML reads a Duration from a YAML string scalar.
func (d *Duration) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind != yaml.ScalarNode {
		return fmt.Errorf("line %d: a duration must be a string such as 2s", n.Line)
	}
	v, err := ParseDuration(n.Value)
	if err != nil {
		return fmt.Errorf("line %d: %w", n.Line, err)
	}
	*d = Duration(v)
	return nil
}

// Std returns d as a time.Duration.
func (d Duration) Std() time.Duration { return time.Duration(d) }
