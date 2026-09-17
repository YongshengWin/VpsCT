package boundedexec

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestOutputOverflowFailsClosed(t *testing.T) {
	out, _, err := Run(context.Background(), "", 100, "sh", "-c", "printf '%0200d' 0")
	if !errors.Is(err, ErrOutputLimit) || len(out) != 100 {
		t.Fatalf("%d %v", len(out), err)
	}
	_, stderr, err := Run(context.Background(), "", 100, "sh", "-c", "printf '%020000d' 0 >&2")
	if !errors.Is(err, ErrOutputLimit) || len(stderr) != 16<<10 {
		t.Fatalf("%d %v", len(stderr), err)
	}
	out, _, err = Run(context.Background(), strings.Repeat("x", 10), 100, "cat")
	if err != nil || len(out) != 10 {
		t.Fatal(err)
	}
}
