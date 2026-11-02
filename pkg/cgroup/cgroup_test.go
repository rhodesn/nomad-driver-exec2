// Copyright IBM Corp. 2026
// SPDX-License-Identifier: MPL-2.0

package cgroup

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/shoenig/test/must"
)

func TestGroupOpen(t *testing.T) {
	fd, close, err := New(t.TempDir()).Open()
	must.NoError(t, err)
	must.True(t, fd >= 0)
	close()
}

func TestGroupConfigure(t *testing.T) {
	testCases := []struct {
		name   string
		limits Limits
		want   map[string]string
	}{
		{
			name: "memory max disabled",
			limits: Limits{
				CPUQuota: 200000,
				Memory:   1024,
			},
			want: map[string]string{
				"cpu.max":    "200000 100000",
				"memory.max": "1024",
			},
		},
		{
			name: "memory max enabled",
			limits: Limits{
				CPUQuota:  200000,
				Memory:    1024,
				MemoryMax: 2048,
			},
			want: map[string]string{
				"cpu.max":    "200000 100000",
				"memory.low": "1024",
				"memory.max": "2048",
			},
		},
		{
			name: "memory max equals memory limit",
			limits: Limits{
				CPUQuota:  200000,
				Memory:    1024,
				MemoryMax: 1024,
			},
			want: map[string]string{
				"cpu.max":    "200000 100000",
				"memory.low": "1024",
				"memory.max": "1024",
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			for name := range tc.want {
				must.NoError(t, os.WriteFile(filepath.Join(dir, name), nil, 0o600))
			}

			must.NoError(t, New(dir).Configure(tc.limits))
			for name, want := range tc.want {
				got, err := os.ReadFile(filepath.Join(dir, name))
				must.NoError(t, err)
				must.Eq(t, want, string(got))
			}
		})
	}
}

func TestGroupConfigureInvalidLimits(t *testing.T) {
	testCases := []struct {
		name   string
		limits Limits
	}{
		{
			name: "zero CPU quota",
			limits: Limits{
				Memory: 1024,
			},
		},
		{
			name: "zero memory limit",
			limits: Limits{
				CPUQuota: 200000,
			},
		},
		{
			name: "maximum memory below memory limit",
			limits: Limits{
				CPUQuota:  200000,
				Memory:    2048,
				MemoryMax: 1024,
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			files := map[string]string{
				"cpu.max":    "unchanged",
				"memory.low": "unchanged",
				"memory.max": "unchanged",
			}
			for name, value := range files {
				must.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(value), 0o600))
			}

			must.Error(t, New(dir).Configure(tc.limits))
			for name, want := range files {
				got, err := os.ReadFile(filepath.Join(dir, name))
				must.NoError(t, err)
				must.Eq(t, want, string(got))
			}
		})
	}
}

func TestGroupKill(t *testing.T) {
	dir := t.TempDir()
	must.NoError(t, os.WriteFile(filepath.Join(dir, "cgroup.kill"), nil, 0o600))

	must.NoError(t, New(dir).Kill())

	got, err := os.ReadFile(filepath.Join(dir, "cgroup.kill"))
	must.NoError(t, err)
	must.Eq(t, "1", string(got))
}

func TestGroupCurrentPIDs(t *testing.T) {
	testCases := []struct {
		name  string
		value string
		want  int
	}{
		{name: "count", value: "4\n", want: 4},
		{name: "empty", value: "", want: 0},
		{name: "invalid", value: "none", want: 0},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			must.NoError(t, os.WriteFile(filepath.Join(dir, "pids.current"), []byte(tc.value), 0o600))

			got, err := New(dir).CurrentPIDs()
			must.NoError(t, err)
			must.Eq(t, tc.want, got)
		})
	}
}

func TestGroupStats(t *testing.T) {
	dir := t.TempDir()
	files := map[string]string{
		"memory.current":      "1024\n",
		"memory.swap.current": "512\n",
		"memory.stat":         "file 128\nanon 256\n",
		"cpu.stat":            "user_usec 4\nsystem_usec 5\nusage_usec 9\n",
	}
	for name, value := range files {
		must.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(value), 0o600))
	}

	stats, err := New(dir).Stats()
	must.NoError(t, err)
	must.Eq(t, uint64(1024), stats.Memory.Current)
	must.Eq(t, uint64(512), stats.Memory.Swap)
	must.Eq(t, uint64(128), stats.Memory.Cache)
	must.Eq(t, uint64(256), stats.Memory.RSS)
	must.Eq(t, 4, stats.CPU.User)
	must.Eq(t, 5, stats.CPU.System)
	must.Eq(t, 9, stats.CPU.Total)
}
