package maintenance

import (
	"context"
	"crypto/rand"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"
)

type candidate struct {
	member
	State     state
	Directory string
}

type agentResult struct {
	Status    string   `json:"status"`
	Summary   string   `json:"summary"`
	Checks    []string `json:"checks"`
	Repairs   string   `json:"repairs"`
	Remaining string   `json:"remaining"`
}

type attempt struct {
	Root                    string
	Config                  Config
	Output                  io.Writer
	Candidate               candidate
	Timeout                 time.Duration
	RunID, RunDir, Baseline string
	ProcessExit             int
	StartFailed             bool
	Terminated              bool
	Interrupted             bool
	Result                  agentResult
}

var commitSubject = regexp.MustCompile(`^[a-z]+\(sweep\): .+`)
var errChanged = errors.New("repository changed before launch; no agent started")

func (a *attempt) execute(ctx context.Context) (err error) {
	c := &a.Candidate
	repo := filepath.Join(a.Root, c.Path)
	id := make([]byte, 4)
	if _, err := rand.Read(id); err != nil {
		return err
	}
	a.RunID = time.Now().UTC().Format("20060102T150405Z") + "-" + hex.EncodeToString(id)
	a.RunDir = filepath.Join(c.Directory, "runs", a.RunID)
	a.ProcessExit = -1
	a.StartFailed = true
	if err := os.MkdirAll(a.RunDir, 0700); err != nil {
		return err
	}
	lock, err := os.OpenFile(filepath.Join(a.RunDir, "active.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, lock.Close()) }()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return err
	}
	previousLog, readErr := os.ReadFile(filepath.Join(c.Directory, "LOG.md"))
	defer func() {
		a.Interrupted = a.Interrupted || ctx.Err() != nil
		err = errors.Join(err, a.record(err, previousLog))
	}()
	if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
		return readErr
	}
	a.Baseline, err = gitValue(ctx, repo, "rev-parse", "HEAD")
	if err != nil {
		return err
	}
	c.State.LastAttempt = time.Now()
	c.State.LastRun = a.RunID
	// Persist before launch: interruption must not cause an immediate repeated attempt.
	c.State.FailedFingerprint, c.State.FailedAt = c.State.Fingerprint, time.Now()
	if err := writeJSON(filepath.Join(c.Directory, "state.json"), c.State); err != nil {
		return err
	}

	ok, err := eligible(ctx, repo, c.Branch)
	if err != nil {
		return err
	}
	current, err := inspectChanges(ctx, repo)
	if err != nil {
		return err
	}
	if !ok || current.Fingerprint != c.State.Fingerprint {
		return errChanged
	}
	prompt, err := a.prompt()
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(a.RunDir, "prompt.md"), prompt, 0600); err != nil {
		return err
	}
	fmt.Fprintf(a.Output, "Codex sweep %s\n", a.RunID)
	if err := writeJSON(filepath.Join(c.Directory, "incomplete.json"), map[string]string{"run_id": a.RunID, "baseline": a.Baseline}); err != nil {
		return err
	}
	if err := a.runCodex(ctx, prompt); err != nil {
		return err
	}
	data, err := os.ReadFile(filepath.Join(a.RunDir, "result.json"))
	if err != nil {
		return fmt.Errorf("missing agent result: %w", err)
	}
	if err := json.Unmarshal(data, &a.Result); err != nil {
		return fmt.Errorf("invalid agent result: %w", err)
	}
	if a.Result.Status != "committed" {
		return fmt.Errorf("Codex did not complete: %s", a.Result.Summary)
	}
	if err := a.validate(ctx); err != nil {
		return errors.Join(err, a.blockPublication(ctx))
	}
	return nil

}

