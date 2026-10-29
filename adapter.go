package xslog

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"runtime"
	"strings"
	"time"
)

// Adapter 将第三方库的日志接口映射到 [slog.Logger]。
// 支持 Print、分级格式化、按行拼接、键值字段、Fatal、Panic 和级别查询。
// 这些方法用于接入不同的日志接口，具体布局、输出目标和级别由底层 Logger 决定。
type Adapter struct {
	// logger 为 nil 时，在调用日志方法时读取全局默认 Logger。
	logger *slog.Logger
}

// NewAdapter 使用给定 Logger 创建适配器，可通过 slog.With 预先附加字段。
// 传入 nil 时，每次写日志都读取当前全局默认 Logger，因此后续 [Setup] 会对适配器生效。
func NewAdapter(logger *slog.Logger) *Adapter {
	return &Adapter{logger: logger}
}

// Print 映射到 Info，参数按 fmt.Sprint 拼接。
func (a *Adapter) Print(args ...any) { a.logPrint(slog.LevelInfo, args...) }

// Printf 映射到 Info，参数按 fmt.Sprintf 格式化。
func (a *Adapter) Printf(format string, args ...any) { a.logf(slog.LevelInfo, format, args...) }

// Println 按 Info 输出 fmt.Sprintln 拼接的消息，并去掉该函数追加的末尾换行。
// 记录末尾的换行由 Handler 自动补齐。
func (a *Adapter) Println(args ...any) { a.logPrintln(slog.LevelInfo, args...) }

// Debugf 映射到 Debug，参数按 fmt.Sprintf 格式化。
func (a *Adapter) Debugf(format string, args ...any) { a.logf(slog.LevelDebug, format, args...) }

// Infof 映射到 Info，参数按 fmt.Sprintf 格式化。
func (a *Adapter) Infof(format string, args ...any) { a.logf(slog.LevelInfo, format, args...) }

// Warnf 映射到 Warn，参数按 fmt.Sprintf 格式化。
func (a *Adapter) Warnf(format string, args ...any) { a.logf(slog.LevelWarn, format, args...) }

// Warningf 按 Warn 级别格式化日志，参数语义与 [Adapter.Warnf] 相同。
func (a *Adapter) Warningf(format string, args ...any) { a.logf(slog.LevelWarn, format, args...) }

// Errorf 映射到 Error，参数按 fmt.Sprintf 格式化。
func (a *Adapter) Errorf(format string, args ...any) { a.logf(slog.LevelError, format, args...) }

// Debug 映射到 Debug，参数按 fmt.Sprint 拼接。
func (a *Adapter) Debug(args ...any) { a.logPrint(slog.LevelDebug, args...) }

// Info 映射到 Info，参数按 fmt.Sprint 拼接。
func (a *Adapter) Info(args ...any) { a.logPrint(slog.LevelInfo, args...) }

// Warn 映射到 Warn，参数按 fmt.Sprint 拼接。
func (a *Adapter) Warn(args ...any) { a.logPrint(slog.LevelWarn, args...) }

// Warning 按 Warn 级别拼接日志，参数语义与 [Adapter.Warn] 相同。
func (a *Adapter) Warning(args ...any) { a.logPrint(slog.LevelWarn, args...) }

// Error 映射到 Error，参数按 fmt.Sprint 拼接。
func (a *Adapter) Error(args ...any) { a.logPrint(slog.LevelError, args...) }

// Debugln 映射到 Debug，参数按 fmt.Sprintln 拼接。
func (a *Adapter) Debugln(args ...any) { a.logPrintln(slog.LevelDebug, args...) }

// Infoln 映射到 Info，参数按 fmt.Sprintln 拼接。
func (a *Adapter) Infoln(args ...any) { a.logPrintln(slog.LevelInfo, args...) }

// Warnln 映射到 Warn，参数按 fmt.Sprintln 拼接。
func (a *Adapter) Warnln(args ...any) { a.logPrintln(slog.LevelWarn, args...) }

// Warningln 按 Warn 级别逐行拼接日志，参数语义与 [Adapter.Warnln] 相同。
func (a *Adapter) Warningln(args ...any) { a.logPrintln(slog.LevelWarn, args...) }

// Errorln 映射到 Error，参数按 fmt.Sprintln 拼接。
func (a *Adapter) Errorln(args ...any) { a.logPrintln(slog.LevelError, args...) }

// Fatal 按 Error 输出 fmt.Sprint 拼接的消息，再调用 os.Exit(1)。
// 即使级别过滤了消息，也会退出进程；os.Exit 不会执行 defer。
func (a *Adapter) Fatal(args ...any) {
	a.logPrint(slog.LevelError, args...)
	os.Exit(1)
}

