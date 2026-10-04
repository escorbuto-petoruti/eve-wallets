package web

import (
	"encoding/json"
	"errors"

	"github.com/escorbuto-petoruti/eve-wallets/internal/auth"
	"github.com/escorbuto-petoruti/eve-wallets/internal/collector"
	"github.com/escorbuto-petoruti/eve-wallets/internal/store"
)

// StatusSnapshot summarises the last collection for the status endpoint. It
// carries no tokens: error texts come from the collector, which scrubs them.
type StatusSnapshot struct {
	// TakenAt is the collection time in unix seconds; 0 means none ran yet.
	TakenAt           int64
	Snapshots         int
	JournalPoints     int
	Skipped           []SkippedItem
	Errors            []ErrorItem
	RateLimited       bool
	RetryAfterSeconds int
}

// SkippedItem is something the collector deliberately did not collect. The
// owner identity (kind and id) and the user whose character produced the skip
// are not serialized: the front end only ever saw owner and reason.
type SkippedItem struct {
	OwnerKind store.Kind `json:"-"`
	OwnerID   int64      `json:"-"`
	Owner     string     `json:"owner"`
	Reason    string     `json:"reason"`
	UserID    int64      `json:"-"`
}

// ErrorItem is one failure in the snapshot. An item error carries the identity
// of the owner it belongs to; an error without an owner identity is a
// run-level error (the collector process, not a user) and is shown to every
// signed-in user. It marshals as the "owner: message" string the front end
// already consumes.
type ErrorItem struct {
	OwnerKind store.Kind `json:"-"`
	OwnerID   int64      `json:"-"`
	Message   string     `json:"-"`
	// Reauth is set when the error means the character must sign in again
	// (errors.Is(err, auth.ErrReauthRequired)). Not serialized.
	Reauth *ReauthItem `json:"-"`
}

// ReauthItem names a character that has to sign in again. It never carries a
// token or SSO text.
type ReauthItem struct {
	CharacterID int64  `json:"character_id"`
	Name        string `json:"name"`
}

func (e ErrorItem) MarshalJSON() ([]byte, error) { return json.Marshal(e.Message) }

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
		s.Skipped = append(s.Skipped, SkippedItem{OwnerKind: k.OwnerKind, OwnerID: k.OwnerID, Owner: k.Owner, Reason: k.Reason, UserID: k.UserID})
	}
	for _, e := range r.Errors {
		it := ErrorItem{OwnerKind: e.OwnerKind, OwnerID: e.OwnerID, Message: e.Error()}
		var re *auth.ReauthError
		if errors.Is(e.Err, auth.ErrReauthRequired) && errors.As(e.Err, &re) {
			it.Reauth = &ReauthItem{CharacterID: re.CharacterID, Name: re.Name}
		}
		s.Errors = append(s.Errors, it)
	}
	return s
}
