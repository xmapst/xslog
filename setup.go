package xslog

import (
	"log/slog"
	"sync"
	"sync/atomic"
)

// manager 管理全局 Logger 的共享级别与 Setup 创建的文件，mu 串行化装配和关闭。
type manager struct {
	mu sync.Mutex
	// level 是全局 Logger 共享的动态级别。
	level *slog.LevelVar

	// file 保存 Setup 当前持有的文件；未配置文件输出时为 nil。
	file atomic.Pointer[FileOutput]
}

func init() {
	// 在首次 Setup 前安装使用默认模板、级别和标准输出的 Logger。
	slog.SetDefault(slog.New(std.newHandler((options{}).apply(nil))))
}

// Setup 按选项替换进程的默认 Logger，未传选项时使用 [DefaultTemplate] 和标准输出。
// 文件参数或打开过程失败时返回错误，并保留当前 Logger。
//
// [WithFile] 指定的文件由 Setup 打开、[Close] 关闭；[WithWriter] 指定的 Writer
// 由调用方管理。重复调用时，先安装新 Logger，再关闭先前持有的文件。
// [WithLevel] 仅设置初始级别，运行时通过 [SetLevel] 调整。
func Setup(opts ...Option) error { return std.setup((options{}).apply(opts)) }

// Close 关闭并解除 Setup 对当前日志文件的持有，可重复调用。
// 它不替换当前 Logger，也不关闭通过 [WithWriter] 传入的 Writer。
// 关闭后的文件写入行为由底层轮转器决定，写入错误仍交给 Handler 返回。
func Close() error { return std.close() }

// SetLevel 调整本包管理的共享级别，初始值为 [slog.LevelDebug]。
// 由 [NewHandler] 独立创建的 Handler 使用调用方配置的 Leveler。
func SetLevel(l slog.Level) { std.level.Set(l) }

// setup 成功打开所需文件后安装新的全局 Logger。
func (m *manager) setup(o options) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	// 文件打开成功后才替换 Logger，避免装配失败破坏当前输出。
	var file *FileOutput
	if o.hasFile {
		var err error
		if file, err = OpenFile(o.filePath, o.fileOpts...); err != nil {
			return err
		}
		o.writer = file
	}
	slog.SetDefault(slog.New(m.newHandler(o)))
	// 新 Logger 安装后，再释放管理器持有的旧文件。
	if err := m.swapFile(file); err != nil {
		// 新 Logger 已生效，旧文件的关闭错误记录为告警。
		slog.Warn("close previous log file", slog.Any("error", err))
	}
	return nil
}

// swapFile 替换持有的文件并关闭旧文件，返回关闭旧文件时的错误。
func (m *manager) swapFile(next *FileOutput) error {
	prev := m.file.Swap(next)
	if prev == nil {
		return nil
	}
	return prev.Close()
}

// close 解除当前文件的持有并关闭它，与 setup 使用同一把锁。
func (m *manager) close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.swapFile(nil)
}

// newHandler 使用配置的初始级别更新共享 LevelVar，再创建 Handler。
func (m *manager) newHandler(o options) *Handler {
	m.level.Set(o.level.Level())
	o.level = m.level
	return o.newHandler()
}
