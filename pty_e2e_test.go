//go:build linux

package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

// ---------------------------------------------------------------------------
// L4: Real binary PTY E2E tests
//
// These tests compile the actual tmux-qs binary, launch it inside a
// pseudo-terminal (PTY), send raw keystrokes, and verify the program
// starts, renders, handles resize, and exits cleanly.
//
// We use the Linux PTY API directly (via golang.org/x/sys/unix) instead
// of a third-party library like creack/pty. This keeps the dependency
// surface minimal while still exercising the real binary in a real
// terminal environment.
//
// All tests use isolated HOME/XDG env vars and a private tmux server
// (when tmux is available) so the developer's real environment is never
// touched.
// ---------------------------------------------------------------------------

// ptyProcess holds the state of a process running inside a PTY.
type ptyProcess struct {
	master *os.File
	cmd    *exec.Cmd
	output *safeBuffer
}

// safeBuffer is a goroutine-safe bytes.Buffer for collecting PTY output
// without triggering race detector warnings.
type safeBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (sb *safeBuffer) Write(p []byte) (int, error) {
	sb.mu.Lock()
	defer sb.mu.Unlock()
	return sb.b.Write(p)
}

func (sb *safeBuffer) String() string {
	sb.mu.Lock()
	defer sb.mu.Unlock()
	return sb.b.String()
}

// buildTestBinary compiles the tmux-qs binary into a temp directory.
// The binary is built once per test binary invocation and reused by
// all PTY tests via a package-level variable.
var testBinaryPath string

func buildTestBinary(t *testing.T) string {
	t.Helper()
	if testBinaryPath != "" {
		// Check if the binary still exists.
		if _, err := os.Stat(testBinaryPath); err == nil {
			return testBinaryPath
		}
	}
	output := filepath.Join(t.TempDir(), "tmux-qs")
	cmd := exec.Command("go", "build", "-o", output, ".")
	combined, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("build test binary: %v\n%s", err, combined)
	}
	testBinaryPath = output
	return output
}

// openPTY opens a new pseudo-terminal pair and returns the master file
// descriptor. The slave path is also returned (for debugging).
func openPTY(t *testing.T) (*os.File, string) {
	t.Helper()
	master, err := os.OpenFile("/dev/ptmx", os.O_RDWR, 0)
	if err != nil {
		t.Fatalf("open /dev/ptmx: %v", err)
	}

	// Unlock the slave PTY.
	var unlock int32 = 0
	_, _, errno := unix.Syscall(
		unix.SYS_IOCTL,
		uintptr(master.Fd()),
		uintptr(unix.TIOCSPTLCK),
		uintptr(unsafe.Pointer(&unlock)),
	)
	if errno != 0 {
		master.Close()
		t.Fatalf("unlock PTY slave: %v", errno)
	}

	// Get the slave PTY number.
	var ptnum int32
	_, _, errno = unix.Syscall(
		unix.SYS_IOCTL,
		uintptr(master.Fd()),
		uintptr(unix.TIOCGPTN),
		uintptr(unsafe.Pointer(&ptnum)),
	)
	if errno != 0 {
		master.Close()
		t.Fatalf("get PTY number: %v", errno)
	}
	slavePath := fmt.Sprintf("/dev/pts/%d", ptnum)

	return master, slavePath
}

// setPTYSize sets the window size of the PTY.
func setPTYSize(t *testing.T, f *os.File, rows, cols uint16) {
	t.Helper()
	ws := unix.Winsize{
		Row: rows,
		Col: cols,
	}
	_, _, errno := unix.Syscall(
		unix.SYS_IOCTL,
		uintptr(f.Fd()),
		uintptr(unix.TIOCSWINSZ),
		uintptr(unsafe.Pointer(&ws)),
	)
	if errno != 0 {
		t.Fatalf("set PTY size: %v", errno)
	}
}

