package maintenance

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"
)

const cycleBudget = 20 * time.Minute

var ErrLocked = errors.New("maintenance already running")

func lock(path string) (*os.File, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, ErrLocked
		}
		return nil, err
	}
	return f, nil
}
func slot() (*os.File, error) {
	home, err := StateHome()
	if err != nil {
		return nil, err
	}
	for i := range 3 {
		f, err := lock(filepath.Join(home, fmt.Sprintf("agent-%d.lock", i)))
		if err == nil {
			return f, nil
		}
		if !errors.Is(err, ErrLocked) {
			return nil, err
		}
	}
	return nil, ErrLocked
}

// Run keeps the final synchronization independent of an agent failure.
func Run(ctx context.Context, c Config, retry bool, out io.Writer) (err error) {
	prefixed := &prefixWriter{out: out, prefix: "[" + c.Repo + "] "}
	defer func() { err = errors.Join(err, prefixed.Flush()) }()
	out = prefixed
	ctx, cancel := context.WithTimeout(ctx, cycleBudget)
	defer cancel()
	common, err := gitValue(ctx, c.Repo, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return err
	}
	guard, err := lock(filepath.Join(common, "repo-person.lock"))
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, guard.Close()) }()
	if err = os.MkdirAll(c.StateDir, 0700); err != nil {
		return err
	}
	logFile, err := os.OpenFile(filepath.Join(c.StateDir, "latest.log"), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, logFile.Close()) }()
	output := io.MultiWriter(out, logFile)
	started := time.Now()
	defer func() {
		err = errors.Join(err, updateCycleLog(c, err))
		err = errors.Join(err, writeJSON(filepath.Join(c.StateDir, "latest.json"), map[string]any{"started": started, "finished": time.Now(), "success": err == nil, "error": errorText(err)}))
		if err != nil {
			fmt.Fprintf(logFile, "Failed: %v\n", err)
		}
	}()
	_, err = fmt.Fprintf(output, "Repository: %s\nStarted: %s\n", c.Repo, started.Local().Format(displayTime))
	if err != nil {
		return err
	}
	if err := incomplete(c.StateDir); err != nil {
		return err
	}
	if err := prune(c.StateDir, time.Now()); err != nil {
		return err
	}
	branch, err := gitValue(ctx, c.Repo, "branch", "--show-current")
	if err != nil {
		return err
	}
	ok, err := eligible(ctx, c.Repo, branch)
	if err != nil {
		return err
	}
	if !ok {
		_, err = fmt.Fprintln(output, "skipped: staged changes, active Git operation, or detached checkout")
		return err
	}
	var failures error

	if c.Sync {
		failures = synchronize(ctx, c, branch, filepath.Join(c.StateDir, "sync-before.log"))
	}
	if ctx.Err() == nil && len(c.AfterSync) > 0 {
		failures = errors.Join(failures, command(ctx, c.Repo, c.AfterSync, output))
	}
	if c.Sweep && ctx.Err() == nil {
		failures = errors.Join(failures, sweep(ctx, c, branch, retry, output))
	}
	if c.Sync {
		finalCtx, finish := context.WithTimeout(context.WithoutCancel(ctx), finalSyncBudget)
		failures = errors.Join(failures, synchronize(finalCtx, c, branch, filepath.Join(c.StateDir, "sync-after.log")))
		finish()
	}
	_, printErr := fmt.Fprintln(output, "Output:", c.StateDir)
	return errors.Join(failures, ctx.Err(), printErr)
}
func errorText(err error) string {
	if err != nil {
		return err.Error()
	}
	return ""
}
func command(ctx context.Context, repo string, args []string, out io.Writer) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	cmd.Dir = repo
	cmd.Stdout = out
	cmd.Stderr = out
	if err := runProcess(cmd); err != nil {
		return fmt.Errorf("command %q: %w", args, err)
	}
	return nil
}
func sweep(ctx context.Context, c Config, branch string, retry bool, out io.Writer) error {
	s, err := loadState(filepath.Join(c.StateDir, "state.json"))
	if err != nil {
		return err
	}
	ok, err := eligible(ctx, c.Repo, branch)
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}
	current, err := inspectChanges(ctx, c.Repo)
	if err != nil {
		return err
	}
	s.Quiet = duration(c.Agent.Quiet)
	s.Cooldown = duration(c.Agent.Cooldown)
	ready := s.observe(current, time.Now(), retry)
	if err = writeJSON(filepath.Join(c.StateDir, "state.json"), s); err != nil {
		return err
	}
	if _, err = fmt.Fprintln(out, s.readiness(time.Now(), retry)); err != nil {
		return err
	}
	if !ready {
		return nil
	}
	if deadline, ok := ctx.Deadline(); ok && time.Until(deadline) < duration(c.Agent.Timeout)+finalSyncBudget {
		_, err := fmt.Fprintln(out, "deferred: insufficient time for agent and final sync")
		return err
	}
	guard, err := slot()
	if errors.Is(err, ErrLocked) {
		_, err = fmt.Fprintln(out, "waiting: three agent jobs already running")
		return err
	}
	if err != nil {
		return err
	}
	defer guard.Close()
	scanner := "gitleaks"
	if len(c.Scanner) > 0 {
		scanner = c.Scanner[0]
	}
	if _, err = exec.LookPath(scanner); err != nil {
		return err
	}
	if _, err = exec.LookPath("codex"); err != nil {
		return err
	}
	if err := command(ctx, c.Repo, []string{"codex", "--version"}, io.Discard); err != nil {
		return fmt.Errorf("Codex startup check: %w", err)
	}
	a := attempt{Root: c.Repo, Config: c, Output: out, Candidate: candidate{member: member{Path: ".", Branch: branch}, State: s, Directory: c.StateDir}, Timeout: duration(c.Agent.Timeout)}
	return a.execute(ctx)
}
func Fingerprint(ctx context.Context, path string) (string, error) {
	value, err := inspectChanges(ctx, path)
	return value.Fingerprint, err
}
func Status(ctx context.Context, c Config, out io.Writer) error {
	s, err := loadState(filepath.Join(c.StateDir, "state.json"))
	if err != nil {
		return err
	}
	current, err := inspectChanges(ctx, c.Repo)
	if err != nil {
		return err
	}
	s.Quiet = duration(c.Agent.Quiet)
	s.Cooldown = duration(c.Agent.Cooldown)
	s.observe(current, time.Now(), false)
	active, err := activity(ctx, c.Repo)
	if err != nil {
		return err
	}
	events := "none"
	if s.LastRun != "" {
		events = filepath.Join(c.StateDir, "runs", s.LastRun, "events.jsonl")
	}
	_, err = fmt.Fprintf(out, "Repository: %s\nActivity: %s\nState: %s\nLatest attempt: %s\nOutput: %s\nEvents: %s\n", c.Repo, active, statusReadiness(c, s), formatTime(s.LastAttempt), c.StateDir, events)

	return err
}
func formatTime(t time.Time) string {
	if t.IsZero() {
		return "never"
	}
	return t.Local().Format(displayTime)
}
func Logs(c Config, out io.Writer) error {
	f, err := os.Open(filepath.Join(c.StateDir, "latest.log"))
	if err != nil {
		return err
	}
	_, err = io.Copy(out, f)
	return errors.Join(err, f.Close())
}

func activity(ctx context.Context, repo string) (string, error) {
	common, err := gitValue(ctx, repo, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return "", err
	}
	f, err := os.Open(filepath.Join(common, "repo-person.lock"))
	if errors.Is(err, os.ErrNotExist) {
		return "idle", nil
	}
	if err != nil {
		return "", err
	}
	defer f.Close()
	err = syscall.Flock(int(f.Fd()), syscall.LOCK_SH|syscall.LOCK_NB)
	if errors.Is(err, syscall.EWOULDBLOCK) {
		return "running", nil
	}
	if err != nil {
		return "", err
	}
	return "idle", nil
}
