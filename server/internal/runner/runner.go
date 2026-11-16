// Package runner 拉起 Python 一次性构建子进程，并把输出逐行回传。
package runner

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"sync"
)

// Runner 执行外部构建命令；Python 进程结束即退出，不常驻。
type Runner struct {
	cmd  string
	args []string
	dir  string
}

// New 创建 Runner。dir 为构建任务工作目录（默认 builder 包所在目录）。
func New(cmd string, args []string, dir string) *Runner {
	return &Runner{cmd: cmd, args: args, dir: dir}
}

// Run 执行构建任务，onLine 接收 stdout/stderr 每一行。
// extraEnv 为额外注入的环境变量（如 SOURCE_COMMIT）。
func (r *Runner) Run(ctx context.Context, lang, version string, extraEnv map[string]string, onLine func(string)) error {
	args := append(append([]string{}, r.args...), "--lang", lang, "--version", version)
	cmd := exec.CommandContext(ctx, r.cmd, args...)
	cmd.Dir = r.dir
	env := os.Environ()
	for k, v := range extraEnv {
		env = append(env, k+"="+v)
	}
	cmd.Env = env

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("attach stdout: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("attach stderr: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start %s: %w", r.cmd, err)
	}

	var wg sync.WaitGroup
	wg.Add(2)
	consume := func(scanner *bufio.Scanner) {
		defer wg.Done()
		scanner.Buffer(make([]byte, 64*1024), 1024*1024)
		for scanner.Scan() {
			onLine(scanner.Text())
		}
	}
	go consume(bufio.NewScanner(stdout))
	go consume(bufio.NewScanner(stderr))
	wg.Wait()

	if err := cmd.Wait(); err != nil {
		return fmt.Errorf("builder exited: %w", err)
	}
	return nil
}
