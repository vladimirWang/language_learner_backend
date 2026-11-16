//go:build darwin

package memutil

import "syscall"

// AvailableMB 返回 macOS 当前空闲 + 可回收内存（MB）；读取失败返回 0。
// 仅用于本地开发时校验水位，生产环境为 Linux。
func AvailableMB() uint64 {
	pageSize := sysctlUint("hw.pagesize")
	free := sysctlUint("vm.page_free_count")
	speculative := sysctlUint("vm.page_speculative_count")
	if pageSize == 0 || free == 0 {
		return 0
	}
	return (free + speculative) * pageSize / 1024 / 1024
}

func sysctlUint(key string) uint64 {
	value, err := syscall.SysctlUint32(key)
	if err != nil {
		return 0
	}
	return uint64(value)
}
