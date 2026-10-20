package natconfig

import (
	"errors"
	"os"
	"testing"
)

func TestLocalAuthorityAbsenceAndUnsafeFilesAreDistinct(t *testing.T) {
	for _, tc := range []struct {
		name          string
		raw           string
		readErr       error
		present, fail bool
	}{
		{"absent", "", os.ErrNotExist, false, false},
		{"empty opt-out", `{"schema":1,"mappings":[]}`, nil, true, false},
		{"invalid", `{"schema":1,"mappings":[`, nil, true, true},
		{"unsafe owner", "", errors.New("unsafe root owner"), true, true},
		{"unreadable", "", os.ErrPermission, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg, present, err := loadLocal(func(path string) ([]byte, error) {
				if path != Path {
					t.Fatal("configuration path was not fixed:", path)
				}
				return []byte(tc.raw), tc.readErr
			})
			if present != tc.present || (err != nil) != tc.fail {
				t.Fatalf("presence=%v cfg=%+v err=%v", present, cfg, err)
			}
		})
	}
}
