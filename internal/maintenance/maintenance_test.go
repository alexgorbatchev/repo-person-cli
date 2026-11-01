package maintenance

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func write(t *testing.T, path, text string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
}
func g(t *testing.T, repo string, args ...string) string {
	t.Helper()
	out, err := gitValue(context.Background(), repo, args...)
	if err != nil {
		t.Fatal(err)
	}
	return out
}
func fixture(t *testing.T) Config {
	t.Helper()
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	for k, v := range map[string]string{"GIT_AUTHOR_NAME": "Test", "GIT_AUTHOR_EMAIL": "test@example.com", "GIT_COMMITTER_NAME": "Test", "GIT_COMMITTER_EMAIL": "test@example.com"} {
		t.Setenv(k, v)
	}
	base := t.TempDir()
	t.Setenv("XDG_STATE_HOME", filepath.Join(base, "state"))
	remote := filepath.Join(base, "remote")
	repo := filepath.Join(base, "repo")
	if err := os.MkdirAll(remote, 0700); err != nil {
		t.Fatal(err)
	}
	g(t, remote, "init", "--bare", "-q", "-b", "main")
	g(t, base, "clone", "-q", remote, repo)
	write(t, filepath.Join(repo, "file"), "original\n")
	write(t, filepath.Join(repo, ".gitignore"), ".tmp/\n")
	if err := Init(context.Background(), repo); err != nil {
		t.Fatal(err)
	}
	g(t, repo, "add", ".")
	g(t, repo, "commit", "-qm", "initial")
	g(t, repo, "push", "-qu", "origin", "main")
	c, err := Load(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}
	c.Scanner = []string{"true"}
	c.Agent.Quiet = "1ns"
	return c
}
func fakeCodex(t *testing.T, c Config, body string) {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "bin")
	path := filepath.Join(bin, "codex")
	write(t, path, "#!/bin/sh\nset -eu\nif [ "+"\"${1:-}\" = \"--version\" ]; then echo codex-test; exit 0; fi\n"+body+"\n")
	if err := os.Chmod(path, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
}

const commitScript = `repo=''; output=''
while [ "$#" -gt 0 ]; do
 case "$1" in -C) repo="$2"; shift;; -o) output="$2"; shift;; esac
 shift
done
prompt=$(cat)
run=$(printf '%s' "$prompt" | sed -n 's/.*"run_id": "\([^"]*\)".*/\1/p')
git -C "$repo" add file
git -C "$repo" commit -qm "fix(sweep): complete fixture

Sweep-Run: $run"
printf '{"status":"committed","summary":"done","checks":["fixture"],"repairs":"none","remaining":"none"}' > "$output"`

