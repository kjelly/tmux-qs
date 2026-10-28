//go:build linux

package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func themeTestTerminal(t *testing.T) (masterFD, slaveFD int, path string) {
	t.Helper()
	master, path := openPTY(t)
	t.Cleanup(func() { master.Close() })
	masterFD = int(master.Fd())
	if err := unix.SetNonblock(masterFD, true); err != nil {
		t.Fatal(err)
	}
	slaveFD, err := unix.Open(path, unix.O_RDWR|unix.O_NONBLOCK|unix.O_NOCTTY|unix.O_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { unix.Close(slaveFD) })
	return masterFD, slaveFD, path
}

func blockThemeTerminal(t *testing.T, fd int) {
	t.Helper()
	// Stop transmission so the kernel cannot free queue space between the
	// first EAGAIN and a later writer (including the subprocess test).
	if err := unix.IoctlSetInt(fd, unix.TCXONC, unix.TCOOFF); err != nil {
		t.Fatal(err)
	}
	if n, err := unix.Write(fd, []byte("blocked")); n > 0 || !errors.Is(err, unix.EAGAIN) {
		t.Fatalf("stopped terminal write = %d, %v; want EAGAIN", n, err)
	}
}

func readThemeTerminal(fd, size int) ([]byte, error) {
	data := make([]byte, size)
	deadline := time.Now().Add(2 * time.Second)
	for offset := 0; offset < size; {
		if time.Until(deadline) <= 0 {
			return nil, os.ErrDeadlineExceeded
		}
		n, err := unix.Read(fd, data[offset:])
		if n > 0 {
			offset += n
		}
		if errors.Is(err, unix.EAGAIN) {
			fds := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
			if _, err := unix.Poll(fds, 20); err != nil && !errors.Is(err, unix.EINTR) {
				return nil, err
			}
			continue
		}
		if err != nil && !errors.Is(err, unix.EINTR) {
			return nil, err
		}
	}
	return data, nil
}

func TestWriteThemeOSCBlockedTerminal(t *testing.T) {
	_, slave, path := themeTestTerminal(t)
	blockThemeTerminal(t, slave)
	before, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Fatal(err)
	}
	// Repeated failures must close their descriptors too.
	for range 3 {
		start := time.Now()
		err := writeThemeOSC(path, lightTmuxTheme.osc)
		if !errors.Is(err, os.ErrDeadlineExceeded) || !strings.Contains(err.Error(), path) {
			t.Fatalf("blocked terminal error = %v, want identifiable deadline error", err)
		}
		if elapsed := time.Since(start); elapsed < themeTTYTimeout || elapsed > time.Second {
			t.Fatalf("blocked write took %v, want approximately %v", elapsed, themeTTYTimeout)
		}
	}
	after, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before) {
		t.Fatalf("descriptor count changed: %d -> %d", len(before), len(after))
	}
}

func TestWriteThemeOSCPartialWriteTimesOut(t *testing.T) {
	master, _, path := themeTestTerminal(t)
	sequence := strings.Repeat("abcdef", 32768)
	if err := writeThemeOSC(path, sequence); !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("partial write error = %v, want deadline exceeded", err)
	}
	data := make([]byte, len(sequence))
	n, err := unix.Read(master, data)
	if err != nil || n <= 0 || n >= len(sequence) || string(data[:n]) != sequence[:n] {
		t.Fatalf("partial output: %d bytes, %v", n, err)
	}
}

func TestWriteThemeBytesResumesAfterPartialWrite(t *testing.T) {
	master, slave, _ := themeTestTerminal(t)
	data := bytes.Repeat([]byte("abcdef"), 32768)
	// Write beyond PTY capacity before the reader starts.
	n, err := unix.Write(slave, data)
	if err != nil || n <= 0 || n >= len(data) {
		t.Fatalf("initial write = %d, %v; want a partial write", n, err)
	}
	type result struct {
		data []byte
		err  error
	}
	done := make(chan result, 1)
	go func() {
		got, err := readThemeTerminal(master, len(data))
		done <- result{got, err}
	}()
	err = writeThemeBytes(slave, data[n:], time.Now().Add(time.Second))
	got := <-done // The reader has its own deadline, including on write failure.
	if err != nil || got.err != nil || !bytes.Equal(got.data, data) {
		t.Fatalf("resumed write: write=%v, read=%v, received %d/%d bytes", err, got.err, len(got.data), len(data))
	}
}

