package xslog

import (
	"context"
	"encoding/hex"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"fmt"
	"io"
	"log/slog"
	"slices"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"
)

// ConsoleHandler 是给人读的 slog.Handler：一条记录一行，消息与字段都以 k="v" 跟在
// 行首之后；只有含换行的值（panic 堆栈、按行汇总的错误）才另起一个竖线块。
//
//	2026-08-20T11:48:55.756+08:00 INFO [gate/gate.go:100] message="listen on" network="tcp" address="[::]:8080"
//	2026-08-20T11:48:55.912+08:00 ERROR [wmap/ctl_march.go:562] message="march frame panic" module="wmap" panic="runtime error: invalid memory address"
//	    stack │ goroutine 42 [running]:
//	          │ runtime/debug.Stack()
//	          │   /usr/local/go/src/runtime/debug/stack.go:26 +0x5e
//
// # 文本值恒定加引号，数字与布尔不加
//
// 消息与文本值一律带引号：它们可以含空格，不加引号就看不出一个值在哪儿结束、
// 下一个键名从哪儿开始——只能靠眼睛去找下一个 '='，而值自己含 '=' 时那个办法还会失灵。
// 恒定加而不是按需加，是为了不让同一个字段的形态随内容跳变，读的人每行都要重新判断
// 一次边界在哪。
//
// 数字、布尔与结构化值（走 jsonv2 的结构体、切片、map）不加：它们不可能含空格，
// 而少了那对引号，"这是个数字还是一串数字组成的文本" 一眼就分得开。
// // # 它和 JSONHandler 的分工
//
// 两者渲染的是同一份记录，取舍相反：JSON 那份要能被机器无歧义读回来，所以每个值
// 都带引号与转义、每条记录恒为一个完整的 JSON 值；这一份只服务于「一个人正盯着
// 控制台」，因此值不加引号、时间写成空格分隔的本地时间、分组前缀压成点分键名——都是为了让
// 一屏里有效信息更多。它**不保证可解析**，不要拿它去喂采集器。
//
// # 头行的顺序是定死的，除非把它接管过去
//
// 不设自定义头时，头行恒为「时间 级别 [位置] 消息」。定死是因为绝大多数人要的只是
// 「每行都长一个样，扫得快」，而为此可调的每一个旋钮都要有人去读文档、去和同事对齐
// 用哪一种。
//
// 真要动它，[WithConsoleHeader] 把「时间之后、消息之前」这一段整个交出去：级别与
// 位置作为参数给到，写在哪一格、写成什么样（缩写成 I/W/E、定宽对齐、夹在两个自定义
// 项中间）都由调用方决定，这里一样都不再写。代价是每条日志一次函数调用与一次切片
// 分配——日志量大而只想放一段恒定标记的进程，闭包里返回一个提前建好的切片就好。
//
// # 为什么要按键名对齐
//
// 字段是竖着读的：一条记录里有七八个字段时，眼睛沿着等号那一列往下扫最快。
// 列宽按本条记录里最长的键名算，而不是取一个固定值——固定值要么在短键名时留出
// 一大片空白，要么在长键名时被撑破，两种都比对齐本身更碍眼。
//
// # 安全性质与 JSON 那份一致
//
// 值里的不可打印字符（终端控制序列、双向覆写符、零宽字符）一律转义成 \uXXXX，
// 玩家可控的文本因此操纵不了终端；多行值的续行一律缩进，顶格的行恒为一条记录的开头，
// 值里被塞进换行也伪造不出一条假记录。
type ConsoleHandler struct {
	renderer

	mu     *sync.Mutex
	writer io.Writer
	level  slog.Leveler

	// time 是行首时间与 time.Time 字段值共用的渲染设置，装配时由 [WithTimeLayout]
	// 的取值解析出来。
	time timeFormat

	// header 逐条日志组装「时间之后、消息之前」的全部内容，设了它就由它全权负责
	// 级别与位置的排布，Handler 自己不再写那两样；没设即为 nil，走默认的
	// 「级别 [位置]」。语义与代价见 [ConsoleHeaderFunc]。
	header ConsoleHeaderFunc

	// attrs 是 With 附加的固定字段，键名与值都已按**添加时**的状态渲染好。
	// 分组归属必须按添加时的状态算，理由见 JSONHandler 的 preformatted 说明。
	attrs []consoleAttr

	// groupPrefix 是当前生效的分组前缀，形如 "battle.frame"；空表示在根层。
	// 控制台这一份把分组压成点分键名而不是嵌套结构：缩进已经用来区分「头行 / 字段 /
	// 多行块」三层了，再叠一层分组缩进，读者要数空格才知道自己在哪一层。
	groupPrefix string
}

