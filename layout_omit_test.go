package xslog

import (
	"encoding/json"
	"fmt"
	"testing"
)

func FuzzOmitMissingJSONValues(f *testing.F) {
	f.Add(uint8(0), "plain")
	f.Add(uint8(255), "null,\"quoted\" 世界")
	f.Add(uint8(85), "\n\r\t\x1b")
	f.Fuzz(checkOmitMissingJSONValues)
}

func TestOmitMissingJSONValuesAllMasks(t *testing.T) {
	for mask := range 256 {
		t.Run(fmt.Sprintf("mask=%02x", mask), func(t *testing.T) {
			for _, text := range []string{"plain", "null,\"quoted\" 世界", "\n\r\t\x1b", "invalid\xff\xfe"} {
				checkOmitMissingJSONValues(t, uint8(mask), text)
			}
		})
	}
}

func checkOmitMissingJSONValues(t *testing.T, mask uint8, text string) {
	t.Helper()
	quoted, err := json.Marshal(text)
	if err != nil {
		t.Fatal(err)
	}
	input := []byte(" \n{\"realNull\":null,\"text\":")
	input = append(input, quoted...)
	var missing []int
	expected := map[string]any{"realNull": nil, "text": json.RawMessage(quoted)}
	for i := range 4 {
		name := fmt.Sprintf("field%d", i)
		input = fmt.Appendf(input, ",\"%s\":", name)
		if mask&(1<<i) != 0 {
			missing = append(missing, len(input))
			input = append(input, "null"...)
		} else {
			input = fmt.Appendf(input, "%d", i)
			expected[name] = i
		}
	}
	input = append(input, `,"nested":{"values":[`...)
	var values []any = []any{}
	for i := range 4 {
		if i > 0 {
			input = append(input, ',')
		}
		if mask&(1<<(i+4)) != 0 {
			missing = append(missing, len(input))
			input = append(input, "null"...)
		} else {
			input = fmt.Appendf(input, "%d", i)
			values = append(values, i)
		}
	}
	input = append(input, "]}"...)
	expected["nested"] = map[string]any{"values": values}
	input = append(input, `,"quoted":"before-`...)
	for i := range 2 {
		if mask&(1<<i) != 0 {
			missing = append(missing, len(input))
			input = append(input, "null"...)
		} else {
			input = fmt.Appendf(input, "%d", i)
		}
	}
	input = append(input, `-after","dynamic-`...)
	if mask&3 == 0 {
		expected["quoted"] = "before-01-after"
	}
	if mask&4 != 0 {
		missing = append(missing, len(input))
		input = append(input, "null"...)
	} else {
		input = append(input, "ok"...)
	}
	input = append(input, `":{"child":`...)
	child := map[string]any{}
	if mask&8 != 0 {
		missing = append(missing, len(input))
		input = append(input, "null"...)
	} else {
		input = append(input, '1')
		child["child"] = 1
	}
	if mask&4 == 0 {
		expected["dynamic-ok"] = child
	}
	input = append(input, `},"literalNull":"null"}`...)
	input = append(input, '\r', '\n')
	expected["literalNull"] = "null"
	want, err := json.Marshal(expected)
	if err != nil {
		t.Fatal(err)
	}
	got, err := (&outputTemplate{}).omitMissing(input, 0, missing)
	if err != nil {
		t.Fatal(err)
	}
	requireJSON(t, string(got), string(want))
}
