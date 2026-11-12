package xslog

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"io"
	"log/slog"
	"os"
	"regexp"
	"sync"
)

const (
	// layoutUnix 按秒输出 Unix 时间戳。
	layoutUnix = "unix"
	// layoutUnixMilli 按毫秒输出 Unix 时间戳。
	layoutUnixMilli = "unixMilli"
	// layoutUnixMicro 按微秒输出 Unix 时间戳。
	layoutUnixMicro = "unixMicro"
	// layoutUnixNano 按纳秒输出 Unix 时间戳。
	layoutUnixNano = "unixNano"
)

// Option 配置 [NewHandler] 或 [Setup]。文件轮转参数使用 [FileOption]。
type Option func(*options)

// ErrorHandler 接收布局和 Writer 返回的错误。
// [slog.Logger] 会忽略 Handler.Handle 的返回值，可通过此回调观察错误。
type ErrorHandler func(error)

// options 是选项应用后的结果。
type options struct {
	writer       io.Writer
	hasFile      bool
	filePath     string
	fileOpts     []FileOption
	level        slog.Leveler
	errorHandler ErrorHandler
	layout       layout
}

// apply 按顺序应用选项，未指定级别时使用默认值。
func (o options) apply(opts []Option) options {
	if o.level == nil {
		o.level = slog.LevelDebug
	}
	for _, opt := range opts {
		if opt != nil {
			opt(&o)
		}
	}
	return o
}

// WithWriter 指定输出目标，nil 表示标准输出。Writer 的生命周期由调用方管理。
// 与 [WithFile] 同时使用时，后传入的选项生效。
func WithWriter(w io.Writer) Option {
	return func(o *options) {
		// 清除先前的文件选项，避免 Setup 打开已被覆盖的路径。
		o.hasFile = false
		o.filePath, o.fileOpts = "", nil
		if w == nil {
			o.writer = os.Stdout
			return
		}
		o.writer = w
	}
}

// WithFile 指定由 [Setup] 打开的轮转日志文件，轮转参数与 [OpenFile] 相同。
// 文件由 [Close] 关闭。该选项仅供 Setup 使用；自行创建 Handler 时应使用
// OpenFile 和 [WithWriter]。与 WithWriter 同时使用时，后传入的选项生效。
//
// 路径校验和文件打开错误由 Setup 返回，选项本身不打开文件。
func WithFile(path string, opts ...FileOption) Option {
	return func(o *options) {
		o.writer = nil
		o.filePath, o.fileOpts = path, opts
		o.hasFile = true
	}
}

// WithLevel 设置级别阈值，未指定或传入 nil 时使用 [slog.LevelDebug]。
// [NewHandler] 保留传入的 Leveler，可通过共享的 [slog.LevelVar] 或 [Handler.SetLevel] 动态调级。
// [Setup] 只读取初始级别，后续通过 [SetLevel] 调整全局 Handler 的级别。
//
// slog.LevelVar 的零值是 Info，传入前应按需要设置初始级别。
func WithLevel(l slog.Leveler) Option {
	return func(o *options) {
		if l == nil {
			o.level = slog.LevelDebug
			return
		}
		o.level = l
	}
}

// WithErrorHandler 设置布局和 Writer 错误的观察回调。回调可能被并发调用。
func WithErrorHandler(fn ErrorHandler) Option {
	return func(o *options) { o.errorHandler = fn }
}

// DefaultTemplate 是未设置模板或传入 WithTemplate("") 时使用的文本模板。
// 业务字段由编码器追加，记录末尾的换行由 Handler 补齐。
const DefaultTemplate = "${Time} ${Level} [${Source}] message=${Message}"

// defaultTimeLayout 是时间占位符省略格式时使用的 RFC3339 毫秒布局。
// 可在占位符中覆盖，例如 ${Time:15:04:05.000}。
const defaultTimeLayout = "2006-01-02T15:04:05.000Z07:00"

// maxPooledBuf 限制池中缓冲区的容量，避免长期保留偶发大记录的内存。
const maxPooledBuf = 64 << 10

// maxPooledAttrs 限制文本属性池中切片的元素容量。
const maxPooledAttrs = 64

// maxAttrGroupDepth 限制单个输入属性的分组深度，防止异常递归耗尽栈。
const maxAttrGroupDepth = 128

// maxResolvedLogValuers 限制单个输入属性树中待解析的 LogValuer 值数量。
const maxResolvedLogValuers = 4096

// errorAsText 将复合值内嵌套的 error 编码为 Error() 文本，避免编码成空对象。
// 直接作为属性传入的 error 由属性取值和编码流程处理。
var errorAsText = json.WithMarshalers(json.MarshalFunc(func(err error) ([]byte, error) {
	return jsontext.AppendQuote(nil, err.Error())
}))

// bufPool 复用记录缓冲区；容量超过 maxPooledBuf 时不放回。
var bufPool = sync.Pool{
	New: func() any {
		b := make([]byte, 0, 1024)
		return &b
	},
}

var (
	// 编译时检查 Handler 是否实现 slog.Handler。
	_ slog.Handler = (*Handler)(nil)

	// std 是进程唯一的日志管理器。
	std = new(manager)
)

// attrPool 复用摊平后的文本字段切片。
var attrPool = sync.Pool{
	New: func() any {
		a := make([]textAttr, 0, 16)
		return &a
	},
}

// moduleVersionPattern 匹配模块目录的版本后缀，使调用位置不包含依赖版本。
var moduleVersionPattern = regexp.MustCompile(`@v[0-9]+\.[0-9]+\.[0-9]+[^/]*`)

// srcCache 缓存程序计数器对应的完整调用信息，减少重复解析。
var srcCache sync.Map // uintptr -> sourceInfo

// output 返回配置的 Writer，未指定时使用标准输出。
func (o options) output() io.Writer {
	if o.writer != nil {
		return o.writer
	}
	return os.Stdout
}
