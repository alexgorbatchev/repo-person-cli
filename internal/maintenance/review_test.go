package maintenance

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestGitHookDiagnostics(t *testing.T) {
	c := fixture(t)
	hook := filepath.Join(c.Repo, ".git/hooks/pre-push")
	write(t, hook, "#!/bin/sh\necho policy-rejected >&2\nexit 1\n")
	if err := os.Chmod(hook, 0700); err != nil {
		t.Fatal(err)
	}
	_, err := git(context.Background(), c.Repo, "push")
	if err == nil || !strings.Contains(err.Error(), "policy-rejected") {
		t.Fatal(err)
	}
}
func TestProcessCancellation(t *testing.T) {
	for _, body := range []string{"sleep 30 & wait", "trap '' TERM; sleep 30 & wait"} {
		t.Run(body, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
			defer cancel()
			cmd := exec.CommandContext(ctx, "sh", "-c", body)
			var out bytes.Buffer
			cmd.Stdout = &out
			cmd.Stderr = &out
			started := time.Now()
			if err := runProcess(cmd); err == nil {
				t.Fatal("accepted canceled process")
			}
			if time.Since(started) > 4*time.Second {
				t.Fatal("descendant pipes held runner open")
			}
		})
	}
}
func TestIncompleteAttemptBlocksPublication(t *testing.T) {
	c := fixture(t)
	before := g(t, c.Repo, "rev-parse", "origin/main")
	write(t, filepath.Join(c.Repo, "file"), "orphan commit")
	g(t, c.Repo, "commit", "-qam", "orphan")
	write(t, filepath.Join(c.StateDir, "incomplete.json"), `{"run_id":"interrupted"}`)
	for _, retry := range []bool{false, true} {
		err := Run(context.Background(), c, retry, io.Discard)
		if err == nil || !strings.Contains(err.Error(), "incomplete agent attempt") {
			t.Fatal(err)
		}
	}
	if got := g(t, g(t, c.Repo, "remote", "get-url", "origin"), "rev-parse", "main"); got != before {
		t.Fatal("published orphan")
	}
}
func TestShortBudgetDefersWithoutCooldown(t *testing.T) {
	c := fixture(t)
	c.Sync = false
	write(t, filepath.Join(c.Repo, "file"), "pending")
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	var out bytes.Buffer
	if err := Run(ctx, c, false, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "deferred:") {
		t.Fatal(out.String())
	}
	s, err := loadState(filepath.Join(c.StateDir, "state.json"))
	if err != nil || !s.FailedAt.IsZero() || s.LastRun != "" {
		t.Fatal(s, err)
	}
}
func TestBrokenCodexStartup(t *testing.T) {
	c := fixture(t)
	c.Sync = false
	write(t, filepath.Join(c.Repo, "file"), "pending")
	fakeCodex(t, c, "exit 127")
	// Break the actual executable, including its version probe.
	bin := strings.Split(os.Getenv("PATH"), ":")[0]
	write(t, filepath.Join(bin, "codex"), "#!/bin/sh\necho missing-node >&2\nexit 127\n")
	if err := Run(context.Background(), c, false, io.Discard); err == nil {
		t.Fatal("accepted startup failure")
	}
	s, err := loadState(filepath.Join(c.StateDir, "state.json"))
	if err != nil || !s.FailedAt.IsZero() {
		t.Fatal(s, err)
	}
}
func TestCycleLogRecoveryAndPrefix(t *testing.T) {
	c := fixture(t)
	c.Sync = false
	c.Sweep = false
	c.AfterSync = []string{"false"}
	var out bytes.Buffer
	if err := Run(context.Background(), c, false, &out); err == nil {
		t.Fatal("expected failure")
	}
	if _, err := os.Stat(filepath.Join(c.StateDir, "LOG.md")); err != nil {
		t.Fatal(err)
	}
	c.AfterSync = nil
	if err := Run(context.Background(), c, false, &out); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(c.StateDir, "LOG.md")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("stale failure", err)
	}
	for _, line := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		if !strings.HasPrefix(line, "["+c.Repo+"] ") {
			t.Fatal("unidentified output", line)
		}
	}
}
func TestFinalSyncAfterDeadline(t *testing.T) {
	c := fixture(t)
	c.Sweep = false
	// Initial sync sees a dirty tree; the hook commits then outlives the budget.
	write(t, filepath.Join(c.Repo, "file"), "pending")
	c.AfterSync = []string{"sh", "-c", "git add file && git commit -qm finished && sleep 30"}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := Run(ctx, c, false, io.Discard); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	if g(t, c.Repo, "rev-parse", "HEAD") != g(t, g(t, c.Repo, "remote", "get-url", "origin"), "rev-parse", "main") {
		t.Fatal("final sync skipped")
	}
}

