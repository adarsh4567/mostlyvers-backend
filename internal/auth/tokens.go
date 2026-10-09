package auth

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/mostlyvers/backend/internal/domain"
)

type Tokens struct {
	private ed25519.PrivateKey
	public  ed25519.PublicKey
	ttl     time.Duration
}

func NewTokens(private ed25519.PrivateKey, public ed25519.PublicKey, ttl time.Duration) *Tokens {
	return &Tokens{private: private, public: public, ttl: ttl}
}

type Claims struct {
	Role, SessionID string
	jwt.RegisteredClaims
}

func (t *Tokens) Issue(accountID, role, sessionID string) (string, time.Time, error) {
	now, expiry := time.Now().UTC(), time.Now().UTC().Add(t.ttl)
	claims := Claims{Role: role, SessionID: sessionID, RegisteredClaims: jwt.RegisteredClaims{Subject: accountID, Issuer: "mostlyvers", Audience: jwt.ClaimStrings{"mostlyvers-api"}, IssuedAt: jwt.NewNumericDate(now), ExpiresAt: jwt.NewNumericDate(expiry)}}
	token, err := jwt.NewWithClaims(jwt.SigningMethodEdDSA, claims).SignedString(t.private)
	return token, expiry, err
}
func (t *Tokens) Verify(value string) (domain.Principal, error) {
	parsed, err := jwt.ParseWithClaims(value, &Claims{}, func(token *jwt.Token) (any, error) {
		if token.Method.Alg() != jwt.SigningMethodEdDSA.Alg() {
			return nil, errors.New("unexpected algorithm")
		}
		return t.public, nil
	}, jwt.WithAudience("mostlyvers-api"), jwt.WithIssuer("mostlyvers"))
	if err != nil || !parsed.Valid {
		return domain.Principal{}, errors.New("invalid access token")
	}
	claims, ok := parsed.Claims.(*Claims)
	if !ok {
		return domain.Principal{}, errors.New("invalid claims")
	}
	return domain.Principal{AccountID: claims.Subject, Role: claims.Role, SessionID: claims.SessionID}, nil
}
func OpaqueToken() (plain string, hash []byte, err error) {
	value := make([]byte, 32)
	if _, err = rand.Read(value); err != nil {
		return
	}
	plain = base64.RawURLEncoding.EncodeToString(value)
	sum := sha256.Sum256([]byte(plain))
	hash = sum[:]
	return
}
func TokenHash(plain string) []byte { sum := sha256.Sum256([]byte(plain)); return sum[:] }
