package audio

import (
	"bytes"
	"context"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
)

func TestParseListFixture(t *testing.T) {
	fixture, err := os.Open("testdata/list-audio.txt")
	if err != nil {
		t.Fatal(err)
	}
	defer fixture.Close()

	nodes, err := ParseList(fixture)
	if err != nil {
		t.Fatal(err)
	}

	want := []Node{
		{ID: 34, Name: "alsa_output.usb-Headset-00.analog-stereo", Default: true, Kind: KindSink},
		{ID: 45, Name: "alsa_output.pci-0000_07_00.1.hdmi-stereo", Kind: KindSink},
		{ID: 48, Name: "alsa_input.usb-Headset-00.mono-fallback", Default: true, Kind: KindSource},
		{ID: 61, Name: "alsa_input.usb-Camera-02.analog-stereo", Kind: KindSource},
	}
	if !reflect.DeepEqual(nodes, want) {
		t.Fatalf("ParseList() = %#v, want %#v", nodes, want)
	}
}

func TestParseInspectFixture(t *testing.T) {
	fixture, err := os.Open("testdata/inspect-sink.txt")
	if err != nil {
		t.Fatal(err)
	}
	defer fixture.Close()

	properties, err := ParseInspect(fixture)
	if err != nil {
		t.Fatal(err)
	}

	want := InspectProperties{
		Name:        "alsa_output.usb-Headset-00.analog-stereo",
		Description: "Headset Analog Stereo",
		Nick:        "Headset",
		Kind:        KindSink,
	}
	if properties != want {
		t.Fatalf("ParseInspect() = %#v, want %#v", properties, want)
	}
}

func TestParseInspectInternalNodes(t *testing.T) {
	for _, tc := range []struct {
		mediaClass string
		kind       Kind
	}{
		{mediaClass: "Audio/Source/Internal", kind: KindSource},
		{mediaClass: "Audio/Sink/Internal", kind: KindSink},
	} {
		t.Run(tc.mediaClass, func(t *testing.T) {
			input := "node.name = \"internal-node\"\nmedia.class = \"" + tc.mediaClass + "\"\n"
			properties, err := ParseInspect(strings.NewReader(input))
			if err != nil {
				t.Fatal(err)
			}
			want := InspectProperties{Name: "internal-node", Kind: tc.kind, Internal: true}
			if properties != want {
				t.Fatalf("ParseInspect() = %#v, want %#v", properties, want)
			}
		})
	}
}

func TestParseInspectRejectsUnsupportedMediaClass(t *testing.T) {
	_, err := ParseInspect(strings.NewReader("media.class = \"Video/Source\"\n"))
	if err == nil || !strings.Contains(err.Error(), "unsupported media.class") {
		t.Fatalf("ParseInspect() error = %v", err)
	}
}

func TestParseListRejectsMalformedAudioNode(t *testing.T) {
	_, err := ParseList(strings.NewReader("34\tname\taudio/sink\t*\textra\n"))
	if err == nil {
		t.Fatal("ParseList() accepted an extra field")
	}
}

func TestParseListBoundsLineLength(t *testing.T) {
	line := "34\t" + strings.Repeat("x", maxParserLineBytes) + "\taudio/sink\t*\n"
	_, err := ParseList(strings.NewReader(line))
	if err == nil {
		t.Fatal("ParseList() accepted an overlong line")
	}
}

func TestListerUsesListStatusAndInspectArgv(t *testing.T) {
	inspectFixture, err := os.ReadFile("testdata/inspect-sink.txt")
	if err != nil {
		t.Fatal(err)
	}

	var calls [][]string
	lister := NewLister()
	lister.run = func(_ context.Context, limit int, args ...string) ([]byte, error) {
		call := append([]string{"wpctl"}, args...)
		calls = append(calls, call)
		if limit != DefaultMaxOutputBytes {
			t.Fatalf("output limit = %d, want %d", limit, DefaultMaxOutputBytes)
		}
		if reflect.DeepEqual(call, []string{"wpctl", "list", "audio"}) {
			return []byte("34\talsa_output.usb-Headset-00.analog-stereo\taudio/sink\t*\n"), nil
		}
		if reflect.DeepEqual(call, []string{"wpctl", "status", "-n"}) {
			return []byte("Audio\n ├─ Filters:\n └─ Streams:\n"), nil
		}
		if reflect.DeepEqual(call, []string{"wpctl", "inspect", "34"}) {
			return inspectFixture, nil
		}
		return nil, errors.New("unexpected command")
	}

	nodes, err := lister.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 1 || nodes[0].Description != "Headset Analog Stereo" {
		t.Fatalf("List() = %#v", nodes)
	}

	wantCalls := [][]string{
		{"wpctl", "list", "audio"},
		{"wpctl", "status", "-n"},
		{"wpctl", "inspect", "34"},
	}
	if !reflect.DeepEqual(calls, wantCalls) {
		t.Fatalf("calls = %#v, want %#v", calls, wantCalls)
	}
}

func TestListerSkipsInternalNodes(t *testing.T) {
	lister := NewLister()
	outputs := map[string]string{
		"list audio": "99\tinternal-source\taudio/source\t \n" +
			"34\tspeaker\taudio/sink\t*\n" +
			"100\tinternal-sink\taudio/sink\t \n" +
			"48\tmicrophone\taudio/source\t*\n",
		"status -n":   "Audio\n ├─ Filters:\n └─ Streams:\n",
		"inspect 99":  "node.name = \"internal-source\"\nmedia.class = \"Audio/Source/Internal\"\n",
		"inspect 34":  "node.name = \"speaker\"\nmedia.class = \"Audio/Sink\"\n",
		"inspect 100": "node.name = \"internal-sink\"\nmedia.class = \"Audio/Sink/Internal\"\n",
		"inspect 48":  "node.name = \"microphone\"\nmedia.class = \"Audio/Source\"\n",
	}
	lister.run = func(_ context.Context, _ int, args ...string) ([]byte, error) {
		output, ok := outputs[strings.Join(args, " ")]
		if !ok {
			t.Fatalf("unexpected command: %v", args)
		}
		return []byte(output), nil
	}

	nodes, err := lister.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := []Node{
		{ID: 34, Name: "speaker", Description: "speaker", Default: true, Kind: KindSink},
		{ID: 48, Name: "microphone", Description: "microphone", Default: true, Kind: KindSource},
	}
	if !reflect.DeepEqual(nodes, want) {
		t.Fatalf("List() = %#v, want %#v", nodes, want)
	}
}

func TestListerRejectsTooManyNodes(t *testing.T) {
	lister := NewLister()
	lister.maxNodes = 1
	lister.run = func(_ context.Context, _ int, _ ...string) ([]byte, error) {
		return []byte("1\tone\taudio/sink\t*\n2\ttwo\taudio/source\t*\n"), nil
	}

	_, err := lister.List(context.Background())
	if err == nil || !strings.Contains(err.Error(), "limit is 1") {
		t.Fatalf("List() error = %v", err)
	}
}

func TestBoundedBuffer(t *testing.T) {
	buffer := newBoundedBuffer(4)
	n, err := buffer.Write([]byte("abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	if n != 6 {
		t.Fatalf("Write() = %d, want 6", n)
	}
	if got := buffer.String(); got != "abcd" {
		t.Fatalf("buffer = %q, want %q", got, "abcd")
	}
	if !buffer.overflow {
		t.Fatal("overflow = false, want true")
	}

	if !bytes.Equal(buffer.Bytes(), []byte("abcd")) {
		t.Fatalf("Bytes() = %q", buffer.Bytes())
	}
}