func TestAttemptCooldownClassification(t *testing.T) {
	for _, tc := range []struct {
		name        string
		interrupted bool
		code        int
		cooldown    bool
	}{
		{"parent canceled", true, -1, false}, {"agent timeout", false, -1, true},
		{"agent killed after launch", false, 137, true}, {"missing interpreter", false, 127, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := fixture(t)
			write(t, filepath.Join(c.Repo, "file"), "pending")
			current, err := inspectChanges(context.Background(), c.Repo)
			if err != nil {
				t.Fatal(err)
			}
			a := attempt{Root: c.Repo, Config: c, Output: io.Discard, RunID: "test", RunDir: filepath.Join(c.StateDir, "runs/test"), Baseline: g(t, c.Repo, "rev-parse", "HEAD"), ProcessExit: tc.code, Interrupted: tc.interrupted, Candidate: candidate{member: member{Path: ".", Branch: "main"}, Directory: c.StateDir, State: state{Fingerprint: current.Fingerprint}}}
			failure := error(context.DeadlineExceeded)
			if tc.code == 127 {
				failure = exec.Command("sh", "-c", "exit 127").Run()
			}
			if err := a.record(failure, nil); err != nil {
				t.Fatal(err)
			}
			s, err := loadState(filepath.Join(c.StateDir, "state.json"))
			if err != nil {
				t.Fatal(err)
			}
			if !s.FailedAt.IsZero() != tc.cooldown {
				t.Fatal(s)
			}
		})
	}
}

func TestTimedOutAgentLeavesPublicationGuard(t *testing.T) {
	c := fixture(t)
	c.Agent.Timeout = "50ms"
	before := g(t, c.Repo, "rev-parse", "HEAD")
	write(t, filepath.Join(c.Repo, "file"), "pending")
	fakeCodex(t, c, "sleep 30")
	if err := Run(context.Background(), c, false, io.Discard); err == nil {
		t.Fatal("expected timeout")
	}
	if err := incomplete(c.StateDir); err == nil {
		t.Fatal("lost incomplete guard")
	}
	// A surviving detached child can finish its commit after the runner returns.
	g(t, c.Repo, "commit", "-qam", "late orphan")
	if err := synchronize(context.Background(), c, "main", filepath.Join(c.StateDir, "scan.log")); err == nil {
		t.Fatal("accepted incomplete publication")
	}
	var out bytes.Buffer
	if err := Status(context.Background(), c, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "State: blocked:") {
		t.Fatal(out.String())
	}
	if g(t, g(t, c.Repo, "remote", "get-url", "origin"), "rev-parse", "main") != before {
		t.Fatal("published orphan")
	}
}
func TestCanceledBeforeLaunch(t *testing.T) {
	c := fixture(t)
	a := attempt{Root: c.Repo, Config: c, Output: io.Discard, Candidate: candidate{member: member{Path: ".", Branch: "main"}, Directory: c.StateDir}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := a.execute(ctx); err == nil {
		t.Fatal("expected cancellation")
	}
	s, err := loadState(filepath.Join(c.StateDir, "state.json"))
	if err != nil || !s.FailedAt.IsZero() {
		t.Fatal(s, err)
	}
}
func TestPartialRepositoryOutput(t *testing.T) {
	var out bytes.Buffer
	shared := &lockedWriter{out: &out}
	a := &prefixWriter{out: shared, prefix: "[A] "}
	b := &prefixWriter{out: shared, prefix: "[B] "}
	for _, step := range []struct {
		w    *prefixWriter
		text string
	}{{a, "Build"}, {b, "other\n"}, {a, "ing\nnext"}, {b, "tail"}} {
		if _, err := step.w.Write([]byte(step.text)); err != nil {
			t.Fatal(err)
		}
	}
	if err := a.Flush(); err != nil {
		t.Fatal(err)
	}
	if err := b.Flush(); err != nil {
		t.Fatal(err)
	}
	if out.String() != "[B] other\n[A] Building\n[A] next\n[B] tail\n" {
		t.Fatal(out.String())
	}
}
func TestAgentAndCycleLogLifecycle(t *testing.T) {
	c := fixture(t)
	write(t, filepath.Join(c.Repo, "file"), "pending")
	current, err := inspectChanges(context.Background(), c.Repo)
	if err != nil {
		t.Fatal(err)
	}
	s := state{LastRun: "old", Fingerprint: current.Fingerprint, FailedFingerprint: current.Fingerprint}
	if err := writeJSON(filepath.Join(c.StateDir, "state.json"), s); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(c.StateDir, "runs/old/LOG.md"), "agent unresolved")
	for _, failure := range []error{errors.New("fetch failed"), nil} {
		if err := updateCycleLog(c, failure); err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(filepath.Join(c.StateDir, "LOG.md"))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(data), "agent unresolved") {
			t.Fatal(string(data))
		}
		if failure == nil && strings.Contains(string(data), "fetch failed") {
			t.Fatal("stale cycle failure")
		}
	}
	g(t, c.Repo, "restore", "file")
	if err := updateCycleLog(c, errors.New("another fetch error")); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(c.StateDir, "LOG.md"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "agent unresolved") {
		t.Fatal("resurrected resolved agent failure")
	}
	if err := updateCycleLog(c, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(c.StateDir, "LOG.md")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
}

