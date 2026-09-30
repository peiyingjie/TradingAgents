package cli

import (
	"context"
	"errors"
	"fmt"
	"golang.org/x/term"
	"io"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

var ErrCancelled = errors.New("Cancelled. Exiting...")
var errKeyTimeout = errors.New("no key available")

func (t terminal) interactive() bool {
	in, ok := t.Input.(*os.File)
	if !ok || !term.IsTerminal(int(in.Fd())) {
		return false
	}
	out, ok := t.Out.(*os.File)
	return ok && term.IsTerminal(int(out.Fd()))
}
func (t terminal) context() context.Context {
	if t.Context != nil {
		return t.Context
	}
	return context.Background()
}

// readKey polls only the console handle, so cancellation never leaves a reader
// goroutine behind to consume the next prompt's input. Escape alone cancels;
// CSI/SS3 sequences represent navigation keys.
func readKey(ctx context.Context, f *os.File) (string, error) {
	var key rune
	var err error
	for {
		key, err = readConsoleRune(ctx, f, 100*time.Millisecond)
		if err == errKeyTimeout {
			continue
		}
		if err != nil {
			return "", err
		}
		break
	}
	if key == 3 || key == 4 {
		return "", ErrCancelled
	}
	if key >= 0x110000 && key <= 0x110006 {
		return []string{"up", "down", "left", "right", "home", "end", "delete"}[key-0x110000], nil
	}
	if key == 27 {
		key, err = readConsoleRune(ctx, f, 60*time.Millisecond)
		if err != nil {
			if err == errKeyTimeout {
				return "", ErrCancelled
			}
			return "", err
		}
		if key != '[' && key != 'O' {
			return "", ErrCancelled
		}
		sequence := ""
		for len(sequence) < 12 {
			key, err = readConsoleRune(ctx, f, 60*time.Millisecond)
			if err != nil {
				return "", err
			}
			sequence += string(key)
			if key >= 64 && key <= 126 {
				break
			}
		}
		switch sequence {
		case "A":
			return "up", nil
		case "B":
			return "down", nil
		case "C":
			return "right", nil
		case "D":
			return "left", nil
		case "H", "1~":
			return "home", nil
		case "F", "4~":
			return "end", nil
		case "3~":
			return "delete", nil
		}
		return "", nil
	}
	switch key {
	case '\r', '\n':
		return "enter", nil
	case 127, 8:
		return "backspace", nil
	}
	return string(key), nil
}
func (t terminal) raw(action func(*os.File) (string, error)) (string, error) {
	f := t.Input.(*os.File)
	saved, err := term.MakeRaw(int(f.Fd()))
	if err != nil {
		return "", err
	}
	defer term.Restore(int(f.Fd()), saved)
	defer fmt.Fprint(t.Out, "\x1b[0m\x1b[?25h\r\n")
	return action(f)
}
func (t terminal) readText(label, def string, secret bool) (string, error) {
	return t.raw(func(f *os.File) (string, error) {
		value := []rune(def)
		position := len(value)
		for {
			shown := string(value)
			if secret {
				shown = strings.Repeat("*", len(value))
			}
			columns, _, err := term.GetSize(int(t.Out.(*os.File).Fd()))
			if err != nil {
				columns = 80
			}
			caption := safeLine(label, max(10, columns/2))
			budget := max(1, columns-cellWidth(caption)-3)
			displayRunes := []rune(shown)
			start := 0
			for start < position && cellWidth(string(displayRunes[start:position])) >= budget {
				start++
			}
			visible := prefixCells(string(displayRunes[start:]), budget)
			fmt.Fprintf(t.Out, "\r\x1b[2K%s: %s", caption, visible)
			distance := cellWidth(visible) - cellWidth(string(displayRunes[start:position]))
			if distance > 0 {
				fmt.Fprintf(t.Out, "\x1b[%dD", distance)
			}
			key, err := readKey(t.context(), f)
			if err != nil {
				return "", err
			}
			switch key {
			case "enter":
				return strings.TrimSpace(string(value)), nil
			case "left":
				position = max(0, position-1)
			case "right":
				position = min(len(value), position+1)
			case "home":
				position = 0
			case "end":
				position = len(value)
			case "backspace":
				if position > 0 {
					value = append(value[:position-1], value[position:]...)
					position--
				}
			case "delete":
				if position < len(value) {
					value = append(value[:position], value[position+1:]...)
				}
			case "up", "down", "":
			default:
				if utf8.RuneCountInString(key) == 1 && []rune(key)[0] >= 32 {
					r := []rune(key)[0]
					value = append(value, 0)
					copy(value[position+1:], value[position:])
					value[position] = r
					position++
				}
			}
		}
	})
}
func (t terminal) requireText(label, def string) (string, error) {
	for {
		v, err := t.ask(label, def)
		if err != nil {
			return "", err
		}
		if v != "" {
			return v, nil
		}
		if !t.interactive() {
			return "", fmt.Errorf("%s is required", label)
		}
		fmt.Fprintln(t.Out, "Please enter a value.")
	}
}
func (t terminal) choose(label string, options []menuOption, def string) (string, error) {
	if len(options) == 0 {
		return "", fmt.Errorf("no choices for %s", label)
	}
	if t.interactive() {
		return t.menu(label, options, []string{def}, false)
	}
	validDefault := options[0].Value
	for i, o := range options {
		fmt.Fprintf(t.Out, "  %d. %s\n", i+1, o.Label)
		if o.Value == def {
			validDefault = def
		}
	}
	value, err := t.ask(label, validDefault)
	if err != nil {
		return "", err
	}
	if n, e := strconv.Atoi(value); e == nil && n > 0 && n <= len(options) {
		return options[n-1].Value, nil
	}
	for _, o := range options {
		if o.Value == value {
			return value, nil
		}
	}
	return "", fmt.Errorf("invalid selection for %s: %q", label, value)
}
func (t terminal) menu(label string, options []menuOption, defaults []string, multiple bool) (string, error) {
	return t.raw(func(f *os.File) (string, error) {
		selected := map[string]bool{}
		cursor := 0
		for i, o := range options {
			if slices.Contains(defaults, o.Value) {
				selected[o.Value] = true
				if !multiple {
					cursor = i
				}
			}
		}
		rows, offset := 0, 0
		for {
			if rows > 0 {
				fmt.Fprintf(t.Out, "\x1b[%dA", rows)
			}
			columns, height, err := term.GetSize(int(t.Out.(*os.File).Fd()))
			if err != nil {
				columns, height = 80, 25
			}
			visible := min(len(options), max(1, height-4))
			if cursor < offset {
				offset = cursor
			}
			if cursor >= offset+visible {
				offset = cursor - visible + 1
			}
			rows = visible + 2
			fmt.Fprintf(t.Out, "\r\x1b[2K\x1b[36m%s\x1b[0m\r\n", safeLine(label, columns-1))
			instruction := "Up/Down: navigate | Enter: select | Esc/Ctrl-C: cancel"
			if multiple {
				instruction = "Up/Down: navigate | Space: toggle | a: toggle all | Enter: confirm"
			}
			fmt.Fprintf(t.Out, "\x1b[2K%s\r\n", safeLine(instruction, columns-1))
			for i := offset; i < offset+visible; i++ {
				o := options[i]
				mark := " "
				if i == cursor {
					mark = ">"
				}
				check := ""
				if multiple {
					check = "[ ] "
					if selected[o.Value] {
						check = "[x] "
					}
				}
				fmt.Fprintf(t.Out, "\x1b[2K%s\r\n", safeLine(mark+" "+check+o.Label, columns-1))
			}
			key, err := readKey(t.context(), f)
			if err != nil {
				return "", err
			}
			switch key {
			case "up":
				cursor = (cursor + len(options) - 1) % len(options)
			case "down":
				cursor = (cursor + 1) % len(options)
			case " ":
				if multiple {
					selected[options[cursor].Value] = !selected[options[cursor].Value]
				}
			case "a":
				if multiple {
					all := true
					for _, o := range options {
						all = all && selected[o.Value]
					}
					for _, o := range options {
						selected[o.Value] = !all
					}
				}
			case "enter":
				if !multiple {
					return options[cursor].Value, nil
				}
				values := []string{}
				for _, o := range options {
					if selected[o.Value] {
						values = append(values, o.Value)
					}
				}
				if len(values) > 0 {
					return strings.Join(values, ","), nil
				}
			}
		}
	})
}

// Plain input retains scriptability and the existing pipe-based smoke tests.
func plainInputError(v string, err error) error {
	if strings.ContainsAny(v, "\x03\x1b") {
		return ErrCancelled
	}
	if err != nil && !(err == io.EOF && len(v) > 0) {
		return err
	}
	return nil
}
