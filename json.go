package xslog

import (
	"context"
	"encoding/hex"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"fmt"
	"io"
	"log/slog"
	"math"
	"slices"
	"strconv"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"
)

// JSONHandler 把每条记录渲染成一个 JSON 对象，逐成员手工拼出，字符串字面量与浮点数交给
// encoding/json/jsontext 渲染，复合值交给 encoding/json/v2 序列化：
//
//	{"@time":"2026-08-20T11:48:55.756+08:00","@level":"INFO","@source":"gate/gate.go:100","@message":"listen on","network":"tcp","address":"[::]:8080"}
//
// 固定成员依次是时间（默认 RFC3339 毫秒，见 [DefaultTimeLayout]；零值时整项省略）、级别、调用位置（上级目录名/
// 文件名:行号，已剥离依赖库路径中的模块版本号）、消息，其后才是结构化字段。
// 自定义头（[WithJSONHeader] 提供，不设即没有）夹在调用位置与消息之间，键名由调用方给定，
// **不带 @ 前缀**——那个前缀是留给 Handler 自己注入的固定成员的，而自定义头的键是
// 调用方自己起的，它同时掌握着业务字段叫什么，撞名与否自己就能避开。
//
// 固定成员一律带 @ 前缀，业务字段一律不带，两者因此永远撞不上——理由见下。
//
// # 为什么是 JSON，不是 key=value
//
// key=value 那种写法靠「值里有空格就加引号」来维持字段边界，而「什么时候该加引号」
// 是一条自定义规则：下游想正确切分，就得把这条规则重新实现一遍，并且实现得跟这里
// 一模一样。规则只要有一处对不上，错法是「某些行的字段静静地串位」，不是报错。
// JSON 的边界规则由 RFC 8259 定死，任何语言都有现成解析器。
//
// 换过来还白捡两样东西：分组字段变回真正的嵌套对象，不必用 "battle.hp" 这种点分
// 键名去模拟；数字、布尔、结构体各自保留 JSON 类型，不再被一律压成字符串——
// 此前 slog.Any 一个结构体打出来是 "{1 2}"，读得懂但解析不了。
//
// # 固定成员为什么都带 @ 前缀
//
// 固定成员与结构化字段写在同一个对象里、同一层。重名不会报错、输出也仍是合法 JSON，
// 但对象里会出现两个同名成员——解析器要么取后一个（jq、Python 都是），要么整条拒收
// （encoding/json/v2 的默认参数即如此）。两种结果都是这条记录的级别或调用位置被业务
// 数据顶掉，而且一路无声。而 level、source 这类名字在业务里本来就有正当含义
// （城堡等级、属性来源），指望每个调用方都记着绕开它们是不现实的。
//
// 所以固定成员一律占用 @ 打头的名字，与业务字段分处两个不会相交的命名空间。
// 业务字段只要不以 @ 开头就永远撞不上，调用方什么都不用记。
//
// 代价是取值要写 jq '.["@message"]' 而不是 jq '.message'。固定成员统共就这几个，
// 换来的是任何字段名都不必再避讳。
//
// 同一条记录里同名的 Attr 出现两次仍会产出重复成员，这里不做去重：
// 去重要么改名要么丢弃，两种都是在替调用方做决定，而调用方并不知道自己被改过。
//
// # @message 为什么排在结构化字段之前
//
// 结构化字段可能开出嵌套对象（WithGroup），排在它们后面的 @message 会掉进最后一个
// 组里，变成 group.@message。固定成员必须在任何组打开之前写完。
//
// # 多行值展开成 JSON 字符串数组
//
// JSON 字符串里不允许出现裸换行（RFC 8259 §7）。panic 堆栈这类多行值若原样塞进一个
// 字符串成员，就是一整行带字面 \n 的东西——合法，但几十帧堆栈挤在一行里没法看。
//
// 所以含换行的字符串值不写成字符串，而是按行拆成**字符串数组**，每个元素单独占一个
// 物理行：
//
//	{"@time":"2026-08-20T11:48:55.756+08:00","@level":"ERROR","@source":"wmap/ctl_march.go:562","@message":"march frame panic","stack":[
//		"goroutine 42 [running]:",
//		"runtime/debug.Stack()",
//		"\t/usr/local/go/src/runtime/debug/stack.go:26 +0x5e"]}
//
// 堆栈在终端上于是真的一帧一行，而整条记录仍然是一个合法 JSON 值——JSON 的词法
// 不在乎标记之间有多少空白，jq 之类按值读取的工具照常工作。
//
// 代价是同一个键的 JSON 类型不固定：值只有一行时是字符串，多行时是数组。下游按
// 「是数组就 join("\n")，否则原样取」归一即可；join 出来的是**规范化之后**的文本，
// 行尾的 CR 与尾部空行已经去掉，行内内容逐字节保留。
//
// # 值本身就是 JSON 时原样嵌入
//
// 以 '{' 或 '[' 打头、且整体是一个合法 JSON 值的字符串，直接嵌进记录里当子对象，
// 不再当字符串转义一遍——框架的运行统计正是这么打的（一个 slog.String，内容是
// 一整个 JSON 对象），转义之后是满屏的 \"，既没法读也没法用 jq 下钻。
// 这同样让键的 JSON 类型随内容而变，判定与取舍见 appendEmbeddedJSON。
//
// 展开出来的每一行都以制表符开头，这有两个作用：
//   - 顶格的行恒为一条记录的开头。值里被塞进换行（客户端上行的 Lua 脚本正文、
//     远端返回的错误文本）也伪造不出一条以假时间戳打头的日志记录；
//   - 「以空白开头的行归并到上一条」是日志采集的通行规则，真要按行采集时配一条即可。
//
// 展开在这一份里没有开关。需要一条记录严格一个物理行时，在采集端按上面那条空白
// 续行规则归并即可——归并是可逆的，而把堆栈压成字面 \n 之后再想还原，就得反解
// 一遍转义。真正只想给人看的场合有 [ConsoleHandler]，不必在这一份上折中。
type JSONHandler struct {
	renderer

	mu     *sync.Mutex
	writer io.Writer
	level  slog.Leveler

	// time 是 @time 成员与 time.Time 字段值共用的渲染设置，装配时由 [WithTimeLayout]
	// 的取值解析出来；它同时决定这两处加不加引号。
	time timeFormat

	// header 是自定义头**已经渲染好**的字节，含各自的前导逗号；没有自定义头时为空。
	//
	// 装配时渲染一次、之后每条日志只是一次 memcpy：它的内容在一个进程里不变，
	// 每行重算一遍是白花的钱。
	header []byte

	// preformatted 是 With 附加的固定字段**已经渲染好**的字节，含各自的前导逗号。
	//
	// 存渲染结果而不是存 []slog.Attr，是因为分组归属必须按「字段被添加时」的分组
	// 状态来算：slog.With("mod", x).WithGroup("battle") 里的 mod 不属于 battle 组。
	// 若把 attr 原样留到 Handle 再统一套用彼时的分组，它会被塞进 battle 对象里
	// ——而这种错不会报错，只会让日志里的字段路径慢慢变得对不上，排查时按 mod
	// 过滤什么都搜不到。顺带的好处是固定字段只渲染一次，之后每条日志只是一次 memcpy。
	preformatted []byte

	// sep 是写下一个成员前要补的分隔符：',' 表示当前对象里已经有成员了，
	// 0 表示刚开过一个 '{'，下一个成员前面不能有逗号。
	//
	// 需要它是因为 preformatted 是提前渲染的：渲染时正处在哪一层对象、那层里有
	// 没有成员，只有当时知道，到 Handle 再判断已经晚了。
	sep byte

	// openGroups 是 preformatted 里已经打开、尚未闭合的 '{' 个数，Handle 末尾按此补 '}'。
	openGroups int

	// pendingGroups 是 WithGroup 声明了、但还没有任何字段落进去的分组名。
	//
	// 延迟到第一个字段才真正开对象，加上「开了却没人落笔就整串回退」，
	// 是为了不留下 "battle":{} 这种只有壳的成员。
	pendingGroups []string
}

