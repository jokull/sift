// Package model defines the account-agnostic types shared across sift.
package model

import (
	"strings"
	"time"
)

// Account identifies a connected mail account.
type Account string

const (
	AccountFastmail Account = "fastmail" // personal jokull@solberg.is
	AccountGmail    Account = "gmail"    // work jokull@triptojapan.com
)

// Thread is a conversation as presented in the triage UI. It is merged across
// accounts and carries an account-native ID plus the metadata needed to
// classify and act on it.
type Thread struct {
	ID           string    // account-native thread id (JMAP threadId or Gmail threadId)
	Account      Account   // which mailbox it came from
	Subject      string    //
	FromName     string    // display name of the originating sender
	FromEmail    string    // sender address (lower-cased)
	Date         time.Time // most recent message time (UTC)
	Unread       int       // number of unread messages in the thread
	MessageCount int       // number of messages in the thread
	Snippet      string    // short body preview
	Labels       []string  // account-native labels/mailbox roles (inbox it resides in)
}

// SenderKey is the identity used for per-sender decisions.
func (t *Thread) SenderKey() string {
	if t.FromEmail != "" {
		return t.FromEmail
	}
	return t.FromName
}

// SenderGroup is the normalized "logical sender" used for cohort aggregation and
// per-sender decisions. It collapses campaign/ESP aliases (no-reply@email.apple.com
// vs updates@apple.com) onto their registered domain so "how many from this sender"
// reflects the brand, not a one-off address. Falls back to the display name.
func (t *Thread) SenderGroup() string {
	if t.FromEmail != "" {
		at := strings.LastIndex(t.FromEmail, "@")
		if at >= 0 {
			return baseDomain(strings.ToLower(t.FromEmail[at+1:]))
		}
		return strings.ToLower(t.FromEmail)
	}
	return strings.ToLower(t.FromName)
}

// baseDomain returns the registered domain (last two labels) of a host, which
// groups subdomains like email.apple.com and news.apple.com under apple.com.
func baseDomain(host string) string {
	labels := strings.Split(host, ".")
	if len(labels) >= 2 {
		return labels[len(labels)-2] + "." + labels[len(labels)-1]
	}
	return host
}

// IsToday reports whether the thread's most recent message arrived on the given
// local date. Today's mail is deliberately left untouched by triage.
func (t *Thread) IsToday(now time.Time) bool {
	nyt := t.Date.In(now.Location())
	nt := now.In(now.Location())
	y, m, d := nyt.Date()
	yt, mt, dt := nt.Date()
	return y == yt && m == mt && d == dt
}

// Message is a single email within a thread (used for the detail pane).
type Message struct {
	ID        string
	ThreadID  string
	Account   Account
	FromName  string
	FromEmail string
	To        []string
	Subject   string
	Date      time.Time
	Snippet   string
	BodyText  string // plain-text body, trimmed
}
