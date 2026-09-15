package auth

import (
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"strings"
)

// EnsureSetupToken persists a first-run capability for the machine's operator.
// It must never be logged or returned by an HTTP endpoint.
func EnsureSetupToken(path string) (string, error) {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err == nil {
		token := RandomToken(32)
		_, writeErr := f.WriteString(token + "\n")
		closeErr := f.Close()
		if err := errors.Join(writeErr, closeErr); err != nil {
			_ = os.Remove(path)
			return "", err
		}
		return token, nil
	}
	if !errors.Is(err, os.ErrExist) {
		return "", err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() || info.Size() > 128 {
		return "", fmt.Errorf("invalid setup token file: %s", path)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return "", err
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	token := strings.TrimSpace(string(b))
	decoded, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil || len(decoded) != 32 {
		return "", fmt.Errorf("invalid setup token file; remove %s and restart to regenerate", path)
	}
	return token, nil
}
