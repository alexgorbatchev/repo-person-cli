package maintenance

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestInvalidRepositories(t *testing.T) {
	t.Setenv("GIT_CEILING_DIRECTORIES", os.TempDir())
	ctx := context.Background()
	bad := t.TempDir()
	c := defaults()
	c.Repo = bad
	c.StateDir = t.TempDir()
	if _, err := Root(ctx, bad); err == nil {
		t.Fatal("root accepted")
	}
	if err := Init(ctx, bad); err == nil {
		t.Fatal("init accepted")
	}
	if _, err := Load(ctx, bad); err == nil {
		t.Fatal("load accepted")
	}
	if err := Register(ctx, bad, true); err == nil {
		t.Fatal("registration accepted")
	}
	if err := Run(ctx, c, false, io.Discard); err == nil {
		t.Fatal("run accepted")
	}
	if _, err := eligible(ctx, bad, "main"); err == nil {
		t.Fatal("eligibility accepted")
	}
	if _, err := tracking(ctx, bad, "main"); err == nil {
		t.Fatal("tracking accepted")
	}
	if _, err := inspectChanges(ctx, bad); err == nil {
		t.Fatal("inspection accepted")
	}
	if err := synchronize(ctx, c, "main", filepath.Join(c.StateDir, "log")); err == nil {
		t.Fatal("sync accepted")
	}
	if err := Status(ctx, c, io.Discard); err == nil {
		t.Fatal("status accepted")
	}
	if err := Logs(c, io.Discard); err == nil {
		t.Fatal("logs accepted")
	}
}
func TestUnsafeCheckout(t *testing.T) {
	ctx := context.Background()
	c := fixture(t)
	for _, marker := range []string{"MERGE_HEAD", "rebase-apply", "sequencer", "index.lock"} {
		path := filepath.Join(c.Repo, ".git", marker)
		write(t, path, "busy")
		ok, err := eligible(ctx, c.Repo, "main")
		if err != nil || ok {
			t.Fatal(ok, err)
		}
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
	}
	if ok, err := eligible(ctx, c.Repo, "other"); err != nil || ok {
		t.Fatal(ok, err)
	}
	g(t, c.Repo, "config", "branch.main.remote", ".")
	if _, err := tracking(ctx, c.Repo, "main"); err == nil {
		t.Fatal("local upstream allowed")
	}
	if _, err := eligible(ctx, c.Repo, "main"); err == nil {
		t.Fatal("local upstream eligible")
	}
	g(t, c.Repo, "config", "--unset", "branch.main.merge")
	if _, err := tracking(ctx, c.Repo, "main"); err == nil {
		t.Fatal("missing merge allowed")
	}
	if err := Run(ctx, c, false, io.Discard); err == nil {
		t.Fatal("unsafe tracking allowed")
	}
}
func TestStateIOFailures(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "file")
	write(t, file, "occupied")
	child := filepath.Join(file, "child")
	if _, err := lock(child); err == nil {
		t.Fatal("lock succeeded")
	}
	if _, err := lock(dir); err == nil {
		t.Fatal("directory lock succeeded")
	}
	if _, err := loadState(dir); err == nil {
		t.Fatal("loaded directory")
	}
	if err := writeJSON(child, "x"); err == nil {
		t.Fatal("write succeeded")
	}
	if err := writeJSON(dir, "x"); err == nil {
		t.Fatal("rename succeeded")
	}
	write(t, filepath.Join(dir, "blocked.new/child"), "x")
	if err := writeJSON(filepath.Join(dir, "blocked"), "x"); err == nil {
		t.Fatal("temporary write succeeded")
	}
	if err := prune(file, time.Now()); err == nil {
		t.Fatal("prune succeeded")
	}
	if err := failureLog(file, "x", "x", "x", nil); err == nil {
		t.Fatal("log succeeded")
	}
	if err := failureLog(dir, "x", "x", "x", make(chan int)); err == nil {
		t.Fatal("invalid details accepted")
	}
	t.Setenv("XDG_STATE_HOME", file)
	if _, err := Registered(); err == nil {
		t.Fatal("registry read succeeded")
	}
	if _, err := slot(); err == nil {
		t.Fatal("slot succeeded")
	}
}
func TestAttemptFailures(t *testing.T) {
	for _, mode := range []string{"changed", "missing-result", "invalid-result", "reported-failure", "prompt", "agent-log", "run-directory", "state-write"} {
		t.Run(mode, func(t *testing.T) {
			c := fixture(t)
			c.Sync = false
			write(t, filepath.Join(c.Repo, "file"), "pending\n")
			current, err := inspectChanges(context.Background(), c.Repo)
			if err != nil {
				t.Fatal(err)
			}
			a := attempt{Root: c.Repo, Config: c, Output: io.Discard, Timeout: time.Second, Candidate: candidate{member: member{Path: ".", Branch: "main"}, Directory: c.StateDir, State: state{Fingerprint: current.Fingerprint, Since: current.Modified}}}
			script := "cat >/dev/null; exit 0"
			switch mode {
			case "changed":
				a.Candidate.State.Fingerprint = "old"
			case "invalid-result":
				script = strings.ReplaceAll(commitScript, `"status":"committed"`, `"status":`)
			case "reported-failure":
				script = strings.ReplaceAll(commitScript, `"status":"committed"`, `"status":"failed"`)
			case "prompt":
				a.Config.Agent.Prompt = "missing-prompt.md"
			case "agent-log":
				script = "cat >/dev/null; printf 'Check missing' > '" + filepath.Join(c.StateDir, "LOG.md") + "'; exit 1"
			case "run-directory":
				write(t, filepath.Join(c.StateDir, "runs"), "file")
			case "state-write":
				write(t, filepath.Join(c.StateDir, "state.json.new/child"), "x")
			}
			fakeCodex(t, c, script)
			err = a.execute(context.Background())
			if err == nil {
				t.Fatal("failed attempt accepted")
			}
			if mode == "changed" && !errors.Is(err, errChanged) {
				t.Fatal(err)
			}
			if mode == "agent-log" {
				data, err := os.ReadFile(filepath.Join(a.RunDir, "agent-LOG.md"))
				if err != nil || !strings.Contains(string(data), "Check missing") {
					t.Fatal(string(data), err)
				}
			}
		})
	}
}
func TestCustomPromptAndScanner(t *testing.T) {
	c := fixture(t)
	c.Agent.Prompt = "custom.md"
	write(t, filepath.Join(c.Repo, c.Agent.Prompt), "custom instructions")
	for _, path := range []string{c.Agent.Prompt, filepath.Join(c.Repo, c.Agent.Prompt)} {
		c.Agent.Prompt = path
		if data, err := c.prompt(); err != nil || string(data) != "custom instructions" {
			t.Fatal(string(data), err)
		}
	}
	bin := t.TempDir()
	write(t, filepath.Join(bin, "gitleaks"), "#!/bin/sh\n[ \"$1\" = git ] && [ \"$2\" = --redact ]\n")
	if err := os.Chmod(filepath.Join(bin, "gitleaks"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	if err := scan(context.Background(), c.Repo, nil, "base", "head", filepath.Join(c.StateDir, "scan.log")); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(c.StateDir, "file"), "occupied")
	if err := scan(context.Background(), c.Repo, []string{"true"}, "base", "head", filepath.Join(c.StateDir, "file/log")); err == nil {
		t.Fatal("log error hidden")
	}
	if err := command(context.Background(), c.Repo, []string{"true"}, io.Discard); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := command(ctx, c.Repo, []string{"sleep", "5"}, io.Discard); err == nil {
		t.Fatal("timeout hidden")
	}
}
func TestRegistrationFailures(t *testing.T) {
	c := fixture(t)
	ctx := context.Background()
	home, err := StateHome()
	if err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(c.Repo, ConfigName), "unknown=true")
	if err := Register(ctx, c.Repo, true); err == nil {
		t.Fatal("registered invalid config")
	}
	write(t, filepath.Join(c.Repo, ConfigName), Template)
	write(t, filepath.Join(home, "repositories.json"), "broken")
	if _, err := Registered(); err == nil {
		t.Fatal("invalid registry accepted")
	}
	if err := RunAll(ctx, io.Discard); err == nil {
		t.Fatal("invalid registry accepted")
	}
	if err := Register(ctx, c.Repo, true); err == nil {
		t.Fatal("invalid registry overwritten")
	}
	if err := writeJSON(filepath.Join(home, "repositories.json"), []string{t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	if err := RunAll(ctx, io.Discard); err == nil {
		t.Fatal("invalid repo hidden")
	}
	guard, err := lock(filepath.Join(home, "registry.lock"))
	if err != nil {
		t.Fatal(err)
	}
	defer guard.Close()
	if err := Register(ctx, c.Repo, true); !errors.Is(err, ErrLocked) {
		t.Fatal(err)
	}
}

func TestPreflightAndReadFailures(t *testing.T) {
	c := fixture(t)
	ctx := context.Background()
	if err := os.Remove(filepath.Join(c.Repo, ConfigName)); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(ctx, c.Repo); err == nil {
		t.Fatal("missing config accepted")
	}
	g(t, c.Repo, "restore", ConfigName)
	statePath := filepath.Join(c.StateDir, "state.json")
	write(t, statePath, "broken")
	if err := Status(ctx, c, io.Discard); err == nil {
		t.Fatal("broken state accepted")
	}
	if err := sweep(ctx, c, "main", false, io.Discard); err == nil {
		t.Fatal("broken sweep state accepted")
	}
	if err := os.Remove(statePath); err != nil {
		t.Fatal(err)
	}
	g(t, c.Repo, "switch", "--detach", "-q")
	if err := Run(ctx, c, false, io.Discard); err != nil {
		t.Fatal(err)
	}
	g(t, c.Repo, "switch", "main", "-q")
	c.StateDir = filepath.Join(t.TempDir(), "file")
	write(t, c.StateDir, "occupied")
	if err := Run(ctx, c, false, io.Discard); err == nil {
		t.Fatal("state error hidden")
	}
	if err := sweep(ctx, c, "main", false, io.Discard); err == nil {
		t.Fatal("state error hidden")
	}
	t.Setenv("XDG_STATE_HOME", "relative")
	if _, err := Registered(); err == nil {
		t.Fatal("invalid XDG accepted")
	}
	if err := Register(ctx, c.Repo, false); err == nil {
		t.Fatal("invalid XDG accepted")
	}
	if _, err := slot(); err == nil {
		t.Fatal("invalid XDG accepted")
	}
	if _, err := Load(ctx, c.Repo); err == nil {
		t.Fatal("invalid XDG accepted")
	}
}
func TestAttemptRecordingAndValidation(t *testing.T) {
	c := fixture(t)
	ctx := context.Background()
	dir := filepath.Join(c.StateDir, "runs/attempt")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	a := attempt{Root: c.Repo, Config: c, Output: io.Discard, Candidate: candidate{member: member{Path: ".", Branch: "main"}, Directory: c.StateDir}, RunDir: dir, RunID: "id", Baseline: g(t, c.Repo, "rev-parse", "HEAD")}
	if err := a.validate(ctx); err == nil {
		t.Fatal("root commit accepted")
	}
	a.Config.Agent.Prompt = "custom.md"
	write(t, filepath.Join(c.Repo, "custom.md"), "custom")
	if data, err := a.prompt(); err != nil || !strings.Contains(string(data), "custom") {
		t.Fatal(string(data), err)
	}
	write(t, filepath.Join(c.StateDir, "LOG.md"), "resolved")
	if err := a.record(nil, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(c.StateDir, "LOG.md")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("resolved log remains")
	}
	if err := os.RemoveAll(filepath.Join(c.Repo, ".git")); err != nil {
		t.Fatal(err)
	}
	if err := a.record(errors.New("failed"), nil); err == nil {
		t.Fatal("lost repo error hidden")
	}
	if err := a.blockPublication(ctx); err == nil {
		t.Fatal("lost HEAD error hidden")
	}
}

func TestMissingCodexDoesNotCooldown(t *testing.T) {
	c := fixture(t)
	c.Sync = false
	write(t, filepath.Join(c.Repo, "file"), "pending")
	bin := t.TempDir()
	if err := os.Symlink("/usr/bin/true", filepath.Join(bin, "true")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/usr/bin/git", filepath.Join(bin, "git")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	if err := sweep(context.Background(), c, "main", false, io.Discard); err == nil {
		t.Fatal("missing Codex accepted")
	}
	s, err := loadState(filepath.Join(c.StateDir, "state.json"))
	if err != nil || !s.FailedAt.IsZero() {
		t.Fatal(s, err)
	}
}
func TestRemoteRewind(t *testing.T) {
	c := fixture(t)
	remote := g(t, c.Repo, "remote", "get-url", "origin")
	base := g(t, c.Repo, "rev-parse", "HEAD")
	write(t, filepath.Join(c.Repo, "file"), "next")
	g(t, c.Repo, "commit", "-qam", "next")
	g(t, c.Repo, "push", "-q")
	head := g(t, c.Repo, "rev-parse", "HEAD")
	g(t, remote, "update-ref", "refs/heads/main", base)
	for range 2 {
		if err := synchronize(context.Background(), c, "main", filepath.Join(c.StateDir, "scan.log")); err == nil || !strings.Contains(err.Error(), "rewound") {
			t.Fatal(err)
		}
	}
	if g(t, c.Repo, "rev-parse", "HEAD") != head {
		t.Fatal("changed checkout")
	}
}

func TestMissingScannerPreventsAgent(t *testing.T) {
	c := fixture(t)
	c.Scanner = []string{"repo-person-missing-scanner"}
	write(t, filepath.Join(c.Repo, "file"), "changed")
	if err := sweep(context.Background(), c, "main", false, io.Discard); err == nil {
		t.Fatal("scanner prerequisite ignored")
	}
	s, err := loadState(filepath.Join(c.StateDir, "state.json"))
	if err != nil || !s.LastAttempt.IsZero() {
		t.Fatal(s, err)
	}
}
