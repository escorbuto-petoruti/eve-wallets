package esi

import (
	"math"
	"strconv"
	"testing"
)

func TestParseCents(t *testing.T) {
	tests := []struct {
		in      string
		want    int64
		wantErr bool
	}{
		{in: "0", want: 0},
		{in: "123", want: 12300},
		{in: "123.4", want: 12340},
		{in: "123.45", want: 12345},
		{in: "0.01", want: 1},
		{in: "-0.5", want: -50},
		{in: "-123.45", want: -12345},
		{in: "1234567890123.99", want: 123456789012399},
		{in: "10.00", want: 1000},
		{in: "10.0000", want: 1000},
		{in: "92233720368547758.07", want: math.MaxInt64},
		{in: "-92233720368547758.08", want: math.MinInt64},
		{in: "", wantErr: true},
		{in: "-", wantErr: true},
		{in: ".5", wantErr: true},
		{in: "5.", wantErr: true},
		{in: "1e5", wantErr: true},
		{in: "1.5E+3", wantErr: true},
		{in: "abc", wantErr: true},
		{in: "1.234", wantErr: true},
		{in: "+1", wantErr: true},
		{in: "1 2", wantErr: true},
		{in: "92233720368547758.08", wantErr: true},
		{in: "99999999999999999999", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(strconv.Quote(tt.in), func(t *testing.T) {
			got, err := ParseCents(tt.in)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("ParseCents(%q) = %d, want error", tt.in, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseCents(%q) error: %v", tt.in, err)
			}
			if got != tt.want {
				t.Fatalf("ParseCents(%q) = %d, want %d", tt.in, got, tt.want)
			}
		})
	}
}
