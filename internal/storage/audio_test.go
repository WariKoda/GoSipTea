package storage_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nibra/gosiptea/internal/storage"
)

func writeAudioTestConfig(t *testing.T, content string) (*storage.Store, string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "baresip")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "config")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return storage.New(dir), path
}

func TestReadAudioConfigSeparatesRingtoneFromOutput(t *testing.T) {
	tests := []struct {
		name   string
		config string
		want   storage.AudioConfig
	}{
		{
			name:   "separate alert device",
			config: "audio_player pipewire,headset\naudio_source pipewire,mic\naudio_alert pipewire,hdmi\n",
			want:   storage.AudioConfig{Output: "headset", Input: "mic", Alert: "hdmi"},
		},
		{
			name:   "alert follows output",
			config: "audio_player pipewire,headset\naudio_alert pipewire,headset\n",
			want:   storage.AudioConfig{Output: "headset"},
		},
		{
			name:   "no alert line",
			config: "audio_player pipewire,headset\n",
			want:   storage.AudioConfig{Output: "headset"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store, _ := writeAudioTestConfig(t, test.config)
			got, err := store.ReadAudioConfig()
			if err != nil {
				t.Fatalf("ReadAudioConfig() error = %v", err)
			}
			if got != test.want {
				t.Fatalf("ReadAudioConfig() = %#v, want %#v", got, test.want)
			}
		})
	}
}

func TestWriteAudioConfigKeepsRingtoneSeparate(t *testing.T) {
	store, path := writeAudioTestConfig(t, "audio_player pipewire,old\naudio_alert pipewire,old\naudio_source pipewire,mic\n")

	want := storage.AudioConfig{Output: "headset", Input: "mic", Alert: "hdmi"}
	if err := store.WriteAudioConfig(want); err != nil {
		t.Fatalf("WriteAudioConfig() error = %v", err)
	}
	got := readFile(t, path)
	for _, line := range []string{
		"audio_player            pipewire,headset",
		"audio_alert             pipewire,hdmi",
		"audio_source            pipewire,mic",
	} {
		if !strings.Contains(got, line) {
			t.Errorf("config does not contain %q:\n%s", line, got)
		}
	}
	if read, err := store.ReadAudioConfig(); err != nil || read != want {
		t.Fatalf("ReadAudioConfig() = %#v, %v, want %#v", read, err, want)
	}

	if err := store.WriteAudioConfig(storage.AudioConfig{Output: "headset", Input: "mic"}); err != nil {
		t.Fatalf("WriteAudioConfig() without alert error = %v", err)
	}
	if got := readFile(t, path); !strings.Contains(got, "audio_alert             pipewire,headset") {
		t.Errorf("empty Alert does not follow the output:\n%s", got)
	}
}

func TestWriteAudioConfigRejectsInvalidRingtone(t *testing.T) {
	store, _ := writeAudioTestConfig(t, "audio_player pipewire\n")
	if err := store.WriteAudioConfig(storage.AudioConfig{Alert: "node with space"}); !errors.Is(err, storage.ErrInvalid) {
		t.Fatalf("WriteAudioConfig() error = %v, want ErrInvalid", err)
	}
}
