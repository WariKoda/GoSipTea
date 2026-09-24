package app

import "testing"

func TestManualRejectIsDistinctFromHangup(t *testing.T) {
	for _, tc := range []struct {
		state CallState
		want  CommandKind
	}{
		{CallIncoming, CommandReject},
		{CallActive, CommandHangup},
		{CallOutgoing, CommandHangup},
	} {
		t.Run(string(tc.state), func(t *testing.T) {
			state := NewState()
			state.CallState = tc.state
			result := Reduce(state, ActionEvent{Type: ActionHangup})
			if result.Err != nil {
				t.Fatal(result.Err)
			}
			if len(result.Commands) != 1 || result.Commands[0].Kind != tc.want {
				t.Fatalf("commands = %#v, want %s", result.Commands, tc.want)
			}
			if result.State.CallState != tc.state {
				t.Fatal("call state changed before CALL_CLOSED")
			}
		})
	}
}
