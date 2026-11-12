package xslog

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"runtime"
	"strings"
	"testing"
	"time"
)

func FuzzHandlerRecord(f *testing.F) {
	for _, seed := range []struct {
		template, message, key, value string
		flags                         uint8
	}{
		{DefaultTemplate, "hello", "key", "value", 0},
		{`{"message":${Message},"time":${Time},"line":${Line}}`, "first\nsecond", "世界\n\"", `{"n":1}`, 3},
		{`{"time":"${Time:unix}","line":"${Line}"}`, "\xff\xfe", "", "\x00\u202e", 2},
		{`${NewLine}{"data":[${Line},null,${File}],"message":${Message}}${NewLine} `, "{}", "n", "one\r\ntwo", 1},
		{`{${Time},"Level":${Level}}`, "${Message}", "XSLOG_FUZZ_FIXED", "${Time}", 4},
	} {
		f.Add(seed.template, seed.message, seed.key, seed.value, seed.flags)
	}
	var pcs [1]uintptr
	runtime.Callers(1, pcs[:])
	f.Fuzz(func(t *testing.T, template, message, key, value string, flags uint8) {
		// 避免把语法模糊测试变成不受限的大文件内存测试；大记录另有确定性测试。
		if len(template) > 16<<10 || len(message)+len(key)+len(value) > 64<<10 {
			t.Skip()
		}
		r := testRecord(slog.String(key, value), slog.Bool("XSLOG_FUZZ_FIXED", true))
		r.Message = message
		if flags&1 != 0 {
			r.PC = pcs[0]
		}
		if flags&2 != 0 {
			r.Time = time.Time{}
		}
		if flags&4 != 0 {
			r.AddAttrs(slog.Group("nested", slog.String(key, value)), slog.Any("bytes", []byte(value)))
		}
		var out testWriter
		var reported error
		callbacks := 0
		h := NewHandler(WithWriter(&out), WithTemplate(template), WithErrorHandler(func(err error) {
			reported = err
			callbacks++
		}))
		if flags&8 != 0 {
			h = h.WithAttrs([]slog.Attr{slog.Bool("XSLOG_FUZZ_BOUND", true)}).WithGroup(key).(*Handler)
		}
		err := h.Handle(context.Background(), r)
		if err != nil {
			if !errors.Is(err, ErrInvalidJSONLayout) || !errors.Is(reported, err) || out.writes != 0 || callbacks != 1 {
				t.Fatalf("failed render wrote output or lost error: error=%v reported=%v callbacks=%d writes=%d", err, reported, callbacks, out.writes)
			}
			return
		}
		if callbacks != 0 || out.writes != 1 || !strings.HasSuffix(out.String(), "\n") {
			t.Fatalf("successful render: callbacks=%d writes=%d output=%q", callbacks, out.writes, out.String())
		}
		if h.currentLayout().(*outputTemplate).json {
			var decoded map[string]any
			if err := json.Unmarshal(out.Bytes(), &decoded); err != nil {
				t.Fatalf("JSON corrupted: output=%q error=%v", out.String(), err)
			}
			if flags&8 != 0 && key != "" {
				group, ok := decoded[string([]rune(key))].(map[string]any)
				if !ok {
					t.Fatalf("JSON lost derived group: output=%q key=%q", out.String(), key)
				}
				decoded = group
			}
			if decoded["XSLOG_FUZZ_FIXED"] != true {
				t.Fatalf("JSON corrupted or lost last business field: output=%q error=%v", out.String(), err)
			}
		}
	})
}

func TestLargeTemplatesAndDeepJSONOmission(t *testing.T) {
	unsetTestEnv(t, "Line")
	for _, template := range []string{
		strings.Repeat("${", 50_000), strings.Repeat("${Message,4096}", 256),
		`{"array":` + strings.Repeat("[", 1024) + `${Line}` + strings.Repeat("]", 1024) + `}`,
	} {
		var out testWriter
		h := NewHandler(WithWriter(&out), WithTemplate(template))
		if err := h.Handle(context.Background(), testRecord(slog.Int("after", 7))); err != nil || out.writes != 1 {
			t.Fatalf("large template: error=%v writes=%d", err, out.writes)
		}
		if strings.HasPrefix(template, "${${") && !strings.HasPrefix(out.String(), template) {
			t.Fatal("unclosed placeholders were not preserved")
		}
		if strings.HasPrefix(template, "${Message,4096}") {
			want := strings.Repeat(strings.Repeat(" ", 4096-len(`"hello"`))+`"hello"`, 256) + " after=7\n"
			if out.String() != want {
				t.Fatal("large aligned template corrupted values")
			}
		}
		if strings.HasPrefix(template, "{") {
			requireJSON(t, out.String(), `{"array":`+strings.Repeat("[", 1024)+strings.Repeat("]", 1024)+`,"after":7}`)
		}
	}
}
