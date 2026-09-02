// Package config resolves sift's runtime configuration by reusing the token and
// credential setup already present from OpenClaw (Fastmail JMAP keychain item +
// the authed gog Gmail CLI), without copying secrets into the repository.
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/jokull/sift/internal/jmap"
)

// Config is the resolved runtime configuration.
type Config struct {
	DeepSeek  DeepSeekConfig  `json:"deepseek"`
	Fastmail  *FastmailConfig `json:"fastmail,omitempty"`
	Gmail     *GmailConfig    `json:"gmail,omitempty"`
	Whitelist []string        `json:"whitelist,omitempty"`
}

type DeepSeekConfig struct {
	BaseURL string `json:"base_url"`
	Model   string `json:"model"`
	APIKey  string `json:"api_key,omitempty"`
}

type FastmailConfig struct {
	User      string `json:"user"`
	Token     string `json:"token,omitempty"` // resolved from keychain unless literal in file
	AccountID string `json:"account_id"`
	APIURL    string `json:"api_url"`
	Folders   Folders `json:"folders"`
}

type Folders struct {
	Inbox    string `json:"inbox"`
	Archive  string `json:"archive"`
	Receipts string `json:"receipts"`
	Reading  string `json:"reading"`
	Spam     string `json:"spam"`
	Trash    string `json:"trash"`
}

type GmailConfig struct {
	Account       string `json:"account"`
	GogBin        string `json:"gog_bin"`
	InboxLabel    string `json:"inbox_label"`
	ReceiptsLabel string `json:"receipts_label"`
	ReadingLabel  string `json:"reading_label"`
	SpamLabel     string `json:"spam_label"`
	TrashLabel    string `json:"trash_label"`

	// Auth for direct Gmail access over SSH (bypasses gog's macOS keychain).
	AccessToken   string `json:"access_token,omitempty"`   // short-lived (~1h)
	ServiceAccount string `json:"service_account_json,omitempty"` // path to Workspace SA key (domain-wide delegation)
	ClientID      string `json:"client_id,omitempty"`
	ClientSecret  string `json:"client_secret,omitempty"`
	RefreshToken  string `json:"refresh_token,omitempty"`

	EnsureLabels bool `json:"-"`
}

// FileConfig mirrors Config for a tolerant, partial override file. Empty
// strings mean "keep the discovered default".
type FileConfig struct {
	DeepSeek *DeepSeekConfig                    `toml:"deepseek"`
	Fastmail *struct {
		User      string  `toml:"user"`
		Token    string  `toml:"token"`
		AccountID string `toml:"account_id"`
		APIURL   string  `toml:"api_url"`
		Folders  Folders `toml:"folders"`
	} `toml:"fastmail"`
	Gmail *struct {
		Account        string `toml:"account"`
		GogBin         string `toml:"gog_bin"`
		InboxLabel     string `toml:"inbox_label"`
		ReceiptsLabel  string `toml:"receipts_label"`
		ReadingLabel   string `toml:"reading_label"`
		SpamLabel      string `toml:"spam_label"`
		TrashLabel     string `toml:"trash_label"`
		AccessToken    string `toml:"access_token"`
		ServiceAccount string `toml:"service_account_json"`
		ClientID       string `toml:"client_id"`
		ClientSecret   string `toml:"client_secret"`
		RefreshToken   string `toml:"refresh_token"`
	} `toml:"gmail"`
	Whitelist []string `toml:"whitelist"`
}

// Load resolves configuration, applying optional file overrides on top of the
// OpenClaw-derived defaults. filePath is empty for pure discovery.
func Load(filePath string) (*Config, error) {
	if filePath == "" {
		filePath = DefaultConfigPath()
	}
	file := loadFile(filePath)

	cfg := &Config{
		DeepSeek: DeepSeekConfig{
			BaseURL: "https://api.deepseek.com",
			Model:   "deepseek-v4-flash",
			APIKey:  os.Getenv("DEEPSEEK_API_KEY"),
		},
	}
	// Fall back to the OpenClaw .env for the DeepSeek key if not in the env.
	if cfg.DeepSeek.APIKey == "" {
		cfg.DeepSeek.APIKey = deepseekKeyFromOpenClaw()
	}

	// Fastmail: resolve the JMAP token from env → config file → keychain, then
	// discover the session. The keychain can be unavailable over SSH (exit 36),
	// so we let a token given in env/config win and produce an actionable error
	// otherwise.
	fileToken := ""
	if file != nil && file.Fastmail != nil {
		fileToken = file.Fastmail.Token
	}
	token, src, err := resolveFastmailToken(fileToken)
	if err != nil {
		return nil, err
	}
	if err := cfg.discoverFastmail(token, src); err != nil {
		return nil, fmt.Errorf("fastmail discovery: %w", err)
	}

	if err := cfg.discoverGmail(); err != nil {
		return nil, fmt.Errorf("gmail discovery: %w", err)
	}

	if file != nil {
		applyFile(cfg, file)
	}
	// If the file supplied a Fastmail token, prefer it over the keychain one.
	if fileToken != "" {
		cfg.Fastmail.Token = fileToken
	}
	// Gmail refresh-token/client creds from ~/.sift/gmail.env (written once on
	// the host, read over SSH) provide Gmail auth without the keychain/daemon.
	applyGmailEnv(cfg.Gmail)

	return cfg, nil
}

