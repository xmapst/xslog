package xslog

import (
	"encoding/hex"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"fmt"
	"log/slog"
	"math"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// jsonEncoder 实现 JSON 编码，不持有 Handler 的写入或级别状态。
type jsonEncoder struct {
	renderer
	time timeFormat
}

// appendSep 在需要时补上成员之间的逗号。
func (h jsonEncoder) appendSep(buf []byte, sep byte) []byte {
	if sep == 0 {
		return buf
	}
	return append(buf, sep)
}

// appendAttr 写入一个结构化字段，返回写完之后的分隔符状态。
// 命名分组编码为嵌套对象，无名分组内联到当前对象。
func (h jsonEncoder) appendAttr(buf []byte, a slog.Attr, sep byte) ([]byte, byte) {
	a.Value = a.Value.Resolve()
	if a.Equal(slog.Attr{}) || (a.Key == "" && a.Value.Kind() != slog.KindGroup) {
		return buf, sep
	}
	if a.Value.Kind() == slog.KindGroup {
		sub := a.Value.Group()
		// 按 slog.Handler 的约定省略空组。
		if len(sub) == 0 {
			return buf, sep
		}
		// 无名组的成员写入当前对象。
		if a.Key == "" {
			for _, ga := range sub {
				buf, sep = h.appendAttr(buf, ga, sep)
			}
			return buf, sep
		}
		// 组内属性可能全部被省略；记录起点，以便撤销整个空组。
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

// appendValue 按 slog.Value 的类型编码字段值。
// 时间使用已配置的格式，数字和布尔直接编码，分组保留为嵌套对象。
func (h jsonEncoder) appendValue(buf []byte, v slog.Value) []byte {
	switch v.Kind() {
	case slog.KindString:
		return h.appendText(buf, v.String())
	case slog.KindInt64:
		return strconv.AppendInt(buf, v.Int64(), 10)
	case slog.KindUint64:
		return strconv.AppendUint(buf, v.Uint64(), 10)
	case slog.KindFloat64:
		f := v.Float64()
		// JSON 不支持 NaN 和 Inf，将非有限浮点数编码为字符串。
		if math.IsNaN(f) || math.IsInf(f, 0) {
			return h.appendJSONString(buf, strconv.FormatFloat(f, 'g', -1, 64))
		}
		return jsontext.AppendFloat(buf, f, 64)
	case slog.KindBool:
		return strconv.AppendBool(buf, v.Bool())
	case slog.KindDuration:
		// 时长使用带单位的文本，例如 "1.5s"。
		return h.appendString(buf, v.Duration().String())
	case slog.KindTime:
		// slog.AnyValue 将 time.Time 归为 KindTime，在此应用时间格式。
		if h.time.numeric() {
			return h.time.append(buf, v.Time())
		}
		var scratch [64]byte
		return h.appendString(buf, h.time.append(scratch[:0], v.Time()))
	case slog.KindGroup:
		buf = append(buf, '{')
		var sep byte
		for _, a := range v.Group() {
			buf, sep = h.appendAttr(buf, a, sep)
		}
		return append(buf, '}')
	default:
		return h.appendAny(buf, v.Any())
	}
}

// appendAny 编码任意值，error 和 fmt.Stringer 优先使用文本方法。
// 复合值交给 JSON 编码器，嵌套 error 使用 errorAsText。
// 序列化失败时输出包含类型的占位文本；外部方法 panic 时撤销部分输出并写入占位文本。
func (h jsonEncoder) appendAny(buf []byte, a any) (out []byte) {
	switch x := a.(type) {
	case nil:
		return append(buf, "null"...)
	case string:
		return h.appendText(buf, x)
	}

	// 外部文本或序列化方法可能 panic；恢复时只替换当前值，保留前面的字段。
	mark := len(buf)
	defer func() {
		if r := recover(); r != nil {
			out = h.appendString(buf[:mark], h.panicText(r))
		}
	}()

	switch x := a.(type) {
	case error:
		return h.appendText(buf, x.Error())
	case fmt.Stringer:
		return h.appendText(buf, x.String())
	case []byte:
		// 合法 UTF-8 按文本编码，其他字节使用十六进制以保留原始内容。
		if utf8.Valid(x) {
			return h.appendText(buf, string(x))
		}
		buf = append(buf, `"0x`...)
		buf = hex.AppendEncode(buf, x)
		return append(buf, '"')
	}

	// 先完整序列化当前值，避免错误发生时向记录缓冲区写入部分 JSON。
	b, err := json.Marshal(a, jsontext.AllowInvalidUTF8(true), errorAsText)
	if err != nil {
		// 只报告类型；格式化原值会在循环 map 或 slice 上再次无限递归。
		return h.appendString(buf, fmt.Sprintf("<cannot encode %T>", a))
	}
	// 补充转义复合值内部字符串中的不可打印字符。
	return h.appendSanitized(buf, b)
}

// appendText 优先嵌入完整的 JSON 对象或数组，否则编码为字符串。
// 其余文本遵循统一的单行字符串或多行数组规则。
func (h jsonEncoder) appendText(buf []byte, s string) []byte {
	if out, ok := h.appendEmbeddedJSON(buf, s); ok {
		return out
	}
	return h.appendString(buf, s)
}

// appendString 编码字符串值，含 LF 时交给 appendMultiline 判断是否按行展开。
// 字符串和字节切片共用扫描路径，只有发现 LF 时才进入多行处理。
func (h jsonEncoder) appendString[S ~string | ~[]byte](buf []byte, s S) []byte {
	for index := 0; index < len(s); index++ {
		if s[index] == '\n' {
			return h.appendMultiline(buf, string(s))
		}
	}
	return h.appendJSONString(buf, s)
}

// appendMultiline 去掉末尾 CR/LF 后，仍有多行内容时按行编码并保留行内空行。
func (h jsonEncoder) appendMultiline(buf []byte, s string) []byte {
	// 仅去掉末尾的 CR/LF，保留行内空行。
	body := strings.TrimRight(s, "\r\n")
	// 去掉末尾换行后不足两行时，按原字符串编码，保留原有的末尾换行。
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
		// 数组元素各占一行并以制表符缩进，去掉 CRLF 行尾的回车。
		buf = append(buf, '\n', '\t')
		buf = h.appendJSONString(buf, strings.TrimSuffix(line, "\r"))
	}
	return append(buf, ']')
}

// appendEmbeddedJSON 校验并紧凑化以对象或数组开头的文本，成功时作为 JSON 值嵌入。
// 校验失败时不修改 buf，由调用方按普通文本处理。
func (h jsonEncoder) appendEmbeddedJSON(buf []byte, s string) ([]byte, bool) {
	if len(s) < 2 || (s[0] != '{' && s[0] != '[') {
		return buf, false
	}
	// string 转为 Value 时复制底层字节，供 Compact 就地修改。
	v := jsontext.Value(s)
	if v.Compact() != nil {
		return buf, false
	}
	return h.appendSanitized(buf, v), true
}

// appendJSONString 编码 JSON 字符串，转义引号、反斜杠及不可打印字符。
// ASCII 文本使用 jsontext.AppendQuote，其他文本按 unicode.IsPrint 逐 rune 检查。
func (h jsonEncoder) appendJSONString[S ~string | ~[]byte](buf []byte, s S) []byte {
	if h.indexUnprintable(s) < 0 {
		buf, _ = jsontext.AppendQuote(buf, s)
		return buf
	}
	return h.appendEscaped(buf, s)
}

// appendEscaped 是 appendJSONString 的慢路径：逐 rune 写出，不可打印的转成 \uXXXX。
func (h jsonEncoder) appendEscaped[S ~string | ~[]byte](buf []byte, s S) []byte {
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

// appendSanitized 追加已校验且紧凑化的 JSON，将其中不可打印的字符转义。
// 输入不包含格式化缩进，待转义字符位于字符串字面量内部。
func (h jsonEncoder) appendSanitized(buf, b []byte) []byte {
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
