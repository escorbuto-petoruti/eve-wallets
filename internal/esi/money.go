package esi

import (
	"fmt"
	"math"
	"strings"
)

// ParseCents converts a decimal ISK amount written as plain text (for example
// "123", "123.4", "-0.05", "3123652530.8712") into integer cents without any
// floating point arithmetic.
//
// ESI emits up to four decimals, so digits beyond the second are rounded to
// the nearest cent, half away from zero ("0.005" is 1 cent, "-0.005" is -1,
// "0.995" is 100). Exponent notation, a leading "+", and bare ".5" or "5."
// forms are rejected rather than guessed at, as is a value that does not fit
// in an int64 after rounding.
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
	roundUp := len(frac) > 2 && frac[2] >= '5'
	if len(frac) > 2 {
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
	if roundUp {
		// The accumulator holds minus the magnitude, so rounding the
		// magnitude up (away from zero) is a decrement.
		if n == math.MinInt64 {
			return 0, fmt.Errorf("esi: ISK amount %q out of range", orig)
		}
		n--
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