// NewJSONHandler 创建 JSONHandler。
//
// 输出目标走 [WithWriter]，不设即标准输出；渲染上的可调项走 [WithJSONHeader] /
// [WithTimeLayout] 这类选项，不传即用默认。[WithConsoleHeader] 是控制台那份的，
// 传进来这里不看。
// 级别阈值走 [WithLevel]，不设即 DefaultLevel。
func NewJSONHandler(opts ...Option) *JSONHandler {
	o := newOptions(opts)
	h := &JSONHandler{
		mu:     &sync.Mutex{},
		writer: o.output(),
		level:  o.level,
		time:   newTimeFormat(o.timeLayout),
		sep:    ',',
	}
	if o.jsonHeader != nil {
		for _, f := range o.jsonHeader() {
			h.header = append(h.header, ',')
			h.header = h.appendJSONString(h.header, f.Key)
			h.header = append(h.header, ':')
			h.header = h.appendJSONString(h.header, f.Value)
		}
	}
	return h
}

// Enabled 检查指定日志级别是否开启。
func (h *JSONHandler) Enabled(_ context.Context, level slog.Level) bool {
	return level >= h.level.Level()
}

// Handle 按固定格式写入一条记录。
//
// 整条记录（含展开出来的多行块）拼在同一个缓冲里，在同一次加锁下一次写出。
// 分两次写会让并发的两条记录交错——网关是每连接一个 goroutine，而打堆栈的点
// 大半在网关，交错出来的半条堆栈既读不了也解析不了。
//
// 解锁与缓冲归还都走 defer：这条路径上会调到别人的代码（字段值的 Error()、
// String()、MarshalJSON，以及 Writer 本身），任何一处 panic 都不该把这把
// 全进程共用的锁永久锁死。
func (h *JSONHandler) Handle(_ context.Context, r slog.Record) error {
	bp := bufPool.Get().(*[]byte)
	buf := (*bp)[:0]
	defer func() {
		if cap(buf) <= maxPooledBuf {
			*bp = buf
			bufPool.Put(bp)
		}
	}()

	buf = append(buf, '{')
	// 零值时间整项省略（slog 规约）：手工构造的 Record 常常不带时间，
	// 写出 "0001-01-01T00:00:00.000Z" 只会让下游拿它去排序。
	if !r.Time.IsZero() {
		buf = append(buf, `"@time":`...)
		buf = h.time.appendJSON(buf, r.Time)
		buf = append(buf, ',')
	}
	buf = append(buf, `"@level":`...)
	buf = h.appendJSONString(buf, r.Level.String())
	buf = h.appendSource(buf, r.PC)
	buf = append(buf, h.header...)
	buf = append(buf, `,"@message":`...)
	buf = h.appendJSONString(buf, r.Message)

	// 结构化字段：Handler 级别（With）字段在前，Record 级别字段在后
	buf = append(buf, h.preformatted...)
	sep, open, pending := h.sep, h.openGroups, h.pendingGroups
	mark := len(buf)
	r.Attrs(func(a slog.Attr) bool {
		if len(pending) > 0 {
			buf, sep, open = h.appendPendingGroups(buf, pending, sep, open)
			pending = nil
		}
		buf, sep = h.appendAttr(buf, a, sep)
		return true
	})
	// 分组开出来了却一个字段都没落进去，整串回退，别留下空壳。
	if open > h.openGroups && sep == 0 {
		buf, open = buf[:mark], h.openGroups
	}
	for range open {
		buf = append(buf, '}')
	}
	buf = append(buf, '}', '\n')

	h.mu.Lock()
	defer h.mu.Unlock()
	_, err := h.writer.Write(buf)
	return err
}

