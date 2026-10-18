// Package flock takes advisory locks on files, shared by every gosha
// process on the host (the daemon, gosha run and the CLI).
package flock

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
)

// ErrLocked means another process holds the lock.
var ErrLocked = errors.New("locked by another process")

// Lock waits for the exclusive lock on path, creating the file if needed.
func Lock(path string) (unlock func(), err error) {
	return lock(path, syscall.LOCK_EX)
}

// TryLock takes the exclusive lock on path, or fails with ErrLocked at once.
func TryLock(path string) (unlock func(), err error) {
	return lock(path, syscall.LOCK_EX|syscall.LOCK_NB)
}

func lock(path string, how int) (func(), error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), how); err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, ErrLocked
		}
		return nil, err
	}
	return func() { f.Close() }, nil
}
