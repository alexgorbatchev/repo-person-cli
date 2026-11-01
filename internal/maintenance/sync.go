package maintenance

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

type upstream struct{ remote, ref, local string }

func tracking(ctx context.Context, repo, branch string) (upstream, error) {
	var u upstream
	var err error
	u.remote, err = gitValue(ctx, repo, "config", "--get", "branch."+branch+".remote")
	if err != nil {
		return u, err
	}
	u.ref, err = gitValue(ctx, repo, "config", "--get", "branch."+branch+".merge")
	if err != nil {
		return u, err
	}
	if u.remote == "." || u.remote == "" || strings.ContainsAny(u.remote, "\n\r") || !strings.HasPrefix(u.ref, "refs/heads/") || strings.ContainsAny(u.ref, "\n\r") {
		return u, errors.New("expected a single remote tracking branch")
	}
	u.local = "refs/remotes/" + u.remote + "/" + strings.TrimPrefix(u.ref, "refs/heads/")
	return u, nil
}
func ancestor(ctx context.Context, repo, a, b string) (bool, error) {
	_, err := git(ctx, repo, "merge-base", "--is-ancestor", a, b)
	if exitCode(err, 1) {
		return false, nil
	}
	return err == nil, err
}
func clean(ctx context.Context, repo, branch, head string) (bool, error) {
	ok, err := eligible(ctx, repo, branch)
	if !ok || err != nil {
		return false, err
	}
	status, err := git(ctx, repo, "status", "--porcelain", "--untracked-files=normal")
	if err != nil || status != "" {
		return false, err
	}
	current, err := gitValue(ctx, repo, "rev-parse", "HEAD")
	return current == head, err
}
func synchronize(ctx context.Context, c Config, branch, log string) error {
	if err := incomplete(c.StateDir); err != nil {
		return err
	}
	head, err := gitValue(ctx, c.Repo, "rev-parse", "HEAD")
	if err != nil {
		return err
	}
	ok, err := clean(ctx, c.Repo, branch, head)
	if !ok || err != nil {
		return err
	}
	blocked, readErr := os.ReadFile(filepath.Join(c.StateDir, "blocked-head"))
	if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
		return readErr
	}
	if len(blocked) > 0 {
		contains, err := ancestor(ctx, c.Repo, strings.TrimSpace(string(blocked)), head)
		if err != nil {
			return err
		}
		if contains {
			return fmt.Errorf("publication blocked by an unvalidated agent commit; review %s and remove blocked-head after resolving it", c.StateDir)
		}
	}
	u, err := tracking(ctx, c.Repo, branch)

	if err != nil {
		return err
	}
	fetchURL, err := gitValue(ctx, c.Repo, "remote", "get-url", "--all", u.remote)
	if err != nil {
		return err
	}
	pushURL, err := gitValue(ctx, c.Repo, "remote", "get-url", "--push", "--all", u.remote)
	if err != nil {
		return err
	}
	if fetchURL != pushURL || strings.Contains(fetchURL, "\n") {
		return errors.New("fetch and push must use the same single destination")
	}
	previous, previousErr := gitValue(ctx, c.Repo, "rev-parse", "--verify", "--quiet", u.local)
	if previousErr != nil && !exitCode(previousErr, 1) {
		return previousErr
	}
	if _, err = git(ctx, c.Repo, "fetch", "--no-recurse-submodules", "--no-write-fetch-head", u.remote, "+"+u.ref+":"+u.local); err != nil {
		return err
	}
	target, err := gitValue(ctx, c.Repo, "rev-parse", u.local)
	if err != nil {
		return err
	}
	if previous != "" {
		forward, err := ancestor(ctx, c.Repo, previous, target)
		if err != nil {
			return err
		}
		if !forward {
			_, restoreErr := git(ctx, c.Repo, "update-ref", u.local, previous, target)
			return errors.Join(errors.New("upstream rewound or diverged; manual reconciliation required"), restoreErr)
		}
	}
	ok, err = clean(ctx, c.Repo, branch, head)

	if err != nil {
		return err
	}
	if !ok {
		return errors.New("checkout changed during fetch")
	}
	behind, err := ancestor(ctx, c.Repo, head, target)
	if err != nil {
		return err
	}
	if behind {
		_, err = git(ctx, c.Repo, "-c", "submodule.recurse=false", "merge", "--no-autostash", "--ff-only", target)
		return err
	}
	if err = scan(ctx, c.Repo, c.Scanner, target, head, log); err != nil {
		return err
	}
	ahead, err := ancestor(ctx, c.Repo, target, head)
	if err != nil {
		return err
	}
	if !ahead {
		ok, err := clean(ctx, c.Repo, branch, head)
		if err != nil {
			return err
		}
		if !ok {
			return errors.New("checkout changed during scan")
		}
		if _, err = git(ctx, c.Repo, "-c", "submodule.recurse=false", "-c", "rebase.updateRefs=false", "rebase", "--no-autostash", target); err != nil {
			cleanup, cancel := context.WithTimeout(context.Background(), gitTimeout)
			defer cancel()
			_, abortErr := git(cleanup, c.Repo, "rebase", "--abort")
			return errors.Join(err, abortErr)
		}
		head, err = gitValue(ctx, c.Repo, "rev-parse", "HEAD")
		if err != nil {
			return err
		}
		if err = scan(ctx, c.Repo, c.Scanner, target, head, log); err != nil {
			return err
		}
	}
	ok, err = clean(ctx, c.Repo, branch, head)
	if err != nil {
		return err
	}
	if !ok {
		return errors.New("checkout changed before push")
	}
	_, err = git(ctx, c.Repo, "-c", "push.followTags=false", "-c", "remote."+u.remote+".mirror=false", "push", "--recurse-submodules=check", u.remote, head+":"+u.ref)
	return err
}
func scan(ctx context.Context, repo string, scanner []string, base, head, log string) error {
	args := append([]string(nil), scanner...)
	if len(args) == 0 {
		args = []string{"gitleaks", "git", "--redact", "--no-banner", "--log-opts=" + base + ".." + head, repo}
	} else {
		args = append(args, repo, base, head)
	}
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	cmd.Dir = repo
	var buf bytes.Buffer
	cmd.Stdout, cmd.Stderr = &buf, &buf
	err := runProcess(cmd)
	output := buf.Bytes()
	if err != nil {
		err = fmt.Errorf("outgoing scanner failed: %w; see %s", err, log)
	}
	if mkdirErr := os.MkdirAll(filepath.Dir(log), 0700); mkdirErr != nil {
		return errors.Join(err, mkdirErr)
	}
	return errors.Join(err, os.WriteFile(log, output, 0600))
}