// appendSource 写入 "@source":"上级目录/文件名:行号"。取不到位置时整个成员省略。
//
// 位置串按说不含引号或反斜杠，仍走一遍转义：「按说不会」的字符一旦真的出现，
// 拼出来的就是一条解析不了的记录，而这条记录恰恰是用来定位问题的。
func (h *JSONHandler) appendSource(buf []byte, pc uintptr) []byte {
	loc := h.source(pc)
	if loc == "" {
		return buf
	}
	buf = append(buf, `,"@source":`...)
	return h.appendJSONString(buf, loc)
}

// WithAttrs 返回附加了固定字段的子 Handler，对应 slog.Logger.With。
//
// 字段在这里就按**当前**的分组状态渲染好，理由见 preformatted 的说明。
func (h *JSONHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	if len(attrs) == 0 {
		return h
	}
	// Clip 让 cap == len，下面的 append 必定分配新数组。不这么做的话，由同一个父
	// Handler 派生出的两个子 Handler 会往同一块底层数组里写，后派生的那个会把先
	// 派生的字段悄悄覆盖掉。
	buf := slices.Clip(h.preformatted)
	mark := len(buf)
	sep, open := h.sep, h.openGroups
	buf, sep, open = h.appendPendingGroups(buf, h.pendingGroups, sep, open)
	for _, a := range attrs {
		buf, sep = h.appendAttr(buf, a, sep)
	}
	// 这一批字段一个都没落笔（全是空 Attr 或空组）：连同为它们开出的分组一起作废，
	// 直接沿用父 Handler——分组仍处在「待落笔」状态，留给下一批字段。
	if len(buf) == mark || (open > h.openGroups && sep == 0) {
		return h
	}
	next := *h
	next.preformatted = buf
	next.sep = sep
	next.openGroups = open
	next.pendingGroups = nil
	return &next
}