func TestSync(t *testing.T) {
	for _, mode := range []string{"clean", "ahead", "behind", "diverged", "conflict", "dirty", "scanner", "destination"} {
		t.Run(mode, func(t *testing.T) {
			c := fixture(t)
			ctx := context.Background()
			before := g(t, c.Repo, "rev-parse", "HEAD")
			other := filepath.Join(t.TempDir(), "other")
			g(t, filepath.Dir(other), "clone", "-q", g(t, c.Repo, "remote", "get-url", "origin"), other)
			if mode == "ahead" || mode == "diverged" || mode == "conflict" || mode == "scanner" {
				write(t, filepath.Join(c.Repo, "file"), "local\n")
				g(t, c.Repo, "commit", "-qam", "local")
				before = g(t, c.Repo, "rev-parse", "HEAD")
			}
			if mode == "behind" || mode == "diverged" || mode == "conflict" {
				name := "remote-file"
				if mode == "conflict" {
					name = "file"
				}
				write(t, filepath.Join(other, name), "remote\n")
				g(t, other, "add", ".")
				g(t, other, "commit", "-qm", "remote")
				g(t, other, "push", "-q")
			}
			if mode == "dirty" {
				write(t, filepath.Join(c.Repo, "file"), "dirty\n")
			}
			if mode == "scanner" {
				c.Scanner = []string{"false"}
			}
			if mode == "destination" {
				g(t, c.Repo, "remote", "set-url", "--push", "origin", "/different")
			}
			err := synchronize(ctx, c, "main", filepath.Join(c.StateDir, "scan.log"))
			wantErr := mode == "conflict" || mode == "scanner" || mode == "destination"
			if (err != nil) != wantErr {
				t.Fatalf("err=%v", err)
			}
			after := g(t, c.Repo, "rev-parse", "HEAD")
			if wantErr && after != before {
				t.Fatal("failed sync changed HEAD")
			}
			if mode == "ahead" || mode == "diverged" {
				remote := g(t, c.Repo, "ls-remote", "origin", "refs/heads/main")
				if !strings.HasPrefix(remote, after) {
					t.Fatal("not pushed")
				}
			}
			if mode == "conflict" {
				if _, err := os.Stat(filepath.Join(c.Repo, ".git/rebase-merge")); !errors.Is(err, os.ErrNotExist) {
					t.Fatal("rebase left active")
				}
			}
		})
	}
}
func TestCycle(t *testing.T) {
	for _, mode := range []string{"success", "failure", "false-success", "timeout", "recent", "staged", "hook-failure", "scanner-failure"} {
		t.Run(mode, func(t *testing.T) {
			c := fixture(t)
			write(t, filepath.Join(c.Repo, "file"), "changed\n")
			script := commitScript
			switch mode {
			case "failure":
				script = "cat >/dev/null; exit 2"
			case "false-success":
				script = strings.ReplaceAll(commitScript, "fix(sweep):", "fix:")
			case "timeout":
				script = "sleep 5"
				c.Agent.Timeout = "30ms"
			case "recent":
				c.Agent.Quiet = "15m"
			case "staged":
				g(t, c.Repo, "add", "file")
			case "hook-failure":
				c.AfterSync = []string{"false"}
			case "scanner-failure":
				c.Scanner = []string{"false"}
			}
			fakeCodex(t, c, script)
			var out bytes.Buffer
			err := Run(context.Background(), c, false, &out)
			wantErr := mode != "success" && mode != "recent" && mode != "staged"
			if (err != nil) != wantErr {
				t.Fatalf("err=%v output=%s", err, &out)
			}
			if mode == "success" {
				if g(t, c.Repo, "status", "--porcelain") != "" {
					t.Fatal("not clean")
				}
				if g(t, c.Repo, "rev-parse", "HEAD") != g(t, c.Repo, "rev-parse", "@{upstream}") {
					t.Fatal("not pushed")
				}
			}
			if wantErr {
				if _, err := os.Stat(filepath.Join(c.StateDir, "LOG.md")); err != nil {
					t.Fatal(err)
				}
			}
			if err := Status(context.Background(), c, io.Discard); err != nil {
				t.Fatal(err)
			}
			if mode != "staged" {
				if err := Logs(c, io.Discard); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "failure" {
				s, err := loadState(filepath.Join(c.StateDir, "state.json"))
				if err != nil {
					t.Fatal(err)
				}
				s.Quiet = time.Nanosecond
				if !strings.Contains(s.readiness(time.Now(), false), "cooldown") {
					t.Fatal("missing cooldown")
				}
				if s.readiness(time.Now(), true) != "ready" {
					t.Fatal("retry blocked")
				}
			}
		})
	}
}
func TestConfigAndRegistry(t *testing.T) {
	c := fixture(t)
	ctx := context.Background()
	if err := Init(ctx, c.Repo); err == nil {
		t.Fatal("overwrote config")
	}
	for _, input := range []string{"bogus=true", "[agent]\nquiet=\"bad\"", "[agent]\neffort=\"bogus\"", "[agent]\nmodel=\"\"", "checks=[[]]", "scanner=[\"\"]", "state_dir=\"state\""} {
		write(t, filepath.Join(c.Repo, ConfigName), input)
		if _, err := Load(ctx, c.Repo); err == nil {
			t.Fatalf("accepted %s", input)
		}
	}
	write(t, filepath.Join(c.Repo, ConfigName), Template+"\n")
	if err := Register(ctx, c.Repo, true); err != nil {
		t.Fatal(err)
	}
	if err := Register(ctx, c.Repo, true); err != nil {
		t.Fatal(err)
	}
	paths, err := Registered()
	if err != nil || len(paths) != 1 {
		t.Fatalf("%v %v", paths, err)
	}
	if err := RunAll(ctx, io.Discard); err != nil {
		t.Fatal(err)
	}
	if err := Register(ctx, c.Repo, false); err != nil {
		t.Fatal(err)
	}
	paths, err = Registered()
	if err != nil || len(paths) != 0 {
		t.Fatalf("%v %v", paths, err)
	}
	if err := RunAll(ctx, io.Discard); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(c.Repo, ConfigName), "state_dir=\".tmp/person\"\n")
	got, err := Load(ctx, c.Repo)
	if err != nil || got.StateDir != filepath.Join(c.Repo, ".tmp/person") {
		t.Fatalf("%+v %v", got, err)
	}
	t.Setenv("XDG_STATE_HOME", "relative")
	if _, err := StateHome(); err == nil {
		t.Fatal("relative XDG allowed")
	}
	t.Setenv("XDG_STATE_HOME", "")
	if _, err := StateHome(); err != nil {
		t.Fatal(err)
	}
}
func TestLocks(t *testing.T) {
	c := fixture(t)
	guards := []*os.File{}
	for range 3 {
		f, err := slot()
		if err != nil {
			t.Fatal(err)
		}
		guards = append(guards, f)
	}
	if _, err := slot(); !errors.Is(err, ErrLocked) {
		t.Fatal(err)
	}
	write(t, filepath.Join(c.Repo, "file"), "changed\n")
	if err := sweep(context.Background(), c, "main", false, io.Discard); err != nil {
		t.Fatal(err)
	}
	for _, f := range guards {
		if err := f.Close(); err != nil {
			t.Fatal(err)
		}
	}
	guard, err := lock(filepath.Join(c.Repo, ".git/repo-person.lock"))
	if err != nil {
		t.Fatal(err)
	}
	defer guard.Close()
	if err := Run(context.Background(), c, false, io.Discard); !errors.Is(err, ErrLocked) {
		t.Fatal(err)
	}
}
func TestFingerprint(t *testing.T) {
	c := fixture(t)
	ctx := context.Background()
	before, err := Fingerprint(ctx, c.Repo)
	if err != nil || before != "" {
		t.Fatal(before, err)
	}
	for _, mode := range []string{"write", "untracked", "symlink", "delete", "deleted-parent", "special"} {
		t.Run(mode, func(t *testing.T) {
			switch mode {
			case "write":
				write(t, filepath.Join(c.Repo, "file"), "changed")
			case "untracked":
				write(t, filepath.Join(c.Repo, "new"), "new")
			case "symlink":
				if err := os.Symlink("file", filepath.Join(c.Repo, "link")); err != nil {
					t.Fatal(err)
				}
			case "delete":
				if err := os.Remove(filepath.Join(c.Repo, "file")); err != nil {
					t.Fatal(err)
				}
			case "deleted-parent":
				write(t, filepath.Join(c.Repo, "deep/nested/file"), "x")
				g(t, c.Repo, "add", "deep")
				g(t, c.Repo, "commit", "-qm", "nested")
				if err := os.RemoveAll(filepath.Join(c.Repo, "deep")); err != nil {
					t.Fatal(err)
				}
			case "special":
				g(t, c.Repo, "init", "-q", "nested-repo")
				write(t, filepath.Join(c.Repo, "nested-repo/file"), "x")
				g(t, filepath.Join(c.Repo, "nested-repo"), "add", ".")
				g(t, filepath.Join(c.Repo, "nested-repo"), "commit", "-qm", "nested")
			}
			after, err := Fingerprint(ctx, c.Repo)
			if mode == "special" {
				if err == nil {
					t.Fatal("nested repo accepted")
				}
				return
			}
			if err != nil || after == before {
				t.Fatal(after, err)
			}
			before = after
		})
	}
}
func TestStateAndLogs(t *testing.T) {
	dir := t.TempDir()
	s, err := loadState(filepath.Join(dir, "missing"))
	if err != nil {
		t.Fatal(err)
	}
	if s.readiness(time.Now(), false) != "clean" {
		t.Fatal(s)
	}
	s.Fingerprint = "x"
	if !strings.Contains(s.readiness(time.Now(), false), "unavailable") {
		t.Fatal(s)
	}
	s.Since = time.Now()
	if !strings.Contains(s.readiness(time.Now(), false), "quiet window") {
		t.Fatal(s)
	}
	old := filepath.Join(dir, "runs/old")
	write(t, filepath.Join(old, "events"), "old")
	if err := os.Chtimes(old, time.Unix(1, 0), time.Unix(1, 0)); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(dir, "runs/new/events"), "new")
	write(t, filepath.Join(dir, "runs/file"), "not directory")
	if err := prune(dir, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(old); !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	if err := failureLog(dir, "id", "sha", "error", map[string]string{"detail": "failed"}); err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(filepath.Join(dir, "state.json"), s); err != nil {
		t.Fatal(err)
	}
	if _, err := loadState(filepath.Join(dir, "state.json")); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(dir, "bad"), "not json")
	if _, err := loadState(filepath.Join(dir, "bad")); err == nil {
		t.Fatal("invalid state allowed")
	}
	if err := writeJSON(filepath.Join(dir, "bad2"), make(chan int)); err == nil {
		t.Fatal("invalid json allowed")
	}
}

