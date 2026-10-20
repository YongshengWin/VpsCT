package assets

import (
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Exercise complete installer flows in an isolated path tree. Every privileged
// service/package/network operation is replaced by a fixture executable.
func TestOpenRCAgentInstallerAndRollback(t *testing.T) {
	checksum, checksumErr := exec.LookPath("sha256sum")
	checksumArgs := ""
	if checksumErr != nil {
		checksum, checksumErr = exec.LookPath("shasum")
		checksumArgs = " -a 256"
	}
	if checksumErr != nil {
		t.Skip("installer fixture requires a host SHA-256 command")
	}
	for _, tc := range []struct {
		name                                                        string
		update, init                                                bool
		failStart, failReady, failRollbackReady, failLock, failStop bool
		readyDelay                                                  int
		failCheck                                                   bool
	}{
		{name: "install", init: true},
		{name: "install preflight failure cleans candidate", init: true, failCheck: true},
		{name: "install waits for readiness", init: true, readyDelay: 3},
		{name: "install rejects status without readiness", init: true, failReady: true},
		{name: "update", update: true, init: true},
		{name: "update waits for readiness", update: true, init: true, readyDelay: 3},
		{name: "restart failure rolls back", update: true, init: true, failStart: true},
		{name: "readiness failure rolls back", update: true, init: true, failReady: true},
		{name: "rollback must become ready", update: true, init: true, failReady: true, failRollbackReady: true},
		{name: "rollback requires configuration lock", update: true, init: true, failReady: true, failLock: true},
		{name: "rollback requires candidate stop", update: true, init: true, failReady: true, failStop: true},
		{name: "reject missing init"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			write := func(name, body string, mode os.FileMode) {
				t.Helper()
				path := filepath.Join(dir, name)
				if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(body), mode); err != nil {
					t.Fatal(err)
				}
			}
			agent := `#!/bin/sh
# new-agent
case "$1" in
  version|capabilities|check-update|openrc-verify|openrc-sync) exit 0 ;;
  openrc-check)
    test "$FIXTURE_FAIL_CHECK" = 0 || exit 95
    # A noexec temporary directory must never be used to execute a candidate.
    case "$0" in "$FIXTURE_ROOT"/usr/local/bin/.ctlvps-agent.*) exit 0;; *) exit 94;; esac ;;
  openrc-stop)
    if [ "$FIXTURE_FAIL_STOP" = 1 ] && grep -q '^# new-agent$' "$0"; then exit 1; fi
    exit 0 ;;
  openrc-ready)
    label=previous
    if grep -q '^# new-agent$' "$0"; then label=new; fi
    printf 'openrc-ready %s\n' "$label" >> "$FIXTURE_ROOT/actions"
    if [ -f "$FIXTURE_ROOT/configuration-locked" ]; then touch "$FIXTURE_ROOT/ready-with-lock"; exit 93; fi
    test -f "$FIXTURE_ROOT/service-ctlvps-agent" || exit 1
    if [ "$label" = previous ]; then test "$FIXTURE_FAIL_ROLLBACK_READY" = 0; exit; fi
    test "$FIXTURE_FAIL_READY" = 0 || exit 1
    count=0
    if [ -f "$FIXTURE_ROOT/ready-count" ]; then count=$(cat "$FIXTURE_ROOT/ready-count"); fi
    count=$((count + 1))
    printf '%s\n' "$count" > "$FIXTURE_ROOT/ready-count"
    test "$count" -gt "$FIXTURE_READY_DELAY" ;;
  openrc-script) printf '#!/sbin/openrc-run\n# fixture-new-init\ncommand="/usr/local/bin/ctlvps-agent"\n' ;;
  enroll) while [ "$#" -gt 0 ]; do if [ "$1" = --state ]; then shift; mkdir -p "$1"; printf '{}' > "$1/state.json"; fi; shift; done ;;
  *) exit 90 ;;
esac
`
			oldAgent := strings.Replace(agent, "# new-agent", "# previous-agent", 1)
			write("fixture-agent", agent, 0755)
			helper := "#!/bin/sh\nexit 0\n"
			write("fixture-helper", helper, 0755)
			write("fixture-sums", fmt.Sprintf("%x  ctlvps-agent-linux-amd64\n%x  ctlvps-verify-linux-amd64\n%x  uninstall.sh\n", sha256.Sum256([]byte(agent)), sha256.Sum256([]byte(helper)), sha256.Sum256([]byte(helper))), 0600)
			for _, p := range []string{"etc/init.d", "temporary"} {
				if err := os.MkdirAll(filepath.Join(dir, p), 0755); err != nil {
					t.Fatal(err)
				}
			}
			if tc.init {
				write("run/openrc/softlevel", "default\n", 0600)
			}
			if tc.update {
				write("usr/local/bin/ctlvps-agent", oldAgent, 0755)
				write("etc/init.d/ctlvps-agent", "#!/sbin/openrc-run\n# fixture-previous-init\n", 0755)
				write("var/lib/ctlvps-agent/state.json", "{\"existing\":true}\n", 0600)
			}
			for name, body := range map[string]string{
				"id":    "#!/bin/sh\nprintf '0\\n'\n",
				"uname": "#!/bin/sh\nprintf 'x86_64\\n'\n",
				"flock": `#!/bin/sh
printf 'flock %s\n' "$*" >> "$FIXTURE_ROOT/actions"
case "$*" in
  '-n 8') touch "$FIXTURE_ROOT/configuration-locked" ;;
  '-w 30 8') test "$FIXTURE_FAIL_LOCK" = 0 || exit 1; touch "$FIXTURE_ROOT/configuration-locked" ;;
  '-u 8') rm -f "$FIXTURE_ROOT/configuration-locked" ;;
esac
`,
				"sleep":     "#!/bin/sh\nexit 0\n",
				"apk":       "#!/bin/sh\nprintf 'apk %s\\n' \"$*\" >> \"$FIXTURE_ROOT/actions\"\n",
				"apt-get":   "#!/bin/sh\nexit 0\n",
				"dnf":       "#!/bin/sh\nexit 0\n",
				"yum":       "#!/bin/sh\nexit 0\n",
				"mv":        "#!/bin/sh\n[ \"$1\" != -Tf ] || shift\n[ \"$1\" != -- ] || shift\nexec /bin/mv -f \"$@\"\n",
				"sha256sum": "#!/bin/sh\nexec '" + strings.ReplaceAll(checksum, "'", "'\"'\"'") + "'" + checksumArgs + " \"$@\"\n",
				"rc-update": "#!/bin/sh\nprintf 'rc-update %s\\n' \"$*\" >> \"$FIXTURE_ROOT/actions\"\n",
				"rc-service": `#!/bin/sh
printf 'rc-service %s\n' "$*" >> "$FIXTURE_ROOT/actions"
case "$2" in
  stop)
    if [ "$1" = ctlvps-agent ] && [ "$FIXTURE_FAIL_STOP" = 1 ] && grep -q '^# new-agent$' "$FIXTURE_ROOT/usr/local/bin/ctlvps-agent"; then exit 1; fi
    rm -f "$FIXTURE_ROOT/service-$1" ;;
  start|restart)
    if [ "$1" = ctlvps-agent ] && [ "$FIXTURE_FAIL" = 1 ] && grep -q '^# new-agent$' "$FIXTURE_ROOT/usr/local/bin/ctlvps-agent"; then exit 1; fi
    touch "$FIXTURE_ROOT/service-$1" ;;
  status) test -f "$FIXTURE_ROOT/service-$1" ;;
  *) exit 91 ;;
esac
`,
				"curl": `#!/bin/sh
target=''; source=''
while [ "$#" -gt 0 ]; do
  case "$1" in -o) shift; target=$1 ;; https://*) source=$1 ;; esac
  shift
done
case "$source" in
  */SHA256SUMS) cp "$FIXTURE_ROOT/fixture-sums" "$target" ;;
  */ctlvps-agent-linux-amd64) cp "$FIXTURE_ROOT/fixture-agent" "$target" ;;
  */ctlvps-verify-linux-amd64|*/uninstall.sh) cp "$FIXTURE_ROOT/fixture-helper" "$target" ;;
  *) exit 92 ;;
esac
`,
			} {
				write("mock-bin/"+name, body, 0755)
			}
			script := string(InstallAgent)
			// Rewrite fixed host paths in this test copy only; production has no
			// environment-controlled root or alternate executable directories.
			for _, prefix := range []string{"/run/", "/etc/", "/usr/local/", "/var/lib/", "/var/log/"} {
				script = strings.ReplaceAll(script, prefix, dir+prefix)
			}
			script = strings.ReplaceAll(script, "RELEASE_VERSION='__VERSION__'", "RELEASE_VERSION='v9.1.0'")
			write("installer.sh", script, 0700)
			args := []string{filepath.Join(dir, "installer.sh"), "--server", "https://panel.example"}
			if tc.update {
				args = append(args, "--update")
			} else {
				args = append(args, "--token", "fixture-enrollment")
			}
			cmd := exec.Command("bash", args...)
			flag := func(enabled bool) string {
				if enabled {
					return "1"
				}
				return "0"
			}
			cmd.Env = append(os.Environ(), "PATH="+filepath.Join(dir, "mock-bin")+":"+os.Getenv("PATH"), "TMPDIR="+filepath.Join(dir, "temporary"), "FIXTURE_ROOT="+dir,
				"FIXTURE_FAIL="+flag(tc.failStart), "FIXTURE_FAIL_READY="+flag(tc.failReady), "FIXTURE_FAIL_ROLLBACK_READY="+flag(tc.failRollbackReady), "FIXTURE_FAIL_LOCK="+flag(tc.failLock), "FIXTURE_FAIL_STOP="+flag(tc.failStop), "FIXTURE_FAIL_CHECK="+flag(tc.failCheck), fmt.Sprintf("FIXTURE_READY_DELAY=%d", tc.readyDelay))
			out, err := cmd.CombinedOutput()
			if (err != nil) != (tc.failStart || tc.failReady || tc.failCheck || !tc.init) {
				t.Fatalf("installer result: %v\n%s", err, out)
			}
			binary, readErr := os.ReadFile(filepath.Join(dir, "usr/local/bin/ctlvps-agent"))
			if tc.failCheck {
				if !os.IsNotExist(readErr) {
					t.Fatal("installed an agent that failed preflight")
				}
				staged, err := filepath.Glob(filepath.Join(dir, "usr/local/bin/.ctlvps-agent.*"))
				if err != nil || len(staged) != 0 {
					t.Fatal("failed preflight retained a staged executable", err)
				}
				return
			}
			if !tc.init {
				if !os.IsNotExist(readErr) {
					t.Fatal("installed without a running init")
				}
				return
			}
			want := agent
			rollback := tc.update && (tc.failStart || tc.failReady) && !tc.failLock && !tc.failStop
			if rollback {
				want = oldAgent
			}
			if readErr != nil || string(binary) != want {
				t.Fatal("wrong installed or restored agent", readErr)
			}
			initScript, err := os.ReadFile(filepath.Join(dir, "etc/init.d/ctlvps-agent"))
			if err != nil || !strings.Contains(string(initScript), map[bool]string{false: "fixture-new-init", true: "fixture-previous-init"}[rollback]) {
				t.Fatal("wrong installed or restored init script", err)
			}
			if tc.update {
				state, err := os.ReadFile(filepath.Join(dir, "var/lib/ctlvps-agent/state.json"))
				if err != nil || string(state) != "{\"existing\":true}\n" {
					t.Fatal("update changed enrollment state", err)
				}
			}
			if _, err := os.Stat(filepath.Join(dir, "etc/systemd/system/ctlvps-agent.service")); !os.IsNotExist(err) {
				t.Fatal("OpenRC installation wrote a systemd unit")
			}
			actions, err := os.ReadFile(filepath.Join(dir, "actions"))
			if err != nil {
				t.Fatal(err)
			}
			log := string(actions)
			if _, err := os.Stat(filepath.Join(dir, "ready-with-lock")); !os.IsNotExist(err) {
				t.Fatal("waited for readiness while holding the configuration lock")
			}
			if !tc.failStart {
				calls := strings.Count(log, "openrc-ready new\n")
				wantCalls := tc.readyDelay + 1
				if tc.failReady {
					wantCalls = 90
				}
				if calls != wantCalls {
					t.Fatalf("readiness calls: got %d want %d", calls, wantCalls)
				}
				if tc.update && strings.Index(log, "flock -u 8\n") > strings.Index(log, "openrc-ready new\n") {
					t.Fatal("configuration lock released after the readiness check")
				}
			}
			if tc.update && (tc.failStart || tc.failReady) {
				if !strings.Contains(log, "flock -w 30 8\n") {
					t.Fatal("rollback did not reacquire the configuration lock")
				}
				if tc.failLock || tc.failStop {
					if strings.Contains(log, "openrc-ready previous\n") || !strings.Contains(string(out), "candidate left unchanged") {
						t.Fatal("rollback mutated configuration without its lock")
					}
				} else {
					wantCalls := 1
					if tc.failRollbackReady {
						wantCalls = 90
					}
					if strings.Count(log, "openrc-ready previous\n") != wantCalls || strings.Count(log, "flock -u 8\n") != 2 {
						t.Fatal("rollback readiness or configuration lock release missing")
					}
				}
			}
			if tc.failRollbackReady && !strings.Contains(string(out), "previous binary restored but did not become ready") {
				t.Fatal("claimed rollback readiness without proof")
			}
			if (tc.failStart || tc.failReady) && (strings.Contains(string(out), "==> done.") || strings.Contains(string(out), "==> agent updated.")) {
				t.Fatal("claimed installation success without readiness")
			}
		})
	}
}

// Reject withdrawn ARM installations before service detection, package installs,
// enrollment or downloads, including when the caller has root privileges.
func TestAgentInstallerRejectsARMBeforeMutation(t *testing.T) {
	dir := t.TempDir()
	for name, body := range map[string]string{"id": "#!/bin/sh\necho 0\n", "uname": "#!/bin/sh\necho aarch64\n"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0755); err != nil {
			t.Fatal(err)
		}
	}
	cmd := exec.Command("bash", "-c", string(InstallAgent), "installer", "--server", "https://panel.example.com", "--token", "fixture-token")
	cmd.Env = append(os.Environ(), "PATH="+dir+":"+os.Getenv("PATH"))
	output, err := cmd.CombinedOutput()
	if err == nil || !strings.Contains(string(output), "ARM 支持已暂停") {
		t.Fatalf("expected explicit ARM refusal, err=%v output=%s", err, output)
	}
}
