package unsub

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jokull/sift/internal/model"
)

func TestParseHeader(t *testing.T) {
	lu := "<https://example.com/unsub?id=1>, <mailto:list@example.com?subject=unsub>"
	info := ParseHeader(lu, "List-Unsubscribe=One-Click")
	if !info.OneClick {
		t.Fatal("expected one-click")
	}
	if len(info.URLs) != 1 || info.URLs[0] != "https://example.com/unsub?id=1" {
		t.Fatalf("urls=%v", info.URLs)
	}
	if info.Mailto != "list@example.com" {
		t.Fatalf("mailto=%q", info.Mailto)
	}
}

func TestParseHeaderNoPost(t *testing.T) {
	info := ParseHeader("<https://example.com/unsub>", "")
	if info.OneClick {
		t.Fatal("expected not one-click")
	}
	if len(info.URLs) != 1 {
		t.Fatalf("urls=%v", info.URLs)
	}
}

func TestPerformOneClick(t *testing.T) {
	var gotPost, gotHeader bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			gotPost = true
			gotHeader = r.Header.Get("List-Unsubscribe-Post") == "List-Unsubscribe=One-Click"
			w.WriteHeader(http.StatusOK)
		} else {
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}))
	defer srv.Close()

	info := &model.UnsubscribeInfo{URLs: []string{srv.URL}, OneClick: true}
	res := Perform(context.Background(), info)
	if !res.Succeeded {
		t.Fatalf("expected succeed, got %+v", res)
	}
	if res.Method != "one-click" {
		t.Fatalf("method=%s", res.Method)
	}
	if !gotPost || !gotHeader {
		t.Fatalf("gotPost=%v gotHeader=%v", gotPost, gotHeader)
	}
	if !strings.Contains(res.Detail, "one-click") {
		t.Fatalf("detail=%s", res.Detail)
	}
}

func TestPerformNil(t *testing.T) {
	res := Perform(nil, nil)
	if res.Method != "none" || res.Succeeded {
		t.Fatalf("unexpected %+v", res)
	}
}
