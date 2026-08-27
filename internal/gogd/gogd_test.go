package gogd

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// writeEchoBin creates a tiny executable that prints its args, used as a fake
// "gog" so the bridge roundtrip can be tested without Google.
func writeEchoBin(t *testing.T, dir string) string {
	t.Helper()
	path := filepath.Join(dir, "fake-gog")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\"\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestServerCallRoundtrip(t *testing.T) {
	dir := t.TempDir()
	sock := filepath.Join(dir, "gogd.sock")
	gogBin := writeEchoBin(t, dir)

	srv := NewServer(gogBin, sock)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = srv.Serve(ctx) }()

	// Wait for the socket to come up.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(sock); err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := os.Stat(sock); err != nil {
		t.Fatalf("socket not created: %v", err)
	}

	out, err := Call(context.Background(), sock, "gmail", "search", "in:inbox", "--max", "5")
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if !strings.Contains(out, "gmail") || !strings.Contains(out, "in:inbox") || !strings.Contains(out, "5") {
		t.Fatalf("unexpected output: %q", out)
	}
}
