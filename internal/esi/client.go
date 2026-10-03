// Package esi is a small client for the EVE Online ESI wallet endpoints.
//
// Money is always returned as integer ISK cents, parsed exactly from the JSON
// text. Access tokens are supplied per call because each character has its
// own; they are sent only in the Authorization header and never appear in
// errors or logs.
package esi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	// DefaultBaseURL is the production ESI host.
	DefaultBaseURL = "https://esi.evetech.net"

	compatibilityDate = "2020-01-01"
	defaultUserAgent  = "eve-wallets"
	maxBodyBytes      = 8 << 20 // largest accepted successful response
	maxErrorBodyBytes = 4 << 10 // largest error body that is read
	maxJournalPages   = 50
	maxCacheEntries   = 512
)

// Options configures a Client. Zero values select sensible defaults.
type Options struct {
	BaseURL    string
	HTTPClient *http.Client
	UserAgent  string
}

// Client talks to ESI. It is safe for concurrent use.
type Client struct {
	baseURL string
	http    *http.Client
	ua      string

	mu    sync.Mutex
	cache map[string]cacheEntry
}

type cacheEntry struct {
	etag   string
	body   []byte
	header http.Header
}

// New returns a Client for the given options.
func New(opts Options) *Client {
	c := &Client{
		baseURL: strings.TrimRight(opts.BaseURL, "/"),
		http:    opts.HTTPClient,
		ua:      opts.UserAgent,
		cache:   make(map[string]cacheEntry),
	}
	if c.baseURL == "" {
		c.baseURL = DefaultBaseURL
	}
	if c.http == nil {
		c.http = &http.Client{Timeout: 30 * time.Second}
	}
	if c.ua == "" {
		c.ua = defaultUserAgent
	}
	return c
}

// DivisionBalance is the balance of one corporation wallet division.
type DivisionBalance struct {
	Division int
	Cents    int64
}

// JournalEntry is one wallet journal row. BalanceCents is nil when ESI omits
// the running balance for the entry.
type JournalEntry struct {
	ID           int64
	Date         time.Time
	AmountCents  int64
	BalanceCents *int64
	RefType      string
	Description  string
}

// CharacterWallet returns the character's wallet balance in cents.
func (c *Client) CharacterWallet(ctx context.Context, token string, characterID int64) (int64, error) {
	body, _, err := c.get(ctx, token, fmt.Sprintf("/characters/%d/wallet", characterID), nil)
	if err != nil {
		return 0, err
	}
	var n json.Number
	if err := json.Unmarshal(body, &n); err != nil {
		return 0, fmt.Errorf("esi: decode character wallet: %w", err)
	}
	return ParseCents(n.String())
}