// Fatalf 同 [Adapter.Fatal]，参数按 fmt.Sprintf 格式化。
func (a *Adapter) Fatalf(format string, args ...any) {
	a.logf(slog.LevelError, format, args...)
	os.Exit(1)
}

// Fatalln 同 [Adapter.Fatal]，参数按 fmt.Sprintln 拼接。
func (a *Adapter) Fatalln(args ...any) {
	a.logPrintln(slog.LevelError, args...)
	os.Exit(1)
}

// Panic 按 Error 输出消息，再以 fmt.Sprint 的结果触发 panic。
func (a *Adapter) Panic(args ...any) {
	msg := fmt.Sprint(args...)
	a.logPrint(slog.LevelError, args...)
	panic(msg)
}

// Panicf 同 [Adapter.Panic]，参数按 fmt.Sprintf 格式化。
func (a *Adapter) Panicf(format string, args ...any) {
	a.logf(slog.LevelError, format, args...)
	panic(fmt.Sprintf(format, args...))
}

// Panicln 同 [Adapter.Panic]，参数按 fmt.Sprintln 拼接。
func (a *Adapter) Panicln(args ...any) {
	msg := strings.TrimSuffix(fmt.Sprintln(args...), "\n")
	a.logPrintln(slog.LevelError, args...)
	panic(msg)
}

// Debugw 按 Debug 级别输出结构化日志，kv 按键和值交替传入。
func (a *Adapter) Debugw(msg string, kv ...any) { a.logw(slog.LevelDebug, msg, kv...) }

// Infow 按 Info 级别输出结构化日志，kv 按键和值交替传入。
func (a *Adapter) Infow(msg string, kv ...any) { a.logw(slog.LevelInfo, msg, kv...) }

// Warnw 按 Warn 级别输出结构化日志，kv 按键和值交替传入。
func (a *Adapter) Warnw(msg string, kv ...any) { a.logw(slog.LevelWarn, msg, kv...) }

// Errorw 按 Error 级别输出结构化日志，kv 按键和值交替传入。
func (a *Adapter) Errorw(msg string, kv ...any) { a.logw(slog.LevelError, msg, kv...) }

// V 查询日志是否启用：l 大于 0 时使用 Debug，其他值使用 Info。
func (a *Adapter) V(l int) bool {
	level := slog.LevelInfo
	if l > 0 {
		level = slog.LevelDebug
	}
	return a.current().Enabled(context.Background(), level)
}

// current 返回绑定的 Logger，未绑定时读取当前全局默认 Logger。
func (a *Adapter) current() *slog.Logger {
	if a.logger != nil {
		return a.logger
	}
	return slog.Default()
}

// adapterSkip 跳过 runtime.Callers、内部日志入口和公开适配方法三层调用帧。
// 内部入口的调用层级改变时需同步调整，确保 Source 指向调用方。
const adapterSkip = 3

// logf 在级别启用时才执行 fmt.Sprintf，并采集调用方位置。
func (a *Adapter) logf(level slog.Level, format string, args ...any) {
	l := a.current()
	if !l.Enabled(context.Background(), level) {
		return
	}
	var pcs [1]uintptr
	runtime.Callers(adapterSkip, pcs[:])
	a.emit(l, level, fmt.Sprintf(format, args...), pcs[0])
}

// logPrint 在级别启用时调用 fmt.Sprint，并采集调用方位置。
func (a *Adapter) logPrint(level slog.Level, args ...any) {
	l := a.current()
	if !l.Enabled(context.Background(), level) {
		return
	}
	var pcs [1]uintptr
	runtime.Callers(adapterSkip, pcs[:])
	a.emit(l, level, fmt.Sprint(args...), pcs[0])
}

// logPrintln 使用 fmt.Sprintln 并去掉其追加的末尾换行。
func (a *Adapter) logPrintln(level slog.Level, args ...any) {
	l := a.current()
	if !l.Enabled(context.Background(), level) {
		return
	}
	var pcs [1]uintptr
	runtime.Callers(adapterSkip, pcs[:])
	a.emit(l, level, strings.TrimSuffix(fmt.Sprintln(args...), "\n"), pcs[0])
}

// logw 通过 slog.Record.Add 添加键值参数，使用标准库的字段校验规则。
func (a *Adapter) logw(level slog.Level, msg string, kv ...any) {
	l := a.current()
	if !l.Enabled(context.Background(), level) {
		return
	}
	var pcs [1]uintptr
	runtime.Callers(adapterSkip, pcs[:])
	a.emit(l, level, msg, pcs[0], kv...)
}

// emit 使用传入的消息、级别和调用位置构建记录，并交给当前 Logger 的 Handler。
func (a *Adapter) emit(l *slog.Logger, level slog.Level, msg string, pc uintptr, kv ...any) {
	r := slog.NewRecord(time.Now(), level, msg, pc)
	r.Add(kv...)
	_ = l.Handler().Handle(context.Background(), r)
}
