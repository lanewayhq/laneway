package config

import (
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
)

// Size is an amount of data in bytes, written in config as a number and a
// unit, such as "1GB", "500 MB" or "2GiB", or as a whole number of bytes.
// The units are B, kB (or KB), MB, GB and TB in powers of 1000, and KiB,
// MiB, GiB and TiB in powers of 1024, in any case.
type Size int64

// sizeUnits are the units a Size may be written in, by their lowercase
// spelling.
var sizeUnits = map[string]float64{
	"b": 1, "": 1,
	"kb": 1e3, "mb": 1e6, "gb": 1e9, "tb": 1e12,
	"kib": 1 << 10, "mib": 1 << 20, "gib": 1 << 30, "tib": 1 << 40,
}

var sizeText = regexp.MustCompile(`^([0-9]+(?:\.[0-9]+)?) *([a-zA-Z]*)$`)

// ParseSize reads a Size such as "1GB".
func ParseSize(s string) (Size, error) {
	m := sizeText.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil {
		return 0, fmt.Errorf("%q isn't a size: write a number and a unit, such as \"1GB\" or \"500MB\"", s)
	}
	unit, ok := sizeUnits[strings.ToLower(m[2])]
	if !ok {
		return 0, fmt.Errorf("%q isn't a size: its unit must be B, kB, MB, GB or TB (or KiB, MiB, GiB or TiB), not %q", s, m[2])
	}
	n, err := strconv.ParseFloat(m[1], 64)
	if err != nil || n*unit >= math.MaxInt64 {
		return 0, fmt.Errorf("%q is too big a size", s)
	}
	return Size(math.Round(n * unit)), nil
}

// UnmarshalTOML reads a Size from config: a string such as "1GB", or a
// whole number of bytes such as 0.
func (s *Size) UnmarshalTOML(value any) error {
	switch v := value.(type) {
	case string:
		size, err := ParseSize(v)
		if err != nil {
			return err
		}
		*s = size
	case int64:
		if v < 0 {
			return fmt.Errorf("%d isn't a size: it can't be negative", v)
		}
		*s = Size(v)
	default:
		return fmt.Errorf("a size is a string with a unit, such as \"1GB\", not a %T", value)
	}
	return nil
}

// exact writes s as config that ParseSize reads back to s: in the largest
// unit it is a whole number of, such as "1GB", "3MiB" or "1500B".
func (s Size) exact() string {
	for _, u := range []struct {
		name string
		size int64
	}{{"TB", 1e12}, {"TiB", 1 << 40}, {"GB", 1e9}, {"GiB", 1 << 30}, {"MB", 1e6}, {"MiB", 1 << 20}, {"kB", 1e3}, {"KiB", 1 << 10}} {
		if s != 0 && int64(s)%u.size == 0 {
			return strconv.FormatInt(int64(s)/u.size, 10) + u.name
		}
	}
	return strconv.FormatInt(int64(s), 10) + "B"
}

// String writes s for people and for config, in the largest unit of
// powers of 1000 it has at least one of, with up to two decimals, cut
// short rather than rounded: "1 GB", "1.5 MB", "12 B".
func (s Size) String() string {
	units := []struct {
		name string
		size int64
	}{{"TB", 1e12}, {"GB", 1e9}, {"MB", 1e6}, {"kB", 1e3}}
	for _, u := range units {
		if int64(s) >= u.size {
			hundredths := int64(s) / (u.size / 100)
			n := strconv.FormatInt(hundredths/100, 10)
			if frac := hundredths % 100; frac != 0 {
				n += strings.TrimRight(fmt.Sprintf(".%02d", frac), "0")
			}
			return n + " " + u.name
		}
	}
	return strconv.FormatInt(int64(s), 10) + " B"
}