// DefaultConfigPath returns the config file location honouring XDG.
func DefaultConfigPath() string {
	if x := os.Getenv("SIFT_CONFIG"); x != "" {
		return x
	}
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, "sift", "config.toml")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "sift", "config.toml")
}

// ConfigPathDir returns the directory containing the config file.
func ConfigPathDir() string { return filepath.Dir(DefaultConfigPath()) }

func (c *Config) discoverFastmail(token, src string) error {
	const user = "jokull@solberg.is"
	sess, err := jmap.Discover(token)
	if err != nil {
		return fmt.Errorf("jmap session: %w", err)
	}
	acctID := sess.MailboxAccountID()
	if acctID == "" {
		return fmt.Errorf("jmap session resolved no primary mail account")
	}
	client := jmap.NewClient(token, sess.APIURL, acctID)
	boxes, err := client.Mailboxes()
	if err != nil {
		return fmt.Errorf("jmap mailboxes: %w", err)
	}
	f := Folders{}
	for _, b := range boxes {
		switch {
		case b.Role == "inbox":
			f.Inbox = b.ID
		case b.Role == "archive":
			f.Archive = b.ID
		case b.Name == "Receipts":
			f.Receipts = b.ID
		case b.Name == "Reading":
			f.Reading = b.ID
		case b.Role == "junk":
			f.Spam = b.ID
		case b.Role == "trash":
			f.Trash = b.ID
		}
	}
	c.Fastmail = &FastmailConfig{
		User:      user,
		Token:     token,
		AccountID: acctID,
		APIURL:    sess.APIURL,
		Folders:   f,
	}
	_ = src
	if c.Fastmail.Folders.Inbox == "" {
		return fmt.Errorf("fastmail discovery found no inbox mailbox")
	}
	return nil
}

func (c *Config) discoverGmail() error {
	gogBin, err := exec.LookPath("gog")
	if err != nil {
		gogBin = "gog" // let exec resolve at call time; surfaced as error there
	}
	c.Gmail = &GmailConfig{
		Account:       "jokull@triptojapan.com",
		GogBin:        gogBin,
		InboxLabel:    "INBOX",
		ReceiptsLabel: "Receipts",
		ReadingLabel:  "Reading",
		SpamLabel:     "SPAM",
		TrashLabel:    "TRASH",
	}
	c.Gmail.EnsureLabels = true
	return nil
}