// WithGroup 返回附加了字段分组的子 Handler，对应 slog.Logger.WithGroup。
func (h *JSONHandler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}
	next := *h
	// 同 WithAttrs：先 Clip 再 append，避免兄弟 Handler 共享底层数组。
	next.pendingGroups = append(slices.Clip(h.pendingGroups), name)
	return &next
}

// appendPendingGroups 把尚未落笔的分组逐层开成嵌套对象，
// 返回新的分隔符状态与累计未闭合的花括号数。
func (h *JSONHandler) appendPendingGroups(buf []byte, pending []string, sep byte, open int) ([]byte, byte, int) {
	for _, g := range pending {
		buf = h.appendSep(buf, sep)
		buf = h.appendJSONString(buf, g)
		buf = append(buf, ':', '{')
		sep = 0
		open++
	}
	return buf, sep, open
}

// appendSep 在需要时补上成员之间的逗号。
func (h *JSONHandler) appendSep(buf []byte, sep byte) []byte {
	if sep == 0 {
		return buf
	}
	return append(buf, sep)
}

// appendAttr 写入一个结构化字段，返回写完之后的分隔符状态。
// 分组字段展开为真正的嵌套 JSON 对象。
func (h *JSONHandler) appendAttr(buf []byte, a slog.Attr, sep byte) ([]byte, byte) {
	a.Value = a.Value.Resolve()
	if a.Equal(slog.Attr{}) {
		return buf, sep
	}
	if a.Value.Kind() == slog.KindGroup {
		sub := a.Value.Group()
		// 空组整个忽略（slog 规约）：否则会留下一个只有壳的成员。
		if len(sub) == 0 {
			return buf, sep
		}
		// 无名组内联展开，不另开一层（slog 规约）。
		if a.Key == "" {
			for _, ga := range sub {
				buf, sep = h.appendAttr(buf, ga, sep)
			}
			return buf, sep
		}
		// 记下开壳之前的位置：组里的成员可能一个都渲染不出来（全是空 Attr、
		// 全是空子组），那时要连壳一起撤销，而不是留下 "battle":{}。
		mark := len(buf)
		buf = h.appendSep(buf, sep)
		buf = h.appendJSONString(buf, a.Key)
		buf = append(buf, ':', '{')
		inner := byte(0)
		for _, ga := range sub {
			buf, inner = h.appendAttr(buf, ga, inner)
		}
		if inner == 0 {
			return buf[:mark], sep
		}
		return append(buf, '}'), ','
	}

	buf = h.appendSep(buf, sep)
	buf = h.appendJSONString(buf, a.Key)
	buf = append(buf, ':')
	return h.appendValue(buf, a.Value), ','
}

