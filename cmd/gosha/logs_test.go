package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func logLine(project, msg string) string {
	return `{"time":"2026-10-18T09:00:00Z","level":"INFO","msg":"` + msg + `","project":"` + project + `","ticket":"NXT-1"}` + "\n"
}

func writeLog(t *testing.T, path, data string, flag int) {
	t.Helper()
	f, err := os.OpenFile(path, flag|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString(data)
	f.Close()
}

func TestLogs(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, "logs", "gosha.log")
	os.MkdirAll(filepath.Dir(path), 0o700)
	writeLog(t, path, logLine("a", "one")+logLine("b", "two")+logLine("a", "picked up"), os.O_CREATE)

	a := &app{}
	root := a.rootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetArgs([]string{"--home", home, "logs", "--project", "a", "-n", "1"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	got := strings.TrimSpace(out.String())
	if !strings.HasSuffix(got, "INFO  picked up project=a ticket=NXT-1") {
		t.Errorf("logs = %q", got)
	}
	if strings.Contains(got, "one") || strings.Contains(got, "two") {
		t.Errorf("logs = %q, want only the last line of a", got)
	}
}

func TestLogsFollowAcrossRotation(t *testing.T) {
	defer func(d time.Duration) { followPoll = d }(followPoll)
	followPoll = 5 * time.Millisecond
	home := t.TempDir()
	path := filepath.Join(home, "logs", "gosha.log")
	os.MkdirAll(filepath.Dir(path), 0o700)
	writeLog(t, path, logLine("a", "old"), os.O_CREATE)

	a := &app{}
	root := a.rootCmd()
	out := &syncBuffer{}
	root.SetOut(out)
	root.SetArgs([]string{"--home", home, "logs", "-f"})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error)
	go func() { done <- root.ExecuteContext(ctx) }()

	wait := func(s string) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for !strings.Contains(out.String(), s) {
			if time.Now().After(deadline) {
				t.Fatalf("no %q in %q", s, out.String())
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
	wait("old")
	writeLog(t, path, logLine("a", "appended"), os.O_APPEND)
	wait("appended")
	if err := os.Rename(path, path+".1"); err != nil {
		t.Fatal(err)
	}
	writeLog(t, path, logLine("a", "rotated"), os.O_CREATE)
	wait("rotated")
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