// CorporationWallets returns the balance of every wallet division of the
// corporation. It needs the Accountant or Junior_Accountant role; without it
// the error satisfies IsForbidden.
func (c *Client) CorporationWallets(ctx context.Context, token string, corporationID int64) ([]DivisionBalance, error) {
	body, _, err := c.get(ctx, token, fmt.Sprintf("/corporations/%d/wallets", corporationID), nil)
	if err != nil {
		return nil, err
	}
	var raw []struct {
		Division int         `json:"division"`
		Balance  json.Number `json:"balance"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("esi: decode corporation wallets: %w", err)
	}
	out := make([]DivisionBalance, 0, len(raw))
	for _, r := range raw {
		cents, err := ParseCents(r.Balance.String())
		if err != nil {
			return nil, fmt.Errorf("esi: corporation wallet division %d: %w", r.Division, err)
		}
		out = append(out, DivisionBalance{Division: r.Division, Cents: cents})
	}
	return out, nil
}

// CharacterJournal returns every journal entry of the character wallet,
// fetching all pages.
func (c *Client) CharacterJournal(ctx context.Context, token string, characterID int64) ([]JournalEntry, error) {
	return c.journal(ctx, token, fmt.Sprintf("/characters/%d/wallet/journal", characterID))
}

// CorporationJournal returns every journal entry of one corporation wallet
// division (1-7), fetching all pages.
func (c *Client) CorporationJournal(ctx context.Context, token string, corporationID int64, division int) ([]JournalEntry, error) {
	return c.journal(ctx, token, fmt.Sprintf("/corporations/%d/wallets/%d/journal", corporationID, division))
}

// CharacterCorporationID returns the corporation the character belongs to.
// This endpoint is public and sends no token.
func (c *Client) CharacterCorporationID(ctx context.Context, characterID int64) (int64, error) {
	body, _, err := c.get(ctx, "", fmt.Sprintf("/characters/%d", characterID), nil)
	if err != nil {
		return 0, err
	}
	var info struct {
		CorporationID int64 `json:"corporation_id"`
	}
	if err := json.Unmarshal(body, &info); err != nil {
		return 0, fmt.Errorf("esi: decode character: %w", err)
	}
	if info.CorporationID == 0 {
		return 0, fmt.Errorf("esi: character %d response has no corporation_id", characterID)
	}
	return info.CorporationID, nil
}

type journalRow struct {
	ID          int64        `json:"id"`
	Date        time.Time    `json:"date"`
	Amount      json.Number  `json:"amount"`
	Balance     *json.Number `json:"balance"`
	RefType     string       `json:"ref_type"`
	Description string       `json:"description"`
}

func (c *Client) journal(ctx context.Context, token, path string) ([]JournalEntry, error) {
	var out []JournalEntry
	pages := 1
	for page := 1; page <= pages; page++ {
		body, hdr, err := c.get(ctx, token, path, url.Values{"page": {strconv.Itoa(page)}})
		if err != nil {
			return nil, err
		}
		if page == 1 {
			if v := hdr.Get("X-Pages"); v != "" {
				n, err := strconv.Atoi(v)
				if err != nil || n < 1 {
					return nil, fmt.Errorf("esi: invalid X-Pages header %q", v)
				}
				pages = min(n, maxJournalPages)
			}
		}
		var rows []journalRow
		if err := json.Unmarshal(body, &rows); err != nil {
			return nil, fmt.Errorf("esi: decode journal page %d: %w", page, err)
		}
		for _, r := range rows {
			e := JournalEntry{ID: r.ID, Date: r.Date, RefType: r.RefType, Description: r.Description}
			if e.AmountCents, err = ParseCents(r.Amount.String()); err != nil {
				return nil, fmt.Errorf("esi: journal entry %d amount: %w", r.ID, err)
			}
			if r.Balance != nil {
				b, err := ParseCents(r.Balance.String())
				if err != nil {
					return nil, fmt.Errorf("esi: journal entry %d balance: %w", r.ID, err)
				}
				e.BalanceCents = &b
			}
			out = append(out, e)
		}
	}
	return out, nil
}

// get performs a GET with conditional-request caching and returns the body
// and response headers. An empty token makes an unauthenticated call.
func (c *Client) get(ctx context.Context, token, path string, query url.Values) ([]byte, http.Header, error) {
	full := c.baseURL + path
	if len(query) > 0 {
		full += "?" + query.Encode()
	}
	key := full + "\x00" + tokenHash(token)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, full, nil)
	if err != nil {
		return nil, nil, fmt.Errorf("esi: build request: %w", err)
	}
	req.Header.Set("X-Compatibility-Date", compatibilityDate)
	req.Header.Set("User-Agent", c.ua)
	req.Header.Set("Accept", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	cached, haveCached := c.lookup(key)
	if haveCached {
		req.Header.Set("If-None-Match", cached.etag)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, nil, redactErr(err, token)
	}
	defer resp.Body.Close()

	switch {
	case resp.StatusCode == http.StatusNotModified && haveCached:
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxErrorBodyBytes))
		return cached.body, cached.header, nil
	case resp.StatusCode == 420 || resp.StatusCode == http.StatusTooManyRequests:
		return nil, nil, &RateLimitError{Status: resp.StatusCode, RetryAfter: parseRetryAfter(resp.Header.Get("Retry-After"))}
	case resp.StatusCode < 200 || resp.StatusCode > 299:
		return nil, nil, &APIError{Status: resp.StatusCode, Message: readErrorMessage(resp.Body, token)}
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes+1))
	if err != nil {
		return nil, nil, redactErr(err, token)
	}
	if len(body) > maxBodyBytes {
		return nil, nil, fmt.Errorf("esi: response exceeds %d bytes", maxBodyBytes)
	}
	if etag := resp.Header.Get("ETag"); etag != "" {
		c.store(key, cacheEntry{etag: etag, body: body, header: resp.Header.Clone()})
	}
	return body, resp.Header, nil
}

func (c *Client) lookup(key string) (cacheEntry, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.cache[key]
	return e, ok
}

func (c *Client) store(key string, e cacheEntry) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, exists := c.cache[key]; !exists && len(c.cache) >= maxCacheEntries {
		for k := range c.cache { // evict an arbitrary entry
			delete(c.cache, k)
			break
		}
	}
	c.cache[key] = e
}

func tokenHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func parseRetryAfter(v string) time.Duration {
	secs, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil || secs < 0 {
		return 0
	}
	return time.Duration(secs) * time.Second
}

// readErrorMessage reads a bounded error body and extracts ESI's "error"
// field when present. The token is scrubbed in case a server echoes it.
func readErrorMessage(r io.Reader, token string) string {
	raw, _ := io.ReadAll(io.LimitReader(r, maxErrorBodyBytes))
	msg := strings.TrimSpace(string(raw))
	var parsed struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(raw, &parsed) == nil && parsed.Error != "" {
		msg = parsed.Error
	}
	return scrub(msg, token)
}

func scrub(s, token string) string {
	if token == "" {
		return s
	}
	return strings.ReplaceAll(s, token, "[redacted]")
}

// redactErr removes the token from a transport error, preserving the chain
// for errors.Is/As on context errors.
func redactErr(err error, token string) error {
	if token == "" || !strings.Contains(err.Error(), token) {
		return err
	}
	return &redactedError{msg: scrub(err.Error(), token), err: err}
}

type redactedError struct {
	msg string
	err error
}

func (e *redactedError) Error() string { return e.msg }
func (e *redactedError) Unwrap() error { return e.err }

// CorporationName returns the public name of a corporation. This endpoint is
// public and sends no token.
func (c *Client) CorporationName(ctx context.Context, corporationID int64) (string, error) {
	body, _, err := c.get(ctx, "", fmt.Sprintf("/corporations/%d", corporationID), nil)
	if err != nil {
		return "", err
	}
	var info struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(body, &info); err != nil {
		return "", fmt.Errorf("esi: decode corporation: %w", err)
	}
	if info.Name == "" {
		return "", fmt.Errorf("esi: corporation %d response has no name", corporationID)
	}
	return info.Name, nil
}
