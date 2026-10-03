package sso

import (
	"net/url"
	"strings"
	"testing"
)

func TestChallengeRFC7636Vector(t *testing.T) {
	got := Challenge("dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk")
	want := "E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM"
	if got != want {
		t.Fatalf("Challenge = %q, want %q", got, want)
	}
}

func TestNewVerifierAndState(t *testing.T) {
	for name, gen := range map[string]func() (string, error){"verifier": NewVerifier, "state": NewState} {
		t.Run(name, func(t *testing.T) {
			a, err := gen()
			if err != nil {
				t.Fatal(err)
			}
			b, _ := gen()
			if len(a) < 43 {
				t.Errorf("len = %d, want >= 43", len(a))
			}
			if strings.ContainsAny(a, "+/=") {
				t.Errorf("%q is not base64url without padding", a)
			}
			if a == b {
				t.Error("two generated values are identical")
			}
		})
	}
}

func TestAuthURL(t *testing.T) {
	c := NewClient(Config{
		ClientID:     "cid",
		RedirectURL:  "http://localhost:8087/callback",
		Scopes:       []string{"esi-a.v1", "esi-b.v1"},
		LoginBaseURL: "https://sso.test",
	})
	raw := c.AuthURL("st", "chal")
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if got := u.Scheme + "://" + u.Host + u.Path; got != "https://sso.test/v2/oauth/authorize" {
		t.Errorf("endpoint = %q", got)
	}
	want := map[string]string{
		"response_type":         "code",
		"client_id":             "cid",
		"redirect_uri":          "http://localhost:8087/callback",
		"scope":                 "esi-a.v1 esi-b.v1",
		"state":                 "st",
		"code_challenge":        "chal",
		"code_challenge_method": "S256",
	}
	q := u.Query()
	for k, v := range want {
		if q.Get(k) != v {
			t.Errorf("%s = %q, want %q", k, q.Get(k), v)
		}
	}
}
