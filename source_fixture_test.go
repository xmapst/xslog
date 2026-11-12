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

// gate 为 README 的调用信息示例提供固定位置：/srv/app/internal/gate/gate.go 第 100 行的 (*gate).serve。
// 每个 //line 指令只影响其后的行，所以这里重新指定位置。
type gate struct{}

//go:noinline
func (*gate) serve() uintptr {
	var pcs [1]uintptr
//line /srv/app/internal/gate/gate.go:100
	runtime.Callers(1, pcs[:])
	return pcs[0]
}
