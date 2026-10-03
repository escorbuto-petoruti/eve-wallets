// Package auth yields ESI access tokens for the characters that signed in
// with EVE SSO.
//
// Refresh tokens live in the application's SQLite file (see StoreTokens). Tokens
// are never logged and never included in error messages.
package auth

import "context"

// Character is an authenticated EVE character with a usable token.
type Character struct {
	ID     int64
	Name   string
	Scopes []string
	// UserID is the app user the character belongs to.
	UserID int64
}

// TokenSource lists the available characters and yields access tokens.
type TokenSource interface {
	Characters(ctx context.Context) ([]Character, error)
	Token(ctx context.Context, characterID int64) (string, error)
}
