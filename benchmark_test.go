package xslog

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

type benchmarkCase struct {
	name, template, message string
	attrs                   []slog.Attr
	bound                   bool
}

func benchmarkCases() []benchmarkCase {
	attrs := []slog.Attr{
		slog.String("request", "req-123"), slog.Int("status", 200), slog.Bool("ok", true),
		slog.Duration("elapsed", 1500*time.Microsecond), slog.Any("error", errors.New("retry")),
		slog.Group("peer", slog.String("address", "127.0.0.1"), slog.Int("port", 8080)),
	}
	stack := strings.Repeat("module/function.go:123\n", 40) + "end"
	large := strings.Repeat("x", 64<<10)
	return []benchmarkCase{
		{name: "TextMinimal", template: "${Message:l}"},
		{name: "TextDefault", template: DefaultTemplate},
		{name: "JSONMinimal", template: `{"time":${TimeUnixMilli},"level":${Level},"message":${Message}}`},
		{name: "JSONMetadata", template: `{"time":${Time},"level":${Level},"message":${Message},"source":${Source},"file":${File},"name":${FileName},"line":${Line},"function":${Function},"package":${Package},"pid":${PID},"host":${Hostname},"executable":${Executable}}${NewLine}`},
		{name: "TextGrouped", template: DefaultTemplate, attrs: attrs, bound: true},
		{name: "JSONGrouped", template: `{"time":${TimeUnixMilli},"level":${Level},"message":${Message}}`, attrs: attrs, bound: true},
		{name: "TextMultiline", template: "${Message}", message: "first\nsecond", attrs: []slog.Attr{slog.String("stack", stack)}},
		{name: "JSONMultiline", template: `{"message":${Message}}`, message: "first\nsecond", attrs: []slog.Attr{slog.String("stack", stack)}},
		{name: "Text64KiB", template: "${Message:l}", message: large, attrs: attrs},
		{name: "JSON64KiB", template: `{"message":${Message}}`, message: large, attrs: attrs},
		{name: "JSONMissing", template: `{"message":${Message},"missing":${XSLOG_BENCH_ABSENT},"nested":[${XSLOG_BENCH_ABSENT},null,${Line}]}`},
	}
}

func (tc benchmarkCase) handler(b *testing.B) (*Handler, slog.Record) {
	b.Helper()
	var pcs [1]uintptr
	runtime.Callers(1, pcs[:])
	message := tc.message
	if message == "" {
		message = "request completed"
	}
	r := slog.NewRecord(testTime(), slog.LevelInfo, message, pcs[0])
	r.AddAttrs(tc.attrs...)
	var sample bytes.Buffer
	h := NewHandler(WithWriter(&sample), WithTemplate(tc.template))
	if tc.bound {
		h = h.WithAttrs([]slog.Attr{slog.String("app", "api")}).WithGroup("request").WithAttrs([]slog.Attr{slog.String("region", "local")}).(*Handler)
	}
	if err := h.Handle(context.Background(), r); err != nil {
		b.Fatal(err)
	}
	b.SetBytes(int64(sample.Len()))
	h.SetWriter(io.Discard)
	return h, r
}

// BenchmarkHandler 复用预建记录和已编译模板，测量属性解析、渲染和同步写入的开销。
// Writer 使用 io.Discard，不包含文件 I/O。
func BenchmarkHandler(b *testing.B) {
	b.Setenv("XSLOG_BENCH_ABSENT", "")
	if err := os.Unsetenv("XSLOG_BENCH_ABSENT"); err != nil {
		b.Fatal(err)
	}
	for _, tc := range benchmarkCases() {
		b.Run(tc.name, func(b *testing.B) {
			h, r := tc.handler(b)
			b.ReportAllocs()
			for b.Loop() {
				if err := h.Handle(context.Background(), r); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// BenchmarkHandlerParallel 所有 goroutine 共享同一个 Handler 和 Writer 锁。
func BenchmarkHandlerParallel(b *testing.B) {
	for _, tc := range benchmarkCases() {
		if tc.name != "TextGrouped" && tc.name != "JSONGrouped" {
			continue
		}
		b.Run(tc.name, func(b *testing.B) {
			h, r := tc.handler(b)
			b.ReportAllocs()
			b.ResetTimer()
			b.RunParallel(func(pb *testing.PB) {
				for pb.Next() {
					if err := h.Handle(context.Background(), r); err != nil {
						b.Error(err)
						return
					}
				}
			})
		})
	}
}

// BenchmarkLogger 包括 slog 创建记录、读取时钟和采集调用 PC 的开销。
func BenchmarkLogger(b *testing.B) {
	for _, tc := range []struct{ name, template string }{
		{"Text", DefaultTemplate},
		{"JSON", `{"time":${TimeUnixMilli},"level":${Level},"source":${Source},"message":${Message}}`},
		{"Filtered", DefaultTemplate},
	} {
		b.Run(tc.name, func(b *testing.B) {
			h := NewHandler(WithWriter(io.Discard), WithTemplate(tc.template))
			if tc.name == "Filtered" {
				h.SetLevel(slog.LevelError)
			}
			logger := slog.New(h)
			attrs := []slog.Attr{slog.String("request", "req-123"), slog.Int("status", 200), slog.Bool("ok", true)}
			b.ReportAllocs()
			for b.Loop() {
				logger.LogAttrs(context.Background(), slog.LevelInfo, "request completed", attrs...)
			}
		})
	}
}

func BenchmarkTemplateCompile(b *testing.B) {
	for _, tc := range []struct{ name, template string }{
		{"Text", DefaultTemplate},
		{"JSON", `{"time":${TimeUnixMilli},"level":${Level},"message":${Message}}${NewLine}`},
		{"MalformedJSON", `{${Time},"Level":${Level}}`},
	} {
		b.Run(tc.name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				_ = WithTemplate(tc.template)
			}
		})
	}
}

func BenchmarkHandlerCreate(b *testing.B) {
	option := WithTemplate(DefaultTemplate)
	b.ReportAllocs()
	for b.Loop() {
		_ = NewHandler(option, WithWriter(io.Discard))
	}
}

func BenchmarkInjector(b *testing.B) {
	b.Setenv("XSLOG_BENCH_ENV", "system")
	var injector Injector
	injector.compile("${name,-12} ${number:04d} ${when:15:04:05.000} %XSLOG_BENCH_ENV%")
	values := map[string]any{"name": "api", "number": 42, "when": testTime()}
	b.ReportAllocs()
	for b.Loop() {
		_ = injector.Inject(values)
	}
}

// BenchmarkFileOutput 测量 Handler 经 OpenFile 向实际文件追加的开销。
// 使用 1 GiB 大小上限并关闭时间轮转，不主动调用 Sync。
func BenchmarkFileOutput(b *testing.B) {
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
	b.Cleanup(func() { slog.SetDefault(previous) })
	for _, tc := range benchmarkCases() {
		if tc.name != "TextGrouped" && tc.name != "JSONGrouped" {
			continue
		}
		b.Run(tc.name, func(b *testing.B) {
			file, err := OpenFile(filepath.Join(b.TempDir(), "app.log"), WithMaxSizeMB(1024), WithMaxBackups(1), WithRotationHours(0))
			if err != nil {
				b.Fatal(err)
			}
			b.Cleanup(func() {
				if err := file.Close(); err != nil {
					b.Error(err)
				}
			})
			h, r := tc.handler(b)
			h.SetWriter(file)
			b.ReportAllocs()
			for b.Loop() {
				if err := h.Handle(context.Background(), r); err != nil {
					b.Fatal(fmt.Errorf("file output: %w", err))
				}
			}
		})
	}
}
