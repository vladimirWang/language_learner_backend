package handler

import (
	"context"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wangfernando/language-learner/server/internal/storage"
	"github.com/wangfernando/language-learner/server/internal/store"
)

// 本地头像 view：key 转成同域相对路径，供小程序 getBaseUrl() 拼接。
func TestNewWXUserViewLocalAvatar(t *testing.T) {
	root := t.TempDir()
	la := storage.NewLocalAvatarStore(root)
	h := &Handler{localAvatar: la, logger: log.New(io.Discard, "", 0)}
	key, err := la.Upload(7, strings.NewReader("png-bytes"), "png")
	if err != nil {
		t.Fatalf("upload: %v", err)
	}
	view := h.newWXUserView(context.Background(), store.WXUser{ID: 7, AvatarURL: key}, false)
	want := "/api/auth/wx-avatar/file/" + key
	if view.Avatar != want {
		t.Fatalf("期望 %q，实际 %q", want, view.Avatar)
	}
	// 历史 S3 key 也能在本地模式下回读。
	legacy := "wx-avatars/7/legacy.png"
	laRoot := filepath.Join(root, "7")
	os.MkdirAll(laRoot, 0o755)
	os.WriteFile(filepath.Join(laRoot, "legacy.png"), []byte("x"), 0o644)
	view2 := h.newWXUserView(context.Background(), store.WXUser{ID: 7, AvatarURL: legacy}, false)
	if view2.Avatar != "/api/auth/wx-avatar/file/7/legacy.png" {
		t.Fatalf("历史 key 未被正确转换：%q", view2.Avatar)
	}
	// 不存在的 key 不应下发坏 URL。
	view3 := h.newWXUserView(context.Background(), store.WXUser{ID: 7, AvatarURL: "7/missing.png"}, false)
	if view3.Avatar != "" {
		t.Fatalf("缺失文件应返回空头像，实际 %q", view3.Avatar)
	}
	// 完整 URL 原样返回。
	view4 := h.newWXUserView(context.Background(), store.WXUser{ID: 7, AvatarURL: "https://cdn.example/a.png"}, false)
	if view4.Avatar != "https://cdn.example/a.png" {
		t.Fatalf("外部 URL 被改写：%q", view4.Avatar)
	}
}
