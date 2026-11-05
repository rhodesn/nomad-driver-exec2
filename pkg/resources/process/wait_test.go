// Copyright IBM Corp. 2024, 2026
// SPDX-License-Identifier: MPL-2.0

package process

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/shoenig/test/must"
)

// writeStatus will write the given status code to a .exit_status.txt file
// and return the filepath to that file.
//
// if delay is 0, this function blocks until the file exists
//
// otherwise, this function is non-blocking and creates the file in a goroutine
// after the delay has elapsed
func writeStatus(t *testing.T, code int, delay time.Duration) string {
	taskDir := t.TempDir()
	path := filepath.Join(taskDir, "local", ".exit_status.txt")
	must.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	switch delay {
	case 0:
		time.Sleep(delay)
		must.NoError(t, os.WriteFile(path, []byte(strconv.FormatInt(int64(code), 10)), 0o644))
	default:
		go func() {
			time.Sleep(delay)
			must.NoError(t, os.WriteFile(path, []byte(strconv.FormatInt(int64(code), 10)), 0o644))
		}()
	}
	return taskDir
}

func TestPIDWaitAlreadyExited(t *testing.T) {
	// in this case the child (shim) already exited and set a status code
	// file for us to read back
	taskDir := writeStatus(t, 7, 0)

	cmd := exec.Command("sleep", ".2s")
	must.NoError(t, cmd.Start())
	defer func() { _ = cmd.Wait() }()

	// take the leaf from a real cgroup path, so the test skips when the
	// namespace root makes reuse unprovable
	cgroup := filepath.Join(filepath.Dir(selfCgroup(t)), "exec2-test-no-such-cgroup")
	pid := cmd.Process.Pid
	must.True(t, pidReused(pid, cgroup))
	waitCh := WaitPID(pid, taskDir, cgroup).Wait()
	result := <-waitCh

	must.NoError(t, result.Err)
	must.Eq(t, 7, result.ExitCode)
}

func TestPID_Wait_poll(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	start := time.Now()

	cmd := exec.CommandContext(ctx, "sleep", ".1s")
	must.NoError(t, cmd.Start())

	// when waiting via PID (the orphan case) we get the exit code from
	// the .exit_status.txt file written by the shim upon task process exit
	//
	// since we aren't running in the context of the real shim here, just
	// fudge an exit code status file ... but wait long enough for the Wait()
	// below to actually start polling the process we started above
	//
	// please never use this test case as inspiration
	taskDir := writeStatus(t, 7, 90*time.Millisecond)

	waitCh := WaitPID(cmd.Process.Pid, taskDir, "/sys/fs/cgroup/exec2-test-no-such-cgroup").Wait()
	result := <-waitCh

	// ensure the pidfd_open + poll magic works; we actually wait for the
	// process to die before the plugin asks about its exit status which
	// we retrive from the .exit_status.txt file
	must.Greater(t, 100*time.Millisecond, time.Since(start))
	must.NoError(t, result.Err)
	must.Eq(t, 7, result.ExitCode)
}

func TestPIDReusedFrom(t *testing.T) {
	cases := []struct {
		name       string
		procCgroup string
		cgroup     string
		want       bool
	}{
		{"mount prefix", "0::/nomad/task", "/sys/fs/cgroup/nomad/task", false},
		{"different prefix with equal leaf", "0::/other/task", "/sys/fs/cgroup/nomad/task", false},
		{"different leaf", "0::/nomad/other", "/sys/fs/cgroup/nomad/task", true},
		{"namespace root", "0::/\n", "/sys/fs/cgroup/nomad/task", false},
		{"missing v2 prefix", "1:name=systemd:/nomad/task\n", "/sys/fs/cgroup/nomad/task", false},
		{"multiple records", "0::/nomad/task\n1:name=systemd:/x\n", "/sys/fs/cgroup/nomad/task", false},
		{"empty input", "", "/sys/fs/cgroup/nomad/task", false},
		{"empty process path", "0::\n", "/sys/fs/cgroup/nomad/task", false},
		{"empty cgroup", "0::/nomad/task", "", false},
		{"task dot", "0::/nomad/task", ".", false},
		{"task root", "0::/nomad/task", "/", false},
		{"relative leaf", "0::/nomad/task", "task", false},
		{"trailing newline", "0::/nomad/other\n", "/sys/fs/cgroup/nomad/task", true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			must.Eq(t, tc.want, pidReusedFrom(tc.procCgroup, tc.cgroup))
		})
	}
}

func selfCgroup(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("/proc/self/cgroup")
	must.NoError(t, err)
	path, ok := strings.CutPrefix(strings.TrimSuffix(string(b), "\n"), "0::")
	if !ok || strings.Contains(path, "\n") {
		t.Skip("expected one cgroup v2 record")
	}
	if !filepath.IsAbs(path) {
		t.Fatalf("expected an absolute cgroup path, got %q", path)
	}
	if path == "/" {
		t.Skip("cgroup namespace root cannot be mapped to a task cgroup")
	}
	return filepath.Join("/sys/fs/cgroup", path)
}

func TestPIDWaitLiveStaleFileIgnored(t *testing.T) {
	cgroup := selfCgroup(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cmd := exec.CommandContext(ctx, "sleep", ".2s")
	must.NoError(t, cmd.Start())
	defer func() { _ = cmd.Wait() }()

	taskDir := writeStatus(t, 7, 0)
	start := time.Now()
	waitCh := WaitPID(cmd.Process.Pid, taskDir, cgroup).Wait()
	select {
	case result := <-waitCh:
		t.Fatalf("waiter returned before the live task exited: %+v", result)
	case <-time.After(50 * time.Millisecond):
	}
	must.NoError(t, cmd.Process.Signal(syscall.Signal(0)))
	result := <-waitCh

	must.Greater(t, 100*time.Millisecond, time.Since(start))
	must.NoError(t, result.Err)
	must.Eq(t, 7, result.ExitCode)
}

func TestPIDReused(t *testing.T) {
	cgroup := selfCgroup(t)
	cases := []struct {
		name   string
		cgroup string
		want   bool
	}{
		{
			name:   "match",
			cgroup: cgroup,
			want:   false,
		},
		{
			name:   "mismatch",
			cgroup: "/sys/fs/cgroup/exec2-test-no-such-cgroup",
			want:   true,
		},
		{
			name: "unknown",
			want: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			must.Eq(t, tc.want, pidReused(os.Getpid(), tc.cgroup))
		})
	}
	must.False(t, pidReused(-1, cgroup))
}
