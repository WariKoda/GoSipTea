package session

import (
	"context"
	"testing"

	"github.com/nibra/gosiptea/internal/app"
)

func TestRejectSendsGlobalDeclineWithoutChangingOtherHangups(t *testing.T) {
	for _, tc := range []struct {
		name    string
		command app.Command
		want    string
	}{
		{"manual reject", app.Command{Kind: app.CommandReject}, "hangup scode=603 reason=Decline"},
		{"hangup", app.Command{Kind: app.CommandHangup}, "hangup"},
		{"busy rejection", app.Command{Kind: app.CommandHangup, Parameter: "second-call"}, "hangup second-call"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			client := newFakeClient(h.order)
			if err := h.session.executeCommands(client, context.Background(), []app.Command{tc.command}); err != nil {
				t.Fatal(err)
			}
			if len(client.commands) != 1 || client.commands[0] != tc.want {
				t.Fatalf("commands = %v, want %q", client.commands, tc.want)
			}
		})
	}
}
