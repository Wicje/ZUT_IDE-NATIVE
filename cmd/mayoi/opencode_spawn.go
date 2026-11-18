package main

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	"mayoi/internal/opencode"
)

var startOnce sync.Mutex

// ensureOpencodeServer makes sure an opencode server answers on
// 127.0.0.1:4096, starting one if there is none. Called before every
// prompt; returns quickly when the server is already up.
func ensureOpencodeServer(ctx context.Context) error {
	cl := opencode.New()
	health := func() bool {
		hctx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		return cl.Health(hctx) == nil
	}
	if health() {
		return nil
	}
	startOnce.Lock()
	defer startOnce.Unlock()
	if health() {
		return nil
	}
	port, host := "4096", "127.0.0.1"
	if u, err := url.Parse(opencode.BaseURL()); err == nil && u.Port() != "" {
		port = u.Port()
		if h := u.Hostname(); h != "" {
			host = h
		}
	}
	// A server on another machine is not ours to start; only spawn for
	// our own loopback target.
	if host != "127.0.0.1" && host != "localhost" {
		return fmt.Errorf("opencode server at %s is not running; start it there", opencode.BaseURL())
	}
	bin := ""
	if p, err := exec.LookPath("opencode"); err == nil {
		bin = p
	} else if home, err := os.UserHomeDir(); err == nil {
		candidate := filepath.Join(home, ".opencode", "bin", "opencode")
		if _, err := os.Stat(candidate); err == nil {
			bin = candidate
		}
	}
	if bin == "" {
		return errors.New("opencode is not installed; run it yourself with 'opencode serve'")
	}
	cmd := exec.Command(bin, "serve", "--port", port, "--hostname", host)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = nil, nil, nil
	detachProcess(cmd)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("opencode serve could not start: %w", err)
	}
	_ = cmd.Process.Release()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(300 * time.Millisecond):
		}
		if health() {
			return nil
		}
	}
	return errors.New("opencode serve did not come up; check ~/.local/share/opencode")
}