// appendValue 按类型渲染字段值。
//
// 分类型处理而不是一律交给 jsonv2 反射，一是为了让数字、布尔走各自的直写路径，
// 省掉反射与中间分配（Handle 是热路径）；二是 time.Time 与 time.Duration 需要
// 固定的可读渲染，而不是它们的结构体形状。
func (h *JSONHandler) appendValue(buf []byte, v slog.Value) []byte {
	switch v.Kind() {
	case slog.KindString:
		return h.appendText(buf, v.String())
	case slog.KindInt64:
		return strconv.AppendInt(buf, v.Int64(), 10)
	case slog.KindUint64:
		return strconv.AppendUint(buf, v.Uint64(), 10)
	case slog.KindFloat64:
		f := v.Float64()
		// JSON 没有 NaN 与 Inf（RFC 8259 §6），直接写出去整条记录就解析不了了。
		// 转成字符串是为了保住这条记录的其余部分——一个算歪了的数值，
		// 不该顺手把记录它的那行日志也一起废掉。
		if math.IsNaN(f) || math.IsInf(f, 0) {
			return h.appendJSONString(buf, strconv.FormatFloat(f, 'g', -1, 64))
		}
		return jsontext.AppendFloat(buf, f, 64)
	case slog.KindBool:
		return strconv.AppendBool(buf, v.Bool())
	case slog.KindDuration:
		// 写成 "1.5s" 而不是纳秒数：纳秒数在日志里要心算，而时长这个字段
		// 存在的意义就是一眼看出快慢。
		return h.appendJSONString(buf, v.Duration().String())
	case slog.KindTime:
		// slog.AnyValue 会把 time.Time 归到 KindTime，因此时间值一定走这条分支，
		// appendAny 里不必再为它留一个（也留不住——time.Time 实现了 fmt.Stringer，
		// 那个 case 会先把它接走）。
		return h.time.appendJSON(buf, v.Time())
	default:
		return h.appendAny(buf, v.Any())
	}
}

// appendAny 渲染 slog.Any 交进来的任意值。
//
// 显式先试 error 与 fmt.Stringer：两者都是日志里的高频类型，走各自的方法比反射
// 省一次分配，语义也更明确（error 就该打 Error()，而不是它的结构体字段）。
// 顺序上 error 在 fmt.Stringer 之前——两者都实现时，Error() 才是这个值想说的话。
//
// 结构体、切片、map 交给 jsonv2 按 JSON 结构渲染，其中嵌套的 error 由 errorAsText
// 接管。三条副作用要知道：值上的 json 标签是生效的，打了 json:"-" 的字段不会出现在
// 日志里，omitempty/omitzero 的字段取零值时也不会；含 time.Duration 这类 jsonv2 编不
// 出来的成员时，整个值会回落成 fmt 的形状；嵌套的 []byte 按 base64，只有顶层的
// []byte 才按文本或十六进制。
func (h *JSONHandler) appendAny(buf []byte, a any) (out []byte) {
	switch x := a.(type) {
	case nil:
		return append(buf, "null"...)
	case string:
		return h.appendText(buf, x)
	}

	// 下面每一条分支都会调到别人的代码：typed-nil 接收者上的 Error()、
	// 第三方类型自己写崩的 String()、MarshalJSON 里的越界。Handle 的典型调用点
	// 恰恰是某个 recover 内部，在那里再炸一次等于把兜底本身炸掉，所以就地兜住，
	// 把已经写了一半的字节退回去，换成一个说得清缘由的占位串。
	mark := len(buf)
	defer func() {
		if r := recover(); r != nil {
			out = h.appendJSONString(buf[:mark], fmt.Sprintf("<log value panicked: %v>", r))
		}
	}()

	switch x := a.(type) {
	case error:
		return h.appendText(buf, x.Error())
	case fmt.Stringer:
		return h.appendText(buf, x.String())
	case []byte:
		// 合法 UTF-8 按文本渲染：这里打出来的字节大多是想直接看内容的。
		// 但 JSON 字符串装不下任意字节序列，非法字节只能变成替换字符——而握手帧、
		// 密钥片段恰恰是要按字节看的，被替换等于内容当场作废，那种情况改走十六进制。
		if utf8.Valid(x) {
			return h.appendText(buf, string(x))
		}
		buf = append(buf, `"0x`...)
		buf = hex.AppendEncode(buf, x)
		return append(buf, '"')
	}

	// 不用流式的 MarshalWrite 直接写进缓冲：编到一半失败会在记录里留下半截 JSON，
	// 而这条路径最常见的失败恰恰来自值本身有问题——那时更需要这条记录还能读。
	b, err := json.Marshal(a, jsontext.AllowInvalidUTF8(true), errorAsText)
	if err != nil {
		// 通道、函数、循环引用这类编不出 JSON 的值回落到 fmt：日志里宁可留一个
		// 粗糙的表示，也不要因为一个字段把整条记录写成非法 JSON。
		return h.appendText(buf, fmt.Sprint(a))
	}
	// jsonv2 只做 JSON 要求的最小转义，结构体内部的字符串同样可能带着不可打印
	// 字符出来，与 appendJSONString 走一样的兜底，理由见那里。
	return h.appendSanitized(buf, b)
}

