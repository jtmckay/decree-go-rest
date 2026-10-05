//go:build unix

package main

import (
	"net"
	"net/http"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"
)

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

func freeAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().String()
}

// TestServeUntilSIGTERM runs decree-api on the example config: it listens
// on DECREE_API_LISTEN, runs decree under umask 027, and on SIGTERM
// finishes the in-flight request before it exits 0.
func TestServeUntilSIGTERM(t *testing.T) {
	old := syscall.Umask(0o022)
	defer syscall.Umask(old)

	path, stub := exampleProject(t)
	stub.SetEmitSleep(t, "1")
	addr := freeAddr(t)
	t.Setenv("DECREE_API_LISTEN", addr)

	exited := make(chan int, 1)
	var stderr strings.Builder
	go func() { exited <- run([]string{"-config", path}, &strings.Builder{}, &stderr) }()
	waitFor(t, "decree-api to listen", func() bool {
		resp, err := http.Get("http://" + addr + "/nope")
		if err != nil {
			return false
		}
		resp.Body.Close()
		return resp.StatusCode == http.StatusNotFound
	})

	status := make(chan int, 1)
	go func() {
		r, _ := http.NewRequest("POST", "http://"+addr+"/notify/backup", strings.NewReader("disk 3 is full"))
		r.Header.Set("Authorization", "Bearer "+strings.Repeat("a", 64))
		resp, err := http.DefaultClient.Do(r)
		if err != nil {
			t.Errorf("in-flight request: %v", err)
			status <- 0
			return
		}
		resp.Body.Close()
		status <- resp.StatusCode
	}()
	waitFor(t, "decree emit to start", func() bool { return len(stub.Emits(t)) == 1 })
	if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}

	if got := <-status; got != http.StatusCreated {
		t.Errorf("in-flight request: status %d, want 201", got)
	}
	select {
	case code := <-exited:
		if code != 0 {
			t.Errorf("exit %d, want 0; stderr %q", code, stderr.String())
		}
	case <-time.After(10 * time.Second):
		t.Fatal("decree-api did not exit after SIGTERM")
	}
	if _, err := net.DialTimeout("tcp", addr, 100*time.Millisecond); err == nil {
		t.Error("still accepting connections after exit")
	}

	e := stub.Emits(t)[0]
	if e.Umask != "0027" {
		t.Errorf("decree's umask %q, want 0027", e.Umask)
	}
	st, err := os.Stat(e.StdinFile)
	if err != nil {
		t.Fatal(err)
	}
	if mode := st.Mode().Perm(); mode != 0o640 {
		t.Errorf("a file decree creates has mode %#o, want 0640", mode)
	}
}