func (a *attempt) prompt() ([]byte, error) {
	prompt, err := a.Config.prompt()
	if err != nil {
		return nil, err
	}
	binary, err := os.Executable()
	if err != nil {
		return nil, err
	}
	repo := filepath.Join(a.Root, a.Candidate.Path)
	data, err := json.MarshalIndent(map[string]any{
		"repository": repo, "branch": a.Candidate.Branch, "starting_head": a.Baseline,
		"run_id": a.RunID, "fingerprint": a.Candidate.State.Fingerprint,
		"log_directory": a.Candidate.Directory, "budget_seconds": a.Timeout.Seconds(), "checks": a.Config.Checks,
		"observation_command_argv": []string{binary, "fingerprint", "--repo", repo},
	}, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(prompt, []byte("\n\nRunner context:\n"+string(data)+"\n")...), nil
}

func (a *attempt) runCodex(ctx context.Context, prompt []byte) error {
	j := codexJob{Repo: filepath.Join(a.Root, a.Candidate.Path), Directory: a.RunDir,
		Schema: filepath.Join(a.RunDir, "schema.json"), Timeout: a.Timeout, Model: a.Config.Agent.Model, Effort: a.Config.Agent.Effort}
	if err := os.WriteFile(j.Schema, schema, 0600); err != nil {
		return err
	}
	err := j.execute(ctx, prompt)
	a.ProcessExit = j.Exit
	a.Interrupted = ctx.Err() != nil
	a.StartFailed = j.StartFailed
	a.Terminated = j.Terminated
	return err
}

type codexJob struct {
	Repo, Directory, Schema, Model, Effort string
	Timeout                                time.Duration
	Exit                                   int
	StartFailed                            bool
	Terminated                             bool
}

func (j *codexJob) execute(ctx context.Context, prompt []byte) error {
	j.Exit = -1
	ctx, cancel := context.WithTimeout(ctx, j.Timeout)
	defer cancel()
	repo := j.Repo
	args := []string{"exec", "-C", repo, "-m", j.Model,
		"-c", fmt.Sprintf("model_reasoning_effort=%q", j.Effort), "-c", `approval_policy="never"`,
		"--sandbox", "danger-full-access", "--ephemeral", "--json",
		"--output-schema", j.Schema,
		"-o", filepath.Join(j.Directory, "result.json"), "-"}
	name := "codex"
	output, err := os.Create(filepath.Join(j.Directory, "events.jsonl"))
	if err != nil {
		return err
	}
	defer output.Close() // write/close errors are checked explicitly after the process exits.
	stderr, err := os.Create(filepath.Join(j.Directory, "stderr.log"))
	if err != nil {
		return err
	}
	defer stderr.Close()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdin = strings.NewReader(string(prompt))
	cmd.Stdout, cmd.Stderr = output, stderr
	err = runProcess(cmd)
	j.StartFailed = cmd.Process == nil
	j.Terminated = cmd.Process != nil && ctx.Err() != nil
	if cmd.ProcessState != nil {
		j.Exit = cmd.ProcessState.ExitCode()
		j.Terminated = j.Terminated || j.Exit == -1
	}
	err = errors.Join(err, output.Close(), stderr.Close())
	if ctx.Err() != nil {
		return fmt.Errorf("time budget exhausted or canceled; process group terminated: %w", ctx.Err())
	}
	if err != nil {
		return fmt.Errorf("Codex execution: %w", err)
	}
	return nil
}

func (a *attempt) validate(ctx context.Context) error {
	repo := filepath.Join(a.Root, a.Candidate.Path)
	parent, err := gitValue(ctx, repo, "rev-parse", "HEAD^")
	if err != nil {
		return err
	}
	message, err := gitValue(ctx, repo, "log", "-1", "--format=%B")
	if err != nil {
		return err
	}
	footer := false
	for _, line := range strings.Split(message, "\n") {
		if line == "Sweep-Run: "+a.RunID {
			footer = true
		}
	}
	ok, err := eligible(ctx, repo, a.Candidate.Branch)
	if err != nil {
		return err
	}
	pending, err := inspectChanges(ctx, repo)
	if err != nil {
		return err
	}
	if parent != a.Baseline || !commitSubject.MatchString(message) || !footer || !ok || pending.Fingerprint != "" {
		return errors.New("commit validation failed; expected one traced commit and a clean tracking checkout")
	}
	return scan(ctx, repo, a.Config.Scanner, a.Baseline, "HEAD", filepath.Join(a.RunDir, "gitleaks.log"))

}

func (a *attempt) record(attemptErr error, previousLog []byte) error {
	c := &a.Candidate
	var recordErr error
	if attemptErr != nil && a.Baseline != "" {
		ctx, cancel := context.WithTimeout(context.Background(), gitTimeout)
		defer cancel()
		recordErr = a.blockPublication(ctx)
		attemptErr = errors.Join(attemptErr, recordErr)
	}
	now := time.Now()
	message := "Committed successfully"
	if attemptErr != nil {
		message = attemptErr.Error()
	}
	if attemptErr == nil {
		fmt.Fprintln(a.Output, message)
	}
	logPath := filepath.Join(c.Directory, "LOG.md")
	details := map[string]any{"result": a.Result}
	if attemptErr != nil {
		data, err := os.ReadFile(logPath)
		if err == nil && string(data) != string(previousLog) {
			details["agent_log"] = string(data)
			recordErr = errors.Join(recordErr, os.WriteFile(filepath.Join(a.RunDir, "agent-LOG.md"), data, 0600))
		} else if err != nil && !errors.Is(err, os.ErrNotExist) {
			recordErr = errors.Join(recordErr, err)
		}
		recordErr = errors.Join(recordErr, failureLog(a.RunDir, a.RunID, a.Baseline, message, details), failureLog(c.Directory, a.RunID, a.Baseline, message, details))
	} else if err := os.Remove(logPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		recordErr = err
	}
	// A timeout may have left repairs. Cool down that resulting diff, not the old one.
	ctx, cancel := context.WithTimeout(context.Background(), gitTimeout)
	defer cancel()
	after, err := inspectChanges(ctx, filepath.Join(a.Root, c.Path))
	if err != nil {
		after = changes{Fingerprint: c.State.Fingerprint, Modified: c.State.Since}
		recordErr = errors.Join(recordErr, err)
	}
	c.State.Fingerprint, c.State.Since = after.Fingerprint, after.Modified
	if attemptErr != nil && !errors.Is(attemptErr, errChanged) && !exitCode(attemptErr, 75) && !exitCode(attemptErr, 126) && !exitCode(attemptErr, 127) && !a.Interrupted && !a.StartFailed {
		c.State.FailedFingerprint, c.State.FailedAt = after.Fingerprint, now
	} else {
		c.State.FailedFingerprint, c.State.FailedAt = "", time.Time{}
	}
	recordErr = errors.Join(recordErr, writeJSON(filepath.Join(c.Directory, "state.json"), c.State))
	recordErr = errors.Join(recordErr, writeJSON(filepath.Join(a.RunDir, "outcome.json"), map[string]any{
		"success": attemptErr == nil, "message": message, "exit_code": a.ProcessExit,
		"baseline": a.Baseline, "fingerprint": after.Fingerprint,
	}))
	if recordErr == nil && !a.Terminated {
		recordErr = removeIfExists(filepath.Join(c.Directory, "incomplete.json"))
	}
	return recordErr
}

//go:embed prompts/commit-sweep.md
var defaultPrompt []byte

//go:embed prompts/commit-sweep.schema.json
var schema []byte

func (a *attempt) blockPublication(ctx context.Context) error {
	head, err := gitValue(ctx, filepath.Join(a.Root, a.Candidate.Path), "rev-parse", "HEAD")
	if err != nil || head == a.Baseline {
		return err
	}
	return os.WriteFile(filepath.Join(a.Candidate.Directory, "blocked-head"), []byte(head+"\n"), 0600)
}
