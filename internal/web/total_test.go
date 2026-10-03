package web

import (
	"reflect"
	"testing"
)

func TestSumForwardFill(t *testing.T) {
	tests := []struct {
		name   string
		series map[int64][]Point
		want   []Point
	}{
		{name: "empty", series: nil, want: []Point{}},
		{
			name:   "single wallet is unchanged",
			series: map[int64][]Point{1: {{10, 100}, {20, 150}}},
			want:   []Point{{10, 100}, {20, 150}},
		},
		{
			name: "late wallet contributes zero until its first point",
			series: map[int64][]Point{
				1: {{10, 100}, {30, 300}},
				2: {{20, 5}},
			},
			want: []Point{{10, 100}, {20, 105}, {30, 305}},
		},
		{
			name: "shared timestamps are merged",
			series: map[int64][]Point{
				1: {{10, 1}, {20, 2}},
				2: {{10, 10}, {20, 20}},
			},
			want: []Point{{10, 11}, {20, 22}},
		},
		{
			name:   "wallet without points adds nothing",
			series: map[int64][]Point{1: {{10, 7}}, 2: nil},
			want:   []Point{{10, 7}},
		},
		{
			name:   "negative balances sum",
			series: map[int64][]Point{1: {{10, -50}}, 2: {{10, 20}}},
			want:   []Point{{10, -30}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := SumForwardFill(tt.series)
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("SumForwardFill = %v, want %v", got, tt.want)
			}
		})
	}
}