// consoleAttr 是一个已经摊平并渲染好的字段。
type consoleAttr struct {
	// key 含分组前缀，形如 "frame.pos.x"。
	key string
	// text 是渲染后的值。
	text string
	// block 为真表示值里有换行，写出时另起一个竖线块。
	block bool
	// quote 为真表示这是个文本值，写出时要加引号。
	quote bool
}

// NewConsoleHandler 创建控制台 Handler。
//
// 输出目标走 [WithWriter]，不设即标准输出；渲染上的可调项走 [WithConsoleHeader] /
// [WithTimeLayout] 这类选项，不传即用默认。[WithJSONHeader] 是 JSON 那份的，
// 传进来这里不看。
//
// 不上色：ANSI 转义序列只在真终端里是颜色，重定向进文件、管道给别的工具、
// 或者落到不认它的终端上，就是一串扎眼的乱码；而"这次到底算不算真终端"要靠猜，
// 猜错的代价全落在读日志的人身上。对齐与缩进已经把层次分清楚了。
func NewConsoleHandler(opts ...Option) *ConsoleHandler {
	o := newOptions(opts)
	return &ConsoleHandler{
		mu:     &sync.Mutex{},
		writer: o.output(),
		level:  o.level,
		time:   newTimeFormat(o.timeLayout),
		header: o.consoleHeader,
	}
}

// Enabled 检查指定日志级别是否开启。
func (h *ConsoleHandler) Enabled(_ context.Context, level slog.Level) bool {
	return level >= h.level.Level()
}

// WithAttrs 返回附加了固定字段的子 Handler，对应 slog.Logger.With。
func (h *ConsoleHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	if len(attrs) == 0 {
		return h
	}
	// Clip 让 cap == len，下面的 append 必定分配新数组。不这么做的话，由同一个父
	// Handler 派生出的两个子 Handler 会往同一块底层数组里写，后派生的那个会把先
	// 派生的字段悄悄覆盖掉。
	next := *h
	next.attrs = slices.Clip(h.attrs)
	for _, a := range attrs {
		next.attrs = h.flatten(next.attrs, h.groupPrefix, a)
	}
	if len(next.attrs) == len(h.attrs) {
		return h
	}
	return &next
}

// WithGroup 返回附加了字段分组的子 Handler，对应 slog.Logger.WithGroup。
func (h *ConsoleHandler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}
	next := *h
	if next.groupPrefix == "" {
		next.groupPrefix = name
	} else {
		next.groupPrefix += "." + name
	}
	return &next
}

