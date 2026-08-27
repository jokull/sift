package accounts

import (
	"errors"
	"strings"
	"testing"
)

func TestGogHintNoAuth(t *testing.T) {
	err := errors.New("exit status 4")
	out := "No auth for gmail jokull@triptojapan.com. OAuth (browser flow): gog auth add ..."
	msg := gogHint("gmail search", err, out).Error()
	for _, want := range []string{"service_account_json", "refresh_token", "access_token", "keychain"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("hint missing %q:\n%s", want, msg)
		}
	}
}

func TestGogHintKeychain(t *testing.T) {
	err := errors.New("exit status 36")
	out := "User interaction is not allowed"
	msg := gogHint("gmail search", err, out).Error()
	if !strings.Contains(msg, "service_account_json") {
		t.Fatalf("hint missing service_account_json:\n%s", msg)
	}
}

func TestGogHintOther(t *testing.T) {
	err := errors.New("boom")
	out := "some unrelated error"
	// Non-credential errors should not be rewritten with auth guidance.
	if strings.Contains(gogHint("gmail search", err, out).Error(), "service_account_json") {
		t.Fatal("unexpected auth hint for unrelated error")
	}
}
