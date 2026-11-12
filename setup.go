package xslog

import (
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
)

// manager 管理进程唯一的全局 Handler 与 Setup 创建的文件，mu 串行化装配和关闭。
type manager struct {
	mu sync.Mutex
	// handler 是全局 Handler，Setup 和包级的 Set 函数都作用于它。
	// 经默认 Logger 派生的 Logger 与它共享设置，因此随之更新。
	handler *Handler

	// file 保存 Setup 当前持有的文件；未配置文件输出时为 nil。
	file atomic.Pointer[FileOutput]
}

func init() {
	// 在首次 Setup 前安装使用默认模板、级别和标准输出的 Logger。
	std.handler = NewHandler()
	slog.SetDefault(slog.New(std.handler))
}

// Setup 按选项配置进程的全局 Handler，并将其安装为 [slog] 的默认 Logger，
// 未传选项时使用 [DefaultTemplate]、[slog.LevelDebug] 和标准输出。
// 文件参数或打开过程失败时返回错误，当前配置保持不变。
//
// 全局 Handler 在包初始化时创建，Setup 原地更新它的模板、级别、Writer 和错误回调，
// 此前经默认 Logger 派生的 Logger 随之使用新配置。各项逐一生效，
// 更新期间并发的记录可能混用新旧设置，但每条记录都是完整的。
//
// [WithFile] 指定的文件由 Setup 打开、[Close] 关闭；[WithWriter] 指定的 Writer
// 由调用方管理。重复调用时，先切换到新配置，再关闭先前持有的文件，此时旧文件已不会再被写入。
// [WithLevel] 仅设置初始级别，运行时通过 [SetLevel] 调整。
func Setup(opts ...Option) error { return std.setup((options{}).apply(opts)) }

// Close 关闭并解除 Setup 对当前日志文件的持有，可重复调用。
// 它不替换当前 Logger，也不关闭通过 [WithWriter] 传入的 Writer。
// 关闭后的文件写入行为由底层轮转器决定，写入错误仍交给 Handler 返回。
func Close() error { return std.close() }

// SetLevel 调整全局 Handler 的级别阈值，初始值为 [slog.LevelDebug]，
// 对经默认 Logger 派生的所有 Logger 立即生效。
// 独立创建的 Handler 使用调用方配置的 Leveler，可通过 [Handler.SetLevel] 调整。
func SetLevel(l slog.Level) { std.handler.SetLevel(l) }

// SetWriter 替换全局 Handler 的输出目标，nil 表示标准输出，规则同 [Handler.SetWriter]。
// Setup 通过 [WithFile] 打开的文件不受影响，仍由 [Close] 关闭。
func SetWriter(w io.Writer) { std.handler.SetWriter(w) }

// SetErrorHandler 替换全局 Handler 的错误回调，nil 表示不再通知，规则同 [Handler.SetErrorHandler]。
func SetErrorHandler(fn ErrorHandler) { std.handler.SetErrorHandler(fn) }

// SetTemplate 编译并安装全局 Handler 的输出模板，规则同 [Handler.SetTemplate]。
func SetTemplate(template string) { std.handler.SetTemplate(template) }

// setup 成功打开所需文件后更新全局 Handler，并将其安装为默认 Logger。
func (m *manager) setup(o options) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	// 文件打开成功后才更新设置，避免装配失败破坏当前输出。
	var file *FileOutput
	if o.hasFile {
		var err error
		if file, err = OpenFile(o.filePath, o.fileOpts...); err != nil {
			return err
		}
		o.writer = file
	}
	// Setup 只读取传入 Leveler 的初始级别，不跟随它此后的变化。
	o.level = o.level.Level()
	m.handler.state.configure(o.settings())
	slog.SetDefault(slog.New(m.handler))
	// 新配置生效后，再释放管理器持有的旧文件。
	if err := m.swapFile(file); err != nil {
		// 新配置已生效，旧文件的关闭错误记录为告警。
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
