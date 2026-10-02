//go:build darwin || linux || freebsd || openbsd || netbsd || dragonfly

package main

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNodeLockPreventsDuplicateRunAndRebindingActiveNode(t *testing.T) {
	root, err := privateDataDir(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := saveNodeConfig(root, testNodeConfig()); err != nil {
		t.Fatal(err)
	}
	first, err := acquireNodeLock(root)
	if err != nil {
		t.Fatal(err)
	}
	if lock, err := acquireNodeLock(root); err == nil {
		lock.Close()
		t.Fatal("duplicate node acquired the workspace lock")
	}
	deps := testDependencies(root)
	if err := execute(context.Background(), []string{"pair", "--server", "https://bots.example.test", "--code", "different-code", "--replace"}, strings.NewReader(""), io.Discard, io.Discard, deps); err == nil {
		t.Fatal("active node was rebound")
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	second, err := acquireNodeLock(root)
	if err != nil {
		t.Fatal("lock was not released", err)
	}
	second.Close()
}

func TestNodeLockRejectsSymlinkWithoutChangingTarget(t *testing.T) {
	root, err := privateDataDir(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, "unrelated")
	if err := os.WriteFile(target, []byte("keep"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("unrelated", filepath.Join(root, "node.lock")); err != nil {
		t.Fatal(err)
	}
	if lock, err := acquireNodeLock(root); err == nil {
		lock.Close()
		t.Fatal("symlink lock was accepted")
	}
	info, err := os.Stat(target)
	if err != nil || info.Mode().Perm() != 0644 {
		t.Fatal("lock changed the symlink target", err)
	}
}
