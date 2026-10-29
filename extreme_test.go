package xslog

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"runtime/debug"
	"strings"
	"testing"
	"time"
)

type cyclicNode struct{ Next *cyclicNode }

type recursiveGroupValuer struct{ fanout bool }

func (v recursiveGroupValuer) LogValue() slog.Value {
	attrs := []slog.Attr{slog.Any("next", v)}
	if v.fanout {
		attrs = append(attrs, slog.Any("other", v))
	}
	return slog.GroupValue(attrs...)
}

type arbitraryPanicMarshaler struct{ value any }

func (v arbitraryPanicMarshaler) MarshalJSON() ([]byte, error) { panic(v.value) }

type arbitraryPanicValuer struct{ value any }

func (v arbitraryPanicValuer) LogValue() slog.Value { panic(v.value) }

// limitedTestOutput 截断子进程输出，最多保留前 4096 字节。
type limitedTestOutput struct{ data []byte }

func (w *limitedTestOutput) Write(p []byte) (int, error) {
	n := len(p)
	if remaining := 4096 - len(w.data); remaining > 0 {
		w.data = append(w.data, p[:min(remaining, n)]...)
	}
	return n, nil
}

func (w *limitedTestOutput) String() string { return string(w.data) }

// TestExtremeInputs 在独立进程中验证循环引用、递归属性和异常值，限制执行时间与栈容量。
func TestExtremeInputs(t *testing.T) {
	if scenario := os.Getenv("XSLOG_EXTREME_CHILD"); scenario != "" {
		debug.SetMaxStack(8 << 20)
		var attr slog.Attr
		switch scenario {
		case "map-cycle":
			value := map[string]any{}
			value["self"] = value
			attr = slog.Any("value", value)
		case "slice-cycle":
			value := make([]any, 1)
			value[0] = value
			attr = slog.Any("value", value)
		case "pointer-cycle":
			value := &cyclicNode{}
			value.Next = value
			attr = slog.Any("value", value)
		case "panic-cycle", "valuer-panic-cycle":
			value := map[string]any{}
			value["self"] = value
			attr = slog.Any("value", arbitraryPanicMarshaler{value})
			if scenario == "valuer-panic-cycle" {
				attr = slog.Any("value", arbitraryPanicValuer{value})
			}
		case "group-cycle":
			attrs := []slog.Attr{slog.Int("next", 0)}
			value := slog.GroupValue(attrs...)
			attrs[0].Value = value
			attr = slog.Attr{Key: "value", Value: value}
		case "valuer-cycle", "valuer-fanout":
			attr = slog.Any("value", recursiveGroupValuer{fanout: scenario == "valuer-fanout"})
		case "deep-groups":
			attr = slog.String("leaf", "kept")
			for range 20_000 {
				attr = slog.Attr{Key: "next", Value: slog.GroupValue(attr)}
			}
		default:
			t.Fatalf("unknown child scenario %q", scenario)
		}
		var out bytes.Buffer
		h := NewHandler(WithWriter(&out), WithTemplate(os.Getenv("XSLOG_EXTREME_TEMPLATE")))
		if os.Getenv("XSLOG_EXTREME_BOUND") == "true" {
			if err := h.WithAttrs([]slog.Attr{attr}).Handle(context.Background(), testRecord(slog.Int("after", 42))); err != nil {
				t.Fatal(err)
			}
		} else if err := h.Handle(context.Background(), testRecord(attr, slog.Int("after", 42))); err != nil {
			t.Fatal(err)
		}
		if got := out.String(); !strings.Contains(got, "after") || len(got) > 16<<20 {
			t.Fatalf("record missing subsequent field or unbounded output: %d bytes", len(got))
		}
		marker := "<cannot encode "
		switch scenario {
		case "panic-cycle":
			marker = "<log value panicked: map[string]interface {}>"
		case "valuer-panic-cycle":
			marker = "LogValue panicked"
		case "group-cycle":
			marker = "<log group cycle>"
		case "valuer-cycle", "deep-groups":
			marker = "<log group depth exceeded>"
		case "valuer-fanout":
			marker = "<log value resolution limit exceeded>"
		}
		if !strings.Contains(out.String(), marker) {
			t.Fatalf("extreme value lost its diagnostic: want %q", marker)
		}
		if os.Getenv("XSLOG_EXTREME_TEMPLATE") == "{}" && !json.Valid(out.Bytes()) {
			t.Fatal("extreme value corrupted JSON")
		}
		if os.Getenv("XSLOG_EXTREME_TEMPLATE") == "{}" {
			var decoded map[string]json.RawMessage
			if err := json.Unmarshal(out.Bytes(), &decoded); err != nil || string(decoded["after"]) != "42" || decoded[attr.Key] == nil {
				t.Fatalf("extreme record lost fields: error=%v", err)
			}
		}
		// 直接退出，由运行时写出覆盖率，避免子测试重复聚合父进程的数据。
		os.Exit(0)
	}

	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, scenario := range []string{"map-cycle", "slice-cycle", "pointer-cycle", "panic-cycle", "valuer-panic-cycle", "group-cycle", "valuer-cycle", "valuer-fanout", "deep-groups"} {
		for _, mode := range []string{"text", "json"} {
			for _, bound := range []string{"false", "true"} {
				t.Run(scenario+"/"+mode+"/bound="+bound, func(t *testing.T) {
					ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
					defer cancel()
					cmd := exec.CommandContext(ctx, executable, "-test.run=^TestExtremeInputs$", "-test.timeout=8s")
					template := "${Message:l}"
					if mode == "json" {
						template = "{}"
					}
					cmd.Env = append(os.Environ(), "XSLOG_EXTREME_CHILD="+scenario, "XSLOG_EXTREME_TEMPLATE="+template, "XSLOG_EXTREME_BOUND="+bound, "GORACE=atexit_sleep_ms=0")
					var output limitedTestOutput
					cmd.Stdout, cmd.Stderr = &output, &output
					if err := cmd.Run(); err != nil {
						t.Fatalf("child failed: %v (deadline: %v)\n%s", err, ctx.Err(), output.String())
					}
				})
			}
		}
	}
}

