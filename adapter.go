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

// Adapter 把第三方库自带的那个 Logger 接口接到 slog 上，让库打印的日志和项目其余
// 日志同一个格式、同一个去向、同一个级别开关。
//
// 大多数库用不上它：库只要直接调 log/slog 的包级函数，就已经走在 [Setup] 装上的
// Handler 上了。它服务的是另一类库——自己定义了一个 Logger 接口、只认那个接口的。
//
// # 一个类型，多族接口
//
// Go 的接口是结构化的：库要的那几个方法在就行，多出来的不碍事。所以下面这些形状
// 全挂在同一个 Adapter 上，不必按库分成一堆类型：
//
//   - Print / Printf / Println——标准库 log.Logger 的形状，也是最常见的注入点
//   - Debugf / Infof / Warnf / Errorf——分级 + 格式化
//   - Debug / Info / Warn / Error——分级 + 拼接（logrus、zap 的 SugaredLogger）
//   - Debugln / Infoln / Warnln / Errorln——同上的 Sprintln 版
//   - Warning / Warningf / Warningln——grpclog 用的是 Warning 而不是 Warn，
//     两套名字都给，省得为一个词单独包一层
//   - Fatal / Fatalf / Fatalln——打完退出进程，见下
//   - Panic / Panicf / Panicln——打完 panic
//   - Debugw / Infow / Warnw / Errorw——zap SugaredLogger 的键值风格，
//     参数按 slog 的 k, v, k, v 交替给
//   - V(int) bool——grpclog.LoggerV2 的最后一块
//
// 合起来它直接满足 grpclog.LoggerV2、logrus 那类 StdLogger / FieldLogger 的
// 方法子集，以及一大票库自定义的 "Debugf/Infof/..." 接口。
//
// # 有意没做的两种
//
// logr 那种 Info(msg string, kv ...any) 装不进来：和上面的 Info(args ...any)
// 只有签名不同，一个类型上放不下两个。logr 生态自己有 slog 的桥，走那边。
//
// gorm 那种带 ctx、还要 LogMode 和 Trace(ctx, begin, fc, err) 的接口也没做：
// 它要的不是「把一行字打出去」，而是把 SQL、耗时、影响行数拆成字段——那是一个
// 有自己主张的适配器，塞进这个通用形状里只会两头不像。
type Adapter struct {
	// logger 为 nil 表示「用写这条日志时的全局默认 Logger」，见 [NewAdapter]。
	logger *slog.Logger
}

// NewAdapter 基于给定 Logger 创建适配器，可先用 slog.With(...) 附加固定字段
// （比如模块名），让这个库的日志在一堆日志里认得出来。
//
//	grpclog.SetLoggerV2(xslog.NewAdapter(slog.With("lib", "grpc")))
//
// 传 nil 表示跟着全局默认 Logger 走，且是**每条日志现取**，不是在这里取一次存住。
// 这个区别要紧：注入库的 Logger 通常发生在 main 的最前面，而 [Setup] 要等配置读完
// 才调得上——存一次的话，这中间装的 Handler 会被库一直用到进程结束，配置里写的
// 格式和级别对它全都不作数。
func NewAdapter(logger *slog.Logger) *Adapter {
	return &Adapter{logger: logger}
}

// Print 映射到 Info，参数按 fmt.Sprint 拼接。
func (a *Adapter) Print(args ...any) { a.logPrint(slog.LevelInfo, args...) }

// Printf 映射到 Info，参数按 fmt.Sprintf 格式化。
func (a *Adapter) Printf(format string, args ...any) { a.logf(slog.LevelInfo, format, args...) }

// Println 映射到 Info，参数按 fmt.Sprintln 拼接（尾部换行会去掉，换行由 Handler 管）。
func (a *Adapter) Println(args ...any) { a.logPrintln(slog.LevelInfo, args...) }

// Debugf 映射到 Debug，参数按 fmt.Sprintf 格式化。
func (a *Adapter) Debugf(format string, args ...any) { a.logf(slog.LevelDebug, format, args...) }

