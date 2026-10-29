package xslog

import "runtime"

// versionedSourcePC 模拟运行时提供带模块版本号的路径，验证缩短位置与原始路径。
//
//go:noinline
func versionedSourcePC() uintptr {
	var pcs [1]uintptr
//line /deps/example/module@v1.2.3/file.go:100
	runtime.Callers(1, pcs[:])
	return pcs[0]
}