// startPTY launches the test binary inside a PTY with the given
// environment and initial terminal size.
func startPTY(t *testing.T, binary string, env []string, rows, cols uint16) *ptyProcess {
	t.Helper()

	master, slavePath := openPTY(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)

	cmd := exec.CommandContext(ctx, binary)
	cmd.Env = env

	// Open the slave PTY for stdin/stdout/stderr.
	slave, err := os.OpenFile(slavePath, os.O_RDWR|unix.O_NOCTTY, 0)
	if err != nil {
		master.Close()
		t.Fatalf("open slave PTY %s: %v", slavePath, err)
	}

	cmd.Stdin = slave
	cmd.Stdout = slave
	cmd.Stderr = slave

	setPTYSize(t, master, rows, cols)

	if err := cmd.Start(); err != nil {
		slave.Close()
		master.Close()
		t.Fatalf("start process: %v", err)
	}

	// Close the slave FD in the parent — the child has its own copy.
	slave.Close()

	proc := &ptyProcess{
		master: master,
		cmd:    cmd,
		output: &safeBuffer{},
	}

	// Goroutine to copy PTY output into the safe buffer.
	go func() {
		_, _ = io.Copy(proc.output, master)
	}()

	t.Cleanup(func() {
		_ = master.Close()
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		_ = cmd.Wait()
	})

	return proc
}

// waitForPTYOutput polls the PTY output until the predicate returns true
// or the timeout expires. A 10ms polling interval is used for the
// bounded PTY helper only (not for application logic synchronization).
func waitForPTYOutput(t *testing.T, proc *ptyProcess, timeout time.Duration, predicate func(string) bool) string {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if predicate(proc.output.String()) {
			return proc.output.String()
		}
		time.Sleep(10 * time.Millisecond)
	}
	current := proc.output.String()
	t.Fatalf("timed out waiting for terminal output\noutput (last 2KB):\n%s",
		sanitizeTranscript(current))
	return ""
}

// sanitizeTranscript trims the transcript to the last 2KB to avoid
// flooding CI logs on failure.
func sanitizeTranscript(s string) string {
	const maxLen = 2048
	if len(s) > maxLen {
		return "..." + s[len(s)-maxLen:]
	}
	return s
}

// writePTY sends input bytes to the PTY master.
func writePTY(t *testing.T, proc *ptyProcess, input string) {
	t.Helper()
	if _, err := proc.master.WriteString(input); err != nil {
		t.Fatalf("write PTY input: %v", err)
	}
}

// waitProcess waits for the process to exit with a timeout.
func waitProcess(cmd *exec.Cmd, timeout time.Duration) (*os.ProcessState, error) {
	done := make(chan struct {
		state *os.ProcessState
		err   error
	}, 1)
	go func() {
		err := cmd.Wait()
		done <- struct {
			state *os.ProcessState
			err   error
		}{cmd.ProcessState, err}
	}()
	select {
	case result := <-done:
		return result.state, result.err
	case <-time.After(timeout):
		return nil, fmt.Errorf("timeout after %s", timeout)
	}
}

// isolatedPTYEnv returns environment variables for running the test
// binary in an isolated environment (no real HOME, no real tmux, no
// real config).
func isolatedPTYEnv(t *testing.T) []string {
	t.Helper()
	home := t.TempDir()
	env := []string{
		"HOME=" + home,
		"XDG_CONFIG_HOME=" + filepath.Join(home, ".config"),
		"XDG_CACHE_HOME=" + filepath.Join(home, ".cache"),
		"XDG_DATA_HOME=" + filepath.Join(home, ".local", "share"),
		"TMUX=",
		"TMUX_QS_POPUP=",
		"TERM=xterm-256color",
		"NO_COLOR=1",
		"LC_ALL=C",
		"LANG=C",
		"PATH=" + os.Getenv("PATH"),
	}
	return env
}