func TestFailedCommitNeverPublishes(t *testing.T) {
	c := fixture(t)
	baseline := g(t, c.Repo, "rev-parse", "HEAD")
	write(t, filepath.Join(c.Repo, "file"), "changed\n")
	fakeCodex(t, c, strings.ReplaceAll(commitScript, "fix(sweep):", "fix:"))
	for range 2 {
		if err := Run(context.Background(), c, false, io.Discard); err == nil {
			t.Fatal("accepted rejected commit")
		}
		if g(t, c.Repo, "ls-remote", "origin", "refs/heads/main") != baseline+"\trefs/heads/main" {
			t.Fatal("published rejected commit")
		}
	}
}

func TestStatusActivity(t *testing.T) {
	c := fixture(t)
	ctx := context.Background()
	if got, err := activity(ctx, c.Repo); err != nil || got != "idle" {
		t.Fatal(got, err)
	}
	f, err := lock(filepath.Join(c.Repo, ".git/repo-person.lock"))
	if err != nil {
		t.Fatal(err)
	}
	if got, err := activity(ctx, c.Repo); err != nil || got != "running" {
		t.Fatal(got, err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if got, err := activity(ctx, c.Repo); err != nil || got != "idle" {
		t.Fatal(got, err)
	}
	if err := writeJSON(filepath.Join(c.StateDir, "state.json"), state{LastRun: "example"}); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := Status(ctx, c, &output); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "runs/example/events.jsonl") {
		t.Fatal(output.String())
	}
}
