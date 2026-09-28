package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

type claims struct {
	Role Role `json:"role"`
	jwt.RegisteredClaims
}

// tokens issues and verifies HS256 access tokens.
type tokens struct {
	secret   []byte
	lifetime time.Duration
	now      func() time.Time
}

func newTokens(secret []byte, lifetime time.Duration, now func() time.Time) tokens {
	return tokens{secret: secret, lifetime: lifetime, now: now}
}

func (t tokens) issue(u User) (string, error) {
	now := t.now()
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims{
		Role: u.Role,
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   u.ID.String(),
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(t.lifetime)),
		},
	})
	signed, err := token.SignedString(t.secret)
	if err != nil {
		return "", fmt.Errorf("sign access token: %w", err)
	}
	return signed, nil
}

func (t tokens) parse(raw string) (Principal, error) {
	var c claims
	parser := jwt.NewParser(
		jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}),
		jwt.WithExpirationRequired(),
		jwt.WithTimeFunc(t.now),
	)
	if _, err := parser.ParseWithClaims(raw, &c, func(*jwt.Token) (any, error) { return t.secret, nil }); err != nil {
		return Principal{}, ErrInvalidToken
	}
	id, err := uuid.Parse(c.Subject)
	if err != nil || !c.Role.valid() {
		return Principal{}, ErrInvalidToken
	}
	return Principal{UserID: id, Role: c.Role}, nil
}

func newRefreshToken() (raw string, hash []byte) {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	raw = base64.RawURLEncoding.EncodeToString(b)
	return raw, hashRefreshToken(raw)
}

func hashRefreshToken(raw string) []byte {
	h := sha256.Sum256([]byte(raw))
	return h[:]
}
