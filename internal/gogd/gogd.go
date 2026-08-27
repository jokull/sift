// Package gogd provides a small "gog bridge" daemon. The daemon runs in the user's
// login (GUI) session — where the login keychain is unlocked — and executes gog
// Gmail commands on behalf of sift. sift talks to it headlessly over a unix
// socket, so it can read Gmail from an SSH session exactly as OpenClaw does
// (OpenClaw's launchd gog is also in the login session).
package gogd

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// request is a single gog invocation to run in the daemon.
type request struct {
	Args []string `json:"args"`
}

// response returns gog's stdout, stderr and exit code.
type response struct {
	Output string `json:"output"` // stdout (JSON)
	Error  string `json:"error"`  // stderr (human messages/notes/errors)
	Code   int    `json:"code"`
}

// DefaultSocket returns the gogd socket path, honouring SIFT_GOGD_SOCK.
func DefaultSocket() string {
	if v := os.Getenv("SIFT_GOGD_SOCK"); v != "" {
		return v
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".sift", "gogd.sock")
}

// Server is the gog bridge daemon.
type Server struct {
	GogBin string
	Socket string

	ln   net.Listener
	http *http.Server
}

// NewServer builds a server bound to socket, running gogBin.
func NewServer(gogBin, socket string) *Server {
	return &Server{GogBin: gogBin, Socket: socket}
}

// Serve runs the daemon until ctx is cancelled. It blocks.
func (s *Server) Serve(ctx context.Context) error {
	if err := os.MkdirAll(filepath.Dir(s.Socket), 0o700); err != nil {
		return err
	}
	_ = os.Remove(s.Socket) // stale socket from a previous run
	ln, err := net.Listen("unix", s.Socket)
	if err != nil {
		return fmt.Errorf("listen %s: %w", s.Socket, err)
	}
	defer func() { _ = os.Remove(s.Socket) }()
	if err := os.Chmod(s.Socket, 0o600); err != nil {
		return err
	}
	s.ln = ln

	s.http = &http.Server{Handler: http.HandlerFunc(s.handle)}
	errCh := make(chan error, 1)
	go func() { errCh <- s.http.Serve(ln) }()

	select {
	case <-ctx.Done():
		shCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = s.http.Shutdown(shCtx)
		return nil
	case err := <-errCh:
		if err == http.ErrServerClosed {
			return nil
		}
		return err
	}
}

func (s *Server) handle(w http.ResponseWriter, r *http.Request) {
	var req request
	body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err := json.Unmarshal(body, &req); err != nil || len(req.Args) == 0 {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	cmd := exec.CommandContext(r.Context(), s.GogBin, req.Args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	code := 0
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			code = ee.ExitCode()
		} else {
			code = -1
		}
	}
	_ = json.NewEncoder(w).Encode(response{Output: stdout.String(), Error: strings.TrimSpace(stderr.String()), Code: code})
}

// Call invokes gog with args on a running daemon and returns its combined
// output. A non-zero gog exit code is returned as an error.
func Call(ctx context.Context, socket string, args ...string) (string, error) {
	var buf bytes.Buffer
	if err := json.NewEncoder(&buf).Encode(request{Args: args}); err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://unix/", &buf)
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	hc := &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", socket)
		},
	}}
	resp, err := hc.Do(req)
	if err != nil {
		return "", fmt.Errorf("gogd call: %w", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	var out response
	if err := json.Unmarshal(respBody, &out); err != nil {
		return "", fmt.Errorf("gogd decode: %w", err)
	}
	if out.Code != 0 {
		return out.Output, fmt.Errorf("gogd exit %d: %s", out.Code, out.Error)
	}
	return out.Output, nil
}

// Available reports whether a daemon is reachable at socket.
func Available(ctx context.Context, socket string) bool {
	_, err := Call(ctx, socket, "--version")
	return err == nil
}
