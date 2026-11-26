package storage

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLocalAvatarStoreUploadAndOpen(t *testing.T) {
	root := t.TempDir()
	s := NewLocalAvatarStore(filepath.Join(root, "wx-avatars"))
	payload := []byte("\x89PNG\r\n\x1a\nfake-image-bytes")
	key, err := s.Upload(42, bytes.NewReader(payload), "png")
	if err != nil {
		t.Fatalf("Upload: %v", err)
	}
	if !strings.HasPrefix(key, "42/") || !strings.HasSuffix(key, ".png") {
		t.Fatalf("期望 <id>/<name>.png，实际 %q", key)
	}
	f, info, err := s.Open(key)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer f.Close()
	if info.Size() != int64(len(payload)) {
		t.Fatalf("大小不符：期望 %d，实际 %d", len(payload), info.Size())
	}
	got, _ := io.ReadAll(f)
	if !bytes.Equal(got, payload) {
		t.Fatalf("内容不符")
	}
	// 落盘位置必须在 root 下。
	target, err := s.Path(key)
	if err != nil || !strings.HasPrefix(target, filepath.Join(root, "wx-avatars")+string(os.PathSeparator)) {
		t.Fatalf("Path 越界：%s %v", target, err)
	}
}

func TestLocalAvatarStoreRejectsTraversal(t *testing.T) {
	s := NewLocalAvatarStore(t.TempDir())
	for _, key := range []string{"../secret", "/etc/passwd", "42/../../secret", ".."} {
		if _, _, err := s.Open(key); err == nil {
			t.Fatalf("Open(%q) 应拒绝越界路径", key)
		}
		if _, err := s.Path(key); err == nil {
			t.Fatalf("Path(%q) 应拒绝越界路径", key)
		}
	}
}

func TestNewLocalAvatarStoreEmptyRoot(t *testing.T) {
	if NewLocalAvatarStore("") != nil || NewLocalAvatarStore("   ") != nil {
		t.Fatalf("空 root 应返回 nil")
	}
}
