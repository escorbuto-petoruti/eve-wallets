package esi

import (
	"errors"
	"fmt"
	"net/http"
	"time"
)

// APIError is returned for any non-2xx ESI response other than rate limiting.
// It never contains the access token.
type APIError struct {
	Status  int
	Message string
}

func (e *APIError) Error() string {
	if e.Message == "" {
		return fmt.Sprintf("esi: HTTP %d", e.Status)
	}
	return fmt.Sprintf("esi: HTTP %d: %s", e.Status, e.Message)
}

// RateLimitError is returned for HTTP 420 and 429 responses. RetryAfter is
// zero when the server did not send a usable Retry-After header.
type RateLimitError struct {
	Status     int
	RetryAfter time.Duration
}

func (e *RateLimitError) Error() string {
	if e.RetryAfter > 0 {
		return fmt.Sprintf("esi: rate limited (HTTP %d), retry after %s", e.Status, e.RetryAfter)
	}
	return fmt.Sprintf("esi: rate limited (HTTP %d)", e.Status)
}

// IsForbidden reports whether err is an HTTP 403, which for corporation
// endpoints means the character lacks the required corporation role.
func IsForbidden(err error) bool { return hasStatus(err, http.StatusForbidden) }

// IsNotFound reports whether err is an HTTP 404.
func IsNotFound(err error) bool { return hasStatus(err, http.StatusNotFound) }

func hasStatus(err error, status int) bool {
	var apiErr *APIError
	return errors.As(err, &apiErr) && apiErr.Status == status
}
