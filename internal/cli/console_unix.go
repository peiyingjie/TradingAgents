//go:build !windows

package cli

import (
	"context"
	"errors"
	"golang.org/x/sys/unix"
	"os"
	"time"
	"unicode/utf8"
)

func readConsoleByte(ctx context.Context, f *os.File, b []byte, timeout time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	ready, err := unix.Poll([]unix.PollFd{{Fd: int32(f.Fd()), Events: unix.POLLIN}}, int(timeout.Milliseconds()))
	if errors.Is(err, unix.EINTR) {
		return errKeyTimeout
	}
	if err != nil {
		return err
	}
	if ready == 0 {
		return errKeyTimeout
	}
	_, err = f.Read(b)
	return err
}
func readConsoleRune(ctx context.Context, f *os.File, timeout time.Duration) (rune, error) {
	b := make([]byte, 1)
	if err := readConsoleByte(ctx, f, b, timeout); err != nil {
		return 0, err
	}
	encoded := append([]byte(nil), b...)
	for !utf8.FullRune(encoded) {
		if err := readConsoleByte(ctx, f, b, timeout); err != nil {
			return 0, err
		}
		encoded = append(encoded, b[0])
	}
	r, _ := utf8.DecodeRune(encoded)
	return r, nil
}
func enableANSI(out *os.File) (func(), error) { return func() {}, nil }
