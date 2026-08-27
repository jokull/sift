// Package config resolves sift's runtime configuration by reusing the token and
// credential setup already present from OpenClaw (Fastmail JMAP keychain item +
// the authed gog Gmail CLI), without copying secrets into the repository.
package config

import (
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
}

type GmailConfig struct {
	Account       string `json:"account"`
	GogBin        string `json:"gog_bin"`
	InboxLabel    string `json:"inbox_label"`
	ReceiptsLabel string `json:"receipts_label"`
	ReadingLabel  string `json:"reading_label"`
	EnsureLabels  bool   `json:"-"`
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
		Account       string `toml:"account"`
		GogBin        string `toml:"gog_bin"`
		InboxLabel    string `toml:"inbox_label"`
		ReceiptsLabel string `toml:"receipts_label"`
		ReadingLabel  string `toml:"reading_label"`
	} `toml:"gmail"`
	Whitelist []string `toml:"whitelist"`
}

// Load resolves configuration, applying optional file overrides on top of the
// OpenClaw-derived defaults. filePath is empty for pure discovery.
func Load(filePath string) (*Config, error) {
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

	if err := cfg.discoverFastmail(); err != nil {
		return nil, fmt.Errorf("fastmail discovery: %w", err)
	}
	if err := cfg.discoverGmail(); err != nil {
		return nil, fmt.Errorf("gmail discovery: %w", err)
	}

	if filePath == "" {
		filePath = DefaultConfigPath()
	}
	if file := loadFile(filePath); file != nil {
		applyFile(cfg, file)
	}

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

func (c *Config) discoverFastmail() error {
	const user = "jokull@solberg.is"
	token, err := keychainSecret(user, "fastmail-jmap")
	if err != nil {
		return fmt.Errorf("read JMAP token from keychain: %w", err)
	}
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
		}
	}
	c.Fastmail = &FastmailConfig{
		User:      user,
		Token:     token,
		AccountID: acctID,
		APIURL:    sess.APIURL,
		Folders:   f,
	}
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
