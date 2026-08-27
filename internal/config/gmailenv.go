package config

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// GmailEnvPath is the machine-local file holding the Gmail refresh token + OAuth
// client credentials for sift. It is written once (`sift setup gmail-token`)
// from a GUI/login session and read over SSH.
func GmailEnvPath() string {
	if v := os.Getenv("SIFT_GMAIL_ENV"); v != "" {
		return v
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".sift", "gmail.env")
}

// gmailEnv is the parsed ~/.sift/gmail.env content.
type gmailEnv struct {
	RefreshToken string
	ClientID     string
	ClientSecret string
}

// loadGmailEnv reads the Gmail env file (empty struct if absent).
func loadGmailEnv() gmailEnv {
	path := GmailEnvPath()
	b, err := os.ReadFile(path)
	if err != nil {
		return gmailEnv{}
	}
	env := gmailEnv{}
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		v = strings.Trim(strings.TrimSpace(v), `"'`)
		switch k {
		case "SIFT_GMAIL_REFRESH_TOKEN":
			env.RefreshToken = v
		case "SIFT_GMAIL_CLIENT_ID":
			env.ClientID = v
		case "SIFT_GMAIL_CLIENT_SECRET":
			env.ClientSecret = v
		}
	}
	return env
}

// ReadGogRefreshToken extracts gog's Gmail refresh token from the login keychain.
// It may prompt for keychain access (approve it from the GUI/login session) and
// times out after 30s, so it does not hang a script.
func ReadGogRefreshToken() (string, error) {
	accounts := []string{"token:default:jokull@triptojapan.com", "token:jokull@triptojapan.com"}
	for _, acct := range accounts {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		out, err := exec.CommandContext(ctx, "security", "find-generic-password",
			"-a", acct, "-s", "gogcli", "-w").Output()
		cancel()
		if err != nil {
			continue
		}
		tok := strings.TrimSpace(string(out))
		if tok == "" {
			continue
		}
		// gog may store a JSON token object; fall back to the raw value.
		var parsed struct {
			RefreshToken string `json:"refresh_token"`
		}
		if json.Unmarshal([]byte(tok), &parsed) == nil && parsed.RefreshToken != "" {
			return parsed.RefreshToken, nil
		}
		return tok, nil
	}
	return "", fmt.Errorf("could not read gog's Gmail refresh token from the keychain (run from a GUI/login session and approve the prompt)")
}

// WriteGmailEnv writes the Gmail refresh token + client creds so sift can use
// them over SSH (mode 0600).
func WriteGmailEnv(path, refreshToken, clientID, clientSecret string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	content := fmt.Sprintf("# Written by 'sift setup gmail-token' (GUI/login session). Machine-local.\n"+
		"SIFT_GMAIL_REFRESH_TOKEN=%s\nSIFT_GMAIL_CLIENT_ID=%s\nSIFT_GMAIL_CLIENT_SECRET=%s\n",
		refreshToken, clientID, clientSecret)
	return os.WriteFile(path, []byte(content), 0o600)
}

// applyGmailEnv populates a GmailConfig from the env file (or env vars) if not
// already set.
func applyGmailEnv(cfg *GmailConfig) {
	if cfg == nil {
		return
	}
	env := loadGmailEnv()
	refresh := env.RefreshToken
	clientID := env.ClientID
	clientSecret := env.ClientSecret
	if v := os.Getenv("SIFT_GMAIL_REFRESH_TOKEN"); v != "" {
		refresh = v
	}
	if v := os.Getenv("SIFT_GMAIL_CLIENT_ID"); v != "" {
		clientID = v
	}
	if v := os.Getenv("SIFT_GMAIL_CLIENT_SECRET"); v != "" {
		clientSecret = v
	}
	if refresh != "" && cfg.RefreshToken == "" {
		cfg.RefreshToken = refresh
	}
	if clientID != "" && cfg.ClientID == "" {
		cfg.ClientID = clientID
	}
	if clientSecret != "" && cfg.ClientSecret == "" {
		cfg.ClientSecret = clientSecret
	}
}
