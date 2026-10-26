// Package prompt asks questions on a terminal, for Gosha's interactive
// commands (init, setup).
package prompt

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/term"
)

// ErrNoInput means input ran out before an answer was given.
var ErrNoInput = errors.New("no answer on stdin")

// Prompter reads answers from in and writes questions to out.
type Prompter struct {
	r   *bufio.Reader
	out io.Writer
	// fd is in's file descriptor when it's a terminal, for reading
	// secrets without echo; -1 otherwise.
	fd int
}

// New returns a prompter. Secrets are read without echo when in is a
// terminal.
func New(in io.Reader, out io.Writer) *Prompter {
	p := &Prompter{r: bufio.NewReader(in), out: out, fd: -1}
	if f, ok := in.(*os.File); ok && term.IsTerminal(int(f.Fd())) {
		p.fd = int(f.Fd())
	}
	return p
}

// IsTerminal reports whether in is a terminal.
func IsTerminal(in io.Reader) bool {
	f, ok := in.(*os.File)
	return ok && term.IsTerminal(int(f.Fd()))
}

// ExitOnInterrupt exits with status 130 when ctx ends (Ctrl-C) before the
// returned stop is called: a blocked read would otherwise keep waiting
// after the signal. A terminal in is restored first, since a secret
// prompt turns its echo off.
func ExitOnInterrupt(ctx context.Context, in io.Reader, out io.Writer) (stop func()) {
	var restore func()
	if f, ok := in.(*os.File); ok && term.IsTerminal(int(f.Fd())) {
		if st, err := term.GetState(int(f.Fd())); err == nil {
			restore = func() { term.Restore(int(f.Fd()), st) }
		}
	}
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			if restore != nil {
				restore()
			}
			fmt.Fprintln(out)
			os.Exit(130)
		case <-done:
		}
	}()
	return func() { close(done) }
}

// Out is where questions go.
func (p *Prompter) Out() io.Writer { return p.out }

// Ask prompts until it gets an answer that check accepts. An empty answer
// takes def, and is refused when def is empty. check may be nil.
func (p *Prompter) Ask(label, def string, check func(string) error) (string, error) {
	if def != "" {
		label += " [" + def + "]"
	}
	for {
		fmt.Fprintf(p.out, "%s: ", label)
		s, err := p.line()
		if err != nil {
			return "", err
		}
		if s == "" {
			s = def
		}
		if s == "" {
			fmt.Fprintln(p.out, "  required")
			continue
		}
		if check != nil {
			if err := check(s); err != nil {
				fmt.Fprintf(p.out, "  %v\n", err)
				continue
			}
		}
		return s, nil
	}
}

// Optional prompts for an answer that may be empty.
func (p *Prompter) Optional(label string) (string, error) {
	fmt.Fprintf(p.out, "%s: ", label)
	return p.line()
}

// Secret prompts for a secret without echo. When current is set, the
// prompt says so and an empty answer keeps it. The value is never printed.
func (p *Prompter) Secret(label, current string) (string, error) {
	if current != "" {
		label += " [set, Enter keeps it]"
	}
	for {
		fmt.Fprintf(p.out, "%s: ", label)
		var s string
		if p.fd >= 0 {
			b, err := term.ReadPassword(p.fd)
			fmt.Fprintln(p.out)
			if err != nil {
				return "", err
			}
			s = strings.TrimSpace(string(b))
		} else {
			var err error
			if s, err = p.line(); err != nil {
				return "", err
			}
		}
		if s == "" {
			s = current
		}
		if s != "" {
			return s, nil
		}
		fmt.Fprintln(p.out, "  required")
	}
}

// Confirm asks a yes/no question.
func (p *Prompter) Confirm(label string, def bool) (bool, error) {
	d := "y/N"
	if def {
		d = "Y/n"
	}
	for {
		fmt.Fprintf(p.out, "%s [%s]: ", label, d)
		s, err := p.line()
		if err != nil {
			return false, err
		}
		switch strings.ToLower(s) {
		case "":
			return def, nil
		case "y", "yes":
			return true, nil
		case "n", "no":
			return false, nil
		}
		fmt.Fprintln(p.out, "  y or n")
	}
}

func (p *Prompter) line() (string, error) {
	line, err := p.r.ReadString('\n')
	if err != nil && line == "" {
		fmt.Fprintln(p.out)
		return "", ErrNoInput
	}
	return strings.TrimSpace(line), nil
}
