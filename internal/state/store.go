// Package state provides lightweight SQLite persistence for the classification
// cache and per-sender decisions (whitelist / archive / unsubscribe). This lets
// re-runs be instant and lets the engine remember sender choices across sessions.
package state

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/jokull/sift/internal/model"

	_ "modernc.org/sqlite"
)

// Store wraps a SQLite database.
type Store struct {
	db *sql.DB
}

// Open opens (creating if needed) the database at path.
func Open(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	// A busy timeout avoids "database is locked" bursts from the async worker.
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`PRAGMA journal_mode=WAL; PRAGMA busy_timeout=5000;`); err != nil {
		return nil, err
	}
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		return nil, err
	}
	return s, nil
}

// DefaultPath returns the state database location honoured by XDG.
func DefaultPath() string {
	if x := os.Getenv("SIFT_STATE"); x != "" {
		return x
	}
	if x := os.Getenv("XDG_DATA_HOME"); x != "" {
		return filepath.Join(x, "sift", "state.db")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "share", "sift", "state.db")
}

func (s *Store) migrate() error {
	_, err := s.db.Exec(`
CREATE TABLE IF NOT EXISTS classification (
  account TEXT NOT NULL,
  thread_id TEXT NOT NULL,
  category TEXT NOT NULL,
  action TEXT NOT NULL,
  confidence REAL NOT NULL DEFAULT 0,
  reason TEXT NOT NULL DEFAULT '',
  sender_wide INTEGER NOT NULL DEFAULT 0,
  ts INTEGER NOT NULL,
  PRIMARY KEY (account, thread_id)
);
CREATE TABLE IF NOT EXISTS sender_decision (
  sender TEXT PRIMARY KEY,
  action TEXT NOT NULL,
  ts INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS whitelist (
  sender TEXT PRIMARY KEY,
  ts INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS unsubscribed (
  sender TEXT PRIMARY KEY,
  ts INTEGER NOT NULL
);
`)
	return err
}

// SaveClassification upserts a per-thread prediction.
func (s *Store) SaveClassification(account, threadID string, p model.Prediction) error {
	_, err := s.db.Exec(`INSERT INTO classification(account, thread_id, category, action, confidence, reason, sender_wide, ts)
		VALUES(?,?,?,?,?,?,?,?)
		ON CONFLICT(account,thread_id) DO UPDATE SET
		category=excluded.category, action=excluded.action, confidence=excluded.confidence,
		reason=excluded.reason, sender_wide=excluded.sender_wide, ts=excluded.ts`,
		account, threadID, p.Category, p.Action, p.Confidence, p.Reason, boolInt(p.SenderWide), time.Now().Unix())
	return err
}

// Classification returns a cached prediction, if any.
func (s *Store) Classification(account, threadID string) (model.Prediction, bool, error) {
	row := s.db.QueryRow(`SELECT category, action, confidence, reason, sender_wide FROM classification WHERE account=? AND thread_id=?`, account, threadID)
	var (
		cat, action, reason      string
		conf                     float64
		senderWide               int
	)
	if err := row.Scan(&cat, &action, &conf, &reason, &senderWide); err != nil {
		if err == sql.ErrNoRows {
			return model.Prediction{}, false, nil
		}
		return model.Prediction{}, false, err
	}
	return model.Prediction{
		Category:   model.Category(cat),
		Action:     model.Action(action),
		Confidence: conf,
		Reason:     reason,
		SenderWide: senderWide == 1,
	}, true, nil
}

// SaveSenderDecision remembers a per-sender action (archive/keep/unsubscribe).
func (s *Store) SaveSenderDecision(sender string, action model.Action) error {
	_, err := s.db.Exec(`INSERT INTO sender_decision(sender,action,ts) VALUES(?,?,?)
		ON CONFLICT(sender) DO UPDATE SET action=excluded.action, ts=excluded.ts`,
		sender, action, time.Now().Unix())
	return err
}

// SenderDecision returns the remembered action for a sender, if any.
func (s *Store) SenderDecision(sender string) (model.Action, bool, error) {
	var action string
	err := s.db.QueryRow(`SELECT action FROM sender_decision WHERE sender=?`, sender).Scan(&action)
	if err != nil {
		if err == sql.ErrNoRows {
			return "", false, nil
		}
		return "", false, err
	}
	return model.Action(action), true, nil
}

// AddWhitelist whitelists a sender so promotions are kept.
func (s *Store) AddWhitelist(sender string) error {
	_, err := s.db.Exec(`INSERT INTO whitelist(sender,ts) VALUES(?,?) ON CONFLICT(sender) DO NOTHING`, sender, time.Now().Unix())
	return err
}

// IsWhitelisted reports whether a sender is whitelisted.
func (s *Store) IsWhitelisted(sender string) bool {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(1) FROM whitelist WHERE sender=?`, sender).Scan(&n)
	return err == nil && n > 0
}

// AddUnsubscribed marks a sender as unsubscribed (bulk archive going forward).
func (s *Store) AddUnsubscribed(sender string) error {
	_, err := s.db.Exec(`INSERT INTO unsubscribed(sender,ts) VALUES(?,?) ON CONFLICT(sender) DO NOTHING`, sender, time.Now().Unix())
	return err
}

// IsUnsubscribed reports whether a sender has been unsubscribed.
func (s *Store) IsUnsubscribed(sender string) bool {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(1) FROM unsubscribed WHERE sender=?`, sender).Scan(&n)
	return err == nil && n > 0
}

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// Ensure package name usage.
var _ = fmt.Sprintf