func applyFile(cfg *Config, f *FileConfig) {
	if f.DeepSeek != nil {
		if f.DeepSeek.BaseURL != "" {
			cfg.DeepSeek.BaseURL = f.DeepSeek.BaseURL
		}
		if f.DeepSeek.Model != "" {
			cfg.DeepSeek.Model = f.DeepSeek.Model
		}
		if f.DeepSeek.APIKey != "" {
			cfg.DeepSeek.APIKey = f.DeepSeek.APIKey
		}
	}
	if f.Fastmail != nil {
		if f.Fastmail.User != "" {
			cfg.Fastmail.User = f.Fastmail.User
		}
		if f.Fastmail.Token != "" {
			cfg.Fastmail.Token = f.Fastmail.Token
		}
		if f.Fastmail.AccountID != "" {
			cfg.Fastmail.AccountID = f.Fastmail.AccountID
		}
		if f.Fastmail.APIURL != "" {
			cfg.Fastmail.APIURL = f.Fastmail.APIURL
		}
		if f.Fastmail.Folders.Inbox != "" {
			cfg.Fastmail.Folders.Inbox = f.Fastmail.Folders.Inbox
		}
		if f.Fastmail.Folders.Archive != "" {
			cfg.Fastmail.Folders.Archive = f.Fastmail.Folders.Archive
		}
		if f.Fastmail.Folders.Receipts != "" {
			cfg.Fastmail.Folders.Receipts = f.Fastmail.Folders.Receipts
		}
		if f.Fastmail.Folders.Reading != "" {
			cfg.Fastmail.Folders.Reading = f.Fastmail.Folders.Reading
		}
		if f.Fastmail.Folders.Spam != "" {
			cfg.Fastmail.Folders.Spam = f.Fastmail.Folders.Spam
		}
		if f.Fastmail.Folders.Trash != "" {
			cfg.Fastmail.Folders.Trash = f.Fastmail.Folders.Trash
		}
	}
	if f.Gmail != nil {
		if f.Gmail.Account != "" {
			cfg.Gmail.Account = f.Gmail.Account
		}
		if f.Gmail.GogBin != "" {
			cfg.Gmail.GogBin = f.Gmail.GogBin
		}
		if f.Gmail.InboxLabel != "" {
			cfg.Gmail.InboxLabel = f.Gmail.InboxLabel
		}
		if f.Gmail.ReceiptsLabel != "" {
			cfg.Gmail.ReceiptsLabel = f.Gmail.ReceiptsLabel
		}
		if f.Gmail.ReadingLabel != "" {
			cfg.Gmail.ReadingLabel = f.Gmail.ReadingLabel
		}
		if f.Gmail.SpamLabel != "" {
			cfg.Gmail.SpamLabel = f.Gmail.SpamLabel
		}
		if f.Gmail.TrashLabel != "" {
			cfg.Gmail.TrashLabel = f.Gmail.TrashLabel
		}
		if f.Gmail.AccessToken != "" {
			cfg.Gmail.AccessToken = f.Gmail.AccessToken
		}
		if f.Gmail.ServiceAccount != "" {
			cfg.Gmail.ServiceAccount = f.Gmail.ServiceAccount
		}
		if f.Gmail.ClientID != "" {
			cfg.Gmail.ClientID = f.Gmail.ClientID
		}
		if f.Gmail.ClientSecret != "" {
			cfg.Gmail.ClientSecret = f.Gmail.ClientSecret
		}
		if f.Gmail.RefreshToken != "" {
			cfg.Gmail.RefreshToken = f.Gmail.RefreshToken
		}
	}
	if len(f.Whitelist) > 0 {
		cfg.Whitelist = f.Whitelist
	}
}

func loadFile(path string) *FileConfig {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var f FileConfig
	if err := tomlUnmarshal(b, &f); err != nil {
		return nil
	}
	return &f
}

func tomlUnmarshal(b []byte, v *FileConfig) error {
	return toml.Unmarshal(b, v)
}

