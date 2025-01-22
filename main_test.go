package main

import (
	"errors"
	"net/netip"
	"testing"
	"time"
)

func TestEnvJoin(t *testing.T) {
	e := newEnv()

	a1 := netip.MustParseAddr("127.0.0.1")
	a12 := netip.MustParseAddr("127.0.0.1")
	a2 := netip.MustParseAddr("127.0.0.2")

	for range 2 {
		if !e.join(a1) {
			t.Fatal("Wants: true")
		}
	}
	if !e.join(a12) {
		t.Fatal("Wants: true")
	}

	if e.join(a2) {
		t.Fatal("Wants: false")
	}

	for range 2 {
		e.leave(a12) // swap a1 / a12 ... same value
	}

	e.leave(a2) // must no effect

	if e.join(a2) {
		t.Fatal("Wants: false")
	}

	e.leave(a1)

	// Must empty

	e.leave(a1) // DO NOT Panic

	if !e.join(a2) {
		t.Fatal("Wants: true")
	}
}

func TestWhoUsesWithCommand(t *testing.T) {
	tests := []struct {
		name      string
		cmdOutput *tailscaleWhoisResult
		cmdError  error
		wants     *who
		wantsErr  bool
	}{
		{
			cmdOutput: &tailscaleWhoisResult{
				Node: tailscaleWhoisResultNode{
					ComputedName: "the-computer",
				},
				UserProfile: tailscaleWhoisResultUserProfile{
					DisplayName: "bob",
				},
			},
			wants: &who{
				Uses: &uses{
					Name:     "bob",
					Computer: "the-computer",
					Since:    100,
				},
			},
		},
		{
			cmdError: errors.New("Some error"),
			wantsErr: true,
		},
		{
			cmdOutput: nil,
			cmdError:  nil,
			wants: &who{
				Uses: &uses{
					Name:     "127.0.0.1",
					Computer: "127.0.0.1",
					Since:    100,
				},
			},
		},
	}

	e := newEnv()
	e.now = func() time.Time {
		return time.Date(1970, time.January, 1, 0, 0, 0, 100_000_000, time.UTC)
	}
	if !e.join(netip.MustParseAddr("127.0.0.1")) {
		t.Fatal()
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result, err := whoUsesWithCommand(e, func(ip string) (*tailscaleWhoisResult, error) {
				return test.cmdOutput, test.cmdError
			})

			if test.wantsErr {
				if err == nil {
					t.Fatal()
				}
				return
			}

			if result == nil {
				t.Fatal()
			}
			if (test.wants.Uses == nil) != (result.Uses == nil) {
				t.Fatal()
			}
			if test.wants.Uses == nil {
				return
			}
			if *test.wants.Uses != *result.Uses {
				t.Fatal()
			}
		})
	}
}
