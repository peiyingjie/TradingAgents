//go:build windows

package cli

import (
	"context"
	"golang.org/x/sys/windows"
	"os"
	"time"
	"unicode/utf16"
	"unsafe"
)

var readConsoleInput = windows.NewLazySystemDLL("kernel32.dll").NewProc("ReadConsoleInputW")

// INPUT_RECORD's keyboard union. Consume records rather than ReadConsoleW:
// key-up/window events signal the handle but cannot satisfy a character read.
type consoleInputRecord struct {
	Type                                uint16
	Padding                             uint16
	Down                                int32
	Repeat, VirtualKey, Scan, Character uint16
	Controls                            uint32
}

func readConsoleRune(ctx context.Context, f *os.File, timeout time.Duration) (rune, error) {
	deadline := time.Now().Add(timeout)
	for {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return 0, errKeyTimeout
		}
		result, err := windows.WaitForSingleObject(windows.Handle(f.Fd()), uint32(max(1, remaining.Milliseconds())))
		if err != nil {
			return 0, err
		}
		if result == uint32(windows.WAIT_TIMEOUT) {
			return 0, errKeyTimeout
		}
		var record consoleInputRecord
		var n uint32
		ok, _, callErr := readConsoleInput.Call(f.Fd(), uintptr(unsafe.Pointer(&record)), 1, uintptr(unsafe.Pointer(&n)))
		if ok == 0 {
			return 0, callErr
		}
		if n == 0 || record.Type != 1 || record.Down == 0 {
			continue
		}
		ch := record.Character
		if ch == 0 {
			switch record.VirtualKey {
			case windows.VK_UP:
				return 0x110000, nil
			case windows.VK_DOWN:
				return 0x110001, nil
			case windows.VK_LEFT:
				return 0x110002, nil
			case windows.VK_RIGHT:
				return 0x110003, nil
			case windows.VK_HOME:
				return 0x110004, nil
			case windows.VK_END:
				return 0x110005, nil
			case windows.VK_DELETE:
				return 0x110006, nil
			}
			continue
		}
		if utf16.IsSurrogate(rune(ch)) {
			if ch >= 0xDC00 {
				return rune(ch), nil
			}
			second, err := readConsoleRune(ctx, f, timeout)
			if err != nil {
				return 0, err
			}
			return utf16.DecodeRune(rune(ch), second), nil
		}
		return rune(ch), nil
	}
}
func enableANSI(out *os.File) (func(), error) {
	handle := windows.Handle(out.Fd())
	var mode uint32
	if err := windows.GetConsoleMode(handle, &mode); err != nil {
		return nil, err
	}
	if err := windows.SetConsoleMode(handle, mode|windows.ENABLE_VIRTUAL_TERMINAL_PROCESSING); err != nil {
		return nil, err
	}
	return func() { _ = windows.SetConsoleMode(handle, mode) }, nil
}
