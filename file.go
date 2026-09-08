package xslog

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/DeRuina/timberjack"
)

// 历史文件的压缩方式，对应配置项 Log.Compression。留空等同 CompressionNone。
const (
	CompressionNone = "none"
	CompressionGzip = "gzip"
	CompressionZstd = "zstd"
)

// 落文件时各项轮转参数的默认值。
//
// 默认值只在这里定义一份，配置层引用这些常量而不是抄一遍数字：抄一遍就有了两个
// 真相，改了一处忘了另一处不会报错，只会让实际行为和文档对不上。
const (
	DefaultMaxSizeMB     = 50
	DefaultMaxBackups    = 7
	DefaultMaxAgeDays    = 7
	DefaultRotationHours = 24
)

// FileOption 调整日志文件的轮转行为，交给 [OpenFile] 或 [WithFile]——两条落盘路
// 共用这一套，参数的含义和默认值完全一样。
//
// 与 [Option] 分开两个类型，是为了让「这一项调的是渲染还是落盘」在类型上就分得清：
// 混成一个之后，把 WithMaxSizeMB 传给 NewConsoleHandler 会编译通过而毫无作用，
// 那种错不报错、只是不生效。
//
// 这里也是「没填」与「填了零」必须区别对待的地方：[WithRotationHours] 不写取默认的
// 24 小时，显式给 0 则是关掉按时间轮转。选项只在被调用时才写入，两者因此分得开——
// 换成配置结构体就分不开了，「只想按大小轮转」会变成没有写法能表达的一种部署。
type FileOption func(*fileOptions)

// fileOptions 是文件选项应用后的结果。
type fileOptions struct {
	maxSizeMB     int
	maxBackups    int
	maxAgeDays    int
	rotationHours int
	compression   string
}

// newFileOptions 按顺序应用选项，后面的覆盖前面的；没被覆盖的留在默认值上。
func (f *FileOutput) newFileOptions(opts []FileOption) fileOptions {
	o := fileOptions{
		maxSizeMB:     DefaultMaxSizeMB,
		maxBackups:    DefaultMaxBackups,
		maxAgeDays:    DefaultMaxAgeDays,
		rotationHours: DefaultRotationHours,
		compression:   CompressionNone,
	}
	for _, opt := range opts {
		if opt != nil {
			opt(&o)
		}
	}
	return o
}

// WithMaxSizeMB 设置单个日志文件的大小上限（MB），超过即轮转；
// 不设即 [DefaultMaxSizeMB]。
func WithMaxSizeMB(mb int) FileOption {
	return func(o *fileOptions) {
		if mb < 0 {
			return
		}
		o.maxSizeMB = mb
	}
}

// WithMaxBackups 设置最多保留多少个历史日志文件；不设即 [DefaultMaxBackups]。
func WithMaxBackups(n int) FileOption {
	return func(o *fileOptions) {
		o.maxBackups = n
	}
}

// WithMaxAgeDays 设置历史日志文件最长保留天数；不设即 [DefaultMaxAgeDays]。
func WithMaxAgeDays(days int) FileOption {
	return func(o *fileOptions) {
		o.maxAgeDays = days
	}
}

// WithRotationHours 设置两次轮转之间的最大间隔（小时），不设即 [DefaultRotationHours]，
// **给 0 表示关掉按时间轮转**、只按大小轮转。
//
// 「不设」与「显式给 0」是两件事，这正是选项相对配置结构体的好处：结构体的零值分不出
// 这两者，只能约定「零值即默认」，于是「只想按大小轮转」就没有任何写法能表达；
// 选项只在被调用时才写入，0 因此是一个真正说得出口的取值。
//
// 负数没有意义，按「不设」处理——为它单独报个错，只会让一个笔误把整个进程挡在
// 启动阶段，而它想表达什么并不含糊。
func WithRotationHours(hours int) FileOption {
	return func(o *fileOptions) {
		if hours < 0 {
			return
		}
		o.rotationHours = hours
	}
}

// WithCompression 设置历史日志的压缩方式，取值见 [CompressionNone] /
// [CompressionGzip] / [CompressionZstd]；不设即不压缩。
func WithCompression(kind string) FileOption {
	return func(o *fileOptions) {
		o.compression = kind
	}
}

// FileOutput 是一个按大小/时间自动轮转的日志文件。
//
// 谁关它取决于谁开的：[OpenFile] 开的归调用方，用完自己 Close；[WithFile] 开的
// 归本包，[Close] 会关掉它。
//
// 它把轮转器包起来只暴露 io.WriteCloser：写日志的那一侧只需要 Write，
// 而"什么时候轮转、留几个备份、压不压"都是打开时就定死的事，
// 没有必要让持有它的人还能在运行期去动它们。
type FileOutput struct {
	io.WriteCloser
}

