// def.go 收拢 xslog 的常量、类型定义与包级变量：选项、自定义头的两套类型、
// 各项默认值、容量上限，以及几个进程级的池与缓存。
//
// 单独成文件是为了让「这个包对外承诺了什么、有哪些可调的旋钮、进程里存着什么」
// 在一个地方看全——散在各自的实现文件里时，想知道有几个默认值、分别是多少，
// 得先读完四个文件；而共享状态散着放，改动时也更容易漏掉其中一处的并发前提。

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

// 输出格式的两种取值，对应配置项 Log.Format。留空等同 FormatConsole。
const (
	// FormatConsole 是给人读的控制台格式，也是默认值：日志绝大多数时候是被人看的，
	// 需要机器读的那些场合（落文件交采集器、管道喂 jq）反倒都是明确知道自己要什么的，
	// 让它们去写一行配置，比让每个开发者去发现「为什么我的日志这么难看」划算。
	FormatConsole = "console"
	// FormatJSON 是给机器读的 JSON，一条记录一个 JSON 对象。
	FormatJSON = "json"
)

// 时间的四个哨兵格式，交给 [WithTimeLayout]，把时间渲染成 Unix 纪元时间戳而不是
// 可读时间。JSON 那份写成**裸数字**（不带引号），下游因此能直接比大小、做范围查询；
// 控制台那份也裸写，与「数字不加引号」的规则一致。
//
// 之所以只能另立取值、而不是找一个 layout 串来表达：time 包的 layout 语法里没有
// 表示纪元的动词——参考时间 2006-01-02T15:04:05 的每个数字都对应一个**日历字段**，
// 纪元秒不是其中之一。
//
// 拿这几个词当哨兵是安全的：layout 的动词是参考时间里的那几个数字，加上
// Jan/Mon/MST/PM 这几个词，u/n/i/x 一个都不沾——真把 "unix" 当 layout 传给 Format，
// 得到的是字面量 "unix"，没有人会想要那个。
const (
	LayoutUnix      = "unix"
	LayoutUnixMilli = "unixmilli"
	LayoutUnixMicro = "unixmicro"
	LayoutUnixNano  = "unixnano"
)

// HeaderField 是 [JSONHandler] 自定义头里的一项，直接作为一个 JSON 成员写出。
//
// 只有 JSON 那份用它。控制台那份的头是 [ConsoleHeaderFunc] 给的一串值，没有键名——
// 那一段的内容在一次运行里几乎不变，键名每行重复一遍纯属占地方，而看的人一眼就
// 知道那几个值是谁。两边因此各用各的类型，谁也不必迁就谁。
//
// Key 不加 @ 前缀：那个前缀是留给 Handler 自己注入的固定成员（time / level /
// source / message）的，业务字段撞不上它靠的正是"业务代码永远不写 @ 开头的键"。
// 自定义头的键是调用方自己起的，它同时也掌握着业务字段叫什么，撞名与否自己就能避开，
// 不需要这套隔离——代价是这里得自己留意别和字段重名。
type HeaderField struct {
	Key   string
	Value string
}

// JSONHeaderFunc 返回 [JSONHandler] 自定义头的内容，在装配 Handler 时**调用一次**，
// 结果夹在 @source 与 @message 之间。
//
// 只调一次而不是每条日志调一次：这一段的用处是「从混在一起的日志里认出这条是谁
// 打的」，答案在一个进程的生命周期里不会变。做成每条调用一次，既要为它付出每行
// 一次函数调用与重新渲染的代价，又会让同一个进程的日志在不同行上给出不同的身份
// ——那正好毁掉了它存在的理由。
//
// 值里的不可打印字符与其它输入走同一条转义路径，实现里不必自己清洗。
//
// 控制台那份不认它，走 [ConsoleHeaderFunc]：两种格式对「头」的要求不是同一件事，
// 理由见那边。
type JSONHeaderFunc func() []HeaderField

