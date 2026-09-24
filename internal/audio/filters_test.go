package audio

import (
	"context"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
)

func TestParseStatusFiltersFixture(t *testing.T) {
	fixture, err := os.Open("testdata/status-filters.txt")
	if err != nil {
		t.Fatal(err)
	}
	defer fixture.Close()

	nodes, err := ParseStatusFilters(fixture)
	if err != nil {
		t.Fatal(err)
	}
	want := []Node{
		{ID: 94, Name: "alsa_input.usb-Yamaha_Corporation_Steinberg_UR22mkII-00.HiFi__Line3__source", Kind: KindSource},
		{ID: 102, Name: "alsa_input.usb-Yamaha_Corporation_Steinberg_UR22mkII-00.HiFi__Line2__source", Kind: KindSource, Default: true},
	}
	if !reflect.DeepEqual(nodes, want) {
		t.Fatalf("ParseStatusFilters() = %#v, want %#v", nodes, want)
	}
}

func TestParseStatusFiltersSectionsAndClasses(t *testing.T) {
	input := `Audio
 ├─ Sources:
 │      48. ordinary-source [Audio/Source]
 ├─ Filters:
 │    - loopback
 │  *   70. filter.sink [Audio/Sink]
 │      71. internal-source [Audio/Source/Internal]
 │      72. internal-sink [Audio/Sink/Internal]
 │      73. capture-stream [Stream/Input/Audio]
 │      74. playback-stream [Stream/Output/Audio]
 └─ Streams:
        75. outside-filters [Audio/Source]
Video
 ├─ Filters:
 │      76. outside-audio [Audio/Source]
Settings
`
	nodes, err := ParseStatusFilters(strings.NewReader(input))
	if err != nil {
		t.Fatal(err)
	}
	want := []Node{{ID: 70, Name: "filter.sink", Kind: KindSink, Default: true}}
	if !reflect.DeepEqual(nodes, want) {
		t.Fatalf("ParseStatusFilters() = %#v, want %#v", nodes, want)
	}
}

func TestParseStatusFiltersWithoutFilters(t *testing.T) {
	for _, input := range []string{"Audio\n └─ Streams:\n", "Audio\n ├─ Filters:\n └─ Streams:\n"} {
		nodes, err := ParseStatusFilters(strings.NewReader(input))
		if err != nil || len(nodes) != 0 {
			t.Fatalf("ParseStatusFilters() = %#v, %v", nodes, err)
		}
	}
}

func TestParseStatusFiltersRejectsMalformedNodes(t *testing.T) {
	for name, input := range map[string]string{
		"missing section":   "unrecognized output",
		"missing separator": "Audio\n ├─ Filters:\n │ 42 source [Audio/Source]\n",
		"invalid ID":        "Audio\n ├─ Filters:\n │ nope. source [Audio/Source]\n",
		"empty name":        "Audio\n ├─ Filters:\n │ 42. [Audio/Source]\n",
		"duplicate ID":      "Audio\n ├─ Filters:\n │ 42. one [Audio/Source]\n │ 42. two [Audio/Source]\n",
		"long line":         "Audio\n ├─ Filters:\n │ 42. " + strings.Repeat("x", maxParserLineBytes) + " [Audio/Source]\n",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseStatusFilters(strings.NewReader(input)); err == nil {
				t.Fatal("ParseStatusFilters() accepted malformed output")
			}
		})
	}
}

func TestListerIncludesSteinbergInputs(t *testing.T) {
	status, err := os.ReadFile("testdata/status-filters.txt")
	if err != nil {
		t.Fatal(err)
	}
	const input1 = "alsa_input.usb-Yamaha_Corporation_Steinberg_UR22mkII-00.HiFi__Line2__source"
	const input2 = "alsa_input.usb-Yamaha_Corporation_Steinberg_UR22mkII-00.HiFi__Line3__source"
	outputs := map[string]string{
		"list audio":  "34\tspeaker\taudio/sink\t*\n99\talsa_input.hw_UR22mkII_0\taudio/source\t \n",
		"status -n":   string(status),
		"inspect 34":  "node.name = \"speaker\"\nmedia.class = \"Audio/Sink\"\n",
		"inspect 99":  "node.name = \"alsa_input.hw_UR22mkII_0\"\nmedia.class = \"Audio/Source/Internal\"\n",
		"inspect 94":  "node.name = \"" + input2 + "\"\nmedia.class = \"Audio/Source\"\nnode.description = \"Steinberg UR22mkII Input 2\"\n",
		"inspect 102": "node.name = \"" + input1 + "\"\nmedia.class = \"Audio/Source\"\nnode.description = \"Steinberg UR22mkII Input 1\"\n",
	}
	lister := NewLister()
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
		{ID: 34, Name: "speaker", Description: "speaker", Kind: KindSink, Default: true},
		{ID: 94, Name: input2, Description: "Steinberg UR22mkII Input 2", Kind: KindSource},
		{ID: 102, Name: input1, Description: "Steinberg UR22mkII Input 1", Kind: KindSource, Default: true},
	}
	if !reflect.DeepEqual(nodes, want) {
		t.Fatalf("List() = %#v, want %#v", nodes, want)
	}
}

func TestListerMergesAndValidatesFilters(t *testing.T) {
	for _, tc := range []struct {
		name      string
		list      string
		status    string
		inspect   string
		maxNodes  int
		wantError string
	}{
		{name: "duplicate", list: "42\tsource\taudio/source\t \n", status: "42. source", inspect: "source"},
		{name: "changed list name", list: "42\told-name\taudio/source\t \n", status: "42. source", wantError: "changed between list and status"},
		{name: "changed list kind", list: "42\tsource\taudio/sink\t \n", status: "42. source", wantError: "changed between list and status"},
		{name: "changed inspect name", status: "42. source", inspect: "renamed", wantError: "changed during discovery"},
		{name: "combined limit", list: "34\tspeaker\taudio/sink\t*\n", status: "42. source", maxNodes: 1, wantError: "limit is 1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			lister := NewLister()
			if tc.maxNodes > 0 {
				lister.maxNodes = tc.maxNodes
			}
			inspections := 0
			lister.run = func(ctx context.Context, _ int, args ...string) ([]byte, error) {
				if _, ok := ctx.Deadline(); !ok {
					t.Fatal("command context has no deadline")
				}
				switch strings.Join(args, " ") {
				case "list audio":
					return []byte(tc.list), nil
				case "status -n":
					return []byte("Audio\n ├─ Filters:\n │ * " + tc.status + " [Audio/Source]\n"), nil
				case "inspect 42":
					inspections++
					return []byte(fmt.Sprintf("node.name = %q\nmedia.class = \"Audio/Source\"\n", tc.inspect)), nil
				default:
					t.Fatalf("unexpected command: %v", args)
					return nil, nil
				}
			}
			nodes, err := lister.List(context.Background())
			if tc.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantError) {
					t.Fatalf("List() error = %v, want %q", err, tc.wantError)
				}
				return
			}
			if err != nil || len(nodes) != 1 || !nodes[0].Default || inspections != 1 {
				t.Fatalf("List() = %#v, %v; inspections = %d", nodes, err, inspections)
			}
		})
	}
}

func TestListerReportsStatusFailure(t *testing.T) {
	failure := errors.New("status failed")
	lister := NewLister()
	lister.run = func(_ context.Context, _ int, args ...string) ([]byte, error) {
		if strings.Join(args, " ") == "list audio" {
			return nil, nil
		}
		return nil, failure
	}
	if _, err := lister.List(context.Background()); !errors.Is(err, failure) {
		t.Fatalf("List() error = %v, want %v", err, failure)
	}
}