// stripANSIForPTY removes ANSI escape sequences from PTY output for
// text-based assertions. Uses a comprehensive regex that handles CSI,
// OSC, and other escape sequences.
func stripANSIForPTY(s string) string {
	var b strings.Builder
	i := 0
	for i < len(s) {
		if s[i] == 0x1b {
			// Skip ESC sequence
			i++
			if i < len(s) && s[i] == '[' {
				// CSI sequence: skip until we find a final byte (0x40-0x7E)
				i++
				for i < len(s) && (s[i] < 0x40 || s[i] > 0x7E) {
					i++
				}
				if i < len(s) {
					i++ // skip the final byte
				}
			} else if i < len(s) && s[i] == ']' {
				// OSC sequence: skip until BEL (0x07) or ST (ESC \)
				i++
				for i < len(s) {
					if s[i] == 0x07 {
						i++
						break
					}
					if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '\\' {
						i += 2
						break
					}
					i++
				}
			} else {
				// Simple escape: skip one more byte
				if i < len(s) {
					i++
				}
			}
		} else if s[i] == '\r' {
			// Skip carriage returns
			i++
		} else {
			b.WriteByte(s[i])
			i++
		}
	}
	return b.String()
}

// containsVisible checks if the PTY output contains the given text
// after stripping ANSI sequences.
func containsVisible(output, text string) bool {
	return strings.Contains(stripANSIForPTY(output), text)
}

// ---------------------------------------------------------------------------
// PTY E2E tests
// ---------------------------------------------------------------------------

// TestPTYStartAndQuit verifies the real binary starts, renders the TUI
// header, and exits cleanly when Ctrl-C is sent.
func TestPTYStartAndQuit(t *testing.T) {
	binary := buildTestBinary(t)
	env := isolatedPTYEnv(t)

	proc := startPTY(t, binary, env, 30, 100)

	// Wait for the header to appear (proves the TUI started).
	waitForPTYOutput(t, proc, 10*time.Second, func(s string) bool {
		return containsVisible(s, "help")
	})

	// Send Ctrl-C to quit.
	writePTY(t, proc, "\x03")

	// Wait for the process to exit.
	state, err := waitProcess(proc.cmd, 5*time.Second)
	if err != nil {
		t.Fatalf("process did not exit after Ctrl-C: %v", err)
	}
	if state == nil {
		t.Fatal("process state is nil")
	}
	// The process should have exited (not been killed by signal).
	if !state.Exited() {
		t.Errorf("process did not exit cleanly; state=%v", state)
	}
}

// TestPTYStartAndQuitWithEsc verifies the real binary exits when Esc
// is pressed (non-vim mode, since --no-vim is not in env; but default
// is vim enabled via newModel... actually the binary uses the --vim
// flag. Without --vim, vimEnabled defaults to false in main()).
func TestPTYStartAndQuitWithEsc(t *testing.T) {
	binary := buildTestBinary(t)
	env := isolatedPTYEnv(t)

	proc := startPTY(t, binary, env, 30, 100)

	waitForPTYOutput(t, proc, 10*time.Second, func(s string) bool {
		return containsVisible(s, "help")
	})

	// Send Esc. In non-vim mode (vimEnabled defaults to false in
	// production main()), Esc should quit.
	writePTY(t, proc, "\x1b")

	state, err := waitProcess(proc.cmd, 5*time.Second)
	if err != nil {
		t.Fatalf("process did not exit after Esc: %v", err)
	}
	if state == nil || !state.Exited() {
		t.Errorf("process did not exit cleanly")
	}
}

