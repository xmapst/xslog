package xslog

import (
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// injectNameExpr 限定名称以 ASCII 字母或下划线开头，
// 后续允许 ASCII 字母、数字、下划线、点和连字符。
const injectNameExpr = `[A-Za-z_][A-Za-z0-9_.-]*`

// injectSpecExpr 为名称附加可选的对齐和格式，格式部分不包含花括号。
const injectSpecExpr = injectNameExpr + `(?:\s*,\s*[^{}:]*)?(?::[^{}]*?)?`

// variablePlaceholder 匹配 %Name% 和 ${Name[,alignment][:format]} 占位符。
var variablePlaceholder = regexp.MustCompile(`%(` + injectNameExpr + `)%|\$\{(` + injectSpecExpr + `)\}`)

// injectPart 保存字面量或占位符及其格式、对齐和未解析原文。
type injectPart struct {
	text       string
	name       string
	format     string
	alignment  int
	unresolved string
}

// Injector 保存编译后的替换模板，编译完成后可并发复用。
// 每次调用需提供独立的目标缓冲区，并保证传入的值可安全读取。
type Injector struct {
	parts []injectPart
}

// compile 解析 %Name% 和 ${Name[,alignment][:format]}，保留其他文本。
// 对齐按 Unicode 码点计数，正值右对齐、负值左对齐，绝对值上限为 4096。
// 无法解析的占位符及无效对齐按原文保留。
func (i *Injector) compile(content string) {
	parts := make([]injectPart, 0, 8)
	last := 0
	for _, match := range variablePlaceholder.FindAllStringSubmatchIndex(content, -1) {
		if match[0] > last {
			parts = append(parts, injectPart{text: content[last:match[0]]})
		}
		start, end := match[2], match[3]
		if start < 0 {
			start, end = match[4], match[5]
		}
		spec := strings.TrimSpace(content[start:end])
		part := injectPart{unresolved: content[match[0]:match[1]]}
		if i := strings.IndexByte(spec, ':'); i >= 0 {
			part.format, spec = spec[i+1:], spec[:i]
		}
		if i := strings.IndexByte(spec, ','); i >= 0 {
			var err error
			part.alignment, err = strconv.Atoi(strings.TrimSpace(spec[i+1:]))
			if err != nil || part.alignment < -4096 || part.alignment > 4096 {
				parts = append(parts, injectPart{text: content[match[0]:match[1]]})
				last = match[1]
				continue
			}
			spec = spec[:i]
		}
		part.name = strings.TrimSpace(spec)
		parts = append(parts, part)
		last = match[1]
	}
	if last < len(content) {
		parts = append(parts, injectPart{text: content[last:]})
	}
	i.parts = parts
}

// AppendFunc 将替换结果追加到 dst，由 replace 负责取值和编码。
// replace 返回 false 时必须保持 dst 不变，此时按原文保留占位符。
// nil 回调保留全部占位符，替换结果不会再次作为模板展开。
func (i *Injector) AppendFunc(dst []byte, replace func(dst []byte, name, format string) ([]byte, bool)) []byte {
	for _, part := range i.parts {
		if part.name == "" {
			dst = append(dst, part.text...)
			continue
		}
		start := len(dst)
		var found bool
		if replace != nil {
			dst, found = replace(dst, part.name, part.format)
		}
		if !found {
			dst = append(dst[:start], part.unresolved...)
			continue
		}
		if part.alignment == 0 {
			continue
		}
		width := part.alignment
		if width < 0 {
			width = -width
		}
		padding := width - utf8.RuneCount(dst[start:])
		if padding <= 0 {
			continue
		}
		end := len(dst)
		for range padding {
			dst = append(dst, ' ')
		}
		if part.alignment > 0 {
			copy(dst[start+padding:], dst[start:end])
			for j := start; j < start+padding; j++ {
				dst[j] = ' '
			}
		}
	}
	return dst
}

// Inject 依次从 envs 和系统环境变量查找值，nil map 可以直接使用。
// 查找区分大小写，空值也算存在；两处均未找到时保留占位符原文。
// 时间格式使用 Go 布局或 unix、unixMilli、unixMicro、unixNano，区分大小写；
// 其他非空格式使用省略百分号的 fmt 格式。
func (i *Injector) Inject(envs map[string]any) string {
	return string(i.AppendFunc(nil, func(dst []byte, name, format string) ([]byte, bool) {
		value, found := envs[name]
		if !found {
			value, found = os.LookupEnv(name)
		}
		if !found {
			return dst, false
		}
		if format == "" {
			return fmt.Append(dst, value), true
		}
		if timestamp, ok := value.(time.Time); ok {
			return (timeFormat{}).parse(format).append(dst, timestamp), true
		}
		return fmt.Appendf(dst, "%"+format, value), true
	}))
}
