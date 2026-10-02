//go:build windows

package bots

import (
	"fmt"
	"os"

	"golang.org/x/sys/windows"
)

type storeFileLock struct {
	file       *os.File
	overlapped windows.Overlapped
}

func acquireStoreLock(path string) (*storeFileLock, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, fmt.Errorf("open owner lock: %w", err)
	}
	lock := &storeFileLock{file: file}
	if err := windows.LockFileEx(windows.Handle(file.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, &lock.overlapped); err != nil {
		file.Close()
		return nil, fmt.Errorf("another Connect Bots process already owns this data directory: %w", err)
	}
	return lock, nil
}

func (lock *storeFileLock) Close() error { return lock.file.Close() }

// Windows does not support fsync on ordinary directory handles. The atomic
// replacement follows the flushed temporary file's close.
func syncStoreDirectory(string) error { return nil }
