package xslog

import (
	"runtime"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// renderer 收拢两个 Handler 共用的渲染原语：调用位置的解析与缓存，
// 以及字符串里不可打印字符的扫描与转义。
//
// 做成一个可内嵌的空结构体而不是一组包级函数，是为了让两边都以 h.xxx 的方法
// 形式调用，与各自 Handler 自身的方法读起来一致。
type renderer struct{}

// timeKind 是时间的渲染方式。
type timeKind uint8

const (
	// timeText 走 layout 串，交给 time.Time 自己格式化。
	timeText timeKind = iota
	timeUnixSec
	timeUnixMilli
	timeUnixMicro
	timeUnixNano
)

// timeFormat 是两个 Handler 共用的时间渲染设置，由 [WithTimeLayout] 的取值定出。
//
// 行首时间（JSON 那份的 @time）与 time.Time 字段值都走它，两者因此恒为同一种形态：
// 不一致的话，想拿日志行里的时间去比对某个时间字段会对不上，而那正是这两个东西
// 最常一起被读的场合。
type timeFormat struct {
	// layout 只在 kind 为 timeText 时有效。
	layout string
	kind   timeKind
}

// newTimeFormat 把 [WithTimeLayout] 收到的串解析成渲染设置，装配时算一次。
//
// 解析放在这里而不是每次渲染时判断：一个 Handler 的时间形态定下来就不再变，
// 每条日志重新比对一遍四个哨兵串是白花的钱。
func newTimeFormat(layout string) timeFormat {
	switch layout {
	case LayoutUnix:
		return timeFormat{kind: timeUnixSec}
	case LayoutUnixMilli:
		return timeFormat{kind: timeUnixMilli}
	case LayoutUnixMicro:
		return timeFormat{kind: timeUnixMicro}
	case LayoutUnixNano:
		return timeFormat{kind: timeUnixNano}
	}
	return timeFormat{layout: layout, kind: timeText}
}

// numeric 报告渲染出来的是不是一个裸数字。
//
// JSON 那份据此决定加不加引号，控制台那份据此决定这个值算不算「数字，不加引号」
// ——两处问的是同一个问题，答案因此只该有一个来源。
func (f timeFormat) numeric() bool { return f.kind != timeText }

// append 写出一个时间。
func (f timeFormat) append(buf []byte, t time.Time) []byte {
	switch f.kind {
	case timeUnixSec:
		return strconv.AppendInt(buf, t.Unix(), 10)
	case timeUnixMilli:
		return strconv.AppendInt(buf, t.UnixMilli(), 10)
	case timeUnixMicro:
		return strconv.AppendInt(buf, t.UnixMicro(), 10)
	case timeUnixNano:
		return strconv.AppendInt(buf, t.UnixNano(), 10)
	default:
		return t.AppendFormat(buf, f.layout)
	}

}

// appendJSON 写出 JSON 里的一个时间值：文本形态自带引号，时间戳裸写成数字。
//
// 引号归这里管而不是留给调用处各写各的：这两件事必须同进同退，分开写就会有一处
// 在换形态时被漏掉，而漏掉的表现是整条记录不再是合法 JSON。
func (f timeFormat) appendJSON(buf []byte, t time.Time) []byte {
	if f.numeric() {
		return f.append(buf, t)
	}
	buf = append(buf, '"')
	buf = f.append(buf, t)
	return append(buf, '"')
}

// text 是 append 的取值版，控制台那份渲染 time.Time 字段值时用。
func (f timeFormat) text(t time.Time) string {
	if f.kind == timeText {
		return t.Format(f.layout)
	}
	return string(f.append(nil, t))
}

// source 返回调用位置 "上级目录名/文件名:行号"；PC 为 0 或取不到文件名时返回空串。
func (rd renderer) source(pc uintptr) string {
	if pc == 0 {
		return ""
	}
	if v, ok := srcCache.Load(pc); ok {
		return v.(string)
	}
	loc := rd.renderSource(pc)
	srcCache.Store(pc, loc)
	return loc
}

// renderSource 真正去解析一个 PC。每个调用点只会走到这里一次，结果由 srcCache 留下。
func (rd renderer) renderSource(pc uintptr) string {
	frame, _ := runtime.CallersFrames([]uintptr{pc}).Next()
	if frame.File == "" {
		return ""
	}
	path := rd.trimmedPath(frame.File)
	// 只有依赖库的缓存路径才含 '@'，项目自身的日志因此完全避开正则。
	if strings.IndexByte(path, '@') >= 0 {
		path = moduleVersionPattern.ReplaceAllString(path, "")
	}
	return path + ":" + strconv.Itoa(frame.Line)
}

// trimmedPath 保留调用位置的「上一级目录名/文件名」，行号由 renderSource 另补。
func (rd renderer) trimmedPath(file string) string {
	idx := strings.LastIndexByte(file, '/')
	if idx < 0 {
		return file
	}
	idx2 := strings.LastIndexByte(file[:idx], '/')
	if idx2 < 0 {
		return file[idx+1:]
	}
	return file[idx2+1:]
}

// indexUnprintable 返回第一个不能原样写出的字节下标：C0 控制符（制表符除外）、
// DEL，以及任何非 ASCII 字节；整串都是 ASCII 可打印字符时返回 -1。
//
// 两个 Handler 的快路径都靠它：日志里绝大多数字符串是纯 ASCII，扫一遍确认之后
// 就能整段照抄，不必逐 rune 判断可打印性。按字节扫而不是按 rune 迭代，
// 是因为这个判断在每个字符串值上都要跑一遍。
//
// C0 也要算进来，哪怕 JSON 那边的 jsontext.AppendQuote 本来就会转义它们：
// 控制台那份没有任何东西替它兜底，ESC 会当场原样进终端。判定放在共用的这一处
// 而不是各写各的，就是为了不让其中一边漏掉一整类字符——它已经漏过一次。
//
// 制表符例外：它可打印、也是堆栈行首缩进的组成部分，若算进来，每一行堆栈都要
// 白走一趟逐 rune 的慢路径。
func (rd renderer) indexUnprintable[S ~string | ~[]byte](s S) int {
	for i := 0; i < len(s); i++ {
		if c := s[i]; (c < 0x20 && c != '\t') || c == 0x7f || c >= 0x80 {
			return i
		}
	}
	return -1
}

// appendRuneEscape 写出一个 \uXXXX 转义；基本多文种平面之外的码点写成代理对。
func (rd renderer) appendRuneEscape(buf []byte, r rune) []byte {
	if r > 0xffff {
		r -= 0x10000
		buf = rd.appendHex4(buf, 0xd800+(r>>10)&0x3ff)
		return rd.appendHex4(buf, 0xdc00+r&0x3ff)
	}
	return rd.appendHex4(buf, r)
}

// appendHex4 写出 \u 加四位小写十六进制。
func (rd renderer) appendHex4(buf []byte, r rune) []byte {
	const digits = "0123456789abcdef"
	return append(buf, '\\', 'u', digits[(r>>12)&0xf], digits[(r>>8)&0xf], digits[(r>>4)&0xf], digits[r&0xf])
}

// appendPlainText 写入一段不加引号的文本，把不可打印字符转义成 \uXXXX。
//
// 日志里有玩家可控的文本——客户端上行帧、上行脚本的正文、远端返回的错误消息。
// 这类值原样进终端有两种坏事：ANSI 与八位形式的控制序列能清屏改色，双向覆写符能让
// 一段文本倒着显示、伪装成别的内容，零宽字符能把东西藏起来。判定按 unicode.IsPrint
// 取反，不枚举已知的坏字符——那样每冒出一个新的控制符类别就会漏一次。
//
// 全 ASCII 可打印（日志里的绝大多数）直接整段拷贝，一次逐字节扫描即可判定。
func (rd renderer) appendPlainText(buf []byte, s string) []byte {
	if rd.indexUnprintable(s) < 0 {
		return append(buf, s...)
	}
	for _, r := range s {
		if r == '\t' || unicode.IsPrint(r) {
			buf = utf8.AppendRune(buf, r)
			continue
		}
		buf = rd.appendRuneEscape(buf, r)
	}
	return buf
}

// plainText 是 appendPlainText 的取值版；整串本来就干净时原样返回，不分配。
//
// 需要它是因为对齐要按**转义之后**的长度算：一个 ESC 转义完占六个字符，
// 按原串长度补空格会把列撑歪。
func (rd renderer) plainText(s string) string {
	if rd.indexUnprintable(s) < 0 {
		return s
	}
	return string(rd.appendPlainText(nil, s))
}
