package gmailauth

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"testing"

	"github.com/golang-jwt/jwt/v5"
)

// genTestKey produces a PEM-encoded RSA private key for signing tests.
func genTestKey(t *testing.T) []byte {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
}

func TestSignJWTProducesValidToken(t *testing.T) {
	key := genTestKey(t)
	claims := jwt.MapClaims{"iss": "sa@project.iam.gserviceaccount.com", "sub": "me@example.com"}
	signed, err := signJWT(claims, key)
	if err != nil {
		t.Fatalf("signJWT: %v", err)
	}
	if signed == "" {
		t.Fatal("empty signed token")
	}
	// The token must parse and verify with the public key.
	tok, err := jwt.Parse(signed, func(t *jwt.Token) (any, error) {
		// Keep the original public key: re-derive from the private key.
		pk, _ := jwt.ParseRSAPrivateKeyFromPEM(key)
		return &pk.PublicKey, nil
	})
	if err != nil {
		t.Fatalf("parse signed token: %v", err)
	}
	if !tok.Valid {
		t.Fatal("token not valid")
	}
}

func TestSignJWTBadKey(t *testing.T) {
	if _, err := signJWT(jwt.MapClaims{}, []byte("not a pem key")); err == nil {
		t.Fatal("expected error for bad PEM key")
	}
}
