//go:build linux

package openrc

import (
	"errors"
	"fmt"
	"testing"

	"golang.org/x/sys/unix"
)

func TestRetryMeterProgramLoad(t *testing.T) {
	for _, tc := range []struct {
		name         string
		again, calls int
		final, want  error
	}{
		{"success", 0, 1, nil, nil},
		{"interrupted then loaded", 1, 2, nil, nil},
		{"last attempt succeeds", 4, 5, nil, nil},
		{"interruption persists", 5, 5, nil, unix.EAGAIN},
		{"verifier rejection", 0, 1, unix.EACCES, unix.EACCES},
		{"permission failure after interruption", 1, 2, unix.EPERM, unix.EPERM},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			fd, err := retryMeterProgramLoad(func() (int, error) {
				calls++
				if calls > tc.calls {
					t.Fatal("unexpected retry")
				}
				if calls <= tc.again {
					return -1, fmt.Errorf("load: %w", unix.EAGAIN)
				}
				if tc.final != nil {
					return -1, tc.final
				}
				return 17, nil
			})
			wantFD := 17
			if tc.want != nil {
				wantFD = -1
			}
			if calls != tc.calls || fd != wantFD || !errors.Is(err, tc.want) {
				t.Fatalf("calls=%d fd=%d err=%v", calls, fd, err)
			}
		})
	}
}
