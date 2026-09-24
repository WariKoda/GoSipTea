package session

import (
	"context"
	"io"
	"reflect"
	"testing"
)

func TestSIPTraceIsOptInAndEnabledBeforeProcessStarts(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		name := "disabled"
		if enabled {
			name = "enabled"
		}
		t.Run(name, func(t *testing.T) {
			h := newHarness(t)
			h.session.config.SIPTrace = enabled
			h.session.config.Log = io.Discard
			ctx := context.Background()
			if err := h.session.Start(ctx); err != nil {
				t.Fatal(err)
			}
			defer h.session.Stop(ctx)

			want := []string{"-f", h.session.config.ConfigDir}
			if enabled {
				want = append(want, "-s")
			}
			options := h.processOptions[0]
			if !reflect.DeepEqual(options.Args, want) {
				t.Fatalf("baresip args = %q, want %q", options.Args, want)
			}
			if options.Stdout == nil || options.Stderr == nil {
				t.Fatal("trace output has no destination")
			}
		})
	}
}
