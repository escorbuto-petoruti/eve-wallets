package sso

import (
	"context"
	"testing"
	"time"
)

func TestExchange(t *testing.T) {
	f := newFakeSSO(t)
	f.respond(200, `{"access_token":"at","refresh_token":"rt","expires_in":1199,"token_type":"Bearer"}`)

	ts, err := f.client().Exchange(context.Background(), "the-code", "the-verifier")
	if err != nil {
		t.Fatal(err)
	}
	if ts.AccessToken != "at" || ts.RefreshToken != "rt" || ts.ExpiresIn != 1199*time.Second {
		t.Errorf("unexpected token set: %+v", ts)
	}
	want := map[string]string{
		"grant_type":    "authorization_code",
		"code":          "the-code",
		"client_id":     testClientID,
		"code_verifier": "the-verifier",
	}
	for k, v := range want {
		if f.form.Get(k) != v {
			t.Errorf("%s = %q, want %q", k, f.form.Get(k), v)
		}
	}
	if f.hasBasic {
		t.Error("public client must not send Basic auth")
	}
	if f.path != "/v2/oauth/token" {
		t.Errorf("path = %q", f.path)
	}
}

func TestExchangeErrors(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   string
	}{
		{"non-200", 400, `{"error":"invalid_grant"}`},
		{"server error", 500, `oops`},
		{"malformed json", 200, `not json`},
		{"missing access token", 200, `{"refresh_token":"rt","expires_in":10}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFakeSSO(t)
			f.respond(tt.status, tt.body)
			if _, err := f.client().Exchange(context.Background(), "c", "v"); err == nil {
				t.Fatal("expected error")
			}
		})
	}
}

func TestRefreshRotation(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{"rotated", `{"access_token":"at2","refresh_token":"rt-new","expires_in":1199}`, "rt-new"},
		{"omitted keeps old", `{"access_token":"at2","expires_in":1199}`, "rt-old"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFakeSSO(t)
			f.respond(200, tt.body)
			ts, err := f.client().Refresh(context.Background(), "rt-old")
			if err != nil {
				t.Fatal(err)
			}
			if ts.RefreshToken != tt.want || ts.AccessToken != "at2" {
				t.Errorf("unexpected token set: %+v", ts)
			}
			if f.form.Get("grant_type") != "refresh_token" ||
				f.form.Get("refresh_token") != "rt-old" ||
				f.form.Get("client_id") != testClientID {
				t.Errorf("unexpected form: %v", f.form)
			}
			if f.hasBasic {
				t.Error("public client must not send Basic auth")
			}
		})
	}
}

func TestRefreshNon200(t *testing.T) {
	f := newFakeSSO(t)
	f.respond(400, `{"error":"invalid_grant"}`)
	if _, err := f.client().Refresh(context.Background(), "rt"); err == nil {
		t.Fatal("expected error")
	}
}

func TestRevoke(t *testing.T) {
	f := newFakeSSO(t)
	if err := f.client().Revoke(context.Background(), "rt"); err != nil {
		t.Fatal(err)
	}
	if f.path != "/v2/oauth/revoke" || f.form.Get("token") != "rt" ||
		f.form.Get("token_type_hint") != "refresh_token" || f.form.Get("client_id") != testClientID {
		t.Errorf("unexpected request: path=%q form=%v", f.path, f.form)
	}
	f.respond(400, `{}`)
	if err := f.client().Revoke(context.Background(), "rt"); err == nil {
		t.Fatal("expected error on non-200")
	}
}
