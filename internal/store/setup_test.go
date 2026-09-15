package store

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"

	"ctlvps/internal/domain"
)

func TestInitialAdminConcurrent(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "setup.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var wg sync.WaitGroup
	var created atomic.Int32
	for i := range 8 {
		wg.Go(func() {
			u := &domain.User{Username: fmt.Sprintf("admin-%d", i), PasswordHash: "test-hash"}
			err := s.CreateInitialAdmin(context.Background(), u)
			if err == nil {
				created.Add(1)
			} else if !errors.Is(err, ErrAlreadyInitialized) {
				t.Errorf("unexpected setup error: %v", err)
			}
		})
	}
	wg.Wait()
	if n, err := s.CountUsers(context.Background()); err != nil || n != 1 || created.Load() != 1 {
		t.Fatalf("created=%d users=%d err=%v", created.Load(), n, err)
	}
}