func TestWriteThemeOSCUnavailableTerminal(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing-tty")
	if err := writeThemeOSC(missing, darkTmuxTheme.osc); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing terminal error = %v", err)
	}
	fifo := filepath.Join(t.TempDir(), "unavailable-fifo")
	if err := unix.Mkfifo(fifo, 0600); err != nil {
		t.Fatal(err)
	}
	if err := writeThemeOSC(fifo, darkTmuxTheme.osc); !errors.Is(err, unix.ENXIO) {
		t.Fatalf("FIFO without reader error = %v, want nonblocking open failure", err)
	}
	f, path := openPTY(t)
	t.Cleanup(func() { f.Close() })
	fd, err := unix.Open(path, unix.O_WRONLY|unix.O_NONBLOCK|unix.O_NOCTTY|unix.O_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { unix.Close(fd) })
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if err := writeThemeBytes(fd, []byte(darkTmuxTheme.osc), time.Now().Add(time.Second)); !errors.Is(err, unix.EIO) {
		t.Fatalf("hung-up terminal write = %v, want EIO", err)
	}
	if err := waitThemeWritable(fd, time.Now().Add(time.Second)); !errors.Is(err, unix.EIO) {
		t.Fatalf("hung-up terminal poll = %v, want EIO", err)
	}
	if err := writeThemeOSC(path, darkTmuxTheme.osc); err == nil || errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("closed terminal error = %v, want immediate failure", err)
	}
}

func TestThemeApplyBlockedClientProcess(t *testing.T) {
	binary := buildTestBinary(t)
	_, blocked, blockedPath := themeTestTerminal(t)
	healthy, _, healthyPath := themeTestTerminal(t)
	blockThemeTerminal(t, blocked)
	dir := t.TempDir()
	logPath := filepath.Join(dir, "tmux.log")
	script := `#!/bin/sh
printf '%s\n' "$*" >> "$THEME_TEST_LOG"
case " $* " in
  *' show-options '*) printf '167,165\n' ;;
  *' list-clients '*) printf '%s\t167\tblocked\n%s\t220\thealthy\n' "$THEME_TEST_BLOCKED" "$THEME_TEST_HEALTHY" ;;
esac
`
	if err := os.WriteFile(filepath.Join(dir, "tmux"), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("LC_IS_EINK", "")
	t.Setenv("TMUX_QS_SERVER", "")
	t.Setenv("TMUX", "")
	t.Setenv(popupEnv, "")
	t.Setenv("XDG_CACHE_HOME", dir)
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("THEME_TEST_LOG", logPath)
	t.Setenv("THEME_TEST_BLOCKED", blockedPath)
	t.Setenv("THEME_TEST_HEALTHY", healthyPath)
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, "theme", "apply")
	output, err := cmd.CombinedOutput()
	var exit *exec.ExitError
	if ctx.Err() != nil || !errors.As(err, &exit) || exit.ExitCode() != 1 {
		t.Fatalf("theme apply: err=%v, context=%v, output=%s", err, ctx.Err(), output)
	}
	if !strings.Contains(string(output), blockedPath) || !strings.Contains(string(output), os.ErrDeadlineExceeded.Error()) {
		t.Fatalf("missing blocked client diagnostic: %s", output)
	}
	got, err := readThemeTerminal(healthy, len(darkTmuxTheme.osc))
	if err != nil || string(got) != darkTmuxTheme.osc {
		t.Fatalf("later client OSC = %q, %v", got, err)
	}
	log, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, session := range []string{"blocked", "healthy"} {
		if !strings.Contains(string(log), fmt.Sprintf("set-option -t %s message-style", session)) {
			t.Errorf("session %s styles were not applied: %s", session, log)
		}
	}
}
