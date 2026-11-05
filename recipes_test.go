package repoperson_test

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func recipeFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, name := range []string{"justfile", "scripts/check-coverage.sh"} {
		data, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		writeRecipeFile(t, dir, name, string(data))
	}
	return dir
}

func TestCoverageOwnsItsScratch(t *testing.T) {
	for _, mode := range []string{"success", "test failure", "low coverage", "interrupted"} {
		t.Run(mode, func(t *testing.T) {
			dir := recipeFixture(t)
			writeRecipeFile(t, dir, ".tmp/another-session/keep", "sentinel")
			writeRecipeFile(t, dir, "go.mod", "module recipefixture\n\ngo 1.27.3\n")
			writeRecipeFile(t, dir, "value.go", "package fixture\nfunc value() int { return 1 }\n")
			body := "if value() != 1 { t.Fatal(\"wrong value\") }"
			switch mode {
			case "test failure":
				body += "; t.Fatal(\"intentional failure\")"
			case "low coverage":
				body = ""
			case "interrupted":
				body += "; if err := os.WriteFile(\"ready\", []byte(\"ready\"), 0600); err != nil { t.Fatal(err) }; for { time.Sleep(time.Second) }"
			}
			imports := "import \"testing\"\n"
			if mode == "interrupted" {
				imports = "import (\"testing\"; \"os\"; \"time\")\n"
			}
			writeRecipeFile(t, dir, "value_test.go", "package fixture\n"+imports+"func TestValue(t *testing.T) { "+body+" }\n")
			ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, "bash", "scripts/check-coverage.sh")
			cmd.Dir = dir
			cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
			cmd.WaitDelay = 10 * time.Second
			var out bytes.Buffer
			cmd.Stdout, cmd.Stderr = &out, &out
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			if mode == "interrupted" {
				for {
					if _, err := os.Stat(filepath.Join(dir, "ready")); err == nil {
						break
					}
					select {
					case <-ctx.Done():
						if err := cmd.Wait(); err != nil {
							t.Fatalf("test did not start: %v\n%s", err, out.String())
						}
						t.Fatal("test did not create readiness marker")
					case <-time.After(20 * time.Millisecond):
					}
				}
				cancel()
			}
			err := cmd.Wait()
			if (err != nil) != (mode != "success") {
				t.Fatalf("coverage error = %v in %s\n%s", err, mode, out.String())
			}
			if mode == "low coverage" && !strings.Contains(out.String(), "Statement coverage: 0.00%") {
				t.Fatalf("coverage gate did not evaluate the profile:\n%s", out.String())
			}
			entries, err := os.ReadDir(filepath.Join(dir, ".tmp"))
			if err != nil || len(entries) != 1 || entries[0].Name() != "another-session" {
				t.Fatalf("coverage left scratch or removed another session: %v, %v\n%s", entries, err, out.String())
			}
		})
	}
}

func writeRecipeFile(t *testing.T, dir, name, data string) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(data), 0o700); err != nil {
		t.Fatal(err)
	}
}

func TestCleanPreservesOtherSessionsScratch(t *testing.T) {
	dir := recipeFixture(t)
	outputs := []string{"bin/app", "dist/package", "coverage.out"}
	for _, name := range append(outputs, ".tmp/other-session/keep") {
		writeRecipeFile(t, dir, name, "sentinel")
	}
	cmd := exec.Command("just", "clean")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("clean: %v\n%s", err, out)
	}
	for _, name := range outputs {
		if _, err := os.Stat(filepath.Join(dir, name)); !os.IsNotExist(err) {
			t.Errorf("clean left %s: %v", name, err)
		}
	}
	if data, err := os.ReadFile(filepath.Join(dir, ".tmp/other-session/keep")); err != nil || string(data) != "sentinel" {
		t.Fatalf("clean damaged another session's scratch: %q, %v", data, err)
	}
}

func TestTestRecipeDoesNotLeaveCoverageProfile(t *testing.T) {
	for _, fail := range []bool{false, true} {
		name := "success"
		body := "if value() != 1 { t.Fatal(\"wrong value\") }"
		if fail {
			name = "failure"
			body += "; t.Fatal(\"intentional failure\")"
		}
		t.Run(name, func(t *testing.T) {
			dir := recipeFixture(t)
			writeRecipeFile(t, dir, "go.mod", "module recipefixture\n\ngo 1.26.2\n")
			writeRecipeFile(t, dir, "value.go", "package fixture\nfunc value() int { return 1 }\n")
			writeRecipeFile(t, dir, "value_test.go", "package fixture\nimport \"testing\"\nfunc TestValue(t *testing.T) { "+body+" }\n")
			cmd := exec.Command("just", "test")
			cmd.Dir = dir
			out, err := cmd.CombinedOutput()
			if (err != nil) != fail {
				t.Fatalf("test error = %v, want failure %t\n%s", err, fail, out)
			}
			if _, err := os.Stat(filepath.Join(dir, "coverage.out")); !os.IsNotExist(err) {
				t.Fatalf("test left coverage.out: %v\n%s", err, out)
			}
		})
	}
}
