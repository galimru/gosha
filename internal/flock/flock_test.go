package flock

import (
	"errors"
	"path/filepath"
	"testing"
)

func TestTryLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x", "run.lock")
	unlock, err := TryLock(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := TryLock(path); !errors.Is(err, ErrLocked) {
		t.Fatalf("second TryLock = %v, want ErrLocked", err)
	}
	unlock()
	unlock, err = Lock(path)
	if err != nil {
		t.Fatalf("Lock after unlock: %v", err)
	}
	unlock()
}