// ConsoleHeaderFunc 逐条日志组装 [ConsoleHandler] 头行里「时间之后、消息之前」的
// 全部内容，**每条日志调用一次**；不设即由 Handler 按固定顺序写「级别 [位置]」。
//
// 它拿到的正是那两样东西：lv 是本条的级别，src 是调用位置，形如 "gate/gate.go:100"、
// 不带方括号（手工构造的 Record、PC 为 0 时是空串）。写在哪一格、写成什么样都由这里
// 决定——缩写成 I/W/E、定宽对齐、夹在两个自定义项中间都行，Handler 自己不再写它们。
//
//	xslog.WithConsoleHeader(func(lv slog.Level, src string) []string {
//	    return []string{"gate", lv.String()[:1], "node-3", "[" + src + "]"}
//	})
//	// 2026-08-20T11:48:55.756+08:00 gate I node-3 [gate/gate.go:100] message="listen on"
//
// 返回的每一项自成一段，Handler 负责用一个空格隔开；空串整项跳过（连同分隔空格），
// 按条件才出现的字段——这条日志没有 trace id——因此不会在行里留下一个孤零零的空格。
//
// 返回 []string 而不是 []HeaderField：控制台那份不写键名，那个 Key 在这里没有用处，
// 留着只会让人以为它会显示出来。也不是 []byte——转义得留在包里做，见下。
//
// 每条日志一次函数调用与一次切片分配，是这个口子的全部代价。只想要一段恒定不变的
// 身份标记（"gate node-3"）的话，闭包里返回一个提前建好的切片即可，那时只剩函数调用；
// 但级别与位置逐条不同，把它们排进去就必然要每条重拼。
//
// 值里的不可打印字符与其它输入走同一条转义路径，实现里不必自己清洗——也正因如此，
// 这里**上不了色**：ANSI 序列里的 ESC 会被转义成 \u001b 原样打出来。这一条不为头
// 破例，控制台那份「值操纵不了终端、顶格的行恒为一条记录的开头」的性质不该分来源，
// 而头的内容同样可能是拼进来的外部数据。
//
// 它自己 panic 时就地兜住，头里写出 <header panicked: …>：Handler 常常正是在
// recover 内部被调用的，在那里再炸一次等于把兜底本身炸掉。
type ConsoleHeaderFunc func(lv slog.Level, src string) []string

// Option 调整日志的装配与渲染，交给 [NewConsoleHandler] 或 [NewJSONHandler]。
//
// 用选项而不是把参数排进签名或塞进一个配置结构体：可调的东西还会增加，每加一个
// 就改一次签名意味着所有调用点跟着改，而它们大多根本不关心新加的那一项；配置结构体
// 还让「没填的字段到底是默认值还是显式的零」变得看不出来，而这两者常常要区别对待
// （例子见 [FileOption]）。
//
// 落盘相关的选项是另一套类型，见 [FileOption]。
type Option func(*options)

// options 是选项应用后的结果。
type options struct {
	writer        io.Writer
	filePath      string
	fileOpts      []FileOption
	level         slog.Leveler
	format        string
	jsonHeader    JSONHeaderFunc
	consoleHeader ConsoleHeaderFunc
	timeLayout    string
}

// newOptions 按顺序应用选项，后面的覆盖前面的；没被覆盖的留在默认值上。
func newOptions(opts []Option) options {
	o := options{
		level:      DefaultLevel,
		format:     FormatConsole,
		timeLayout: DefaultTimeLayout,
	}
	for _, opt := range opts {
		if opt != nil {
			opt(&o)
		}
	}
	return o
}

// WithWriter 指定输出目标；不设即标准输出。
//
// 要写进按大小/时间自动轮转的日志文件，用 [OpenFile] 开一个再传进来。
// Writer 的生命周期归调用方：这个包只往里写，不打开也不关闭——它没打开的东西，
// 也就不知道关掉会不会影响别的地方。
func WithWriter(w io.Writer) Option {
	return func(o *options) {
		// 与 [WithFile] 互为覆盖：两者说的都是「日志往哪去」，同时给了就按选项顺序
		// 由后者作数，和这个包里其余选项的规矩一致。在这里把路径清掉，装配时也就
		// 不必再为「既给了 Writer 又给了路径」单独定一条规则。
		o.filePath, o.fileOpts = "", nil
		if w == nil {
			o.writer = os.Stdout
			return
		}
		o.writer = w
	}
}

// WithFile 让 [Setup] 把日志写进 path，按大小/时间自动轮转；轮转参数用 [FileOption] 调，
// 取值与 [OpenFile] 的完全一样。
//
//	if err := xslog.Setup(xslog.WithFormat(xslog.FormatJSON),
//		xslog.WithFile("/var/log/game.log", xslog.WithMaxSizeMB(50))); err != nil {
//		return err
//	}
//	defer xslog.Close()
//
// 文件由 [Setup] 打开、[Close] 关闭——这是本包唯一一处替调用方持有资源的地方。
// 它只在 [Setup] 这条路上成立：那里装的本来就是进程唯一的那个 Logger，再挂一个
// 文件句柄上去，要管的生命周期仍然只有一份。自己 New Handler 的那条路不适用，
// 那里没有「唯一」可言，路径得自己 [OpenFile] 再用 [WithWriter] 传进去，谁开的谁关。
//
// 与 [WithWriter] 互为覆盖，两个都给按选项顺序由后者作数。
//
// 路径开不开得了要到 [Setup] 才见分晓：选项本身没有返回错误的地方，真正的打开
// 因此推迟到那时候——目录不存在、没有写权限这些事仍然是 Setup 的返回值，
// 而不是等到运行期变成一次没人看得见的写失败。
func WithFile(path string, opts ...FileOption) Option {
	return func(o *options) {
		o.writer = nil
		o.filePath, o.fileOpts = path, opts
	}
}