func TestSlowGitHookExceedsReadTimeout(t *testing.T) {
	c := fixture(t)
	hook := filepath.Join(c.Repo, ".git/hooks/pre-push")
	write(t, hook, "#!/bin/sh\nsleep 31\n")
	if err := os.Chmod(hook, 0700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	if _, err := git(ctx, c.Repo, "push"); err != nil {
		t.Fatal("slow hook killed at read timeout", err)
	}
}

func TestUnresolvedReportSurvivesPruning(t *testing.T) {
	c := fixture(t)
	if err := writeJSON(filepath.Join(c.StateDir, "state.json"), state{LastRun: "unresolved", FailedFingerprint: "pending"}); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"unresolved", "resolved"} {
		path := filepath.Join(c.StateDir, "runs", name)
		write(t, filepath.Join(path, "LOG.md"), "report")
		old := time.Now().Add(-2 * logRetention)
		if err := os.Chtimes(path, old, old); err != nil {
			t.Fatal(err)
		}
	}
	if err := prune(c.StateDir, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(c.StateDir, "runs/unresolved/LOG.md")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(c.StateDir, "runs/resolved")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
}
func TestInspectionFailureStillUpdatesLog(t *testing.T) {
	c := fixture(t)
	if err := writeJSON(filepath.Join(c.StateDir, "state.json"), state{LastRun: "old", FailedFingerprint: "pending"}); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(c.Repo, ".git/index"), "broken index")
	if err := updateCycleLog(c, errors.New("current failure")); err == nil {
		t.Fatal("inspection error hidden")
	}
	data, err := os.ReadFile(filepath.Join(c.StateDir, "LOG.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "current failure") || !strings.Contains(string(data), "index") {
		t.Fatal(string(data))
	}
}
func TestCancellationDuringValidation(t *testing.T) {
	c := fixture(t)
	marker := filepath.Join(t.TempDir(), "validating")
	c.Scanner = []string{"sh", "-c", `touch "$1"; sleep 30`, "scanner", marker}
	write(t, filepath.Join(c.Repo, "file"), "pending")
	fakeCodex(t, c, commitScript)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- Run(ctx, c, false, io.Discard) }()
	deadline := time.NewTimer(10 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	waiting := true
	for waiting {
		select {
		case err := <-done:
			t.Fatalf("run ended before validation: %v", err)
		case <-deadline.C:
			cancel()
			<-done
			t.Fatal("never reached validation")
		case <-tick.C:
			if _, err := os.Stat(marker); err == nil {
				waiting = false
			}
		}
	}
	cancel()
	if err := <-done; err == nil {
		t.Fatal("cancellation ignored")
	}
	s, err := loadState(filepath.Join(c.StateDir, "state.json"))
	if err != nil || !s.FailedAt.IsZero() {
		t.Fatal(s, err)
	}
	if _, err := os.Stat(filepath.Join(c.StateDir, "blocked-head")); err != nil {
		t.Fatal("lost guard", err)
	}
}
func TestCanceledCodexNeverStarted(t *testing.T) {
	c := fixture(t)
	fakeCodex(t, c, "exit 0")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	j := codexJob{Repo: c.Repo, Directory: t.TempDir(), Timeout: time.Minute}
	if err := j.execute(ctx, nil); err == nil || !j.StartFailed || j.Terminated {
		t.Fatal(err, j)
	}
}
