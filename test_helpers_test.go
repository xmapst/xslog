package xslog

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path"
	"reflect"
	"strconv"
	"testing"
	"time"
)

func testTime() time.Time {
	return time.Date(2024, 1, 2, 3, 4, 5, 678901234, time.UTC)
}

func testRecord(attrs ...slog.Attr) slog.Record {
	r := slog.NewRecord(testTime(), slog.LevelInfo, "hello", 0)
	r.AddAttrs(attrs...)
	return r
}

func testSourceLocation(file string, line int) string {
	return path.Join(path.Base(path.Dir(file)), path.Base(file)) + ":" + strconv.Itoa(line)
}

func renderRecord(t *testing.T, r slog.Record, opts ...Option) string {
	t.Helper()
	var out bytes.Buffer
	opts = append(opts, WithWriter(&out))
	if err := NewHandler(opts...).Handle(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	return out.String()
}

func requireJSON(t *testing.T, got, want string) {
	t.Helper()
	decode := func(input string) any {
		t.Helper()
		d := json.NewDecoder(bytes.NewBufferString(input))
		d.UseNumber()
		var v any
		if err := d.Decode(&v); err != nil {
			t.Fatalf("invalid JSON %q: %v", input, err)
		}
		var extra any
		if err := d.Decode(&extra); err != io.EOF {
			t.Fatalf("trailing JSON in %q: %v", input, err)
		}
		return v
	}
	if actual, expected := decode(got), decode(want); !reflect.DeepEqual(actual, expected) {
		t.Fatalf("JSON = %s\nwant = %s", got, want)
	}
}

func unsetTestEnv(t *testing.T, name string) {
	t.Helper()
	t.Setenv(name, "")
	if err := os.Unsetenv(name); err != nil {
		t.Fatal(err)
	}
}

// preserveDefaultLogger 保存默认 Logger 和全局 Handler 的设置，测试结束后恢复。
// Setup 和包级的 Set 函数原地更新全局 Handler，必须整体恢复，避免影响后续测试。
func preserveDefaultLogger(t *testing.T) {
	t.Helper()
	previous, settings := slog.Default(), std.handler.state.settings()
	t.Cleanup(func() {
		if err := Close(); err != nil {
			t.Errorf("close test log file: %v", err)
		}
		std.handler.state.configure(settings)
		slog.SetDefault(previous)
	})
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
}

// settings 返回当前设置的快照，供测试保存和恢复。
func (s *handlerState) settings() handlerSettings {
	s.mu.Lock()
	writer := s.writer
	s.mu.Unlock()
	v := handlerSettings{writer: writer, level: *s.level.Load(), layout: *s.layout.Load()}
	if fn := s.onError.Load(); fn != nil {
		v.onError = *fn
	}
	return v
}

type testWriter struct {
	bytes.Buffer
	writes int
}

func (w *testWriter) Write(p []byte) (int, error) {
	w.writes++
	return w.Buffer.Write(p)
}

type writeFunc func([]byte) (int, error)

func (f writeFunc) Write(p []byte) (int, error) { return f(p) }

type layoutFunc func([]byte, templateValues) ([]byte, error)

func (f layoutFunc) Append(dst []byte, values templateValues) ([]byte, error) {
	return f(dst, values)
}

func withTestLayout(l layout) Option {
	return func(o *options) { o.layout = l }
}