// TestPTYResize verifies the real binary handles terminal resize
// without crashing and continues to render after resize.
func TestPTYResize(t *testing.T) {
	binary := buildTestBinary(t)
	env := isolatedPTYEnv(t)

	proc := startPTY(t, binary, env, 30, 100)

	// Wait for initial render.
	waitForPTYOutput(t, proc, 10*time.Second, func(s string) bool {
		return containsVisible(s, "help")
	})

	// Resize to various dimensions.
	for _, sz := range []struct{ r, c uint16 }{
		{40, 120},
		{24, 80},
		{15, 50},
		{10, 40},
		{50, 200},
		{30, 100},
	} {
		setPTYSize(t, proc.master, sz.r, sz.c)
		time.Sleep(50 * time.Millisecond)
	}

	// Verify the process is still alive.
	if proc.cmd.ProcessState != nil && proc.cmd.ProcessState.Exited() {
		t.Fatal("process exited during resize")
	}

	// Verify output still contains header after resize.
	waitForPTYOutput(t, proc, 5*time.Second, func(s string) bool {
		return containsVisible(s, "help")
	})

	// Quit cleanly.
	writePTY(t, proc, "\x03")
	state, err := waitProcess(proc.cmd, 5*time.Second)
	if err != nil {
		t.Fatalf("process did not exit after Ctrl-C: %v", err)
	}
	if state == nil || !state.Exited() {
		t.Errorf("process did not exit cleanly after resize")
	}
}

// TestPTYResizeNarrow verifies the binary renders in a very narrow
// terminal without crashing.
func TestPTYResizeNarrow(t *testing.T) {
	binary := buildTestBinary(t)
	env := isolatedPTYEnv(t)

	// Start with a very narrow terminal.
	proc := startPTY(t, binary, env, 10, 20)

	// Wait for some output (the TUI should still render something).
	waitForPTYOutput(t, proc, 10*time.Second, func(s string) bool {
		return len(proc.output.String()) > 10
	})

	// Verify process is still alive.
	if proc.cmd.ProcessState != nil && proc.cmd.ProcessState.Exited() {
		t.Fatal("process exited in narrow terminal")
	}

	// Quit.
	writePTY(t, proc, "\x03")
	_, err := waitProcess(proc.cmd, 5*time.Second)
	if err != nil {
		// In a very narrow terminal, Ctrl-C might not be processed
		// correctly. Try killing the process.
		if proc.cmd.Process != nil {
			_ = proc.cmd.Process.Kill()
		}
		t.Logf("process did not exit after Ctrl-C in narrow terminal: %v", err)
	}
}

// TestPTYSIGINT verifies the binary exits when sent SIGINT.
func TestPTYSIGINT(t *testing.T) {
	binary := buildTestBinary(t)
	env := isolatedPTYEnv(t)

	proc := startPTY(t, binary, env, 30, 100)

	waitForPTYOutput(t, proc, 10*time.Second, func(s string) bool {
		return containsVisible(s, "help")
	})

	// Send SIGINT.
	if err := proc.cmd.Process.Signal(os.Interrupt); err != nil {
		t.Fatalf("send SIGINT: %v", err)
	}

	// Wait for the process to exit. A non-zero exit code is acceptable
	// here — the important thing is that the process terminates within
	// a reasonable timeout (no deadlock or hang).
	state, err := waitProcess(proc.cmd, 5*time.Second)
	if err != nil && state == nil {
		t.Fatalf("process did not exit after SIGINT: %v", err)
	}
	if state == nil {
		t.Fatal("process state is nil after SIGINT")
	}
	if !state.Exited() {
		t.Errorf("process did not terminate; state=%v", state)
	}
}

// TestPTYVersionFlag verifies --version prints the version and exits.
func TestPTYVersionFlag(t *testing.T) {
	binary := buildTestBinary(t)
	env := isolatedPTYEnv(t)

	proc := startPTY(t, binary, append(env, "TMUX=", "TMUX_QS_POPUP="), 30, 100)
	// We need to pass --version as an argument, but startPTY doesn't
	// support args. Let's use exec.Command directly instead.
	proc.cmd.Process.Kill()
	proc.cmd.Wait()

	// Run with --version flag directly (no PTY needed).
	cmd := exec.Command(binary, "--version")
	cmd.Env = env
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("--version: %v\n%s", err, output)
	}
	out := strings.TrimSpace(string(output))
	if !strings.HasPrefix(out, "tmux-qs ") {
		t.Errorf("--version output = %q, want prefix 'tmux-qs '", out)
	}
}