func keychainSecret(account, service string) (string, error) {
	out, err := exec.Command("security", "find-generic-password",
		"-a", account, "-s", service, "-w").Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// resolveFastmailToken sources the JMAP token from, in order: env, config file,
// macOS keychain, then the legacy ME_FASTMAIL_PASS env var. It returns a
// descriptive source for diagnostics.
func resolveFastmailToken(fileToken string) (string, string, error) {
	const user = "jokull@solberg.is"

	if v := strings.TrimSpace(os.Getenv("SIFT_FASTMAIL_JMAP_TOKEN")); v != "" {
		return v, "env SIFT_FASTMAIL_JMAP_TOKEN", nil
	}
	if fileToken != "" {
		return fileToken, "config file", nil
	}
	if kc, err := keychainSecret(user, "fastmail-jmap"); err == nil {
		return kc, "keychain", nil
	} else if isKeychainInteractionError(err) {
		// SSH sessions often cannot unlock the login keychain. Suggest the env
		// route rather than failing cryptically.
		if v := strings.TrimSpace(os.Getenv("ME_FASTMAIL_PASS")); v != "" {
			return v, "env ME_FASTMAIL_PASS", nil
		}
		return "", "", fmt.Errorf("macOS keychain is not accessible here (%s). If you are over SSH, establish the token with `sift setup` (or set SIFT_FASTMAIL_JMAP_TOKEN) and retry.", errHint(err))
	}
	if v := strings.TrimSpace(os.Getenv("ME_FASTMAIL_PASS")); v != "" {
		return v, "env ME_FASTMAIL_PASS", nil
	}
	return "", "", fmt.Errorf("could not read the Fastmail JMAP token. Set SIFT_FASTMAIL_JMAP_TOKEN, or run `sift setup` to write it to the sift config.")
}

// isKeychainInteractionError reports whether a `security` failure is due to
// user interaction being disallowed (e.g. locked keychain over SSH), which we
// want to route towards the env/config fallback.
func isKeychainInteractionError(err error) bool {
	ee, ok := err.(*exec.ExitError)
	if !ok {
		return false
	}
	// errSecInteractionNotAllowed surfaces as exit status 36 from the CLI.
	return ee.ExitCode() == 36
}

// errHint extracts a short stderr tail from a security failure for the message.
func errHint(err error) string {
	if ee, ok := err.(*exec.ExitError); ok {
		s := strings.TrimSpace(string(ee.Stderr))
		if s != "" {
			return truncate(s, 120)
		}
	}
	return err.Error()
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// ReadFastmailToken returns the JMAP token from env or the macOS keychain.
func ReadFastmailToken() (string, error) {
	const user = "jokull@solberg.is"
	if v := strings.TrimSpace(os.Getenv("SIFT_FASTMAIL_JMAP_TOKEN")); v != "" {
		return v, nil
	}
	token, err := keychainSecret(user, "fastmail-jmap")
	if err != nil {
		return "", fmt.Errorf("read %s/fastmail-jmap from keychain: %w", user, err)
	}
	return token, nil
}

// ReadGogClientCredentials returns gog's OAuth client id/secret from the gogcli
// credentials file, used to configure the refresh-token path over SSH.
func ReadGogClientCredentials() (clientID, clientSecret string, err error) {
	home, _ := os.UserHomeDir()
	path := filepath.Join(home, "Library", "Application Support", "gogcli", "credentials.json")
	b, err := os.ReadFile(path)
	if err != nil {
		return "", "", err
	}
	var c struct {
		ClientID     string `json:"client_id"`
		ClientSecret string `json:"client_secret"`
	}
	if err := json.Unmarshal(b, &c); err != nil {
		return "", "", err
	}
	return c.ClientID, c.ClientSecret, nil
}

// WriteFastmailToken persists the JMAP token into the sift config file, merging
// it into an existing [fastmail] section (or appending one). This lets sift run
// without the keychain, which is often unavailable over SSH.
func WriteFastmailToken(path, token string) error {
	return upsertConfigKey(path, "fastmail", "token", token)
}

// WriteGmailClient persists gog's OAuth client id/secret into the [gmail]
// section of the config, so the refresh-token path is ready over SSH.
func WriteGmailClient(path, clientID, clientSecret string) error {
	if err := upsertConfigKey(path, "gmail", "client_id", clientID); err != nil {
		return err
	}
	return upsertConfigKey(path, "gmail", "client_secret", clientSecret)
}

// upsertConfigKey sets a single `key = "value"` inside a TOML section, creating
// the section if needed and preserving the rest of the file.
func upsertConfigKey(path, section, key, value string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	b, _ := os.ReadFile(path)
	lines := strings.Split(strings.TrimRight(string(b), "\n"), "\n")

	out := []string{}
	inserted := false
	inSection := false
	for _, ln := range lines {
		t := strings.TrimSpace(ln)
		isSection := strings.HasPrefix(t, "[") && strings.HasSuffix(t, "]") && !strings.HasPrefix(t, "[[")
		if isSection {
			inSection = t == "["+section+"]"
			out = append(out, ln)
			if inSection && !inserted {
				out = append(out, key+" = "+tomlQuote(value))
				inserted = true
			}
			continue
		}
		if inSection {
			if strings.HasPrefix(t, key+" ") || strings.HasPrefix(t, key+"=") {
				continue // drop a stale value; the fresh one was inserted above
			}
			if t == "" && !inserted {
				continue
			}
		}
		out = append(out, ln)
	}
	if !inserted {
		if len(out) > 0 && out[len(out)-1] != "" {
			out = append(out, "")
		}
		out = append(out, "["+section+"]", key+" = "+tomlQuote(value))
	}
	return os.WriteFile(path, []byte(strings.Join(out, "\n")+"\n"), 0o600)
}

func tomlQuote(s string) string {
	return `"` + tomlEscape(s) + `"`
}

func tomlEscape(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`)
	return r.Replace(s)
}

func deepseekKeyFromOpenClaw() string {
	home, _ := os.UserHomeDir()
	b, err := os.ReadFile(filepath.Join(home, ".openclaw", ".env"))
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "DEEPSEEK_API_KEY=") {
			return strings.TrimPrefix(line, "DEEPSEEK_API_KEY=")
		}
	}
	return ""
}
