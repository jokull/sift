// Package gmailauth mints Google OAuth access tokens for the Gmail account using
// either a Workspace service account (domain-wide delegation) or a classic OAuth
// refresh token. The resulting access token is handed to gog via
// GOG_ACCESS_TOKEN, so sift can talk to Gmail over SSH without reading the
// macOS keychain.
package gmailauth

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const (
	gmailScope    = "https://mail.google.com/"
	tokenEndpoint = "https://oauth2.googleapis.com/token"
)

// ServiceAccountJSON is the subset of a Google service-account key we need.
type ServiceAccountJSON struct {
	ClientEmail string `json:"client_email"`
	PrivateKey  string `json:"private_key"`
	TokenURI    string `json:"token_uri"`
}

// AuthToken is an OAuth access token plus its expiry (used to cache/refresh).
type AuthToken struct {
	AccessToken string
	Expiry      time.Time
}

// NewServiceAccountToken mints an access token for subject by signing a JWT with
// the service account's private key (domain-wide delegation). The Workspace
// admin must have granted the service account's client_id the mail.google.com
// scope for the subject's mailbox.
func NewServiceAccountToken(ctx context.Context, keyJSON []byte, subject string) (string, error) {
	var sa ServiceAccountJSON
	if err := json.Unmarshal(keyJSON, &sa); err != nil {
		return "", fmt.Errorf("parse service account key: %w", err)
	}
	if sa.ClientEmail == "" || sa.PrivateKey == "" {
		return "", fmt.Errorf("service account key missing client_email or private_key")
	}
	tokenURI := sa.TokenURI
	if tokenURI == "" {
		tokenURI = tokenEndpoint
	}
	if subject == "" {
		return "", fmt.Errorf("impersonation subject is required for service accounts")
	}

	now := time.Now()
	claims := jwt.MapClaims{
		"iss":   sa.ClientEmail,
		"sub":   subject,
		"aud":   tokenURI,
		"iat":   now.Unix(),
		"exp":   now.Add(time.Hour).Unix(),
		"scope": gmailScope,
	}
	signed, err := signJWT(claims, []byte(sa.PrivateKey))
	if err != nil {
		return "", err
	}

	form := url.Values{}
	form.Set("grant_type", "urn:ietf:params:oauth:grant-type:jwt-bearer")
	form.Set("assertion", signed)
	return exchangeToken(ctx, tokenURI, form)
}

// signJWT signs an RS256 JWT with a PEM-encoded RSA private key.
func signJWT(claims jwt.MapClaims, keyPEM []byte) (string, error) {
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	pk, err := jwt.ParseRSAPrivateKeyFromPEM(keyPEM)
	if err != nil {
		return "", fmt.Errorf("parse service-account private key: %w", err)
	}
	signed, err := tok.SignedString(pk)
	if err != nil {
		return "", fmt.Errorf("sign service-account jwt: %w", err)
	}
	return signed, nil
}

// RefreshAccessToken exchanges a refresh token for a fresh access token using the
// supplied OAuth client credentials.
func RefreshAccessToken(ctx context.Context, clientID, clientSecret, refreshToken string) (string, error) {
	form := url.Values{}
	form.Set("grant_type", "refresh_token")
	form.Set("client_id", clientID)
	form.Set("client_secret", clientSecret)
	form.Set("refresh_token", refreshToken)
	return exchangeToken(ctx, tokenEndpoint, form)
}

// exchangeToken POSTs the form to the token endpoint and returns access_token.
func exchangeToken(ctx context.Context, endpoint string, form url.Values) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var out struct {
		AccessToken string `json:"access_token"`
		Error       string `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", fmt.Errorf("decode token response: %w", err)
	}
	if out.Error != "" {
		return "", fmt.Errorf("token endpoint error: %s", out.Error)
	}
	if out.AccessToken == "" {
		return "", fmt.Errorf("token endpoint returned no access_token")
	}
	return out.AccessToken, nil
}

// ReadServiceAccountFile reads and returns a service-account JSON from a path.
func ReadServiceAccountFile(path string) ([]byte, error) {
	return os.ReadFile(path)
}