// OpenFile 打开一个按大小/时间自动轮转的日志文件，交给 [WithWriter] 使用。
//
//	w, err := xslog.OpenFile("/var/log/game.log", xslog.WithMaxSizeMB(50))
//	if err != nil { return err }
//	defer w.Close()
//	slog.SetDefault(slog.New(xslog.NewJSONHandler(xslog.WithWriter(w))))
//
// # 为什么开文件不归 Handler 管
//
// Handler 的职责是决定记录怎么渲染。「字节最后落到哪」是另一回事：它带来一个需要
// 释放的资源，而资源的生命周期只有持有它的人说得清。一个进程里可以有好几个 Handler，
// 谁也没资格替别人决定那个文件什么时候关——所以 Handler 只往给定的 Writer 里写，
// 不打开也不关闭任何东西，谁开的谁关。
//
// [Setup] 那条路上这个前提不一样：它装的是进程唯一的那个 Logger，「唯一」在，
// 生命周期就只有一份，因此 [WithFile] 可以把开和关都接过去（配套的是 [Close]）。
// 换句话说不是「这个包不持有资源」，而是「只有唯一性成立的地方才持有」。
//
// # 为什么要先试开一次
//
// 轮转器是**首次写入时**才打开文件的，打不开也只把错误交给 io.Writer 的返回值
// ——而 slog 那条链上没人看这个返回值。于是路径写错、目录不存在、没有写权限，
// 装配照样成功、进程照常跑，整个服务的日志全进黑洞，并且没有任何一条提示——
// 恰恰因为提示本身也进了黑洞。所以这里提前试开一次，失败就把错误交出去。
func OpenFile(path string, opts ...FileOption) (*FileOutput, error) {
	var f = new(FileOutput)
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("xslog: empty log file path")
	}
	o := f.newFileOptions(opts)
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolve log path %q: %w", path, err)
	}
	compression, err := f.resolveCompression(o.compression)
	if err != nil {
		return nil, err
	}
	if err = f.ensureLogDir(filepath.Dir(abs)); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(abs, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open log path %q: %w", abs, err)
	}
	_ = file.Close()
	// 相对路径按进程工作目录解析，同一份配置在不同目录下起服会写到不同的文件里。
	// 把最终落点打出来，省得事后猜。此刻日志还在往原来的目的地写，这条一定看得见。
	slog.Info("logging to file", slog.String("path", abs))
	f.WriteCloser = &timberjack.Logger{
		Filename:   abs,
		MaxSize:    o.maxSizeMB,
		MaxBackups: o.maxBackups,
		MaxAge:     o.maxAgeDays,
		// 0 小时即不按时间轮转，与轮转器自己的约定一致；默认值由 newFileOptions 给。
		RotationInterval: time.Duration(o.rotationHours) * time.Hour,
		Compression:      compression,
		// 备份文件名用本地时间，与日志行里的时间同一口径；两者不一致时，
		// 想按时间去某个备份里找一条日志会对不上。
		LocalTime:        true,
		BackupTimeFormat: "2006-01-02-15-04-05",
	}
	return f, nil
}

// ensureLogDir 确保目录存在，顺手建出来但要留一条告警。
//
// 路径写错时最常见的表现就是「日志跑到了别处」，而这条告警是唯一的线索——
// 正常部署里它只在第一次出现一次，一个跑了很久的服务突然打出它就说明路径变了。
func (f *FileOutput) ensureLogDir(dir string) error {
	if _, err := os.Stat(dir); !errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	slog.Warn("log directory does not exist, creating it", slog.String("dir", dir))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create log directory %q: %w", dir, err)
	}
	return nil
}

// resolveCompression 校验压缩方式，留空按不压缩处理。
//
// 自己校验而不是交给轮转器：它对不认识的值是「回落到不压缩 + 打一条警告」，
// 而配置里的拼写错误应该在启动时就拦住，不是运行半年后才有人发现历史日志没压。
func (f *FileOutput) resolveCompression(kind string) (string, error) {
	switch s := strings.ToLower(strings.TrimSpace(kind)); s {
	case "":
		return CompressionNone, nil
	case CompressionNone, CompressionGzip, CompressionZstd:
		return s, nil
	default:
		return "", fmt.Errorf("invalid log compression %q: want %s / %s / %s",
			kind, CompressionNone, CompressionGzip, CompressionZstd)
	}
}