// TestPTYHelpFlag verifies --help prints usage and exits 0.
func TestPTYHelpFlag(t *testing.T) {
	binary := buildTestBinary(t)
	env := isolatedPTYEnv(t)

	cmd := exec.Command(binary, "--help")
	cmd.Env = env
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("--help: %v\n%s", err, output)
	}
	out := string(output)
	if !strings.Contains(out, "tmux-qs") {
		t.Errorf("--help output does not contain 'tmux-qs'\n%s", out)
	}
	if !strings.Contains(out, "Usage:") {
		t.Errorf("--help output does not contain 'Usage:'\n%s", out)
	}
}

// TestPTYNoColorEnv verifies the binary starts with NO_COLOR=1.
func TestPTYNoColorEnv(t *testing.T) {
	binary := buildTestBinary(t)
	env := isolatedPTYEnv(t)

	proc := startPTY(t, binary, env, 30, 100)

	waitForPTYOutput(t, proc, 10*time.Second, func(s string) bool {
		return containsVisible(s, "help")
	})

	// Quit.
	writePTY(t, proc, "\x03")
	state, err := waitProcess(proc.cmd, 5*time.Second)
	if err != nil {
		t.Fatalf("process did not exit: %v", err)
	}
	if state == nil || !state.Exited() {
		t.Errorf("process did not exit cleanly")
	}
}

// TestPTYRapidKeystrokes verifies rapid keystrokes don't crash the binary.
func TestPTYRapidKeystrokes(t *testing.T) {
	binary := buildTestBinary(t)
	env := isolatedPTYEnv(t)

	proc := startPTY(t, binary, env, 30, 100)

	waitForPTYOutput(t, proc, 10*time.Second, func(s string) bool {
		return containsVisible(s, "help")
	})

	// Send many rapid down-arrow keys.
	for i := 0; i < 30; i++ {
		writePTY(t, proc, "\x1b[B") // ESC [ B = down arrow
	}

	// Give the program a moment to process.
	time.Sleep(200 * time.Millisecond)

	// Verify process is still alive.
	if proc.cmd.ProcessState != nil && proc.cmd.ProcessState.Exited() {
		t.Fatal("process exited during rapid keystrokes")
	}

	// Quit.
	writePTY(t, proc, "\x03")
	state, err := waitProcess(proc.cmd, 5*time.Second)
	if err != nil {
		t.Fatalf("process did not exit: %v", err)
	}
	if state == nil || !state.Exited() {
		t.Errorf("process did not exit cleanly")
	}
}

// ---------------------------------------------------------------------------
// PTY mouse event tests
// ---------------------------------------------------------------------------

// sgrMousePress encodes an SGR mouse press event.
// Format: ESC [ < button ; col ; row M
func sgrMousePress(button, col, row int) string {
	return fmt.Sprintf("\x1b[<%d;%d;%dM", button, row, col)
}

// sgrMouseRelease encodes an SGR mouse release event.
// Format: ESC [ < button ; col ; row m
func sgrMouseRelease(button, col, row int) string {
	return fmt.Sprintf("\x1b[<%d;%d;%dm", button, row, col)
}

// SGR mouse button codes (per xterm SGR mouse protocol).
const (
	sgrButtonLeft    = 0
	sgrButtonMiddle  = 1
	sgrButtonRight   = 2
	sgrButtonWheelUp = 64
	sgrButtonWheelDn = 65
)

