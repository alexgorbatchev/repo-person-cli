package maintenance

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"
)

const gitTimeout = 30 * time.Second

func git(ctx context.Context, repo string, args ...string) (string, error) {
	timeout := gitTimeout
	if len(args) > 0 && (args[0] == "fetch" || args[0] == "merge" || args[0] == "rebase" || args[0] == "-c" || args[0] == "push" || args[0] == "commit") {
		timeout = 5 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", repo}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := runProcess(cmd)
	data := stdout.Bytes()
	if err != nil {
		return "", fmt.Errorf("git %v in %s: %w: %s", args, repo, errors.Join(err, ctx.Err()), strings.TrimSpace(stderr.String()))
	}
	return string(data), nil
}

func gitValue(ctx context.Context, repo string, args ...string) (string, error) {
	data, err := git(ctx, repo, args...)
	return strings.TrimSpace(data), err
}

func exitCode(err error, code int) bool {
	var exit *exec.ExitError
	return errors.As(err, &exit) && exit.ExitCode() == code
}

type member struct{ Path, Branch string }

func eligible(ctx context.Context, repo, branch string) (bool, error) {
	current, err := gitValue(ctx, repo, "branch", "--show-current")
	if err != nil {
		return false, err
	}
	if current == "" || current != branch {
		return false, nil
	}
	if _, err := tracking(ctx, repo, branch); err != nil {
		return false, err
	}
	for _, marker := range []string{"MERGE_HEAD", "CHERRY_PICK_HEAD", "REVERT_HEAD", "rebase-merge", "rebase-apply", "sequencer", "index.lock"} {
		path, err := gitValue(ctx, repo, "rev-parse", "--git-path", marker)
		if err != nil {
			return false, err
		}
		if !filepath.IsAbs(path) {
			path = filepath.Join(repo, path)
		}
		if _, err := os.Stat(path); err == nil {
			return false, nil
		} else if !errors.Is(err, os.ErrNotExist) {
			return false, err
		}
	}
	_, err = git(ctx, repo, "diff", "--cached", "--quiet")
	if exitCode(err, 1) {
		return false, nil
	}
	return err == nil, err
}

type changes struct {
	Fingerprint string
	Modified    time.Time
}

// inspectChanges uses modification and inode-change times, not the first scan time.
func inspectChanges(ctx context.Context, repo string) (changes, error) {
	var result changes
	status, err := git(ctx, repo, "status", "--porcelain=v1", "-z", "--untracked-files=all")
	if err != nil || status == "" {
		return result, err
	}
	digest := sha256.New()
	fmt.Fprint(digest, status)
	for _, args := range [][]string{
		{"rev-parse", "HEAD"}, {"diff", "--binary", "--no-ext-diff", "--no-textconv", "HEAD"},
	} {
		data, err := git(ctx, repo, args...)
		if err != nil {
			return result, err
		}
		fmt.Fprint(digest, data)
	}
	paths := make(map[string]bool)
	for _, args := range [][]string{
		{"diff", "--name-only", "-z", "--no-renames", "HEAD"}, {"ls-files", "--others", "--exclude-standard", "-z"},
	} {
		data, err := git(ctx, repo, args...)
		if err != nil {
			return result, err
		}
		for _, path := range strings.Split(data, "\x00") {
			if path != "" {
				paths[path] = true
			}
		}
	}
	names := make([]string, 0, len(paths))
	for path := range paths {
		names = append(names, path)
	}
	sort.Strings(names)
	for _, name := range names {
		if !filepath.IsLocal(name) {
			return result, fmt.Errorf("invalid changed path %q", name)
		}
		path := filepath.Join(repo, name)
		fmt.Fprintf(digest, "%s\x00", name)
		info, err := os.Lstat(path)
		if errors.Is(err, os.ErrNotExist) {
			// A removed file has no timestamps. Its nearest surviving directory
			// records entry removal, including removal of an entire subtree.
			info, err = deletedParent(repo, path)
			if err != nil {
				return result, err
			}
			modified := latestChange(info)
			if modified.After(result.Modified) {
				result.Modified = modified
			}
			fmt.Fprintf(digest, "deleted:%d", modified.UnixNano())
			continue
		}
		if err != nil {
			return result, err
		}
		modified := latestChange(info)
		if modified.After(result.Modified) {
			result.Modified = modified
		}
		fmt.Fprintf(digest, "%d:%d:%d:%d:%d", info.Mode(), info.Size(), info.ModTime().UnixNano(), info.Sys().(*syscall.Stat_t).Ino, modified.UnixNano())
		switch {
		case info.Mode()&os.ModeSymlink != 0:
			target, err := os.Readlink(path)
			if err != nil {
				return result, err
			}
			fmt.Fprint(digest, target)
		case info.Mode().IsRegular():
			file, err := os.Open(path)
			if err != nil {
				return result, err
			}
			_, copyErr := io.Copy(digest, file)
			if err := errors.Join(copyErr, file.Close()); err != nil {
				return result, err
			}
		default:
			return result, fmt.Errorf("unsupported changed path (nested repository or special file): %s", name)
		}
	}
	result.Fingerprint = fmt.Sprintf("%x", digest.Sum(nil))
	return result, nil
}

func deletedParent(repo, path string) (os.FileInfo, error) {
	for path != repo {
		path = filepath.Dir(path)
		info, err := os.Lstat(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if !info.IsDir() {
			return nil, fmt.Errorf("deleted path parent is not a directory: %s", path)
		}
		return info, nil
	}
	return nil, fmt.Errorf("cannot determine deletion time in %s", repo)
}

func latestChange(info os.FileInfo) time.Time {
	changed := inodeChange(info.Sys().(*syscall.Stat_t))
	if info.ModTime().After(changed) {
		return info.ModTime()
	}
	return changed
}
