package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func executeCommand(args ...string) (string, error) {
	buf := new(bytes.Buffer)
	cmd := newRootCommand()
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs(args)

	err := cmd.Execute()
	return buf.String(), err
}

func TestRun(t *testing.T) {
	for _, tt := range []struct {
		name string
		mode string
		args []string
		code int
		want string
	}{
		{"success", "0", []string{"--version"}, 0, version + "\n"},
		{"human error", "0", []string{"unknown-command"}, 1, "[ERROR] unknown command"},
		{"agent error", "1", []string{"unknown-command"}, 1, "ERR: unknown command"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("AGENT", tt.mode)
			stdout, stderr := new(bytes.Buffer), new(bytes.Buffer)
			cmd := newRootCommand()
			cmd.SetArgs(tt.args)
			cmd.SetOut(stdout)
			cmd.SetErr(stderr)
			if code := run(cmd); code != tt.code {
				t.Fatalf("exit code = %d, want %d", code, tt.code)
			}
			if tt.code == 0 {
				if stdout.String() != tt.want || stderr.Len() != 0 {
					t.Fatalf("stdout = %q, stderr = %q", stdout, stderr)
				}
			} else if stdout.Len() != 0 || !strings.Contains(stderr.String(), tt.want) {
				t.Fatalf("stdout = %q, stderr = %q", stdout, stderr)
			}
		})
	}
}

func TestRootCommand_HelpAndVersion(t *testing.T) {
	t.Setenv("AGENT", "0")
	out, err := executeCommand("--help")
	if err != nil {
		t.Fatalf("help failed: %v", err)
	}
	if len(out) == 0 {
		t.Fatal("expected help output")
	}

	outVer, errVer := executeCommand("--version")
	if errVer != nil {
		t.Fatalf("version failed: %v", errVer)
	}
	if outVer != version+"\n" {
		t.Errorf("expected '%s\\n', got %q", version, outVer)
	}
}

func TestOperations(t *testing.T) {
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	repo := t.TempDir()
	for _, args := range [][]string{{"init", "-q", "-b", "main"}, {"config", "user.name", "Test"}, {"config", "user.email", "test@example.com"}} {
		cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatal(err, string(out))
		}
	}
	if _, err := executeCommand("init", "--repo", repo); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "repo-person.toml"), []byte("sync=false\nsweep=false\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "."}, {"commit", "-qm", "initial"}, {"remote", "add", "origin", "/offline"}, {"config", "branch.main.remote", "origin"}, {"config", "branch.main.merge", "refs/heads/main"}} {
		cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatal(err, string(out))
		}
	}
	for _, args := range [][]string{{"enable"}, {"run"}, {"retry"}, {"status"}, {"fingerprint"}, {"logs"}, {"run", "--all"}, {"disable"}} {
		out, err := executeCommand(append(args, "--repo", repo)...)
		if err != nil {
			t.Fatal(args, err, out)
		}
	}
	for _, args := range [][]string{{"run"}, {"fingerprint"}, {"status"}} {
		if _, err := executeCommand(append(args, "--repo", t.TempDir())...); err == nil {
			t.Fatal("accepted non-repo")
		}
	}
}