// Infof 映射到 Info，参数按 fmt.Sprintf 格式化。
func (a *Adapter) Infof(format string, args ...any) { a.logf(slog.LevelInfo, format, args...) }

// Warnf 映射到 Warn，参数按 fmt.Sprintf 格式化。
func (a *Adapter) Warnf(format string, args ...any) { a.logf(slog.LevelWarn, format, args...) }

// Warningf 是 [Adapter.Warnf] 的另一个名字，给用 Warning 这套叫法的库（如 grpclog）。
func (a *Adapter) Warningf(format string, args ...any) { a.logf(slog.LevelWarn, format, args...) }

// Errorf 映射到 Error，参数按 fmt.Sprintf 格式化。
func (a *Adapter) Errorf(format string, args ...any) { a.logf(slog.LevelError, format, args...) }

// Debug 映射到 Debug，参数按 fmt.Sprint 拼接。
func (a *Adapter) Debug(args ...any) { a.logPrint(slog.LevelDebug, args...) }

// Info 映射到 Info，参数按 fmt.Sprint 拼接。
func (a *Adapter) Info(args ...any) { a.logPrint(slog.LevelInfo, args...) }

// Warn 映射到 Warn，参数按 fmt.Sprint 拼接。
func (a *Adapter) Warn(args ...any) { a.logPrint(slog.LevelWarn, args...) }

// Warning 是 [Adapter.Warn] 的另一个名字，给用 Warning 这套叫法的库（如 grpclog）。
func (a *Adapter) Warning(args ...any) { a.logPrint(slog.LevelWarn, args...) }

// Error 映射到 Error，参数按 fmt.Sprint 拼接。
func (a *Adapter) Error(args ...any) { a.logPrint(slog.LevelError, args...) }

// Debugln 映射到 Debug，参数按 fmt.Sprintln 拼接。
func (a *Adapter) Debugln(args ...any) { a.logPrintln(slog.LevelDebug, args...) }

// Infoln 映射到 Info，参数按 fmt.Sprintln 拼接。
func (a *Adapter) Infoln(args ...any) { a.logPrintln(slog.LevelInfo, args...) }

// Warnln 映射到 Warn，参数按 fmt.Sprintln 拼接。
func (a *Adapter) Warnln(args ...any) { a.logPrintln(slog.LevelWarn, args...) }

// Warningln 是 [Adapter.Warnln] 的另一个名字，给用 Warning 这套叫法的库（如 grpclog）。
func (a *Adapter) Warningln(args ...any) { a.logPrintln(slog.LevelWarn, args...) }

// Errorln 映射到 Error，参数按 fmt.Sprintln 拼接。
func (a *Adapter) Errorln(args ...any) { a.logPrintln(slog.LevelError, args...) }

// Fatal 按 Error 打一条再 os.Exit(1)，参数按 fmt.Sprint 拼接。
//
// 真的退出，而不是只打一条了事：库调 Fatal 是认定「再走下去没意义」才调的，
// 拦下这一步，接在后面的代码就会在一个它自己都宣告不成立的状态上继续跑。
//
// 退出与级别无关。级别把这条日志挡掉了，进程照样退——不然一个日志级别就能改掉
// 程序的控制流，那是比少打一行日志坏得多的事。也因此，[Close] 之类的收尾在这条
// 路径上不会执行，这是 os.Exit 的固有性质，库选了 Fatal 就是选了它。
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

// Panic 按 Error 打一条再 panic，参数按 fmt.Sprint 拼接。
//
// panic 的值就是那条消息：recover 到它的人拿到的是「出了什么事」，
// 而不是一个还要再解一层的包装类型。
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

// Debugw 按 Debug 打一条带字段的日志，kv 按 k, v, k, v 交替给。
func (a *Adapter) Debugw(msg string, kv ...any) { a.logw(slog.LevelDebug, msg, kv...) }

// Infow 按 Info 打一条带字段的日志，kv 按 k, v, k, v 交替给。
func (a *Adapter) Infow(msg string, kv ...any) { a.logw(slog.LevelInfo, msg, kv...) }

// Warnw 按 Warn 打一条带字段的日志，kv 按 k, v, k, v 交替给。
func (a *Adapter) Warnw(msg string, kv ...any) { a.logw(slog.LevelWarn, msg, kv...) }

