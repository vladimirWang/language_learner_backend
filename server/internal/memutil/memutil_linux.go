//go:build linux

package memutil

import (
	"bufio"
	"os"
	"strconv"
	"strings"
)

// AvailableMB 返回 Linux 主机当前可用内存（MB）；读取失败返回 0。
func AvailableMB() uint64 {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return 0
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "MemAvailable:") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			return 0
		}
		kb, err := strconv.ParseUint(fields[1], 10, 64)
		if err != nil {
			return 0
		}
		return kb / 1024
	}
	return 0
}
