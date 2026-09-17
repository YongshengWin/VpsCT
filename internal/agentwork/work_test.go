package agentwork

import (
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
	"time"
)

func TestMain(m *testing.M) {
	if len(os.Args) == 2 && (os.Args[1] == "core-install" || os.Args[1] == "agent-update") {
		b, _ := io.ReadAll(io.LimitReader(os.Stdin, 64<<10))
		time.Sleep(100 * time.Millisecond)
		if strings.Contains(string(b), "fail") {
			os.Stderr.WriteString("fixture failure")
			os.Exit(7)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}
func TestSingleWorkSlotDoesNotQueue(t *testing.T) {
	ctx := context.Background()
	if err := Start(ctx, "core-install", []byte("first")); !errors.Is(err, ErrPending) {
		t.Fatal(err)
	}
	first := slot.job
	for i := 0; i < 1000; i++ {
		if err := Start(ctx, "agent-update", []byte("second")); !errors.Is(err, ErrPending) {
			t.Fatal(err)
		}
		if slot.job != first {
			t.Fatal("queued extra work")
		}
	}
	deadline := time.Now().Add(5 * time.Second)
	for Pending() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if Pending() {
		t.Fatal("worker never completed")
	}
	if err := Start(ctx, "core-install", []byte("first")); err != nil {
		t.Fatal(err)
	}
	if slot.job != nil {
		t.Fatal("finished job retained as active")
	}
}
func TestWorkerFailureHasBoundedRetry(t *testing.T) {
	if err := Start(context.Background(), "core-install", []byte("fail")); !errors.Is(err, ErrPending) {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for Pending() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if Pending() {
		t.Fatal("worker timed out")
	}
	if err := Start(context.Background(), "core-install", []byte("fail")); err == nil || errors.Is(err, ErrPending) {
		t.Fatal("missing failure", err)
	}
	if err := Start(context.Background(), "core-install", []byte("fail")); err == nil || errors.Is(err, ErrPending) {
		t.Fatal("failure retried without cooldown", err)
	}
	if slot.job != nil {
		t.Fatal("failed job queued")
	}
}
