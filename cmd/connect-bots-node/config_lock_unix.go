//go:build darwin || linux || freebsd || openbsd || netbsd || dragonfly

package main

import (
	"errors"
	"fmt"
	"os"
	"syscall"
)

type nodeLock struct{ file *os.File }

// Pair and run share this lock so a live node cannot be rebound to a different
// account, and a second process cannot open another dedicated app-server.
func acquireNodeLock(root string) (*nodeLock, error) {
	capability, err := os.OpenRoot(root)
	if err != nil {
		return nil, err
	}
	defer capability.Close()
	info, err := capability.Lstat("node.lock")
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if err == nil && !info.Mode().IsRegular() {
		return nil, errors.New("node lock must be a regular file without a symbolic link")
	}
	file, err := capability.OpenFile("node.lock", os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return nil, fmt.Errorf("open node lock: %w", err)
	}
	opened, err := file.Stat()
	after, statErr := capability.Lstat("node.lock")
	if err != nil || statErr != nil || !opened.Mode().IsRegular() || !os.SameFile(opened, after) {
		file.Close()
		return nil, errors.New("node lock changed while opening")
	}
	if err := file.Chmod(0600); err != nil {
		file.Close()
		return nil, fmt.Errorf("secure node lock: %w", err)
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		file.Close()
		return nil, errors.New("another Connect Bots Node process already owns this workspace")
	}
	return &nodeLock{file: file}, nil
}

func (lock *nodeLock) Close() error { return lock.file.Close() }
