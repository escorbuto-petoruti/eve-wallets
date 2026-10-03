package web

import "github.com/escorbuto-petoruti/eve-wallets/internal/collector"

// StatusSnapshot summarises the last collection for the status endpoint. It
// carries no tokens: error texts come from the collector, which scrubs them.
type StatusSnapshot struct {
	// TakenAt is the collection time in unix seconds; 0 means none ran yet.
	TakenAt           int64
	Snapshots         int
	JournalPoints     int
	Skipped           []SkippedItem
	Errors            []string
	RateLimited       bool
	RetryAfterSeconds int
}

// SkippedItem is something the collector deliberately did not collect.
type SkippedItem struct {
	Owner  string `json:"owner"`
	Reason string `json:"reason"`
}

// StatusFromReport converts a collector report to a StatusSnapshot.
func StatusFromReport(r collector.Report) StatusSnapshot {
	s := StatusSnapshot{
		Snapshots:         len(r.Snapshots),
		JournalPoints:     r.JournalPoints,
		RateLimited:       r.RateLimited,
		RetryAfterSeconds: int(r.RetryAfter.Seconds()),
	}
	if !r.TakenAt.IsZero() {
		s.TakenAt = r.TakenAt.Unix()
	}
	for _, k := range r.Skipped {
		s.Skipped = append(s.Skipped, SkippedItem{Owner: k.Owner, Reason: k.Reason})
	}
	for _, e := range r.Errors {
		s.Errors = append(s.Errors, e.Error())
	}
	return s
}
