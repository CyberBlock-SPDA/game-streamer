package auth

import (
	"crypto/ed25519"
	"crypto/rand"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

func TestSignedClaims(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	_, other, _ := ed25519.GenerateKey(rand.Reader)
	for _, tt := range []struct {
		name   string
		change func(*Claims)
		key    ed25519.PrivateKey
		ok     bool
	}{
		{"valid", func(*Claims) {}, priv, true}, {"wrong signing key", func(*Claims) {}, other, false},
		{"expired", func(c *Claims) { c.ExpiresAt = jwt.NewNumericDate(time.Now().Add(-time.Minute)) }, priv, false},
		{"issuer", func(c *Claims) { c.Issuer = "elsewhere" }, priv, false}, {"audience", func(c *Claims) { c.Audience = nil }, priv, false},
		{"game", func(c *Claims) { c.GameID = "another" }, priv, false}, {"subject", func(c *Claims) { c.Subject = "" }, priv, false},
		{"missing expiry", func(c *Claims) { c.ExpiresAt = nil }, priv, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			c := Claims{RegisteredClaims: jwt.RegisteredClaims{Issuer: "cloud-gaming-backend", Audience: jwt.ClaimStrings{"game-streamer"}, Subject: "s", ID: "j", IssuedAt: jwt.NewNumericDate(time.Now()), ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour))}, GameID: "doom-1993"}
			tt.change(&c)
			token, err := jwt.NewWithClaims(jwt.SigningMethodEdDSA, c).SignedString(tt.key)
			if err != nil {
				t.Fatal(err)
			}
			id, err := Verify(token, pub)
			if (err == nil) != tt.ok || tt.ok && id != "s" {
				t.Fatalf("subject %q error %v", id, err)
			}
		})
	}
}
