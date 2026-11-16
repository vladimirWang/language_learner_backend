// Package repo 提供素材仓库的远程状态查询（最新 commit）。
package repo

import (
	"context"
	"os/exec"
	"strings"
	"time"
)

// Info 表示一个语种仓库的远程状态。
type Info struct {
	Lang       string `json:"lang"`
	URL        string `json:"url"`
	Commit     string `json:"commit"`      // 远程最新 commit hash（短 hash）
	CommitFull string `json:"commit_full"` // 完整 commit hash
	Error      string `json:"error,omitempty"`
}

// LatestCommit 通过 git ls-remote 查询仓库 HEAD 的最新 commit hash。
// 不依赖本地仓库，直接查询远程；超时 10 秒。
func LatestCommit(ctx context.Context, url string) (string, error) {
	c, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(c, "git", "ls-remote", url, "HEAD")
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	// 输出格式：<hash>\tHEAD
	line := strings.TrimSpace(string(out))
	if line == "" {
		return "", nil
	}
	parts := strings.Fields(line)
	if len(parts) == 0 {
		return "", nil
	}
	return parts[0], nil
}

// ShortHash 返回 commit hash 的前 12 位。
func ShortHash(full string) string {
	if len(full) > 12 {
		return full[:12]
	}
	return full
}
