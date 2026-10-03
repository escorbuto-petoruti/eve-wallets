package sso

const (
	// DefaultClientID is the embedded EVE application id. It belongs to a PKCE
	// public client, so it is an identifier and not a secret.
	DefaultClientID = "de451bb9d1d7458ab02c32c1f0998ee8"
	// DefaultRedirectURL is the fixed callback registered for DefaultClientID.
	DefaultRedirectURL = "http://localhost:8088/auth/callback"
)

// Wallet scopes; the strings match the ones the collector requires.
const (
	scopeCharacterWallet   = "esi-wallet.read_character_wallet.v1"
	scopeCorporationWallet = "esi-wallet.read_corporation_wallets.v1"
	scopeCorporationNames  = "esi-corporations.read_divisions.v1"
)

// WalletScopes returns the scopes needed to read character and corporation
// wallets. It returns a fresh slice so callers cannot mutate shared state.
func WalletScopes() []string {
	return []string{scopeCharacterWallet, scopeCorporationWallet, scopeCorporationNames}
}

// DefaultConfig returns the embedded client configuration. Every other field
// is zero so tests and callers can override it.
func DefaultConfig() Config {
	return Config{
		ClientID:    DefaultClientID,
		RedirectURL: DefaultRedirectURL,
		Scopes:      WalletScopes(),
	}
}
