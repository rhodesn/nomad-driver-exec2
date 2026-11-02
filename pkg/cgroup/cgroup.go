// Copyright IBM Corp. 2026
// SPDX-License-Identifier: MPL-2.0

package cgroup

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/hashicorp/nomad-driver-exec2/pkg/resources"
	"golang.org/x/sys/unix"
)

// Group represents one cgroup v2 directory.
type Group struct {
	path string
}

// Limits describes driver resource policy, not raw cgroup control values.
type Limits struct {
	// CPUQuota is the CPU quota in microseconds for a 100000-microsecond period.
	CPUQuota uint64
	// Memory is the memory limit in bytes.
	Memory uint64
	// MemoryMax is the maximum memory limit in bytes. A zero value uses Memory.
	MemoryMax uint64
}

// Stats contains resource usage for a cgroup v2 directory.
type Stats struct {
	// Memory contains memory usage statistics.
	Memory MemoryStats
	// CPU contains CPU usage statistics.
	CPU CPUStats
}

// MemoryStats contains cgroup memory usage statistics.
type MemoryStats struct {
	// Current is the current memory usage in bytes.
	Current uint64
	// Swap is the current swap usage in bytes.
	Swap uint64
	// Cache is the file-backed memory usage in bytes.
	Cache uint64
	// RSS is the anonymous memory usage in bytes.
	RSS uint64
}

// CPUStats contains cgroup CPU usage statistics.
type CPUStats struct {
	// User is the CPU time in microseconds spent in user mode.
	User resources.MicroSecond
	// System is the CPU time in microseconds spent in system mode.
	System resources.MicroSecond
	// Total is the total CPU time in microseconds.
	Total resources.MicroSecond
}

// New returns the cgroup v2 group at path.
func New(path string) Group {
	return Group{path: path}
}

// Open returns a cgroup directory file descriptor and a function that closes it.
func (g Group) Open() (int, func(), error) {
	fd, err := unix.Open(g.path, unix.O_PATH, 0)
	if err != nil {
		return 0, func() {}, err
	}
	return fd, func() { _ = unix.Close(fd) }, nil
}

// Configure writes cgroup control values for limits.
//
// CPUQuota and Memory must be greater than zero. A nonzero MemoryMax must not
// be lower than Memory.
func (g Group) Configure(limits Limits) error {
	if limits.CPUQuota == 0 {
		return errors.New("cpu quota must be greater than zero")
	}
	if limits.Memory == 0 {
		return errors.New("memory limit must be greater than zero")
	}
	if limits.MemoryMax > 0 && limits.MemoryMax < limits.Memory {
		return errors.New("maximum memory limit must not be lower than memory limit")
	}

	if err := g.write("cpu.max", fmt.Sprintf("%d 100000", limits.CPUQuota)); err != nil {
		return err
	}

	if limits.MemoryMax == 0 {
		return g.write("memory.max", strconv.FormatUint(limits.Memory, 10))
	}

	if err := g.write("memory.low", strconv.FormatUint(limits.Memory, 10)); err != nil {
		return err
	}
	return g.write("memory.max", strconv.FormatUint(limits.MemoryMax, 10))
}

// Kill kills the processes in the cgroup.
func (g Group) Kill() error {
	return g.write("cgroup.kill", "1")
}

// CurrentPIDs returns the live process count in the cgroup.
func (g Group) CurrentPIDs() (int, error) {
	s, err := g.read("pids.current")
	if err != nil {
		return 0, err
	}
	if s == "" {
		return 0, nil
	}
	count, err := strconv.Atoi(s)
	if err != nil {
		return 0, nil
	}
	return count, nil
}

// Stats returns resource usage for the cgroup.
//
// Stats attempts each read and returns all read errors.
func (g Group) Stats() (Stats, error) {
	var stats Stats
	var errs []error

	memory, err := g.read("memory.current")
	if err != nil {
		errs = append(errs, err)
	}
	stats.Memory.Current = parseUint(memory)

	swap, err := g.read("memory.swap.current")
	if err != nil {
		errs = append(errs, err)
	}
	stats.Memory.Swap = parseUint(swap)

	memoryStat, err := g.read("memory.stat")
	if err != nil {
		errs = append(errs, err)
	}
	stats.Memory.Cache, stats.Memory.RSS = extractMemoryStats(memoryStat)

	cpuStat, err := g.read("cpu.stat")
	if err != nil {
		errs = append(errs, err)
	}
	stats.CPU.User, stats.CPU.System, stats.CPU.Total = extractCPUStats(cpuStat)

	return stats, errors.Join(errs...)
}

func (g Group) read(name string) (string, error) {
	b, err := os.ReadFile(filepath.Join(g.path, name))
	return strings.TrimSpace(string(b)), err
}

func (g Group) write(name, content string) error {
	f, err := os.OpenFile(filepath.Join(g.path, name), os.O_WRONLY, 0o700)
	if err != nil {
		return fmt.Errorf("failed to open cgroup file: %w", err)
	}
	if _, err = io.WriteString(f, content); err != nil {
		_ = f.Close()
		return fmt.Errorf("failed to write pid to cgroup file: %w", err)
	}
	return f.Close()
}

func parseUint(s string) uint64 {
	v, _ := strconv.Atoi(s)
	return uint64(v)
}

func extractMemoryStats(s string) (cache, rss uint64) {
	read := func(line string) uint64 {
		num := line[strings.Index(line, " ")+1:]
		v, _ := strconv.ParseUint(num, 10, 64)
		return v
	}
	scanner := bufio.NewScanner(strings.NewReader(s))
	for scanner.Scan() {
		text := scanner.Text()
		switch {
		case strings.HasPrefix(text, "file "):
			cache = read(text)
		case strings.HasPrefix(text, "anon "):
			rss = read(text)
		}
		if cache != 0 && rss != 0 {
			break
		}
	}
	return cache, rss
}

func extractCPUStats(s string) (user, system, total resources.MicroSecond) {
	read := func(line string, value *resources.MicroSecond) {
		num := line[strings.Index(line, " ")+1:]
		v, _ := strconv.ParseInt(num, 10, 64)
		*value = resources.MicroSecond(v)
	}
	scanner := bufio.NewScanner(strings.NewReader(s))
	for scanner.Scan() {
		text := scanner.Text()
		switch {
		case strings.HasPrefix(text, "user_usec"):
			read(text, &user)
		case strings.HasPrefix(text, "system_usec"):
			read(text, &system)
		case strings.HasPrefix(text, "usage_usec"):
			read(text, &total)
		}
	}
	return user, system, total
}