// WithLevel 设置级别阈值，不设即 [DefaultLevel]。
//
// 收 slog.Leveler 而不是 *slog.LevelVar：后者只是它最常用的一种实现，传一个固定的
// slog.Level（"这个 Handler 只收 Warn 以上"）同样成立，没有理由逼调用方为此包一层。
//
// 传 *slog.LevelVar 时级别仍可运行期改：Leveler 是接口，装进去的就是那个指针，
// 每条日志都顺着它读当前值。传裸的 slog.Level 则是钉死的——要哪一种由调用方决定。
//
// 运行期怎么调级，两条装配路各不相同。
//
// 走 [Setup]：这个选项只定装配那一刻的阈值，之后一律用 [SetLevel]。装出来的 Handler
// 持有的始终是本包那一个 LevelVar，传自己的 LevelVar 进去也只是给了个起点，
// 后续再 Set 它不会传导过去。
//
// 自己 New Handler：调级就是调用方自己的事——留住那个 LevelVar，想调的时候 Set
// 一下即可，多个 Handler 要一起跟着变就把同一个都传进去。
//
//	lv := new(slog.LevelVar)
//	lv.Set(slog.LevelDebug) // LevelVar 的零值是 Info，不是 [DefaultLevel]
//	slog.SetDefault(slog.New(xslog.NewConsoleHandler(xslog.WithLevel(lv))))
//	lv.Set(slog.LevelInfo)  // 立即生效，不必重建 Handler
//
// [DefaultLevel] 只在**不传这个选项**时生效。自己 new 的 LevelVar 归标准库管，
// 它的零值是 Info——想要 Debug 就显式 Set 一下，别指望它跟这个包的默认值对齐。
func WithLevel(l slog.Leveler) Option {
	return func(o *options) {
		if l == nil {
			o.level = DefaultLevel
			return
		}
		o.level = l
	}
}

// WithJSONHeader 设置 [JSONHandler] 自定义头的来源，装配时调用一次；
// 不设即不输出自定义头。语义见 [JSONHeaderFunc]。
//
// 只有 NewJSONHandler 认它，控制台那份完全不看——头在两种格式里不是同一件事：
// JSON 要的是稳定的键值对，控制台要的是一段排好的文本，那边走 [WithConsoleHeader]。
func WithJSONHeader(fn JSONHeaderFunc) Option {
	return func(o *options) {
		if fn == nil {
			return
		}
		o.jsonHeader = fn
	}
}

// WithFormat 选输出格式，取值 [FormatConsole] 或 [FormatJSON]；留空即控制台。
//
// 只有 [Setup] 认它：那条路上格式是从配置来的一个字符串，总得有人把它翻成具体的
// Handler。自己 New 的那条路不需要——选哪个格式就是选哪个构造函数，中间没有
// 需要分派的余地。
//
// 拼错了不在这里报错，留给 [Setup] 报：选项没有返回错误的地方，而这个错误恰恰是
// 最该在启动时就拦住的那种——静静换一种格式，要等有人去看日志才发现。
func WithFormat(format string) Option {
	return func(o *options) {
		if format == "" {
			format = FormatConsole
		}
		o.format = format
	}
}

// WithConsoleHeader 把 [ConsoleHandler] 头行里级别与位置的排布交给调用方，
// 逐条日志调用一次；不设即由 Handler 按固定顺序自己写「级别 [位置]」。
// 语义、代价与适用场合见 [ConsoleHeaderFunc]。
//
// 只有 NewConsoleHandler 认它，JSON 那份完全不看，理由同 [WithJSONHeader]。
func WithConsoleHeader(fn ConsoleHeaderFunc) Option {
	return func(o *options) {
		if fn == nil {
			return
		}
		o.consoleHeader = fn
	}
}

// WithTimeLayout 设置时间的渲染格式（time 包的参考时间写法），
// 不设即 [DefaultTimeLayout]。
//
// 要 Unix 纪元时间戳就传 [LayoutUnix] / [LayoutUnixMilli] / [LayoutUnixMicro] /
// [LayoutUnixNano]——那几种形态没有对应的 layout 串，理由见那里。
//
// 它同时作用于行首时间与 time.Time 类型的字段值：两者格式不一致时，想拿日志行里
// 的时间去比对某个时间字段会对不上，而那正是这两个东西最常一起被读的场合。
func WithTimeLayout(layout string) Option {
	return func(o *options) {
		if layout == "" {
			o.timeLayout = DefaultTimeLayout
			return
		}
		o.timeLayout = layout
	}
}