func TestHandlerDetectsShortWrites(t *testing.T) {
	for _, tc := range []struct {
		name string
		n    int
		err  error
	}{
		{"zero", 0, nil}, {"partial", 1, nil}, {"negative", -1, nil}, {"oversized", 1000, nil},
		{"explicit error", 1, errors.New("destination failed")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls, callbacks := 0, 0
			var reported error
			h := NewHandler(WithTemplate("${Message:l}"), WithWriter(writeFunc(func([]byte) (int, error) {
				calls++
				return tc.n, tc.err
			})), WithErrorHandler(func(err error) { callbacks++; reported = err }))
			want := tc.err
			if want == nil {
				want = io.ErrShortWrite
			}
			if err := h.Handle(context.Background(), testRecord()); !errors.Is(err, want) || !errors.Is(reported, want) || calls != 1 || callbacks != 1 {
				t.Fatalf("error=%v callback=%v writes=%d callbacks=%d; want %v once", err, reported, calls, callbacks, want)
			}
		})
	}
}

func TestErrorCallbackPanicDoesNotPoisonHandler(t *testing.T) {
	writes := 0
	h := NewHandler(WithTemplate("${Message:l}"), WithWriter(writeFunc(func(p []byte) (int, error) {
		writes++
		if writes == 1 {
			return 0, errors.New("first write failed")
		}
		return len(p), nil
	})), WithErrorHandler(func(error) { panic("callback panic") }))
	func() {
		defer func() {
			if got := recover(); got != "callback panic" {
				t.Errorf("callback panic=%v", got)
			}
		}()
		_ = h.Handle(context.Background(), testRecord())
		t.Error("callback panic did not propagate")
	}()
	done := make(chan error, 1)
	go func() { done <- h.Handle(context.Background(), testRecord()) }()
	select {
	case err := <-done:
		if err != nil || writes != 2 {
			t.Fatalf("subsequent write: error=%v writes=%d", err, writes)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("callback panic left Handler locked")
	}
}