// appendText 写入一个文本值：单行写成 JSON 字符串，多行拆成 JSON 字符串数组。
//
// 判定看的是**渲染之后的字符串**，不是 slog 的 Kind——多行值最常见的形态并不是
// slog.String("stack", …)，而是一个把堆栈拼进了 Error() 文本里的 error，
// 按 Kind 判定会整类漏掉。
func (h *JSONHandler) appendText(buf []byte, s string) []byte {
	if out, ok := h.appendEmbeddedJSON(buf, s); ok {
		return out
	}
	if strings.IndexByte(s, '\n') < 0 {
		return h.appendJSONString(buf, s)
	}
	// 尾部的空行不携带信息（堆栈与按行汇总的错误都以换行收尾），留着只会在数组
	// 末尾多出若干个 ""。cutset 带上 '\r' 才能同时管住 CRLF 收尾的文本。
	// 行内的空行是内容，保留。
	body := strings.TrimRight(s, "\r\n")
	// 削完尾只剩一行的（"文本\n" 这种），展开成单元素数组白占一个物理行却什么也没
	// 说清，退回单行字符串，原值一个字节不改。
	if strings.IndexByte(body, '\n') < 0 {
		return h.appendJSONString(buf, s)
	}

	buf = append(buf, '[')
	first := true
	for line := range strings.SplitSeq(body, "\n") {
		if !first {
			buf = append(buf, ',')
		}
		first = false
		// 每个元素单独占一个物理行，且一律以制表符开头，理由见 JSONHandler 的说明。
		// 行尾的 CR 一并去掉，CRLF 文本因此被规整成 LF。
		buf = append(buf, '\n', '\t')
		buf = h.appendJSONString(buf, strings.TrimSuffix(line, "\r"))
	}
	return append(buf, ']')
}

// appendEmbeddedJSON 处理「值本身就是一段 JSON 文本」的字段：原样嵌进记录，
// 而不是当普通字符串再转义一遍。命中时返回 true。
//
// 框架的运行统计就是这么打的——一个 slog.String，内容是一整个 JSON 对象。当字符串
// 渲染出来是满屏的 \"，既没法读，也没法用 jq 钻进去查；嵌进去之后它就是记录里的一个
// 真正的子对象。
//
// 只认以 '{' 或 '[' 打头的值，这一条判断挡掉了绝大多数误伤：裸的数字、true/false/null
// 与带引号的字符串同样是合法 JSON，把玩家取名叫 "42" 的字段变成数字 42 才是真的坏事。
// 首字节对上之后才做完整校验，正常字段一次都不会走到那里。
//
// 用 Compact 而不是 IsValid：它同时完成校验与去空白，嵌进来的值因此不会带着自己的
// 换行进到记录里，把一条记录撑成几行。校验失败（不合法、或后面还跟着别的东西）
// 就当普通字符串处理，不会有半个值漏进输出。
func (h *JSONHandler) appendEmbeddedJSON(buf []byte, s string) ([]byte, bool) {
	if len(s) < 2 || (s[0] != '{' && s[0] != '[') {
		return buf, false
	}
	// 转成 Value 会复制一份：Compact 就地改写，不能动调用方的字符串。
	v := jsontext.Value(s)
	if v.Compact() != nil {
		return buf, false
	}
	return h.appendSanitized(buf, v), true
}