// DefaultLevel 是不设 [WithLevel] 时的级别阈值。
//
// 取 Debug 而不是标准库那样取 Info：这个包的默认输出是给人看的控制台格式，
// 而会去看控制台的场合几乎都是在开发或排查，那时被默认值挡掉的 Debug 正是要看的东西。
// 线上把它调到 Info 是一行配置的事，反过来「线上出问题了想看 Debug」却要重启。
const DefaultLevel = slog.LevelDebug

// DefaultTimeLayout 是两个 Handler 共用的默认时间格式。
//
// 选 RFC3339 毫秒：它按字典序排就是按时间序排，带时区偏移因此跨机器的日志能直接
// 比对，且任何 JSON 消费方都认得这个形状，不需要额外约定。想在控制台上换成更短的
// 写法（比如去掉日期只留时分秒），用 [WithTimeLayout]。
const DefaultTimeLayout = "2006-01-02T15:04:05.000Z07:00"

// maxPooledBuf 是允许放回缓冲池的容量上限。一条几十 KB 的堆栈会把缓冲撑大，
// 若原样收回池子，这块内存就被池子长期占住了。
const maxPooledBuf = 64 << 10

// maxPooledAttrs 是允许放回 attrPool 的切片容量上限，理由同 maxPooledBuf：
// 一条字段特别多的记录会把切片撑大，原样收回去这块内存就被池子长期占住了。
const maxPooledAttrs = 64

// errorAsText 让**嵌套在**结构体、切片、map 里的 error 也按 Error() 文本渲染。
//
// 不挂这一条的话，jsonv2 会按 error 动态类型的结构体形状去编，而错误类型的字段
// 几乎都是非导出的——一个 errors.New 出来的值编完就是 {}，那句话整个没了，
// 而它往往正是这条日志唯一想说的东西。顶层 error 走各自 Handler 的分支，
// 不经过这里。
var errorAsText = json.WithMarshalers(json.MarshalFunc(func(err error) ([]byte, error) {
	return jsontext.AppendQuote(nil, err.Error())
}))

// bufPool 复用日志行缓冲区。Handle 在游戏服里是不折不扣的热路径（行军、战斗每帧
// 都有若干条 Debug），每条都新建一个缓冲会把 GC 压力平白抬高一截。
var bufPool = sync.Pool{
	New: func() any {
		b := make([]byte, 0, 1024)
		return &b
	},
}

// 编译期确认两个 Handler 都满足接口；漏实现一个方法要在这里报错，
// 而不是等到 slog.New 那行才报。
var (
	_ slog.Handler = (*JSONHandler)(nil)
	_ slog.Handler = (*ConsoleHandler)(nil)
	// std 是进程唯一的日志管理器。
	std = &manager{level: new(slog.LevelVar)}
)

// attrPool 复用摊平字段的切片，避免每条记录都为它分配一次。
var attrPool = sync.Pool{
	New: func() any {
		a := make([]consoleAttr, 0, 16)
		return &a
	},
}

// moduleVersionPattern 匹配 Go module 缓存路径里的版本后缀，例如
// "zk@v1.0.3" 中的 "@v1.0.3"，以及伪版本 "@v0.0.0-20230101000000-abcdef123456"。
// trimmedPath 只保留调用位置的上一级目录名与文件名，项目自身代码的上一级目录
// 是包名（如 xslog/json.go），但依赖库源码在 pkg/mod 缓存里的目录名本身就是
// "模块名@版本号"，若不剥离会把版本号也打进日志（如 zk@v1.0.3/conn.go）。
var moduleVersionPattern = regexp.MustCompile(`@v[0-9]+\.[0-9]+\.[0-9]+[^/]*`)

// srcCache 缓存 PC → "包名/文件名:行号"。
//
// runtime.CallersFrames 是写一条日志时最贵的一步，也是唯一还在做分配的地方——
// 实测一条三字段的常规记录里，它一个人占掉三分之一还多。而日志调用点是一组固定
// 且有限的位置：每个位置只需要算一次，之后每条日志就只是一次 append。
// 缓存因此天然有界（上限就是二进制里日志语句的条数），不需要淘汰。
var srcCache sync.Map // uintptr -> string

// output 返回实际的输出目标：没设 [WithWriter] 时是标准输出。
//
// 集中在这里而不是让每个构造函数各判一次 nil：漏判一处的表现是那个 Handler
// 往 nil 写入时 panic，而它可能要等到某条罕见级别的日志才第一次被触发。
func (o options) output() io.Writer {
	if o.writer != nil {
		return o.writer
	}
	return os.Stdout
}
