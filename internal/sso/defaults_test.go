package sso

import (
	"reflect"
	"testing"
)

func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()

	if cfg.ClientID != "de451bb9d1d7458ab02c32c1f0998ee8" {
		t.Errorf("ClientID = %q", cfg.ClientID)
	}
	if cfg.RedirectURL != "http://localhost:8088/auth/callback" {
		t.Errorf("RedirectURL = %q", cfg.RedirectURL)
	}
	want := []string{
		"esi-wallet.read_character_wallet.v1",
		"esi-wallet.read_corporation_wallets.v1",
		"esi-corporations.read_divisions.v1",
	}
	if !reflect.DeepEqual(cfg.Scopes, want) {
		t.Errorf("Scopes = %v, want %v", cfg.Scopes, want)
	}
}

func TestDefaultConfigLeavesOverridableFieldsZero(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.LoginBaseURL != "" || cfg.HTTPClient != nil || cfg.AllowedIssuers != nil {
		t.Errorf("expected zero overridable fields, got %+v", cfg)
	}
}

func TestDefaultConfigScopesAreNotShared(t *testing.T) {
	a := DefaultConfig()
	a.Scopes[0] = "mutated"
	if b := DefaultConfig(); b.Scopes[0] == "mutated" {
		t.Error("DefaultConfig must return a fresh Scopes slice")
	}
}
