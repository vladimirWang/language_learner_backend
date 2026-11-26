package storage

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// randomAvatarName 生成不可猜测的对象名，避免头像 key 被枚举。
func randomAvatarName(ext string) string {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		// 兜底：即使随机数不可用也用时间戳，仅影响可猜测性。
		return fmt.Sprintf("%d.%s", os.Getpid(), ext)
	}
	return hex.EncodeToString(buf) + "." + ext
}

// LocalAvatarStore 在未配置对象存储时把头像写到本地磁盘（容器 WORK_DIR 下），
// 并通过同域 /api/auth/wx-avatar/file 路径回传，避免依赖 OSS/OSS 合法域名。
type LocalAvatarStore struct {
	Root string // 例如 /builder/work/wx-avatars
}

// NewLocalAvatarStore 创建本地头像存储；root 为空时返回 nil。
func NewLocalAvatarStore(root string) *LocalAvatarStore {
	if strings.TrimSpace(root) == "" {
		return nil
	}
	return &LocalAvatarStore{Root: filepath.Clean(root)}
}

// Upload 把头像写入 Root/<userID>/<random>.<ext>，返回相对 key。
func (s *LocalAvatarStore) Upload(userID int64, src io.Reader, ext string) (string, error) {
	if s == nil || userID <= 0 {
		return "", ErrNotConfigured
	}
	name := randomAvatarName(ext)
	dir := filepath.Join(s.Root, fmt.Sprintf("%d", userID))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	target := filepath.Join(dir, name)
	tmp := target + ".tmp"
	out, err := os.Create(tmp)
	if err != nil {
		return "", err
	}
	if _, err := io.Copy(out, src); err != nil {
		out.Close()
		os.Remove(tmp)
		return "", err
	}
	if err := out.Close(); err != nil {
		os.Remove(tmp)
		return "", err
	}
	if err := os.Rename(tmp, target); err != nil {
		os.Remove(tmp)
		return "", err
	}
	return fmt.Sprintf("%d/%s", userID, name), nil
}

// Open 按 key 打开本地头像文件；拒绝越界路径。
func (s *LocalAvatarStore) Open(key string) (*os.File, os.FileInfo, error) {
	if s == nil || strings.TrimSpace(key) == "" {
		return nil, nil, ErrNotConfigured
	}
	clean := filepath.Clean(filepath.FromSlash(key))
	if filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(os.PathSeparator)) {
		return nil, nil, errors.New("invalid avatar key")
	}
	target := filepath.Join(s.Root, clean)
	rootClean := filepath.Clean(s.Root)
	if target != rootClean && !strings.HasPrefix(target, rootClean+string(os.PathSeparator)) {
		return nil, nil, errors.New("avatar key escapes root")
	}
	f, err := os.Open(target)
	if err != nil {
		return nil, nil, err
	}
	info, err := f.Stat()
	if err != nil || info.IsDir() {
		f.Close()
		return nil, nil, errors.New("avatar not found")
	}
	return f, info, nil
}

// Path 返回 key 对应的本地绝对路径（仅用于内部校验与清理）。
func (s *LocalAvatarStore) Path(key string) (string, error) {
	if s == nil || strings.TrimSpace(key) == "" {
		return "", ErrNotConfigured
	}
	clean := filepath.Clean(filepath.FromSlash(key))
	if filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(os.PathSeparator)) {
		return "", errors.New("invalid avatar key")
	}
	target := filepath.Join(s.Root, clean)
	rootClean := filepath.Clean(s.Root)
	if target != rootClean && !strings.HasPrefix(target, rootClean+string(os.PathSeparator)) {
		return "", errors.New("avatar key escapes root")
	}
	return target, nil
}
