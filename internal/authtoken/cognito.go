package authtoken

import (
	"context"
	"fmt"

	"github.com/MicahParks/keyfunc/v3"
	"github.com/golang-jwt/jwt/v5"
)

// CognitoVerifier verifies AWS Cognito access tokens against the issuing
// User Pool's published JWKS. It never calls a Cognito API and needs no
// AWS credentials - just a public HTTPS fetch to a well-known URL.
type CognitoVerifier struct {
	keys        keyfunc.Keyfunc
	issuer      string
	appClientID string // optional; empty skips the client_id check
}

// NewCognitoVerifier builds a verifier for the given User Pool. appClientID
// may be empty to skip checking which App Client a token was issued to.
func NewCognitoVerifier(ctx context.Context, userPoolID, region, appClientID string) (*CognitoVerifier, error) {
	issuer := fmt.Sprintf("https://cognito-idp.%s.amazonaws.com/%s", region, userPoolID)
	return newVerifierFromJWKSURL(ctx, issuer+"/.well-known/jwks.json", issuer, appClientID)
}

// newVerifierFromJWKSURL builds a verifier from an explicit JWKS URL and
// issuer, bypassing Cognito's URL convention - lets tests point at a local
// httptest.Server instead of depending on real AWS.
func newVerifierFromJWKSURL(ctx context.Context, jwksURL, issuer, appClientID string) (*CognitoVerifier, error) {
	keys, err := keyfunc.NewDefaultCtx(ctx, []string{jwksURL})
	if err != nil {
		return nil, fmt.Errorf("fetch jwks: %w", err)
	}
	return &CognitoVerifier{keys: keys, issuer: issuer, appClientID: appClientID}, nil
}

// Verify checks the token's signature against the User Pool's JWKS, its
// issuer and expiry, and - critically - that it's an access token and not
// an ID token, which carries no authorization semantics but would
// otherwise parse and verify just as validly.
func (v *CognitoVerifier) Verify(ctx context.Context, tokenString string) (Claims, error) {
	claims := jwt.MapClaims{}
	token, err := jwt.ParseWithClaims(tokenString, claims, v.keys.Keyfunc,
		jwt.WithValidMethods([]string{"RS256"}),
		jwt.WithIssuer(v.issuer),
		jwt.WithExpirationRequired(),
	)
	if err != nil || !token.Valid {
		return Claims{}, ErrInvalidToken
	}

	if use, _ := claims["token_use"].(string); use != "access" {
		return Claims{}, ErrInvalidToken
	}

	clientID, _ := claims["client_id"].(string)
	if v.appClientID != "" && clientID != v.appClientID {
		return Claims{}, ErrInvalidToken
	}

	sub, _ := claims["sub"].(string)
	if sub == "" {
		return Claims{}, ErrInvalidToken
	}

	exp, err := claims.GetExpirationTime()
	if err != nil || exp == nil {
		return Claims{}, ErrInvalidToken
	}

	username, _ := claims["username"].(string)

	return Claims{
		Sub:      sub,
		Username: username,
		ClientID: clientID,
		Exp:      exp.Time,
	}, nil
}
