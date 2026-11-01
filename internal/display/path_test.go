package display

import (
	"bytes"
	"errors"
	"io"
	"testing"
)

func TestPaths(t *testing.T) {
	for _, tt := range []struct{ in, want string }{{"/home/dev", "~"}, {"/home/dev/a", "~/a"}, {"/home/developer/a", "/home/developer/a"}, {"/tmp/a", "/tmp/a"}} {
		if got := fold(tt.in, "/home/dev"); got != tt.want {
			t.Fatal(got)
		}
	}
	t.Setenv("HOME", "/home/dev")
	var b bytes.Buffer
	if _, err := New(&b).Write([]byte("/home/dev/a")); err != nil || b.String() != "~/a" {
		t.Fatal(err, b.String())
	}
	if _, err := New(broken{}).Write([]byte("x")); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatal(err)
	}
}

type broken struct{}

func (broken) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

func TestMissingHome(t *testing.T) {
	t.Setenv("HOME", "")
	if Path("/tmp/file") != "/tmp/file" {
		t.Fatal("changed path without home")
	}
}
