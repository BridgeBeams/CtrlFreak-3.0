package relay

import (
	"errors"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// Claims is the JWT payload issued at login and presented on the signaling
// WebSocket and to admin API calls.
type Claims struct {
	Username string `json:"u"`
	IsAdmin  bool   `json:"a"`
	jwt.RegisteredClaims
}

// TokenAuth issues and verifies signed session tokens.
type TokenAuth struct {
	secret []byte
	ttl    time.Duration
}

func NewTokenAuth(secret []byte, ttl time.Duration) *TokenAuth {
	return &TokenAuth{secret: secret, ttl: ttl}
}

// Issue mints a signed token for a user.
func (t *TokenAuth) Issue(username string, admin bool) (string, error) {
	now := time.Now()
	c := Claims{
		Username: username,
		IsAdmin:  admin,
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   username,
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(t.ttl)),
		},
	}
	return jwt.NewWithClaims(jwt.SigningMethodHS256, c).SignedString(t.secret)
}

// Verify parses and validates a token, returning its claims.
func (t *TokenAuth) Verify(token string) (*Claims, error) {
	parsed, err := jwt.ParseWithClaims(token, &Claims{}, func(tok *jwt.Token) (interface{}, error) {
		if _, ok := tok.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, errors.New("unexpected signing method")
		}
		return t.secret, nil
	})
	if err != nil {
		return nil, err
	}
	claims, ok := parsed.Claims.(*Claims)
	if !ok || !parsed.Valid {
		return nil, errors.New("invalid token")
	}
	return claims, nil
}
