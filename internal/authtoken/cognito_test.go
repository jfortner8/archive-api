package authtoken

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/MicahParks/jwkset"
	"github.com/golang-jwt/jwt/v5"
)

const (
	testIssuer   = "https://cognito-idp.us-east-1.amazonaws.com/us-east-1_test"
	testKID      = "test-key"
	testClientID = "test-client-id"
)

// testJWKSServer serves a JWKS for the given RSA key, standing in for
// Cognito's real .well-known/jwks.json endpoint.
func testJWKSServer(t *testing.T, key *rsa.PrivateKey) *httptest.Server {
	t.Helper()

	jwk, err := jwkset.NewJWKFromKey(key.Public(), jwkset.JWKOptions{
		Metadata: jwkset.JWKMetadataOptions{KID: testKID, ALG: jwkset.AlgRS256},
	})
	if err != nil {
		t.Fatalf("build jwk: %v", err)
	}
	set := jwkset.JWKSMarshal{Keys: []jwkset.JWKMarshal{jwk.Marshal()}}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(set)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// signToken mints a token signed by key, with sensible defaults callers
// can override via claims.
func signToken(t *testing.T, key *rsa.PrivateKey, claims jwt.MapClaims) string {
	t.Helper()

	token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	token.Header["kid"] = testKID

	signed, err := token.SignedString(key)
	if err != nil {
		t.Fatalf("sign token: %v", err)
	}
	return signed
}

func defaultClaims() jwt.MapClaims {
	return jwt.MapClaims{
		"iss":       testIssuer,
		"sub":       "user-sub-123",
		"username":  "alice",
		"client_id": testClientID,
		"token_use": "access",
		"exp":       time.Now().Add(time.Hour).Unix(),
		"iat":       time.Now().Unix(),
	}
}

func newTestVerifier(t *testing.T, jwksURL, appClientID string) *CognitoVerifier {
	t.Helper()
	v, err := newVerifierFromJWKSURL(context.Background(), jwksURL, testIssuer, appClientID)
	if err != nil {
		t.Fatalf("newVerifierFromJWKSURL: %v", err)
	}
	return v
}

func mustRSAKey(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate rsa key: %v", err)
	}
	return key
}

func TestCognitoVerifier_Verify(t *testing.T) {
	key := mustRSAKey(t)
	srv := testJWKSServer(t, key)
	v := newTestVerifier(t, srv.URL, testClientID)

	t.Run("valid token", func(t *testing.T) {
		token := signToken(t, key, defaultClaims())
		claims, err := v.Verify(context.Background(), token)
		if err != nil {
			t.Fatalf("Verify() error = %v", err)
		}
		if claims.Sub != "user-sub-123" {
			t.Errorf("Sub = %q, want %q", claims.Sub, "user-sub-123")
		}
		if claims.Username != "alice" {
			t.Errorf("Username = %q, want %q", claims.Username, "alice")
		}
		if claims.ClientID != testClientID {
			t.Errorf("ClientID = %q, want %q", claims.ClientID, testClientID)
		}
	})

	t.Run("expired token rejected", func(t *testing.T) {
		c := defaultClaims()
		c["exp"] = time.Now().Add(-time.Hour).Unix()
		token := signToken(t, key, c)

		if _, err := v.Verify(context.Background(), token); err != ErrInvalidToken {
			t.Fatalf("Verify() error = %v, want ErrInvalidToken", err)
		}
	})

	t.Run("wrong issuer rejected", func(t *testing.T) {
		c := defaultClaims()
		c["iss"] = "https://cognito-idp.us-east-1.amazonaws.com/us-east-1_someone-else"
		token := signToken(t, key, c)

		if _, err := v.Verify(context.Background(), token); err != ErrInvalidToken {
			t.Fatalf("Verify() error = %v, want ErrInvalidToken", err)
		}
	})

	t.Run("id token rejected", func(t *testing.T) {
		// An ID token parses and signs just as validly as an access token -
		// only token_use tells them apart, so this check must not be skipped.
		c := defaultClaims()
		c["token_use"] = "id"
		token := signToken(t, key, c)

		if _, err := v.Verify(context.Background(), token); err != ErrInvalidToken {
			t.Fatalf("Verify() error = %v, want ErrInvalidToken", err)
		}
	})

	t.Run("garbage token rejected", func(t *testing.T) {
		if _, err := v.Verify(context.Background(), "not.a.token"); err != ErrInvalidToken {
			t.Fatalf("Verify() error = %v, want ErrInvalidToken", err)
		}
	})

	t.Run("wrong client id rejected", func(t *testing.T) {
		c := defaultClaims()
		c["client_id"] = "some-other-client"
		token := signToken(t, key, c)

		if _, err := v.Verify(context.Background(), token); err != ErrInvalidToken {
			t.Fatalf("Verify() error = %v, want ErrInvalidToken", err)
		}
	})

	t.Run("client id check skipped when unconfigured", func(t *testing.T) {
		vNoClientCheck := newTestVerifier(t, srv.URL, "")
		c := defaultClaims()
		c["client_id"] = "whatever-client"
		token := signToken(t, key, c)

		if _, err := vNoClientCheck.Verify(context.Background(), token); err != nil {
			t.Fatalf("Verify() error = %v, want nil", err)
		}
	})

	t.Run("signed by a different key rejected", func(t *testing.T) {
		otherKey := mustRSAKey(t)
		token := signToken(t, otherKey, defaultClaims())

		if _, err := v.Verify(context.Background(), token); err != ErrInvalidToken {
			t.Fatalf("Verify() error = %v, want ErrInvalidToken", err)
		}
	})
}
