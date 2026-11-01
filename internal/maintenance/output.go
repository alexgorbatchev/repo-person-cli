package maintenance

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

type prefixWriter struct {
	out     io.Writer
	prefix  string
	pending []byte
}

func (w *prefixWriter) Write(p []byte) (int, error) {
	w.pending = append(w.pending, p...)
	for {
		end := bytes.IndexByte(w.pending, '\n')
		if end < 0 {
			break
		}
		if _, err := fmt.Fprintf(w.out, "%s%s", w.prefix, w.pending[:end+1]); err != nil {
			return 0, err
		}
		w.pending = w.pending[end+1:]
	}
	return len(p), nil
}
func (w *prefixWriter) Flush() error {
	if len(w.pending) == 0 {
		return nil
	}
	_, err := fmt.Fprintf(w.out, "%s%s\n", w.prefix, w.pending)
	w.pending = nil
	return err
}
func removeIfExists(path string) error {
	err := os.Remove(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}
func incomplete(directory string) error {
	_, err := os.Stat(filepath.Join(directory, "incomplete.json"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	return fmt.Errorf("incomplete agent attempt: stop any surviving agent, review its changes and %s, then remove incomplete.json before retrying", directory)
}
func statusReadiness(c Config, s state) string {
	if err := incomplete(c.StateDir); err != nil {
		return "blocked: " + err.Error()
	}
	return s.readiness(time.Now(), false)
}

// Refresh the agent report from the current checkout, not stale cached state.
func updateCycleLog(c Config, failure error) error {
	s, err := loadState(filepath.Join(c.StateDir, "state.json"))
	inspectionErr := err
	var agentLog []byte
	if inspectionErr == nil && s.LastRun != "" && s.FailedFingerprint != "" {
		ctx, cancel := context.WithTimeout(context.Background(), gitTimeout)
		defer cancel()
		current, err := inspectChanges(ctx, c.Repo)
		inspectionErr = err
		if err == nil && current.Fingerprint == s.FailedFingerprint {
			agentLog, err = os.ReadFile(filepath.Join(c.StateDir, "runs", s.LastRun, "LOG.md"))
			if err != nil && !errors.Is(err, os.ErrNotExist) {
				inspectionErr = err
			}
		}
	}
	failure = errors.Join(failure, inspectionErr)
	if failure != nil {
		details := map[string]string{}
		if len(agentLog) > 0 {
			details["agent_failure"] = string(agentLog)
		}
		return errors.Join(inspectionErr, failureLog(c.StateDir, "cycle", "", failure.Error(), details))
	}
	if len(agentLog) > 0 {
		return os.WriteFile(filepath.Join(c.StateDir, "LOG.md"), agentLog, 0600)
	}
	return removeIfExists(filepath.Join(c.StateDir, "LOG.md"))
}
