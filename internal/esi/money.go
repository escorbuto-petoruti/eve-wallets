package esi

import (
	"fmt"
	"math"
	"strings"
)

// ParseCents converts a decimal ISK amount written as plain text (for example
// "123", "123.4", "-0.05") into integer cents without any floating point
// arithmetic.
//
// ESI emits plain decimal numbers with at most two significant decimals, so
// exponent notation, a leading "+", and bare ".5" or "5." forms are rejected
// rather than guessed at. Extra decimals are accepted only when they are
// zeros ("10.0000"); anything that would lose precision is an error, as is a
// value that does not fit in an int64.
func ParseCents(s string) (int64, error) {
	orig := s
	neg := strings.HasPrefix(s, "-")
	if neg {
		s = s[1:]
	}
	intPart, frac, hasFrac := strings.Cut(s, ".")
	if !isDigits(intPart) || (hasFrac && !isDigits(frac)) {
		return 0, fmt.Errorf("esi: invalid ISK amount %q", orig)
	}
	if len(frac) > 2 {
		if strings.Trim(frac[2:], "0") != "" {
			return 0, fmt.Errorf("esi: ISK amount %q has sub-cent precision", orig)
		}
		frac = frac[:2]
	}
	frac += strings.Repeat("0", 2-len(frac))

	// Accumulate as a negative number so math.MinInt64 is representable.
	var n int64
	for _, d := range intPart + frac {
		digit := int64(d - '0')
		if n < (math.MinInt64+digit)/10 {
			return 0, fmt.Errorf("esi: ISK amount %q out of range", orig)
		}
		n = n*10 - digit
	}
	if neg {
		return n, nil
	}
	if n == math.MinInt64 {
		return 0, fmt.Errorf("esi: ISK amount %q out of range", orig)
	}
	return -n, nil
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}
