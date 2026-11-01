package display

import (
	"io"
	"os"
	"strings"
)

func Path(value string) string {
	home, err := os.UserHomeDir()
	if err != nil {
		return value
	}
	return fold(value, home)
}
func fold(value, home string) string {
	if value == home {
		return "~"
	}
	return strings.ReplaceAll(value, home+string(os.PathSeparator), "~"+string(os.PathSeparator))
}

type writer struct{ out io.Writer }

func New(out io.Writer) io.Writer { return writer{out} }
func (w writer) Write(p []byte) (int, error) {
	_, err := io.WriteString(w.out, Path(string(p)))
	if err != nil {
		return 0, err
	}
	return len(p), nil
}
