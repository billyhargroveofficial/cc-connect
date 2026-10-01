//go:build darwin || linux || freebsd || openbsd || netbsd || dragonfly

package bots

import (
	"fmt"
	"os"
	"syscall"
)

type storeFileLock struct{ file *os.File }

func acquireStoreLock(path string) (*storeFileLock, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, fmt.Errorf("open owner lock: %w", err)
	}
	if err := file.Chmod(0600); err != nil {
		file.Close()
		return nil, err
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		file.Close()
		return nil, fmt.Errorf("another Connect Bots process already owns this data directory: %w", err)
	}
	return &storeFileLock{file: file}, nil
}

func (lock *storeFileLock) Close() error { return lock.file.Close() }

func syncStoreDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}
