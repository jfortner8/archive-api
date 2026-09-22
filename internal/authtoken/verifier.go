// Package authtoken verifies bearer tokens issued by AWS Cognito, so
// handlers can learn which account is calling without ever handling a
// password or issuing a token themselves - that's Cognito's job.
package authtoken

import (
	"context"
	"errors"
	"time"
)

// Claims describes the identity carried by a verified access token.
type Claims struct {
	Sub      string // Cognito's stable per-user id - use this, not Username, as an account identifier
	Username string
	ClientID string
	Exp      time.Time
}

// Verifier checks a bearer token's signature and claims, returning who it
// belongs to.
type Verifier interface {
	Verify(ctx context.Context, token string) (Claims, error)
}

// ErrInvalidToken is returned for any token that fails signature, issuer,
// expiry, token-use, or client-id validation. Callers shouldn't try to
// distinguish between these cases in their response - just reject.
var ErrInvalidToken = errors.New("invalid token")
