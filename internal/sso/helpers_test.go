package sso

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const testClientID = "test-client"

// fakeSSO is an httptest server emulating the EVE SSO endpoints.
type fakeSSO struct {
	*httptest.Server
	key *rsa.PrivateKey
	kid string

	mu         sync.Mutex
	form       url.Values
	user, pass string
	hasBasic   bool
	path       string
	status     int
	body       string
	jwksCalls  int
	jwksKeys   map[string]*rsa.PublicKey
}

func newFakeSSO(t *testing.T) *fakeSSO {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeSSO{key: key, kid: "kid-1", status: http.StatusOK}
	f.jwksKeys = map[string]*rsa.PublicKey{f.kid: &key.PublicKey}
	mux := http.NewServeMux()
	handle := func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		f.mu.Lock()
		f.form, f.path = r.PostForm, r.URL.Path
		f.user, f.pass, f.hasBasic = r.BasicAuth()
		status, body := f.status, f.body
		f.mu.Unlock()
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}
	mux.HandleFunc("/v2/oauth/token", handle)
	mux.HandleFunc("/v2/oauth/revoke", handle)
	mux.HandleFunc("/oauth/jwks", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.jwksCalls++
		keys := f.jwksKeys
		f.mu.Unlock()
		out := `{"keys":[`
		first := true
		for kid, pub := range keys {
			if !first {
				out += ","
			}
			first = false
			out += `{"kty":"RSA","alg":"RS256","use":"sig","kid":"` + kid + `","n":"` +
				base64.RawURLEncoding.EncodeToString(pub.N.Bytes()) + `","e":"` +
				base64.RawURLEncoding.EncodeToString(big.NewInt(int64(pub.E)).Bytes()) + `"}`
		}
		out += `]}`
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(out))
	})
	f.Server = httptest.NewServer(mux)
	t.Cleanup(f.Close)
	return f
}

func (f *fakeSSO) client() *Client {
	return NewClient(Config{
		ClientID:       testClientID,
		RedirectURL:    "http://localhost:8087/callback",
		LoginBaseURL:   f.URL,
		AllowedIssuers: []string{"login.eveonline.com", "https://login.eveonline.com"},
	})
}

func (f *fakeSSO) respond(status int, body string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.status, f.body = status, body
}

// claimsOpt mutates the default claims of a signed test token.
type claimsOpt func(jwt.MapClaims)

func (f *fakeSSO) sign(t *testing.T, method jwt.SigningMethod, key any, kid string, opts ...claimsOpt) string {
	t.Helper()
	claims := jwt.MapClaims{
		"iss":  "https://login.eveonline.com",
		"sub":  "CHARACTER:EVE:90000001",
		"aud":  []string{testClientID, "EVE Online"},
		"name": "Test Pilot",
		"scp":  []string{"esi-a.v1", "esi-b.v1"},
		"exp":  time.Now().Add(20 * time.Minute).Unix(),
		"iat":  time.Now().Unix(),
	}
	for _, o := range opts {
		o(claims)
	}
	tok := jwt.NewWithClaims(method, claims)
	tok.Header["kid"] = kid
	s, err := tok.SignedString(key)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func (f *fakeSSO) token(t *testing.T, opts ...claimsOpt) string {
	return f.sign(t, jwt.SigningMethodRS256, f.key, f.kid, opts...)
}
