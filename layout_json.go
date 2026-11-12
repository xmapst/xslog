package xslog

import (
	"bytes"
	"encoding/json/jsontext"
	"errors"
	"log/slog"
	"time"
)

// ErrInvalidJSONLayout 表示 JSON 模式下实际生成的记录不是有效的 JSON 对象。
// 校验失败的记录不会写入 Writer。
var ErrInvalidJSONLayout = errors.New("xslog: template rendered an invalid JSON object")

// detectJSON 以示例值执行 JSON 渲染，由标准 JSON 校验结果决定模板编码。
// 示例值不读取实际记录或环境变量，编码在配置完成后保持不变。
func (t *outputTemplate) detectJSON() {
	t.json = true
	_, err := t.Append(nil, jsonLayoutProbe{})
	t.json = err == nil
}

// validateRecord 校验 JSON 布局生成的对象，文本布局无需校验。
func (t *outputTemplate) validateRecord(buf []byte) error {
	if !t.json {
		return nil
	}
	body := bytes.Trim(buf, " \t\r\n")
	if len(body) < 2 || body[0] != '{' || !jsontext.Value(body).IsValid(jsontext.AllowDuplicateNames(true)) {
		return ErrInvalidJSONLayout
	}
	return nil
}

// jsonLayoutProbe 提供配置阶段判断编码使用的带类型示例值。
type jsonLayoutProbe struct{ renderer }

// Lookup 返回元数据或环境变量占位符的类型示例，不读取实际记录或环境变量。
func (p jsonLayoutProbe) Lookup(name string) (slog.Value, bool) {
	builtin := p.builtinName(name)
	switch {
	case builtin == "time":
		return slog.TimeValue(time.Unix(0, 0)), true
	case p.numericBuiltin(builtin):
		return slog.IntValue(1), true
	case builtin == "newline":
		return slog.StringValue("\n"), true
	default:
		return slog.StringValue("value"), true
	}
}

// Attrs 返回空属性，使格式判断只取决于模板本身。
func (jsonLayoutProbe) Attrs() []slog.Attr { return nil }

// appendJSONAttrs 将业务属性追加为对象成员，再补回模板的结束括号和尾部空白。
func (t *outputTemplate) appendJSONAttrs(dst []byte, start int, attrs []slog.Attr) []byte {
	encoder := jsonEncoder{}
	body := bytes.TrimSpace(dst[start:])
	var sep byte
	if len(body) > 0 && body[len(body)-1] != '{' {
		sep = ','
	}
	for _, attr := range attrs {
		dst, sep = encoder.appendAttr(dst, attr, sep)
	}
	return append(dst, t.jsonTail...)
}
