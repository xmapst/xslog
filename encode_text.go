package xslog

import (
	"encoding/hex"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"unicode/utf8"
)

// textEncoder 实现文本编码，不持有 Handler 的写入或级别状态。
type textEncoder struct {
	renderer
	time timeFormat
}

// textAttr 保存已渲染的字段文本及其引号、多行标记。
type textAttr struct {
	key   string
	text  string
	block bool
	quote bool
}

// flatten 将属性展开为文本字段列表，分组使用点分键名，省略空属性和空组。
func (h textEncoder) flatten(dst []textAttr, prefix string, a slog.Attr) []textAttr {
	a.Value = a.Value.Resolve()
	if a.Equal(slog.Attr{}) || (a.Key == "" && a.Value.Kind() != slog.KindGroup) {
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
		// 空组不产生字段；无名组沿用当前前缀。
		for _, ga := range sub {
			dst = h.flatten(dst, key, ga)
		}
		return dst
	}
	// 在计算多行块的键宽前完成转义，列宽使用实际显示的文本。
	text, block, quote := h.render(a.Value)
	return append(dst, textAttr{key: h.displayKey(key), text: text, block: block, quote: quote})
}

// displayKey 转义字段名中制表符以外的不可打印字符。
// 字段名为空或包含空格、制表符、等号、引号时，使用引号包裹以明确 k=v 边界。
func (h textEncoder) displayKey(k string) string {
	k = h.plainText(k)
	if k != "" && !strings.ContainsAny(k, " \t=\"") {
		return k
	}
	return string(h.appendQuoted(nil, k))
}

// appendQuoted 写入带双引号的文本，转义引号及制表符以外的不可打印字符。
// 文本布局的引用规则不等同于 JSON 字符串编码。
func (h textEncoder) appendQuoted(buf []byte, s string) []byte {
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

// appendBlock 将多行属性写为带竖线和缩进的文本块。
// width 是本条记录中多行字段的最大显示键宽，单位为字节。
func (h textEncoder) appendBlock(buf []byte, a textAttr, width int) []byte {
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

// render 将值转为文本，并报告是否使用多行块及是否需要引号。
// 数字和布尔不加引号，nil 输出为 <nil>，复合值优先使用 JSON 编码。
func (h textEncoder) render(v slog.Value) (text string, block, quote bool) {
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
		// Unix 时间戳不加引号，其他时间格式按字符串输出。
		return h.time.text(v.Time()), false, !h.time.numeric()
	default:
		text, quote = h.renderAny(v.Any())
	}

	// 完整的 JSON 对象或数组作为结构化文本写出，不额外加引号。
	if quote {
		if compact, ok := h.embeddedJSON(text); ok {
			return compact, false, false
		}
	}

	// 属性值的制表符展开为两个空格，使单行字段和多行块使用固定缩进。
	if strings.IndexByte(text, '\t') >= 0 {
		text = strings.ReplaceAll(text, "\t", "  ")
	}
	if strings.IndexByte(text, '\n') < 0 {
		return text, false, quote
	}
	// 去掉尾部换行后仍包含换行时才使用多行块。
	body := strings.TrimRight(text, "\r\n")
	return body, strings.IndexByte(body, '\n') >= 0, quote
}

// embeddedJSON 校验以对象或数组开头的文本，返回去除多余空白的完整 JSON。
// 普通字符串、数字和布尔文本不作为嵌入的 JSON 处理。
func (h textEncoder) embeddedJSON(s string) (string, bool) {
	if len(s) < 2 || (s[0] != '{' && s[0] != '[') {
		return "", false
	}
	// string 转为 Value 时复制底层字节，供 Compact 就地修改。
	v := jsontext.Value(s)
	if v.Compact() != nil {
		return "", false
	}
	return string(v), true
}

// renderAny 编码任意值，并报告是否需要引号。
// error 和 fmt.Stringer 使用文本方法，复合值优先使用 JSON。
// 序列化失败或外部方法 panic 时返回占位文本。
func (h textEncoder) renderAny(a any) (out string, quote bool) {
	switch x := a.(type) {
	case nil:
		return "<nil>", false
	case string:
		return x, true
	}

	// 外部文本方法或 JSON 序列化可能 panic，避免它们中断当前日志的渲染。
	defer func() {
		if r := recover(); r != nil {
			out, quote = h.panicText(r), true
		}
	}()

	switch x := a.(type) {
	case error:
		return x.Error(), true
	case fmt.Stringer:
		return x.String(), true
	case []byte:
		// 合法 UTF-8 按文本编码，其他字节使用不带引号的十六进制。
		if utf8.Valid(x) {
			return string(x), true
		}
		return "0x" + hex.EncodeToString(x), false
	}

	b, err := json.Marshal(a, jsontext.AllowInvalidUTF8(true), errorAsText)
	if err != nil {
		return fmt.Sprintf("<cannot encode %T>", a), true
	}
	return string(b), false
}
