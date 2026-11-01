package maintenance

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

const (
	quietPeriod     = 15 * time.Minute
	failureCooldown = 24 * time.Hour
	logRetention    = 14 * 24 * time.Hour
	displayTime     = "2006-01-02 15:04"
)

type state struct {
	LastRun           string        `json:"last_run,omitempty"`
	Quiet             time.Duration `json:"-"`
	Cooldown          time.Duration `json:"-"`
	Fingerprint       string        `json:"fingerprint"`
	Since             time.Time     `json:"since"`
	LastAttempt       time.Time     `json:"last_attempt"`
	FailedFingerprint string        `json:"failed_fingerprint,omitempty"`
	FailedAt          time.Time     `json:"failed_at,omitempty"`
}

func (s *state) observe(current changes, now time.Time, retry bool) bool {
	s.Fingerprint, s.Since = current.Fingerprint, current.Modified
	return s.readiness(now, retry) == "ready"
}

func (s state) readiness(now time.Time, retry bool) string {
	if s.Quiet == 0 {
		s.Quiet = quietPeriod
	}
	if s.Cooldown == 0 {
		s.Cooldown = failureCooldown
	}
	if s.Fingerprint == "" {
		return "clean"
	}
	if s.Since.IsZero() {
		return "waiting: filesystem change time unavailable"
	}
	if remaining := s.Quiet - now.Sub(s.Since); remaining > 0 {
		return fmt.Sprintf("waiting: quiet window remaining %s (last change %s)", remaining.Round(time.Second), s.Since.Local().Format(displayTime))
	}
	if !retry && s.FailedFingerprint == s.Fingerprint && now.Sub(s.FailedAt) < s.Cooldown {
		return "waiting: failure cooldown until " + s.FailedAt.Add(s.Cooldown).Local().Format(displayTime)
	}
	return "ready"
}

func loadState(path string) (state, error) {
	var s state
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return s, err
	}
	if err := json.Unmarshal(data, &s); err != nil {
		return s, fmt.Errorf("read state %s: %w", path, err)
	}
	return s, nil
}

func writeJSON(path string, data any) error {
	encoded, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	temporary := path + ".new"
	if err := os.WriteFile(temporary, append(encoded, '\n'), 0600); err != nil {
		return err
	}
	return os.Rename(temporary, path)
}

func prune(directory string, now time.Time) error {
	s, err := loadState(filepath.Join(directory, "state.json"))
	if err != nil {
		return err
	}
	entries, err := os.ReadDir(filepath.Join(directory, "runs"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		if entry.Name() == s.LastRun && s.FailedFingerprint != "" {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if now.Sub(info.ModTime()) > logRetention {
			if err := os.RemoveAll(filepath.Join(directory, "runs", entry.Name())); err != nil {
				return err
			}
		}
	}
	return nil
}

func failureLog(directory, runID, baseline, message string, result any) error {
	details, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(directory, 0700); err != nil {
		return err
	}
	text := fmt.Sprintf("# Unresolved commit sweep\n\nRun: `%s`\n\nBaseline: `%s`\n\n%s\n\n```json\n%s\n```\n\nExecution output: `runs/%s/`\n", runID, baseline, message, details, runID)
	return os.WriteFile(filepath.Join(directory, "LOG.md"), []byte(text), 0600)
}
