package xslog

import (
	"bytes"
	"cmp"
	"encoding/json/jsontext"
	"slices"
)

// jsonOmission 表示需要移除的成员、数组元素或分隔符的字节范围。
type jsonOmission struct{ start, end int }

// jsonOmissions 借助标准 JSON 解码器定位缺失值，不改变其他值或字面量格式。
// missing 记录 Injector 渲染缺失占位符前的偏移，包含对齐添加的前导空白。
type jsonOmissions struct {
	decoder *jsontext.Decoder
	input   []byte
	missing []int
	spans   []jsonOmission
}

// omitMissing 移除含缺失值的成员或数组元素，并保留真正的 null 值。
// 没有缺失值时直接返回；有缺失值时解析一次，再在原缓冲中压缩删除范围。
func (t *outputTemplate) omitMissing(dst []byte, start int, missing []int) ([]byte, error) {
	if len(missing) == 0 {
		return dst, nil
	}
	input := dst[start:]
	omissions := jsonOmissions{
		decoder: jsontext.NewDecoder(bytes.NewReader(input), jsontext.AllowDuplicateNames(true)),
		input:   input,
		missing: missing,
	}
	if _, err := omissions.value(); err != nil || len(omissions.missing) != 0 {
		return dst, ErrInvalidJSONLayout
	}
	slices.SortFunc(omissions.spans, func(a, b jsonOmission) int { return cmp.Compare(a.start, b.start) })
	output, next := dst[:start], 0
	for _, span := range omissions.spans {
		if span.start >= next {
			output = append(output, input[next:span.start]...)
		}
		next = max(next, span.end)
	}
	return append(output, input[next:]...), nil
}

// value 消费一个完整值，报告它是否来自缺失的占位符。
func (o *jsonOmissions) value() (bool, error) {
	before := int(o.decoder.InputOffset())
	token, err := o.decoder.ReadToken()
	if err != nil {
		return false, err
	}
	switch token.Kind() {
	case '{', '[':
		return false, o.container(token.Kind())
	}
	return o.consumeMissing(before), nil
}

// consumeMissing 消费当前标量或成员名内的缺失标记，支持字符串内多个占位符。
func (o *jsonOmissions) consumeMissing(before int) bool {
	after := int(o.decoder.InputOffset())
	found := false
	for len(o.missing) > 0 && o.missing[0] >= before && o.missing[0] < after {
		o.missing = o.missing[1:]
		found = true
	}
	return found
}

// container 记录缺失子值的删除范围，并修正首个保留成员之前的逗号。
func (o *jsonOmissions) container(kind jsontext.Kind) error {
	closing := jsontext.Kind(']')
	if kind == '{' {
		closing = '}'
	}
	first, kept := true, false
	for o.decoder.PeekKind() != closing {
		before := int(o.decoder.InputOffset())
		missingKey := false
		if kind == '{' {
			if _, err := o.decoder.ReadToken(); err != nil {
				return err
			}
			missingKey = o.consumeMissing(before)
		}
		missing, err := o.value()
		if err != nil {
			return err
		}
		if missingKey || missing {
			o.spans = append(o.spans, jsonOmission{before, int(o.decoder.InputOffset())})
		} else {
			if !kept && !first {
				comma := before
				for comma < len(o.input) && bytes.IndexByte([]byte(" \t\r\n"), o.input[comma]) >= 0 {
					comma++
				}
				o.spans = append(o.spans, jsonOmission{comma, comma + 1})
			}
			kept = true
		}
		first = false
	}
	_, err := o.decoder.ReadToken()
	return err
}
