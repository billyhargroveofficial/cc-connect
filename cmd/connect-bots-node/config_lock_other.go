//go:build !(darwin || linux || freebsd || openbsd || netbsd || dragonfly)

package main

import "errors"

type nodeLock struct{}

func acquireNodeLock(string) (*nodeLock, error) {
	return nil, errors.New("Connect Bots Node currently supports macOS and Unix hosts")
}

func (*nodeLock) Close() error { return nil }
