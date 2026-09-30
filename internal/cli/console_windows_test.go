//go:build windows

package cli

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"golang.org/x/sys/windows"
	"golang.org/x/term"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
	"unsafe"
)

// The helper runs in a real Windows pseudo console, not a mocked key decoder.
func TestConsoleChild(t *testing.T) {
	if os.Getenv("TRADINGAGENTS_CONSOLE_TEST") != "1" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	// CreateProcess inherits the test runner's redirected standard handles;
	// open the attached ConPTY console explicitly for this helper.
	in, err := os.OpenFile("CONIN$", os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	out, err := os.OpenFile("CONOUT$", os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	// The child test runner still writes its PASS/coverage summary after this
	// function returns. The process owns and closes this console handle on exit.
	// Keep child test2json markers out of the parent's test stream.
	os.Stdout, os.Stderr = out, out
	originalState, err := term.GetState(int(in.Fd()))
	if err != nil {
		t.Fatal(err)
	}
	ui := terminal{Reader: bufio.NewReader(in), Input: in, Out: out, Context: ctx}
	if !ui.interactive() {
		t.Fatal("no terminal")
	}
	restore, err := enableANSI(out)
	if err != nil {
		t.Fatal(err)
	}
	defer restore()
	picked, err := ui.choose("PICK", []menuOption{{"First", "first"}, {"Second", "second"}}, "")
	if err != nil {
		t.Fatal(err)
	}
	multi, err := ui.menu("MULTI", []menuOption{{"Market", "market"}, {"News", "news"}}, []string{"market"}, true)
	if err != nil {
		t.Fatal(err)
	}
	text, err := ui.ask("UNICODE", "")
	if err != nil {
		t.Fatal(err)
	}
	_, esc := ui.choose("CANCEL", []menuOption{{"Only", "only"}}, "")
	_, ctrl := ui.ask("CONTROL", "")
	if picked != "second" || multi != "news" || text != "中文" || !errors.Is(esc, ErrCancelled) || !errors.Is(ctrl, ErrCancelled) {
		t.Fatalf("%q %q %q %v %v", picked, multi, text, esc, ctrl)
	}
	currentState, err := term.GetState(int(in.Fd()))
	if err != nil || !reflect.DeepEqual(originalState, currentState) {
		t.Fatal("console mode not restored", err)
	}
	cancelled, stopContext := context.WithCancel(context.Background())
	stopContext()
	if _, err = readKey(cancelled, in); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	view := newProgressView([]string{"market"})
	stop := view.start(out, func() string { return "LLM calls: 0" })
	time.Sleep(250 * time.Millisecond)
	if err = stop(); err != nil {
		t.Fatal(err)
	}
	fmt.Fprintln(out, "CONSOLE_SMOKE_OK")
}

func TestWindowsConsoleInteraction(t *testing.T) {
	t.Setenv("TRADINGAGENTS_CONSOLE_TEST", "1")
	inputR, inputW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer inputR.Close()
	defer inputW.Close()
	outputR, outputW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer outputR.Close()
	defer outputW.Close()
	var console windows.Handle
	if err = windows.CreatePseudoConsole(windows.Coord{X: 100, Y: 40}, windows.Handle(inputR.Fd()), windows.Handle(outputW.Fd()), 0, &console); err != nil {
		t.Fatal(err)
	}
	defer windows.ClosePseudoConsole(console)
	attrs, err := windows.NewProcThreadAttributeList(1)
	if err != nil {
		t.Fatal(err)
	}
	defer attrs.Delete()
	// This attribute takes the opaque console handle itself, not an address.
	// Pass it as uintptr to Win32 rather than inventing a Go heap pointer.
	update := windows.NewLazySystemDLL("kernel32.dll").NewProc("UpdateProcThreadAttribute")
	ok, _, updateErr := update.Call(uintptr(unsafe.Pointer(attrs.List())), 0, windows.PROC_THREAD_ATTRIBUTE_PSEUDOCONSOLE, uintptr(console), unsafe.Sizeof(console), 0, 0)
	if ok == 0 {
		t.Fatal(updateErr)
	}
	info := windows.StartupInfoEx{StartupInfo: windows.StartupInfo{Cb: uint32(unsafe.Sizeof(windows.StartupInfoEx{}))}, ProcThreadAttributeList: attrs.List()}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	args := []string{executable, "-test.run=^TestConsoleChild$", "-test.timeout=20s"}
	if cover := flag.Lookup("test.gocoverdir"); cover != nil && cover.Value.String() != "" {
		args = append(args, "-test.gocoverdir="+cover.Value.String())
	}
	command, err := windows.UTF16PtrFromString(windows.ComposeCommandLine(args))
	if err != nil {
		t.Fatal(err)
	}
	var process windows.ProcessInformation
	if err = windows.CreateProcess(nil, command, nil, nil, false, windows.EXTENDED_STARTUPINFO_PRESENT, nil, nil, &info.StartupInfo, &process); err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(process.Thread)
	defer windows.CloseHandle(process.Process)
	defer windows.TerminateProcess(process.Process, 1)
	var mu sync.Mutex
	var output strings.Builder
	go func() {
		buf := make([]byte, 4096)
		for {
			n, e := outputR.Read(buf)
			if n > 0 {
				mu.Lock()
				output.Write(buf[:n])
				mu.Unlock()
			}
			if e != nil {
				return
			}
		}
	}()
	waitFor := func(marker string) {
		t.Helper()
		deadline := time.Now().Add(12 * time.Second)
		for time.Now().Before(deadline) {
			mu.Lock()
			seen := strings.Contains(output.String(), marker)
			mu.Unlock()
			if seen {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		mu.Lock()
		captured := output.String()
		mu.Unlock()
		t.Fatalf("waiting for %s: %q", marker, captured)
	}
	for _, step := range [][2]string{{"Second", "\x1b[B\r"}, {"News", " \x1b[B \r"}, {"UNICODE:", "中文\r"}, {"Only", "\x1b"}, {"CONTROL:", "\x03"}} {
		waitFor(step[0])
		if _, err = inputW.WriteString(step[1]); err != nil {
			t.Fatal(err)
		}
	}
	waitFor("CONSOLE_SMOKE_OK")
	result, err := windows.WaitForSingleObject(process.Process, 5000)
	if err != nil || result != windows.WAIT_OBJECT_0 {
		t.Fatal(result, err)
	}
	var code uint32
	if err = windows.GetExitCodeProcess(process.Process, &code); err != nil || code != 0 {
		mu.Lock()
		captured := output.String()
		mu.Unlock()
		t.Fatal(code, err, captured)
	}
}
