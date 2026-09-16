package proxysandbox

import "testing"

func TestFixedCommandBoundary(t *testing.T) {
	for _, args := range [][]string{{"singbox", "run", "public"}, {"singbox", "candidate", "private"}, {"snell", "run", "443"}} {
		if _, _, e := Command(args); e != nil {
			t.Fatal(e)
		}
	}
	for _, args := range [][]string{{"singbox", "run", "../../etc"}, {"singbox", "run", "/etc/passwd"}, {"snell", "run", "0443"}, {"snell", "run", "0"}, {"snell", "run", "65536"}, {"singbox", "shell", "public"}, {"sh", "run", "public"}, {"singbox", "run", "public", "-c", "/tmp/evil"}} {
		if _, _, e := Command(args); e == nil {
			t.Fatalf("accepted arbitrary command: %q", args)
		}
	}
}
