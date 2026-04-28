package cron_test

import (
	"testing"

	"github.com/libtnb/cron"
)

func TestMissedFirePolicy_String(t *testing.T) {
	cases := map[cron.MissedFirePolicy]string{
		cron.MissedSkip:            "skip",
		cron.MissedRunOnce:         "run-once",
		cron.MissedFirePolicy(255): "unknown",
	}
	for p, want := range cases {
		if got := p.String(); got != want {
			t.Fatalf("%d: got %q want %q", p, got, want)
		}
	}
}
