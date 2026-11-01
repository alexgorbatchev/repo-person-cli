package maintenance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"sync"
)

func Registered() ([]string, error) {
	home, err := StateHome()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(filepath.Join(home, "repositories.json"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var paths []string
	err = json.Unmarshal(data, &paths)
	return paths, err
}
func Register(ctx context.Context, path string, enable bool) (err error) {
	root, err := Root(ctx, path)
	if err != nil {
		return err
	}
	if enable {
		if _, err = Load(ctx, root); err != nil {
			return err
		}
	}
	home, err := StateHome()
	if err != nil {
		return err
	}
	guard, err := lock(filepath.Join(home, "registry.lock"))
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, guard.Close()) }()
	paths, err := Registered()
	if err != nil {
		return err
	}
	next := []string{}
	for _, p := range paths {
		if p != root {
			next = append(next, p)
		}
	}
	if enable {
		next = append(next, root)
	}
	sort.Strings(next)
	return writeJSON(filepath.Join(home, "repositories.json"), next)
}

// RunAll bounds repository concurrency; machine-wide slot locks also limit separate invocations.
func RunAll(ctx context.Context, out io.Writer) error {
	ctx, cancel := context.WithTimeout(ctx, cycleBudget)
	defer cancel()
	paths, err := Registered()
	if err != nil {
		return err
	}
	queue := make(chan string, len(paths))
	for _, p := range paths {
		queue <- p
	}
	close(queue)
	failures := make(chan error, len(paths))
	var workers sync.WaitGroup
	writer := &lockedWriter{out: out}
	for range 3 {
		workers.Go(func() {
			for path := range queue {
				if ctx.Err() != nil {
					fmt.Fprintf(writer, "[%s] deferred: batch budget exhausted or canceled\n", path)
					continue
				}
				c, err := Load(ctx, path)
				if err == nil {
					err = Run(ctx, c, false, writer)
				}
				if err != nil {
					failures <- fmt.Errorf("%s: %w", path, err)
				}
			}
		})
	}
	workers.Wait()
	close(failures)
	for failure := range failures {
		err = errors.Join(err, failure)
	}
	return err
}

type lockedWriter struct {
	mu  sync.Mutex
	out io.Writer
}

func (w *lockedWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.out.Write(p)
}
