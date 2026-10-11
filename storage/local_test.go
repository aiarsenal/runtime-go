package storage

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func newTempLocal(t *testing.T) *localStorage {
	t.Helper()
	root := filepath.Join(t.TempDir(), "skill")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	return &localStorage{root: root}
}

func TestLocalSaveOpenStatDelete(t *testing.T) {
	s := newTempLocal(t)
	ctx := context.Background()
	key := "skill/sk1/1.0.0/SKILL.md"
	body := []byte("---\nname: foo\ndescription: bar\n---\n# body")

	n, err := s.Save(ctx, key, bytes.NewReader(body))
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if n != int64(len(body)) {
		t.Fatalf("saved bytes %d want %d", n, len(body))
	}

	rc, err := s.Open(ctx, key)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	got, _ := io.ReadAll(rc)
	rc.Close()
	if !bytes.Equal(got, body) {
		t.Fatalf("content mismatch")
	}

	info, err := s.Stat(ctx, key)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Size != int64(len(body)) {
		t.Fatalf("stat size %d want %d", info.Size, len(body))
	}

	// 覆盖写
	if _, err := s.Save(ctx, key, bytes.NewReader([]byte("overwrite"))); err != nil {
		t.Fatalf("overwrite save: %v", err)
	}

	if err := s.Delete(ctx, key); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := s.Stat(ctx, key); !IsNotFound(err) {
		t.Fatalf("after delete want ErrNotFound, got %v", err)
	}
	// 幂等删除
	if err := s.Delete(ctx, key); err != nil {
		t.Fatalf("idempotent delete: %v", err)
	}
}

func TestLocalPathTraversal(t *testing.T) {
	s := newTempLocal(t)
	ctx := context.Background()
	for _, key := range []string{"../escape", "a/../../b", "foo/../bar"} {
		if _, err := s.Save(ctx, key, bytes.NewReader([]byte("x"))); err == nil {
			t.Fatalf("expected rejection for key %q", key)
		}
	}
}

func TestNewLocalViaFactory(t *testing.T) {
	s, err := New(Config{Type: "local", Params: map[string]any{"localRoot": filepath.Join(t.TempDir(), "s")}})
	if err != nil {
		t.Fatalf("New local: %v", err)
	}
	if s.Provider() != "local" {
		t.Fatalf("provider %s want local", s.Provider())
	}
}
