package media

import (
	"reflect"
	"testing"
)

func TestPlayerNamesFiltersSortsAndDeduplicates(t *testing.T) {
	names := []string{
		":1.42",
		"org.freedesktop.DBus",
		"org.mpris.MediaPlayer2.vlc",
		"org.mpris.MediaPlayer2.firefox.instance2",
		"org.mpris.MediaPlayer2.vlc",
	}
	want := []string{
		"org.mpris.MediaPlayer2.firefox.instance2",
		"org.mpris.MediaPlayer2.vlc",
	}
	if got := PlayerNames(names); !reflect.DeepEqual(got, want) {
		t.Fatalf("PlayerNames() = %#v, want %#v", got, want)
	}
}
