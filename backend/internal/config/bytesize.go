package config

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

// ByteSize is a size in bytes written as 512, 256K, 256M or 1G.
type ByteSize int64

// Set parses a size string.
func (b *ByteSize) Set(s string) error {
	s = strings.TrimSpace(strings.ToUpper(s))
	s = strings.TrimSuffix(s, "B")
	mult := int64(1)
	if n := len(s); n > 0 {
		switch s[n-1] {
		case 'K':
			mult = 1 << 10
		case 'M':
			mult = 1 << 20
		case 'G':
			mult = 1 << 30
		}
		if mult > 1 {
			s = s[:n-1]
		}
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return fmt.Errorf("bad size %q", s)
	}
	if n > math.MaxInt64/mult || n < math.MinInt64/mult {
		return fmt.Errorf("size is too large")
	}
	*b = ByteSize(n * mult)
	return nil
}

func (b ByteSize) String() string {
	switch {
	case b > 0 && b%(1<<30) == 0:
		return fmt.Sprintf("%dG", b>>30)
	case b > 0 && b%(1<<20) == 0:
		return fmt.Sprintf("%dM", b>>20)
	case b > 0 && b%(1<<10) == 0:
		return fmt.Sprintf("%dK", b>>10)
	}
	return strconv.FormatInt(int64(b), 10)
}

// MarshalYAML writes the short form.
func (b ByteSize) MarshalYAML() (any, error) { return b.String(), nil }

// UnmarshalYAML accepts both numbers and short forms.
func (b *ByteSize) UnmarshalYAML(unmarshal func(any) error) error {
	var s string
	if err := unmarshal(&s); err != nil {
		return err
	}
	return b.Set(s)
}
