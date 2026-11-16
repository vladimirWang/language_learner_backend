//go:build !linux && !darwin

package memutil

// AvailableMB 在未适配的平台返回 0，调用方应按"水位未知"处理。
func AvailableMB() uint64 {
	return 0
}