// Errorw 按 Error 打一条带字段的日志，kv 按 k, v, k, v 交替给。
func (a *Adapter) Errorw(msg string, kv ...any) { a.logw(slog.LevelError, msg, kv...) }

// V 报告 verbosity 为 l 的日志是否该打，满足 grpclog.LoggerV2 的最后一个方法。
//
// grpc 那边 l 越大越啰嗦，而 slog 只有固定的几档，中间没有一一对应的换算：
// 这里把 0 当 Info、更大的当 Debug——库拿它当「要不要费劲拼这条日志」的开关，
// 分到这两档已经够用，硬凑一个精确映射反而要凭空规定「l=3 是哪一级」。
func (a *Adapter) V(l int) bool {
	level := slog.LevelInfo
	if l > 0 {
		level = slog.LevelDebug
	}
	return a.current().Enabled(context.Background(), level)
}

// current 取这条日志该用的 Logger。见 [NewAdapter] 里为什么是现取而不是存住。
func (a *Adapter) current() *slog.Logger {
	if a.logger != nil {
		return a.logger
	}
	return slog.Default()
}

// 下面四个是仅有的四个内部入口，公开方法一律直接调它们、中间不再夹一层：
// 调用位置靠 runtime.Callers 数帧数出来，多一层少一层，日志里的 @source 就指到
// 这个文件上，而它恰恰是最没用的一个位置——要看的是库里哪一行打了这条。
//
// 三个数字（0=runtime.Callers、1=这四个之一、2=公开方法）之后是真正的调用方，
// 所以 skip 是 3。改动这里的调用层级，这个 3 要跟着改。
const adapterSkip = 3

// logf 走 fmt.Sprintf。先问 Enabled 再格式化：被级别挡掉的那些，连拼字符串的钱
// 都不该花——库里 Debugf 打在热路径上是常事。
func (a *Adapter) logf(level slog.Level, format string, args ...any) {
	l := a.current()
	if !l.Enabled(context.Background(), level) {
		return
	}
	var pcs [1]uintptr
	runtime.Callers(adapterSkip, pcs[:])
	a.emit(l, level, fmt.Sprintf(format, args...), pcs[0])
}

// logPrint 走 fmt.Sprint。
func (a *Adapter) logPrint(level slog.Level, args ...any) {
	l := a.current()
	if !l.Enabled(context.Background(), level) {
		return
	}
	var pcs [1]uintptr
	runtime.Callers(adapterSkip, pcs[:])
	a.emit(l, level, fmt.Sprint(args...), pcs[0])
}

// logPrintln 走 fmt.Sprintln，并去掉它加在尾巴上的换行——换行是 Handler 的事，
// 留着的话，一条日志会在输出里变成带一个空行的两行。
func (a *Adapter) logPrintln(level slog.Level, args ...any) {
	l := a.current()
	if !l.Enabled(context.Background(), level) {
		return
	}
	var pcs [1]uintptr
	runtime.Callers(adapterSkip, pcs[:])
	a.emit(l, level, strings.TrimSuffix(fmt.Sprintln(args...), "\n"), pcs[0])
}

// logw 把 k, v, k, v 交给 slog.Record.Add，键值的合法性由它按 slog 自己的规矩判，
// 这里不另立一套——多一套规矩，同一个写错的键在两个地方就有两种表现。
func (a *Adapter) logw(level slog.Level, msg string, kv ...any) {
	l := a.current()
	if !l.Enabled(context.Background(), level) {
		return
	}
	var pcs [1]uintptr
	runtime.Callers(adapterSkip, pcs[:])
	a.emit(l, level, msg, pcs[0], kv...)
}

// emit 组装记录并交给 Handler。它不数帧，pc 由上面四个入口各自取好传进来。
func (a *Adapter) emit(l *slog.Logger, level slog.Level, msg string, pc uintptr, kv ...any) {
	r := slog.NewRecord(time.Now(), level, msg, pc)
	r.Add(kv...)
	_ = l.Handler().Handle(context.Background(), r)
}
