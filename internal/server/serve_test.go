package server

import (
	"context"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

// waitFor polls cond until it holds, or fails the test after 5 s.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestGracefulShutdown: when the context ends, Serve stops accepting and
// lets the in-flight request finish.
func TestGracefulShutdown(t *testing.T) {
	f := newFixture(t, exampleConfig(t))
	f.stub.SetEmitSleep(t, "1")
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	served := make(chan error, 1)
	go func() { served <- Serve(ctx, ln, f.srv) }()

	status := make(chan int, 1)
	go func() {
		r, _ := http.NewRequest("POST", "http://"+addr+"/notify/backup", strings.NewReader("x"))
		r.Header.Set("Authorization", "Bearer "+secret)
		resp, err := http.DefaultClient.Do(r)
		if err != nil {
			t.Errorf("in-flight request: %v", err)
			status <- 0
			return
		}
		resp.Body.Close()
		status <- resp.StatusCode
	}()
	waitFor(t, "decree emit to start", func() bool { return len(f.stub.Emits(t)) == 1 })
	cancel()

	waitFor(t, "the listener to close", func() bool {
		c, err := net.DialTimeout("tcp", addr, 100*time.Millisecond)
		if err == nil {
			c.Close()
		}
		return err != nil
	})
	select {
	case err := <-served:
		t.Fatalf("Serve returned %v before the in-flight request finished", err)
	default:
	}
	if got := <-status; got != http.StatusCreated {
		t.Errorf("in-flight request: status %d, want 201", got)
	}
	select {
	case err := <-served:
		if err != nil {
			t.Errorf("Serve: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Serve did not return")
	}
}
