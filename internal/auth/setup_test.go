package auth

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSetupTokenPersistsPrivately(t *testing.T) {
	path := filepath.Join(t.TempDir(), "setup-token")
	first, err := EnsureSetupToken(path)
	if err != nil || len(first) != 43 {
		t.Fatalf("create: length=%d err=%v", len(first), err)
	}
	second, err := EnsureSetupToken(path)
	if err != nil || second != first {
		t.Fatal("restart did not preserve setup capability")
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatal("token file must be private")
	}
	if err := os.WriteFile(path, []byte("broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := EnsureSetupToken(path); err == nil {
		t.Fatal("corrupt token must fail closed")
	}
}

func TestSetupTokenRejectsSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	if _, err := EnsureSetupToken(target); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "setup-token")
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	if _, err := EnsureSetupToken(path); err == nil {
		t.Fatal("symlink accepted")
	}
}
