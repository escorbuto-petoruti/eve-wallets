package web

import "sort"

// Point is one balance observation: unix seconds and integer ISK cents.
type Point struct {
	T     int64 `json:"t"`
	Cents int64 `json:"cents"`
}

// SumForwardFill adds up several wallet series over the union of their
// timestamps. Each series must be ordered by time. Between its own points a
// wallet keeps its last known balance, and before its first point it
// contributes 0. The result is ordered by time and never nil.
func SumForwardFill(series map[int64][]Point) []Point {
	seen := make(map[int64]struct{})
	for _, pts := range series {
		for _, p := range pts {
			seen[p.T] = struct{}{}
		}
	}
	times := make([]int64, 0, len(seen))
	for t := range seen {
		times = append(times, t)
	}
	sort.Slice(times, func(i, j int) bool { return times[i] < times[j] })

	idx := make(map[int64]int, len(series)) // next unread point per wallet
	last := make(map[int64]int64, len(series))
	out := make([]Point, 0, len(times))
	for _, t := range times {
		var sum int64
		for id, pts := range series {
			i := idx[id]
			for i < len(pts) && pts[i].T <= t {
				last[id] = pts[i].Cents
				i++
			}
			idx[id] = i
			sum += last[id] // 0 until the wallet's first point
		}
		out = append(out, Point{T: t, Cents: sum})
	}
	return out
}
