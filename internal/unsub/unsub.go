// Package unsub performs actual list-unsubscribes using the RFC 8058 one-click
// mechanism when the sender supports it, falling back to a plain GET on the
// List-Unsubscribe URL. It is best-effort: failures are reported but never block
// the archive.
package unsub

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/jokull/sift/internal/model"
)

// Result describes what was attempted/done for a sender.
type Result struct {
	Succeeded bool
	Method    string // "one-click" | "get" | "none"
	Detail    string // human summary, e.g. "sent one-click unsubscribe to <url>"
}

// Perform tries to unsubscribe using info. It returns a non-nil Result always
// (with Succeeded reflecting the unsubscribe request, not the archive).
func Perform(ctx context.Context, info *model.UnsubscribeInfo) *Result {
	if info == nil {
		return &Result{Method: "none", Detail: "no unsubscribe header found"}
	}
	client := &http.Client{Timeout: 20 * time.Second}

	// RFC 8058 one-click: POST the URL with List-Unsubscribe-Post.
	if info.OneClick {
		if u := firstURL(info.URLs); u != "" {
			req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, nil)
			if err == nil {
				req.Header.Set("List-Unsubscribe-Post", "List-Unsubscribe=One-Click")
				resp, err := client.Do(req)
				if err == nil {
					_ = resp.Body.Close()
					if resp.StatusCode >= 200 && resp.StatusCode < 400 {
						return &Result{Succeeded: true, Method: "one-click", Detail: "one-click unsubscribe sent to " + u}
					}
					return &Result{Succeeded: false, Method: "one-click", Detail: fmt.Sprintf("one-click unsubscribe returned HTTP %d", resp.StatusCode)}
				}
				return &Result{Succeeded: false, Method: "one-click", Detail: err.Error()}
			}
		}
	}

	// Fallback: GET the first HTTPS URL.
	if u := firstURL(info.URLs); u != "" {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err == nil {
			resp, err := client.Do(req)
			if err == nil {
				_ = resp.Body.Close()
				if resp.StatusCode >= 200 && resp.StatusCode < 400 {
					return &Result{Succeeded: true, Method: "get", Detail: "unsubscribe link opened: " + u}
				}
				return &Result{Succeeded: false, Method: "get", Detail: fmt.Sprintf("unsubscribe link returned HTTP %d", resp.StatusCode)}
			}
			return &Result{Succeeded: false, Method: "get", Detail: err.Error()}
		}
	}

	// Mailto only — we don't send mail, so report it and let the archive proceed.
	if info.Mailto != "" {
		return &Result{Method: "none", Detail: "only a mailto: unsubscribe exists (" + info.Mailto + "); archival only"}
	}
	return &Result{Method: "none", Detail: "no machine-unsubscribable link found; archival only"}
}

// firstURL returns the first http/https URL in the list.
func firstURL(urls []string) string {
	for _, u := range urls {
		low := strings.ToLower(u)
		if strings.HasPrefix(low, "https://") || strings.HasPrefix(low, "http://") {
			return u
		}
	}
	return ""
}

// ParseHeader turns a raw List-Unsubscribe header value into UnsubscribeInfo.
// It also accepts the companion List-Unsubscribe-Post value for one-click.
func ParseHeader(listUnsubscribe, listUnsubscribePost string) *model.UnsubscribeInfo {
	info := &model.UnsubscribeInfo{Raw: listUnsubscribe, OneClick: strings.Contains(strings.ToLower(listUnsubscribePost), "one-click")}
	for _, part := range strings.Split(listUnsubscribe, ",") {
		part = strings.TrimSpace(part)
		part = strings.Trim(part, "<> \t")
		switch {
		case strings.HasPrefix(strings.ToLower(part), "mailto:"):
			addr := strings.TrimPrefix(strings.ToLower(part), "mailto:")
			if i := strings.IndexByte(addr, '?'); i >= 0 {
				addr = addr[:i]
			}
			info.Mailto = addr
		default:
			if u, err := url.Parse(part); err == nil && (u.Scheme == "http" || u.Scheme == "https") {
				info.URLs = append(info.URLs, part)
			}
		}
	}
	return info
}