// appendJSONString 把 s 写成一个 JSON 字符串字面量。成员名、消息与字段值都走这里。
//
// jsontext.AppendQuote 按 RFC 8785 只做**最小**转义：双引号、反斜杠与 0x20 以下的
// 控制字符会被转义，非法 UTF-8 字节替换成 U+FFFD。ASCII 的 ESC（0x1B）因此进不了
// 输出，但同样能操纵终端的那些字符它一概放行——U+007F（DEL）、U+0080–U+009F
// （C1 控制符，其中 U+009B 是八位形式的 CSI，部分终端把它当 ESC[ 解释）、
// U+2028/U+2029（行分隔符）、U+202E 一类的双向覆写符（能让一段文本在终端上倒着
// 显示，用来伪装内容），以及零宽字符。
//
// 所以判定不按「枚举已知的坏字符」写，而是按 unicode.IsPrint 取反：不可打印的
// 一律转成 \uXXXX。这与改造前那版靠 strconv.Quote 得到的口径一致，不会因为又冒出
// 一个新的控制符类别而漏掉。
//
// 只要值全是 ASCII 可打印字符（日志里的绝大多数）就走 AppendQuote 快路径，
// 一次逐字节扫描即可判定；含任何非 ASCII 才转入逐 rune 的慢路径。
func (h *JSONHandler) appendJSONString[S ~string | ~[]byte](buf []byte, s S) []byte {
	if h.indexUnprintable(s) < 0 {
		buf, _ = jsontext.AppendQuote(buf, s)
		return buf
	}
	return h.appendEscaped(buf, s)
}

// appendEscaped 是 appendJSONString 的慢路径：逐 rune 写出，不可打印的转成 \uXXXX。
func (h *JSONHandler) appendEscaped[S ~string | ~[]byte](buf []byte, s S) []byte {
	buf = append(buf, '"')
	for _, r := range string(s) {
		switch r {
		case '"':
			buf = append(buf, '\\', '"')
		case '\\':
			buf = append(buf, '\\', '\\')
		case '\n':
			buf = append(buf, '\\', 'n')
		case '\r':
			buf = append(buf, '\\', 'r')
		case '\t':
			buf = append(buf, '\\', 't')
		case '\b':
			buf = append(buf, '\\', 'b')
		case '\f':
			buf = append(buf, '\\', 'f')
		default:
			// 非法 UTF-8 字节在 range 中给出 U+FFFD，与 AppendQuote 的处理一致。
			if unicode.IsPrint(r) {
				buf = utf8.AppendRune(buf, r)
				continue
			}
			buf = h.appendRuneEscape(buf, r)
		}
	}
	return append(buf, '"')
}

// appendSanitized 把一段**已经是 JSON 文本**的字节原样写出，只把其中不可打印的
// 字符换成 \uXXXX。
//
// 这么改是安全的：JSON 的结构性字符（花括号、引号、逗号、冒号）全是 ASCII，
// 不可打印字符只可能出现在字符串字面量内部，替换动不到边界。
func (h *JSONHandler) appendSanitized(buf, b []byte) []byte {
	if h.indexUnprintable(b) < 0 {
		return append(buf, b...)
	}
	for _, r := range string(b) {
		if r == '\t' || unicode.IsPrint(r) {
			buf = utf8.AppendRune(buf, r)
			continue
		}
		buf = h.appendRuneEscape(buf, r)
	}
	return buf
}
