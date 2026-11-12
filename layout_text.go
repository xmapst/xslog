package xslog

import (
	"log/slog"
)

// lookupValue 先查记录元数据，缺失时按原名回退系统环境变量，并按格式中的类型动词转换。
func (t *outputTemplate) lookupValue(values templateValues, name, format string) (slog.Value, bool) {
	if value, found := values.Lookup(name); found {
		return value, true
	}
	return parseVerb(format).lookupEnv(name)
}

// appendValue 按占位符的格式和当前布局编码已找到的值。
func (t *outputTemplate) appendValue(dst []byte, value slog.Value, name, format string) []byte {
	builtin := t.builtinName(name)
	if builtin == "newline" {
		return append(dst, '\n')
	}
	v := parseVerb(format)
	if builtin == "level" {
		value = slog.StringValue(v.level(value.String()))
	} else if value.Kind() == slog.KindString {
		value = slog.StringValue(v.applyCase(value.String()))
	}
	plainMetadata := false
	switch builtin {
	case "level", "source", "file", "filename", "function", "package", "hostname", "executable":
		plainMetadata = true
	}
	if t.json || v.isJSON() {
		encoder := jsonEncoder{time: v.timeFormat()}
		if plainMetadata || builtin == "message" ||
			(t.numericBuiltin(builtin) && value.Kind() == slog.KindString) {
			return encoder.appendString(dst, value.String())
		}
		return encoder.appendValue(dst, value)
	}
	encoder := textEncoder{time: v.timeFormat()}
	if builtin == "time" {
		if value.Kind() == slog.KindTime {
			start := len(dst)
			dst = encoder.time.append(dst, value.Time())
			if encoder.indexUnprintable(dst[start:]) >= 0 {
				text := string(dst[start:])
				dst = encoder.appendPlainText(dst[:start], text)
			}
			return dst
		}
	}
	if plainMetadata {
		return encoder.appendPlainText(dst, value.String())
	}
	if builtin == "message" {
		if v.isPlain() {
			return encoder.appendPlainText(dst, value.String())
		}
		return encoder.appendQuoted(dst, value.String())
	}
	text, _, quote := encoder.render(value)
	if quote && !v.isPlain() {
		return encoder.appendQuoted(dst, text)
	}
	return encoder.appendPlainText(dst, text)
}

// appendAttrs 追加以空白分隔的 key=value 字段和对齐的多行属性块。
// 记录末尾的换行由 Handler 统一补齐。
func (h textEncoder) appendAttrs(dst []byte, values []slog.Attr) []byte {
	ap := attrPool.Get().(*[]textAttr)
	attrs := (*ap)[:0]
	defer func() {
		if cap(attrs) <= maxPooledAttrs {
			clear(attrs[:cap(attrs)])
			*ap = attrs
			attrPool.Put(ap)
		}
	}()
	for _, a := range values {
		attrs = h.flatten(attrs, "", a)
	}
	width := 0
	for _, a := range attrs {
		if a.block {
			width = max(width, len(a.key))
			continue
		}
		if len(dst) > 0 {
			switch dst[len(dst)-1] {
			case ' ', '\t', '\r', '\n':
			default:
				dst = append(dst, ' ')
			}
		}
		dst = append(dst, a.key...)
		dst = append(dst, '=')
		if a.quote {
			dst = h.appendQuoted(dst, a.text)
		} else {
			dst = h.appendPlainText(dst, a.text)
		}
	}
	if width == 0 {
		return dst
	}
	if len(dst) > 0 && dst[len(dst)-1] != '\n' {
		dst = append(dst, '\n')
	}
	for _, a := range attrs {
		if a.block {
			dst = h.appendBlock(dst, a, width)
		}
	}
	return dst[:len(dst)-1]
}
