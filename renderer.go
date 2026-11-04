package xslog

import (
	"fmt"
	"net/url"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// renderer 提供编码器共用的调用位置解析、文本检查和字符转义方法。
type renderer struct{}

// panicText 安全描述外部方法的 panic。非字符串载荷只打印类型，避免调用其方法
// 或递归遍历循环引用，在错误处理过程中再次触发 panic 或栈溢出。
func (rd renderer) panicText(cause any) string {
	if text, ok := cause.(string); ok {
		return "<log value panicked: " + text + ">"
	}
	return fmt.Sprintf("<log value panicked: %T>", cause)
}

// sourceInfo 保存一个 PC 的调用信息和缩短位置，放入缓存后只读。
type sourceInfo struct {
	location string
	file     string
	fileName string
	line     int
	function string
	pkg      string
}

// timeKind 是时间的渲染方式。
type timeKind uint8

const (
	// timeText 使用 Go 时间布局格式化时间。
	timeText timeKind = iota
	// timeUnixSec 按秒输出 Unix 时间戳。
	timeUnixSec
	// timeUnixMilli 按毫秒输出 Unix 时间戳。
	timeUnixMilli
	// timeUnixMicro 按微秒输出 Unix 时间戳。
	timeUnixMicro
	// timeUnixNano 按纳秒输出 Unix 时间戳。
	timeUnixNano
)

// timeFormat 保存占位符选定的 Go 时间布局或 Unix 时间戳类型。
type timeFormat struct {
	// layout 只在 kind 为 timeText 时有效。
	layout string
	kind   timeKind
}

// parse 解析 Go 时间布局或 Unix 时间戳格式，返回新的格式值。
func (f timeFormat) parse(layout string) timeFormat {
	f.layout = ""
	switch layout {
	case layoutUnix:
		f.kind = timeUnixSec
	case layoutUnixMilli:
		f.kind = timeUnixMilli
	case layoutUnixMicro:
		f.kind = timeUnixMicro
	case layoutUnixNano:
		f.kind = timeUnixNano
	default:
		f.layout, f.kind = layout, timeText
	}
	return f
}

// numeric 报告时间是否输出为不带引号的 Unix 时间戳。
func (f timeFormat) numeric() bool { return f.kind != timeText }

// append 将按当前格式编码的时间追加到 buf。
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

// text 返回按当前格式编码的时间文本。
func (f timeFormat) text(t time.Time) string {
	if f.kind == timeText {
		return t.Format(f.layout)
	}
	return string(f.append(nil, t))
}

// source 返回缓存的调用信息；PC 为 0 时返回空值，各字段独立判断是否存在。
func (rd renderer) source(pc uintptr) sourceInfo {
	if pc == 0 {
		return sourceInfo{}
	}
	if v, ok := srcCache.Load(pc); ok {
		return v.(sourceInfo)
	}
	loc := rd.renderSource(pc)
	srcCache.Store(pc, loc)
	return loc
}

// renderSource 解析程序计数器，提取文件、函数名和包路径，并生成缩短位置。
// 缓存由 source 管理。
// 首次并发查询同一位置时可能重复解析。
func (rd renderer) renderSource(pc uintptr) sourceInfo {
	frame, _ := runtime.CallersFrames([]uintptr{pc}).Next()
	info := sourceInfo{
		file:     frame.File,
		line:     frame.Line,
		function: rd.functionName(frame.Function),
		pkg:      rd.packagePath(frame.Function),
	}
	if frame.File != "" {
		info.fileName = filepath.Base(frame.File)
		path := rd.trimmedPath(frame.File)
		// 路径含 @ 时才匹配模块版本后缀，完整文件路径保留原值。
		if strings.IndexByte(path, '@') >= 0 {
			path = moduleVersionPattern.ReplaceAllString(path, "")
		}
		info.location = path + ":" + strconv.Itoa(frame.Line)
	}
	return info
}

// functionName 去掉运行时符号的包路径，保留接收者、泛型参数和闭包后缀。
func (rd renderer) functionName(function string) string {
	prefix, _, _ := strings.Cut(function, "[")
	start := strings.LastIndexByte(prefix, '/') + 1
	dot := strings.IndexByte(prefix[start:], '.')
	if dot < 0 {
		return function
	}
	return function[start+dot+1:]
}

// packagePath 按运行时符号规则提取包路径，忽略泛型参数并还原路径转义。
func (rd renderer) packagePath(function string) string {
	function, _, _ = strings.Cut(function, "[")
	start := strings.LastIndexByte(function, '/') + 1
	dot := strings.IndexByte(function[start:], '.')
	if dot < 0 {
		return ""
	}
	prefix := function[:start+dot]
	if path, err := url.PathUnescape(prefix); err == nil {
		return path
	}
	return prefix
}

// trimmedPath 缩短使用斜杠分隔的运行时文件路径，行号由 renderSource 追加。
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

// indexUnprintable 返回需要逐 rune 检查的首个字节下标。
// 可打印 ASCII 字符和制表符走快路径，其他控制字符与非 ASCII 字节触发慢路径。
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

// appendPlainText 写入不加引号的文本，将制表符以外的不可打印字符转义。
// ASCII 文本直接追加；其他文本按 unicode.IsPrint 逐 rune 判断。
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

// plainText 返回转义后的文本，可打印 ASCII 与制表符直接复用输入。
// 列宽计算使用转义后的文本，避免控制字符的转义长度破坏对齐。
func (rd renderer) plainText(s string) string {
	if rd.indexUnprintable(s) < 0 {
		return s
	}
	return string(rd.appendPlainText(nil, s))
}
