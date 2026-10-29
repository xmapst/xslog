package xslog

import (
	"log/slog"
	"strconv"
	"strings"
)

// templateValues 向布局提供记录元数据和业务属性。
// 返回值仅在本次 Append 期间有效，布局不得修改或保留。
type templateValues interface {
	// Lookup 只查找内置的时间、级别、消息、调用位置和进程信息，名称不区分大小写。
	// 元数据缺失时返回 false。
	Lookup(name string) (slog.Value, bool)

	// Attrs 返回保留分组和类型的业务属性。
	Attrs() []slog.Attr
}

// layout 决定记录的布局与编码。
// Append 必须支持并发调用，将结果追加到 dst，且不得保留 dst 或 values。
// Handler 会在记录末尾缺少 LF 时自动补齐换行。
// 返回错误时，Handler 不会将该记录写入 Writer。
type layout interface {
	Append(dst []byte, values templateValues) ([]byte, error)
}

// WithTemplate 通过 [Injector] 预编译输出模板，并应用到布局。
// 仅识别 %Name% 和 ${Name[,alignment][:format]}，其他分隔符按普通文本保留。
//
// 时间格式使用 Go 布局或 unix、unixMilli、unixMicro、unixNano，区分大小写。
// 级别支持 u3、w3 缩写，文本消息的 l、lj 格式省略引号。
// 文本模板中的 j 格式按 JSON 规则编码对应值。
// NewLine 显式插入 LF，用于文本布局或 JSON 成员之间的空白。
// Handler 仍自动补齐记录末尾的换行，已有换行时不重复追加。
// 调用信息使用 Source、File、FileName、Line、Function、Package；
// 进程信息使用 PID、Hostname、Executable，在创建 Handler 时读取一次。
// Line 和 PID 的元数据保留数值类型，相应的环境变量回退值为字符串。
// JSON 中直接传入的多行字符串，包括消息、元数据及环境变量值，按行展开为字符串数组。
//
// 配置时以示例值展开模板，结果为合法 JSON 对象时选择 JSON 编码，否则选择文本编码。
// 编码确定后不随记录或环境变量变化。JSON 占位符通常表示完整值，无需添加引号。
// 缺少的记录值按原名回退系统环境变量。
// 两处都缺失时，文本省略占位值，JSON 省略对应的成员或数组元素。
// 业务属性不参与占位符查找；文本追加 key=value 字段，分组展开为点分键名，
// 多行值写为竖线块；JSON 追加对象成员，保留分组嵌套，多行字符串按行展开为数组。
//
// 空模板使用 [DefaultTemplate]。JSON 模式下，实际生成的对象若无效，Handle 返回错误，
// 不写出该记录，并通过 [WithErrorHandler] 报告错误。
func WithTemplate(template string) Option {
	if template == "" {
		template = DefaultTemplate
	}
	compiled := new(outputTemplate)
	compiled.compile(template)
	return func(o *options) {
		o.layout = compiled
	}
}

// outputTemplate 保存编译后的占位符和输出编码，编译完成后只读。
type outputTemplate struct {
	renderer
	injector *Injector
	json     bool
	jsonTail string
}

// compile 通过 Injector 解析占位符，并以 JSON 示例渲染结果确定编码。
// 渲染记录时复用编译结果。
func (t *outputTemplate) compile(template string) {
	t.injector = new(Injector)
	t.injector.compile(template)
	t.jsonTail = ""
	t.detectJSON()
	if t.json {
		t.splitJSONTail()
	}
}

// splitJSONTail 留出对象结束括号，并保留其后的空白和 NewLine 控制符。
// 已验证的 JSON 对象末尾只可能出现这些常量，仍使用 Injector 处理对齐。
func (t *outputTemplate) splitJSONTail() {
	for index := len(t.injector.parts) - 1; index >= 0; index-- {
		part := &t.injector.parts[index]
		if part.name != "" {
			continue
		}
		end := len(strings.TrimRight(part.text, " \t\r\n"))
		if end == 0 {
			continue
		}
		t.jsonTail, part.text = part.text[end-1:], part.text[:end-1]
		tail := Injector{parts: t.injector.parts[index+1:]}
		t.jsonTail += string(tail.AppendFunc(nil, func(dst []byte, name, format string) ([]byte, bool) {
			value, _ := (jsonLayoutProbe{}).Lookup(name)
			return t.appendValue(dst, value, name, format), true
		}))
		t.injector.parts = t.injector.parts[:index+1]
		return
	}
}

// Append 渲染模板并追加业务属性，JSON 编码还会校验生成的完整对象。
func (t *outputTemplate) Append(dst []byte, values templateValues) ([]byte, error) {
	startRecord := len(dst)
	var missing []int
	dst = t.injector.AppendFunc(dst, func(dst []byte, name, format string) ([]byte, bool) {
		value, found := t.lookupValue(values, name)
		if !found {
			if t.json {
				missing = append(missing, len(dst)-startRecord)
				dst = append(dst, "null"...)
			}
			return dst, true
		}
		return t.appendValue(dst, value, name, format), true
	})
	if t.json {
		dst = t.appendJSONAttrs(dst, startRecord, values.Attrs())
	} else {
		encoder := textEncoder{time: (timeFormat{}).parse(defaultTimeLayout)}
		dst = encoder.appendAttrs(dst, values.Attrs())
	}
	if err := t.validateRecord(dst[startRecord:]); err != nil {
		return dst, err
	}
	return t.omitMissing(dst, startRecord, missing)
}

// formatLevel 处理 uN、wN 级别格式，宽度为 3 时使用常用缩写。
func (t *outputTemplate) formatLevel(text string, format string) string {
	if len(format) == 0 || (format[0] != 'u' && format[0] != 'w') {
		return text
	}
	width, _ := strconv.Atoi(format[1:])
	if width == 3 {
		switch text {
		case "DEBUG":
			text = "DBG"
		case "INFO":
			text = "INF"
		case "WARN":
			text = "WRN"
		case "ERROR":
			text = "ERR"
		}
	}
	if width > 0 && width < len(text) {
		text = text[:width]
	}
	if format[0] == 'w' {
		return strings.ToLower(text)
	}
	return strings.ToUpper(text)
}

// builtinName 统一内置名称的大小写，不修改环境变量查找使用的原始名称。
func (rd renderer) builtinName(name string) string {
	// 常用拼写直接返回常量，避免逐条记录分配小写副本。
	switch name {
	case "Time":
		return "time"
	case "Level":
		return "level"
	case "Source":
		return "source"
	case "File":
		return "file"
	case "FileName":
		return "filename"
	case "Line":
		return "line"
	case "Function":
		return "function"
	case "Package":
		return "package"
	case "PID":
		return "pid"
	case "Hostname":
		return "hostname"
	case "Executable":
		return "executable"
	case "Message":
		return "message"
	case "NewLine":
		return "newline"
	default:
		return strings.ToLower(name)
	}
}