// Handle 按控制台格式写入一条记录。
//
// 整条记录（头行、全部字段、多行块）拼在同一个缓冲里，在同一次加锁下一次写出：
// 分两次写会让并发的两条记录交错，而交错出来的多行块既读不了也对不上归属。
func (h *ConsoleHandler) Handle(_ context.Context, r slog.Record) error {
	bp := bufPool.Get().(*[]byte)
	buf := (*bp)[:0]
	defer func() {
		if cap(buf) <= maxPooledBuf {
			*bp = buf
			bufPool.Put(bp)
		}
	}()

	ap := attrPool.Get().(*[]consoleAttr)
	attrs := (*ap)[:0]
	defer func() {
		if cap(attrs) > maxPooledAttrs {
			return
		}
		// 清空后再归还：consoleAttr 里的字符串留在池子里，会让一条早就写完的日志
		// 把它引用的那块内存一直吊着不放。
		clear(attrs[:cap(attrs)])
		*ap = attrs
		attrPool.Put(ap)
	}()

	attrs = append(attrs, h.attrs...)
	r.Attrs(func(a slog.Attr) bool {
		attrs = h.flatten(attrs, h.groupPrefix, a)
		return true
	})

	// 头行：时间 [自定义头] 级别 [位置] 消息  k=v k=v …
	//
	// 每个片段自己写前导空格，可选的片段（时间、自定义头、位置）省略时才不会
	// 在行里留下多余的空格。
	if !r.Time.IsZero() {
		// 零值时间整项省略（slog 规约，JSONHandler 同样如此）：手工构造的 Record
		// 常常不带时间，打出 0001-01-01 只会让看的人以为机器的时钟坏了。
		buf = h.time.append(buf, r.Time)
	}
	if h.header != nil {
		// 级别与位置连同顺序一起交了出去（[WithConsoleHeader]），这里一样都不写：
		// 再写一遍就是重复，而调用方无从把它去掉。
		buf = h.appendHeader(buf, r)
	} else {
		// 前面确实写了东西才补分隔空格：时间可能整项省略，
		// 那时行首不该凭空多出一个空格。
		if len(buf) > 0 {
			buf = append(buf, ' ')
		}
		buf = append(buf, r.Level.String()...)
		// 位置块在 PC 为 0（手工构造的 Record）或取不到文件名时整段省略。
		// 分隔空格归后面那个片段所有，省略时才不会在级别与消息之间留下两个空格。
		if loc := h.source(r.PC); loc != "" {
			buf = append(buf, ' ', '[')
			buf = h.appendPlainText(buf, loc)
			buf = append(buf, ']')
		}
	}
	// 消息也写成一个字段。它是自由文本、可以带空格，裸着写就看不出它在哪儿结束、
	// 第一个字段从哪儿开始。
	buf = append(buf, ` message=`...)
	buf = h.appendQuoted(buf, r.Message)

	// 单行字段跟在消息后面；含换行的留到下面各起一个竖线块。
	width := 0
	for _, a := range attrs {
		if a.block {
			// 列宽只按有块的那些字段算，几个块的左边界因此对齐。
			width = max(width, len(a.key))
			continue
		}
		buf = append(buf, ' ')
		buf = append(buf, a.key...)
		buf = append(buf, '=')
		if a.quote {
			buf = h.appendQuoted(buf, a.text)
		} else {
			buf = h.appendPlainText(buf, a.text)
		}
	}
	buf = append(buf, '\n')

	for _, a := range attrs {
		if a.block {
			buf = h.appendBlock(buf, a, width)
		}
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	_, err := h.writer.Write(buf)
	return err
}

// appendHeader 写出自定义头的各项，项与项之间用一个空格隔开。
//
// 空串整项跳过（连同分隔空格）：这个头逐条重算，「这条没有 trace id」是常态而不是
// 配错了，让它在行里留下一个孤零零的空格，读的人还要去数是谁缺了。
//
// 行首那一项不加前导空格：时间可能整项省略（手工构造的 Record），
// 那时行首不该凭空多出一个空格。
func (h *ConsoleHandler) appendHeader(buf []byte, r slog.Record) []byte {
	for _, f := range h.headerFields(r) {
		if f == "" {
			continue
		}
		if len(buf) > 0 {
			buf = append(buf, ' ')
		}
		buf = h.appendPlainText(buf, f)
	}
	return buf
}

// headerFields 调一次调用方的头函数，就地兜住它自己的 panic。
//
// 兜底单独占一个函数，是为了让没设头的那条路径一分钱都不花：带 recover 的 defer
// 无法开放编码，写在 Handle 里就是每条日志都要付。
//
// 兜住而不是让它炸出去：这个包的 Handler 常常正是在 recover 内部被调用的
// （框架兜住一个 panic，随即打一条 ERROR 把堆栈记下来），在那里再炸一次，
// 丢掉的是那条本来要留下的现场。
func (h *ConsoleHandler) headerFields(r slog.Record) (fields []string) {
	defer func() {
		if v := recover(); v != nil {
			fields = []string{fmt.Sprintf("<header panicked: %v>", v)}
		}
	}()
	return h.header(r.Level, h.source(r.PC))
}

// flatten 把一个 Attr 摊平进列表：分组递归展开成点分键名，空 Attr 与空组丢弃。
func (h *ConsoleHandler) flatten(dst []consoleAttr, prefix string, a slog.Attr) []consoleAttr {
	a.Value = a.Value.Resolve()
	if a.Equal(slog.Attr{}) {
		return dst
	}
	key := a.Key
	if prefix != "" && key != "" {
		key = prefix + "." + key
	} else if key == "" {
		key = prefix
	}
	if a.Value.Kind() == slog.KindGroup {
		sub := a.Value.Group()
		// 空组整个忽略；无名组内联展开，不加前缀（都是 slog 规约）。
		for _, ga := range sub {
			dst = h.flatten(dst, key, ga)
		}
		return dst
	}
	// 键名在这里就定成最终显示形态，而不是留到写出时：竖线块的列宽要按**渲染之后**
	// 的长度算，一个 ESC 转义完占六个字符，按原串长度补空格会把竖线列撑歪。
	text, block, quote := h.render(a.Value)
	return append(dst, consoleAttr{key: h.displayKey(key), text: text, block: block, quote: quote})
}

// displayKey 是键名的显示形态：先转义不可打印字符，再在会读歪 k=v 边界时
// （含空白、'='、引号，或为空）加一对引号。
//
// 值恒定带引号已经挡住了大部分歧义，但数字与布尔是裸写的，`a b=1` 这种键名会让
// 「上一个字段到哪儿结束」重新变得不明。键名带这些字符是代码味道（规范要求键名是
// 字面量标识符），但真出现时不该让整行读不下去。
func (h *ConsoleHandler) displayKey(k string) string {
	k = h.plainText(k)
	if k != "" && !strings.ContainsAny(k, " \t=\"") {
		return k
	}
	return string(h.appendQuoted(nil, k))
}

// appendQuoted 加一对引号写出一段文本，内部的引号转义成 \"。
//
// 不转义的话 k="a"b" 看上去像是断在了中间，而断句正是加引号的全部目的。
// 除此之外不做别的转义——这一份本来就不保证可解析。
func (h *ConsoleHandler) appendQuoted(buf []byte, s string) []byte {
	buf = append(buf, '"')
	// 双引号是 ASCII，不可能出现在多字节 UTF-8 序列的内部，按字节切分是安全的。
	for {
		i := strings.IndexByte(s, '"')
		if i < 0 {
			break
		}
		buf = h.appendPlainText(buf, s[:i])
		buf = append(buf, '\\', '"')
		s = s[i+1:]
	}
	buf = h.appendPlainText(buf, s)
	return append(buf, '"')
}

// appendBlock 把一个多行值写成竖线块：第一行接在键名后面，其余行缩进到同一列。
//
// width 是本条记录里最长的**多行字段**键名，几个块因此左边界对齐。
// 块里的值不加引号——竖线已经把边界说清楚了，再加引号只会让每一行多两个字符。
//
// 每一行都排在竖线之后，顶格的行恒为一条记录的开头：值里被塞进换行（客户端上行的
// 脚本正文、远端返回的错误文本）也伪造不出一条以假时间戳打头的记录。
func (h *ConsoleHandler) appendBlock(buf []byte, a consoleAttr, width int) []byte {
	indent := make([]byte, 0, width+4)
	indent = append(indent, "    "...)
	for range width {
		indent = append(indent, ' ')
	}

	first := true
	for line := range strings.SplitSeq(a.text, "\n") {
		if first {
			buf = append(buf, "    "...)
			buf = append(buf, a.key...)
			for i := len(a.key); i < width; i++ {
				buf = append(buf, ' ')
			}
			first = false
		} else {
			buf = append(buf, indent...)
		}
		buf = append(buf, " │ "...)
		// 制表符已在 render 里展开过，这里只需去掉 CRLF 留下的回车。
		buf = h.appendPlainText(buf, strings.TrimSuffix(line, "\r"))
		buf = append(buf, '\n')
	}
	return buf
}

// render 把一个值渲染成控制台文本，并报告它是不是多行、要不要加引号。
//
// 与 JSON 那份取舍相近但不等价：这边的引号只为断句而不是语法（因此块里不加、
// 数字不加），nil 写成 <nil> 而不是 null。复合值仍按 JSON 结构渲染——
// `{"X":1,"Y":2}` 比 fmt 的 `{1 2}` 多说了字段名。
func (h *ConsoleHandler) render(v slog.Value) (text string, block, quote bool) {
	switch v.Kind() {
	case slog.KindString:
		text, quote = v.String(), true
	case slog.KindInt64:
		return strconv.FormatInt(v.Int64(), 10), false, false
	case slog.KindUint64:
		return strconv.FormatUint(v.Uint64(), 10), false, false
	case slog.KindFloat64:
		return strconv.FormatFloat(v.Float64(), 'g', -1, 64), false, false
	case slog.KindBool:
		return strconv.FormatBool(v.Bool()), false, false
	case slog.KindDuration:
		return v.Duration().String(), false, true
	case slog.KindTime:
		// 时间戳形态是个裸数字，跟着「数字不加引号」那条规则走。
		return h.time.text(v.Time()), false, !h.time.numeric()
	default:
		text, quote = h.renderAny(v.Any())
	}

	// 值本身就是一段 JSON 时按结构化值处理：原样写出、不加引号。
	// 框架的运行统计正是这么打的——一个 slog.String，内容却是一整个 JSON 对象；
	// 当普通文本加引号，里面每个引号都要转义成 \"，满屏反斜杠，什么都看不出来。
	if quote {
		if compact, ok := h.embeddedJSON(text); ok {
			return compact, false, false
		}
	}

	// 制表符一律展开成两个空格。它在终端里跳到下一个制表位，缩进量随终端设置而变：
	// 堆栈的层次感会没掉，跟在消息后面的单行值还会把后面的字段推得忽左忽右。
	// 放在这里做，竖线块与单行值就只有这一条规则。
	if strings.IndexByte(text, '\t') >= 0 {
		text = strings.ReplaceAll(text, "\t", "  ")
	}
	if strings.IndexByte(text, '\n') < 0 {
		return text, false, quote
	}
	// 尾部的空行不携带信息（堆栈与按行汇总的错误都以换行收尾）。削完之后可能只剩
	// 一行，那就没必要为它开一个块。
	body := strings.TrimRight(text, "\r\n")
	return body, strings.IndexByte(body, '\n') >= 0, quote
}

// embeddedJSON 判断一段文本本身是不是一个完整的 JSON 对象或数组，是则返回去掉多余
// 空白之后的形态。
//
// 只认 '{' 与 '[' 打头，这一条判断挡掉了绝大多数误伤：裸的数字、true/false/null 与
// 带引号的字符串同样是合法 JSON，把玩家取名叫 "42" 的字段变成数字才是真的坏事。
// 首字节对上之后才做完整校验，正常字段一次都不会走到那里。
//
// 用 Compact 而不是 IsValid：它同时完成校验与去空白，嵌进来的值因此不会带着自己的
// 换行把一条记录撑成几行；后面还跟着别的东西（`{"a":1} trailing`）一律判为不合法，
// 不会有半个值漏进输出。
func (h *ConsoleHandler) embeddedJSON(s string) (string, bool) {
	if len(s) < 2 || (s[0] != '{' && s[0] != '[') {
		return "", false
	}
	// 转成 Value 会复制一份：Compact 就地改写，不能动调用方的字符串。
	v := jsontext.Value(s)
	if v.Compact() != nil {
		return "", false
	}
	return string(v), true
}

// renderAny 渲染 slog.Any 交进来的任意值，并报告要不要加引号。
// 取舍与 JSON 那份一致：error 与 fmt.Stringer 走各自的方法，复合值交给 jsonv2，
// 外部代码 panic 就地兜住。
func (h *ConsoleHandler) renderAny(a any) (out string, quote bool) {
	switch x := a.(type) {
	case nil:
		return "<nil>", false
	case string:
		return x, true
	}

	// 这些分支都会调到别人的代码：typed-nil 接收者上的 Error()、第三方类型自己
	// 写崩的 String()。控制台 Handler 同样常被 recover 内部调用，在那里再炸一次
	// 等于把兜底本身炸掉。
	defer func() {
		if r := recover(); r != nil {
			out, quote = fmt.Sprintf("<log value panicked: %v>", r), true
		}
	}()

	switch x := a.(type) {
	case error:
		return x.Error(), true
	case fmt.Stringer:
		return x.String(), true
	case []byte:
		// 合法 UTF-8 按文本，否则按十六进制：握手帧、密钥片段这类字节按文本渲染
		// 只会得到一串替换字符。十六进制不含空格，不必加引号。
		if utf8.Valid(x) {
			return string(x), true
		}
		return "0x" + hex.EncodeToString(x), false
	}

	b, err := json.Marshal(a, jsontext.AllowInvalidUTF8(true), errorAsText)
	if err != nil {
		return fmt.Sprint(a), true
	}
	return string(b), false
}
