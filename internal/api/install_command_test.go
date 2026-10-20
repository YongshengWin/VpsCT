package api

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestOfficialAgentCommandFailureAndQuoting(t *testing.T) {
	for _, tc := range []struct {
		name                    string
		download, install, want int
	}{
		{"success", 0, 0, 0}, {"download failure", 23, 0, 23}, {"install failure", 0, 42, 42},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			write := func(name, body string) {
				t.Helper()
				if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0700); err != nil {
					t.Fatal(err)
				}
			}
			write("curl", "#!/bin/sh\nexit \"$DOWNLOAD_STATUS\"\n")
			write("sudo", "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$ARGS_FILE\"\nexit \"$INSTALL_STATUS\"\n")
			server := "https://panel.example/'$(touch injected)"
			token := "token'with$quotes"
			cmd := exec.Command("sh", "-c", officialAgentCommand(server, token, false))
			cmd.Dir = dir
			cmd.Env = append(os.Environ(), "PATH="+dir+":"+os.Getenv("PATH"), "TMPDIR="+dir,
				"DOWNLOAD_STATUS="+strconv.Itoa(tc.download),
				"INSTALL_STATUS="+strconv.Itoa(tc.install), "ARGS_FILE="+filepath.Join(dir, "args"))
			err := cmd.Run()
			got := 0
			if err != nil {
				if e, ok := err.(*exec.ExitError); ok {
					got = e.ExitCode()
				} else {
					t.Fatal(err)
				}
			}
			if got != tc.want {
				t.Fatalf("exit=%d want=%d", got, tc.want)
			}
			args, readErr := os.ReadFile(filepath.Join(dir, "args"))
			if tc.download != 0 {
				if !os.IsNotExist(readErr) {
					t.Fatal("installer ran after failed download")
				}
			} else {
				if readErr != nil {
					t.Fatal(readErr)
				}
				lines := strings.Split(strings.TrimSpace(string(args)), "\n")
				if len(lines) != 6 || lines[3] != server || lines[5] != token {
					t.Fatalf("arguments changed: %q", lines)
				}
				if _, err := os.Stat(lines[1]); !os.IsNotExist(err) {
					t.Fatal("temporary installer was not removed")
				}
			}
			if _, err := os.Stat(filepath.Join(dir, "injected")); !os.IsNotExist(err) {
				t.Fatal("shell injection")
			}
		})
	}
}

func TestOfficialAgentCommandBootstrapsRootAlpineWithoutSudo(t *testing.T) {
	dir := t.TempDir()
	for name, body := range map[string]string{
		"id":   "#!/bin/sh\nprintf '0\\n'\n",
		"apk":  "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$BOOTSTRAP_FILE\"\n",
		"curl": "#!/bin/sh\nexit 0\n",
		"bash": "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$ARGS_FILE\"\n",
		"sudo": "#!/bin/sh\nexit 99\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0700); err != nil {
			t.Fatal(err)
		}
	}
	cmd := exec.Command("sh", "-c", officialAgentCommand("https://panel.example", "", true))
	cmd.Env = append(os.Environ(), "PATH="+dir+":"+os.Getenv("PATH"), "TMPDIR="+dir,
		"BOOTSTRAP_FILE="+filepath.Join(dir, "bootstrap"), "ARGS_FILE="+filepath.Join(dir, "args"))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("root bootstrap: %v %s", err, out)
	}
	bootstrap, err := os.ReadFile(filepath.Join(dir, "bootstrap"))
	if err != nil || string(bootstrap) != "add\n--no-cache\nbash\ncurl\nca-certificates\n" {
		t.Fatal("Alpine bootstrap dependencies changed", err)
	}
	args, err := os.ReadFile(filepath.Join(dir, "args"))
	lines := strings.Split(strings.TrimSpace(string(args)), "\n")
	if err != nil || len(lines) != 4 || lines[1] != "--server" || lines[2] != "https://panel.example" || lines[3] != "--update" {
		t.Fatalf("update arguments changed: %q %v", lines, err)
	}
	if _, err := os.Stat(lines[0]); !os.IsNotExist(err) {
		t.Fatal("temporary installer was not cleaned up")
	}
}
