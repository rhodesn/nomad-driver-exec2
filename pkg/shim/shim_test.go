// Copyright IBM Corp. 2024, 2026
// SPDX-License-Identifier: MPL-2.0

package shim

import (
	"slices"
	"testing"

	"github.com/shoenig/test/must"
)

func Test_parameters(t *testing.T) {
	const uid, gid = 1000, 1000

	// setprivSegment returns the setpriv invocation (up to and including its
	// trailing "--") from a generated argument list.
	setprivSegment := func(args []string) []string {
		i := slices.Index(args, "setpriv")
		must.True(t, i >= 0, must.Sprint("setpriv not found in", args))
		end := i + slices.Index(args[i:], "--")
		return args[i : end+1]
	}

	cases := []struct {
		name    string
		env     *Environment
		opts    *Options
		useNS   bool     // expect nsenter to lead the chain
		setpriv []string // expected setpriv segment
	}{
		{
			name:  "no caps no net",
			env:   &Environment{Net: ""},
			opts:  &Options{Command: "env"},
			useNS: false,
			setpriv: []string{
				"setpriv", "--reuid=1000", "--regid=1000", "--clear-groups",
				"--no-new-privs", "--bounding-set=-all", "--",
			},
		},
		{
			name: "caps with net",
			env:  &Environment{Net: "/var/run/netns/abc"},
			opts: &Options{
				Command: "nc",
				CapAdd:  []string{"net_bind_service", "sys_time"},
			},
			useNS: true,
			setpriv: []string{
				"setpriv", "--reuid=1000", "--regid=1000", "--clear-groups",
				"--no-new-privs",
				"--inh-caps=+net_bind_service,+sys_time",
				"--ambient-caps=+net_bind_service,+sys_time",
				"--bounding-set=-all,+net_bind_service,+sys_time",
				"--",
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			args := (&exe{env: tc.env, opts: tc.opts}).parameters(uid, gid)

			// nsenter only leads the chain when a network namespace is set
			must.Eq(t, tc.useNS, args[0] == "nsenter")

			// the uid/gid switch moved from unshare to setpriv
			must.False(t, slices.Contains(args, "--setuid=1000"))
			must.Eq(t, tc.setpriv, setprivSegment(args))
		})
	}
}
