// Package jmap is a minimal Fastmail JMAP client (core + mail) used by sift for
// the personal account. It speaks bare RFC 8620 method calls over HTTP with a
// bearer token, which is all the triage workload needs.
package jmap

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const (
	Core = "urn:ietf:params:jmap:core"
	Mail = "urn:ietf:params:jmap:mail"
	// WellKnown is Fastmail's account-discovery resource for JMAP users.
	WellKnown = "https://api.fastmail.com/.well-known/jmap"
)

// Session is the relevant subset of the Fastmail JMAP session resource.
type Session struct {
	APIURL          string            `json:"apiUrl"`
	PrimaryAccounts map[string]string `json:"primaryAccounts"`
	Accounts        map[string]Account `json:"accounts"`
	DownloadURL     string            `json:"downloadUrl"`
}

// Account is the per-user JMAP account identity.
type Account struct {
	Name        string `json:"name"`
	IsReadOnly  bool   `json:"isReadOnly"`
}

// Mailbox is a folder in the JMAP Mailbox/Email model.
type Mailbox struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Role    string `json:"role"`
	Parent string `json:"parentId"`
	Total   int    `json:"totalEmails"`
	Unread  int    `json:"unreadEmails"`
}

// Client performs JMAP method calls against a resolved API URL.
type Client struct {
	token  string
	apiURL string
	acctID string
	http   *http.Client
}

// Discover resolves the session resource for a bearer token.
func Discover(token string) (*Session, error) {
	sess := &Session{}
	if err := doJSON(http.MethodGet, WellKnown, token, nil, sess, nil); err != nil {
		return nil, err
	}
	if sess.APIURL == "" && sess.PrimaryAccounts == nil {
		return nil, fmt.Errorf("session missing apiUrl/primaryAccounts")
	}
	return sess, nil
}

// NewClient builds a client bound to an account. Use Session to get apiUrl and
// the primary mail account id first.
func NewClient(token, apiURL, accountID string) *Client {
	return &Client{
		token:  token,
		apiURL: strings.TrimRight(apiURL, "/"),
		acctID: accountID,
		http:   &http.Client{Timeout: 30 * time.Second},
	}
}

// SessionAccountID returns the primary mail account id for a session.
func (s *Session) SessionAccountID() string {
	if s.PrimaryAccounts == nil {
		return ""
	}
	id, _ := s.PrimaryAccounts[Mail]
	if id != "" {
		return id
	}
	for k, v := range s.PrimaryAccounts {
		return k + "|" + v
	}
	return ""
}

// MailboxID returns the account id (session primary mail account).
func (s *Session) MailboxAccountID() string {
	if id := s.SessionAccountID(); strings.Contains(id, "|") {
		parts := strings.SplitN(id, "|", 2)
		return parts[1]
	}
	return s.SessionAccountID()
}

// Mailboxes lists all folders for the account.
func (c *Client) Mailboxes() ([]Mailbox, error) {
	var out struct {
		MethodResponses [][]json.RawMessage `json:"methodResponses"`
	}
	err := c.Call(&out, NewCall("Mailbox/get", map[string]any{"accountId": c.acctID, "ids": nil}, "m0"))
	if err != nil {
		return nil, err
	}
	for _, resp := range out.MethodResponses {
		var name string
		_ = json.Unmarshal(resp[0], &name)
		if name != "Mailbox/get" {
			continue
		}
		var args struct {
			List []Mailbox `json:"list"`
		}
		if err := json.Unmarshal(resp[1], &args); err != nil {
			return nil, err
		}
		return args.List, nil
	}
	return nil, fmt.Errorf("no Mailbox/get response")
}

// Call performs one or more method calls and fills `out` with the raw envelope.
func (c *Client) Call(out any, calls ...Call) error {
	req := map[string]any{
		"using":       []string{Core, Mail},
		"methodCalls": calls,
	}
	var buf bytes.Buffer
	if err := json.NewEncoder(&buf).Encode(req); err != nil {
		return err
	}
	resp, err := doRaw(c.http, c.apiURL+"/", c.token, &buf)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("jmap %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	return json.Unmarshal(body, out)
}

// Call is an ordered JMAP method invocation.
type Call struct {
	Name string
	Args map[string]any
	Tag  string
}

// NewCall builds a Call.
func NewCall(name string, args map[string]any, tag string) Call {
	return Call{Name: name, Args: args, Tag: tag}
}

// MarshalJSON encodes a Call as [name, args, tag].
func (c Call) MarshalJSON() ([]byte, error) {
	return json.Marshal([]any{c.Name, c.Args, c.Tag})
}

// doJSON performs a request, decodes the response body into out (optional), and
// follows redirects while preserving the Authorization header.
func doJSON(method, url, token string, in io.Reader, out any, hdr http.Header) error {
	req, err := http.NewRequest(method, url, in)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if hdr != nil {
		for k, v := range hdr {
			req.Header[k] = v
		}
	}
	// Fastmail redirects the well-known resource to the account session URL; a
	// custom client keeps the bearer token across those hops.
	client := &http.Client{
		Timeout: 30 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return fmt.Errorf("too many redirects")
			}
			req.Header.Set("Authorization", "Bearer "+token)
			return nil
		},
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("%s %s: %s", method, resp.Status, strings.TrimSpace(string(body)))
	}
	if out != nil {
		return json.Unmarshal(body, out)
	}
	return nil
}

func doRaw(client *http.Client, url, token string, body *bytes.Buffer) (*http.Response, error) {
	req, err := http.NewRequest(http.MethodPost, url, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	return client.Do(req)
}
