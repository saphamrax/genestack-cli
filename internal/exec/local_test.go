package exec

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestNewSelectsExecutor(t *testing.T) {
	if _, ok := New(Config{Local: true}).(*LocalClient); !ok {
		t.Error("Local config should yield *LocalClient")
	}
	if _, ok := New(Config{Host: "h"}).(*Client); !ok {
		t.Error("non-local config should yield *Client (SSH)")
	}
}

func TestLocalRunStream(t *testing.T) {
	l := NewLocal()
	if err := l.Connect(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !l.Connected() {
		t.Fatal("expected Connected() true after Connect")
	}

	out := make(chan string, 16)
	err := l.RunStream(context.Background(), "echo hello; echo oops 1>&2", out)
	if err != nil {
		t.Fatalf("RunStream: %v", err)
	}
	close(out)

	got := map[string]bool{}
	for s := range out {
		got[s] = true
	}
	if !got["hello"] || !got["oops"] {
		t.Errorf("expected stdout+stderr lines, got %v", got)
	}
}

func TestLocalRunStreamError(t *testing.T) {
	l := NewLocal()
	_ = l.Connect(context.Background())
	out := make(chan string, 4)
	if err := l.RunStream(context.Background(), "exit 3", out); err == nil {
		t.Error("expected non-nil error for non-zero exit")
	}
	close(out)
}

func TestLocalUpload(t *testing.T) {
	l := NewLocal()
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "inventory.yaml")
	if err := l.Upload(context.Background(), []byte("data\n"), path); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "data\n" {
		t.Errorf("got %q", string(b))
	}
}
