package auth

import (
	"crypto/ed25519"
	"errors"

	"github.com/golang-jwt/jwt/v5"
)

type Claims struct {
	jwt.RegisteredClaims
	GameID string `json:"gameId"`
}

func Verify(raw string, key ed25519.PublicKey) (string, error) {
	var c Claims
	_, err := jwt.ParseWithClaims(raw, &c, func(*jwt.Token) (any, error) { return key, nil },
		jwt.WithValidMethods([]string{"EdDSA"}), jwt.WithIssuer("cloud-gaming-backend"),
		jwt.WithAudience("game-streamer"), jwt.WithExpirationRequired(), jwt.WithIssuedAt())
	if err != nil {
		return "", err
	}
	if c.Subject == "" || c.ID == "" || c.GameID != "doom-1993" || c.IssuedAt == nil || !c.ExpiresAt.After(c.IssuedAt.Time) {
		return "", errors.New("invalid allocation claims")
	}
	return c.Subject, nil
}
