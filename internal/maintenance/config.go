package maintenance

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/pelletier/go-toml/v2"
)

const ConfigName = "repo-person.toml"

// Commands are argv arrays, executed without a shell in the repository root.
type Config struct {
	Sync      bool        `toml:"sync"`
	Sweep     bool        `toml:"sweep"`
	StateDir  string      `toml:"state_dir"`
	AfterSync []string    `toml:"after_sync"`
	Checks    [][]string  `toml:"checks"`
	Scanner   []string    `toml:"scanner"`
	Agent     AgentConfig `toml:"agent"`
	Repo      string      `toml:"-"`
}
type AgentConfig struct {
	Model    string `toml:"model"`
	Effort   string `toml:"effort"`
	Quiet    string `toml:"quiet"`
	Cooldown string `toml:"cooldown"`
	Timeout  string `toml:"timeout"`
	Prompt   string `toml:"prompt"`
}

const Template = `# Synchronize clean checkouts and finish quiet changes with Codex.
sync = true
sweep = true
# Optional argv commands; run without a shell from the repository root.
# after_sync = ["just", "setup"]
# checks = [["just", "check"], ["just", "test"]]
# Scanner receives REPOSITORY BASE HEAD appended to these arguments.
# scanner = ["bash", "/path/to/shared/gitleaks/check.sh", "outgoing"]

[agent]
model = "gpt-6.1-sol"
effort = "low"
quiet = "15m"
cooldown = "24h"
timeout = "10m"
# prompt = "prompts/maintenance.md"
`

func defaults() Config {
	return Config{Sync: true, Sweep: true, Agent: AgentConfig{Model: "gpt-6.1-sol", Effort: "low", Quiet: "15m", Cooldown: "24h", Timeout: "10m"}}
}
func Root(ctx context.Context, path string) (string, error) {
	root, err := gitValue(ctx, path, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(root)
}
func Load(ctx context.Context, path string) (Config, error) {
	c := defaults()
	root, err := Root(ctx, path)
	if err != nil {
		return c, err
	}
	c.Repo = root
	data, err := os.ReadFile(filepath.Join(root, ConfigName))
	if err != nil {
		return c, err
	}
	if err = toml.NewDecoder(bytes.NewReader(data)).DisallowUnknownFields().Decode(&c); err != nil {
		return c, fmt.Errorf("%s: %w", ConfigName, err)
	}
	for name, value := range map[string]string{"quiet": c.Agent.Quiet, "cooldown": c.Agent.Cooldown, "timeout": c.Agent.Timeout} {
		d, err := time.ParseDuration(value)
		if err != nil || d <= 0 {
			return c, fmt.Errorf("agent.%s must be a positive duration", name)
		}
	}
	if c.Agent.Model == "" {
		return c, errors.New("agent.model must not be empty")
	}
	switch c.Agent.Effort {
	case "minimal", "low", "medium", "high", "xhigh":
	default:
		return c, errors.New("invalid agent.effort")
	}
	for _, cmd := range append(c.Checks, c.AfterSync, c.Scanner) {
		if len(cmd) > 0 && cmd[0] == "" {
			return c, errors.New("command executable must not be empty")
		}
	}
	for _, cmd := range c.Checks {
		if len(cmd) == 0 {
			return c, errors.New("check command must not be empty")
		}
	}
	if c.StateDir == "" {
		base, err := StateHome()
		if err != nil {
			return c, err
		}
		c.StateDir = filepath.Join(base, fmt.Sprintf("%x", sha256.Sum256([]byte(root))))
	} else if !filepath.IsAbs(c.StateDir) {
		c.StateDir = filepath.Join(root, c.StateDir)
	}
	// State must not turn into work for the next sweep.
	rel, err := filepath.Rel(root, c.StateDir)
	if err != nil {
		return c, err
	}
	if filepath.IsLocal(rel) {
		if _, err := git(ctx, root, "check-ignore", "-q", filepath.Join(c.StateDir, "state.json")); err != nil {
			return c, errors.New("state_dir inside repository must be Git-ignored")
		}
	}
	return c, nil
}
func StateHome() (string, error) {
	if dir := os.Getenv("XDG_STATE_HOME"); dir != "" {
		if !filepath.IsAbs(dir) {
			return "", errors.New("XDG_STATE_HOME must be absolute")
		}
		return filepath.Join(dir, "repo-person"), nil
	}
	home, err := os.UserHomeDir()
	return filepath.Join(home, ".local/state/repo-person"), err
}
func Init(ctx context.Context, path string) error {
	root, err := Root(ctx, path)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(root, ConfigName), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
	if err != nil {
		return err
	}
	_, err = f.WriteString(Template)
	return errors.Join(err, f.Close())
}
func (c Config) prompt() ([]byte, error) {
	if c.Agent.Prompt == "" {
		return append([]byte(nil), defaultPrompt...), nil
	}
	path := c.Agent.Prompt
	if !filepath.IsAbs(path) {
		path = filepath.Join(c.Repo, path)
	}
	return os.ReadFile(path)
}
func duration(value string) time.Duration { d, _ := time.ParseDuration(value); return d } // Load validates durations.
