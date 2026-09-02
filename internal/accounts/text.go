package accounts

import (
	"encoding/base64"
	"strings"

	"github.com/jaytaylor/html2text"
)

// decodeBase64URL decodes Gmail's base64url-encoded body data. It tolerates both
// raw (no padding) and padded URL-encoding.
func decodeBase64URL(s string) string {
	if s == "" {
		return ""
	}
	for _, enc := range []*base64.Encoding{
		base64.RawURLEncoding,
		base64.URLEncoding,
		base64.RawStdEncoding,
		base64.StdEncoding,
	} {
		if b, err := enc.DecodeString(s); err == nil {
			return string(b)
		}
	}
	return s
}

// htmlToText converts an HTML email body to clean plain text, dropping tags,
// scripts/styles and formatting so the TUI shows readable prose rather than
// markup. It falls back to the raw HTML string on a conversion error.
func htmlToText(html string) string {
	if html == "" {
		return ""
	}
	text, err := html2text.FromString(html, html2text.Options{TextOnly: true})
	if err != nil {
		return strings.TrimSpace(html)
	}
	return strings.TrimSpace(text)
}

// decodeBody decodes a JMAP bodyValues part value, honouring its encoding
// (JMAP commonly base64-encodes text body parts).
func decodeBody(encoding, value string) string {
	if value == "" {
		return ""
	}
	if strings.EqualFold(encoding, "base64") {
		if b, err := base64.StdEncoding.DecodeString(value); err == nil {
			return string(b)
		}
		if b, err := base64.RawStdEncoding.DecodeString(value); err == nil {
			return string(b)
		}
	}
	return value
}

// addr returns an address string, preferring a non-empty email address.
func addr(email, name string) string {
	if email != "" {
		return email
	}
	return name
}

// splitToList splits a comma/space-separated To/Cc header into addresses.
func splitToList(s string) []string {
	parts := strings.FieldsFunc(s, func(r rune) bool {
		return r == ',' || r == ';'
	})
	var out []string
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
