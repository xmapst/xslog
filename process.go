package xslog

import "os"

// processInfo 保存 Handler 创建时读取的进程信息，派生 Handler 复用同一份快照。
type processInfo struct {
	pid        int
	hostname   string
	executable string
}

// load 读取进程信息；读取失败的字段保持为空，由占位符回退规则处理。
func (p processInfo) load() processInfo {
	p = processInfo{pid: os.Getpid()}
	if hostname, err := os.Hostname(); err == nil {
		p.hostname = hostname
	}
	if executable, err := os.Executable(); err == nil {
		p.executable = executable
	}
	return p
}