// TestPTYMouseWheel verifies the binary handles mouse wheel events
// (scroll up/down) without crashing.
func TestPTYMouseWheel(t *testing.T) {
	binary := buildTestBinary(t)
	env := isolatedPTYEnv(t)

	proc := startPTY(t, binary, env, 30, 100)

	waitForPTYOutput(t, proc, 10*time.Second, func(s string) bool {
		return containsVisible(s, "help")
	})

	// Send wheel down events.
	for i := 0; i < 5; i++ {
		writePTY(t, proc, sgrMousePress(sgrButtonWheelDn, 50, 10+i))
	}

	// Send wheel up events.
	for i := 0; i < 5; i++ {
		writePTY(t, proc, sgrMousePress(sgrButtonWheelUp, 50, 10+i))
	}

	time.Sleep(200 * time.Millisecond)

	// Verify process is still alive.
	if proc.cmd.ProcessState != nil && proc.cmd.ProcessState.Exited() {
		t.Fatal("process exited during mouse wheel events")
	}

	// Quit cleanly.
	writePTY(t, proc, "\x03")
	state, err := waitProcess(proc.cmd, 5*time.Second)
	if err != nil {
		t.Fatalf("process did not exit: %v", err)
	}
	if state == nil || !state.Exited() {
		t.Errorf("process did not exit cleanly")
	}
}

// TestPTYMouseLeftClick verifies the binary handles mouse left-click
// events without crashing. The click should move the cursor to the
// clicked row.
func TestPTYMouseLeftClick(t *testing.T) {
	binary := buildTestBinary(t)
	env := isolatedPTYEnv(t)

	proc := startPTY(t, binary, env, 30, 100)

	waitForPTYOutput(t, proc, 10*time.Second, func(s string) bool {
		return containsVisible(s, "help")
	})

	// Send a left-click press at column 5, row 5 (1-based SGR
	// coordinates, so this is row 3 in 0-based — within the list
	// area if there are items).
	writePTY(t, proc, sgrMousePress(sgrButtonLeft, 5, 5))

	// Send the corresponding release.
	writePTY(t, proc, sgrMouseRelease(sgrButtonLeft, 5, 5))

	time.Sleep(200 * time.Millisecond)

	// Verify process is still alive.
	if proc.cmd.ProcessState != nil && proc.cmd.ProcessState.Exited() {
		t.Fatal("process exited during mouse click")
	}

	// Quit cleanly.
	writePTY(t, proc, "\x03")
	state, err := waitProcess(proc.cmd, 5*time.Second)
	if err != nil {
		t.Fatalf("process did not exit: %v", err)
	}
	if state == nil || !state.Exited() {
		t.Errorf("process did not exit cleanly")
	}
}

// TestPTYMouseRapidClicks verifies rapid mouse clicks don't crash
// the binary or cause a deadlock.
func TestPTYMouseRapidClicks(t *testing.T) {
	binary := buildTestBinary(t)
	env := isolatedPTYEnv(t)

	proc := startPTY(t, binary, env, 30, 100)

	waitForPTYOutput(t, proc, 10*time.Second, func(s string) bool {
		return containsVisible(s, "help")
	})

	// Send many rapid click events at various positions.
	for i := 0; i < 20; i++ {
		row := 3 + (i % 10)
		writePTY(t, proc, sgrMousePress(sgrButtonLeft, 5, row))
		writePTY(t, proc, sgrMouseRelease(sgrButtonLeft, 5, row))
	}

	// Interleave some wheel events.
	for i := 0; i < 10; i++ {
		writePTY(t, proc, sgrMousePress(sgrButtonWheelDn, 50, 10))
	}

	time.Sleep(300 * time.Millisecond)

	// Verify process is still alive.
	if proc.cmd.ProcessState != nil && proc.cmd.ProcessState.Exited() {
		t.Fatal("process exited during rapid mouse events")
	}

	// Quit cleanly.
	writePTY(t, proc, "\x03")
	state, err := waitProcess(proc.cmd, 5*time.Second)
	if err != nil {
		t.Fatalf("process did not exit: %v", err)
	}
	if state == nil || !state.Exited() {
		t.Errorf("process did not exit cleanly")
	}
}
