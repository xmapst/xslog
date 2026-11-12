package xslog

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// levelerFunc 把函数适配为自定义 slog.Leveler。
type levelerFunc func() slog.Level

func (f levelerFunc) Level() slog.Level { return f() }

func TestHandlerSetLevel(t *testing.T) {
	ctx := context.Background()
	// requireThreshold 逐级检查 h 的启用状态，threshold 是应当启用的最低级别。
	requireThreshold := func(t *testing.T, h slog.Handler, threshold slog.Level) {
		t.Helper()
		for _, level := range []slog.Level{slog.LevelDebug, slog.LevelInfo, slog.LevelWarn, slog.LevelError} {
			if got, want := h.Enabled(ctx, level), level >= threshold; got != want {
				t.Errorf("Enabled(%v) = %v, want %v (threshold %v)", level, got, want, threshold)
			}
		}
	}

	t.Run("existing loggers", func(t *testing.T) {
		var out bytes.Buffer
		h := NewHandler(WithWriter(&out), WithLevel(slog.LevelWarn), WithTemplate("${Message:l}"))
		log := slog.New(h).With("app", "gate") // 先于 SetLevel 派生。
		log.Info("dropped")
		h.SetLevel(slog.LevelInfo)
		log.Info("kept")
		h.SetLevel(slog.LevelError)
		log.Warn("dropped again")
		if got, want := out.String(), "kept app=\"gate\"\n"; got != want {
			t.Fatalf("output = %q, want %q", got, want)
		}
	})

	t.Run("fixed level is shared with derived handlers", func(t *testing.T) {
		h := NewHandler(WithLevel(slog.LevelWarn))
		before := h.WithAttrs([]slog.Attr{slog.String("app", "gate")})
		requireThreshold(t, h, slog.LevelWarn)
		h.SetLevel(slog.LevelDebug)
		after := h.WithGroup("frame")
		for _, derived := range []slog.Handler{h, before, after} {
			requireThreshold(t, derived, slog.LevelDebug)
		}
		// 从派生实例调整，同样影响原实例和其他派生实例。
		before.(*Handler).SetLevel(slog.LevelError)
		for _, derived := range []slog.Handler{h, before, after} {
			requireThreshold(t, derived, slog.LevelError)
		}
	})

	t.Run("LevelVar is updated in place", func(t *testing.T) {
		var level slog.LevelVar
		level.Set(slog.LevelWarn)
		h := NewHandler(WithLevel(&level))
		h.SetLevel(slog.LevelDebug)
		if level.Level() != slog.LevelDebug {
			t.Fatalf("shared LevelVar = %v, want %v", level.Level(), slog.LevelDebug)
		}
		requireThreshold(t, h, slog.LevelDebug)
		// SetLevel 之后 Handler 仍然跟随该变量。
		level.Set(slog.LevelError)
		requireThreshold(t, h, slog.LevelError)
	})

	t.Run("custom Leveler is replaced", func(t *testing.T) {
		current := slog.LevelError
		h := NewHandler(WithLevel(levelerFunc(func() slog.Level { return current })))
		requireThreshold(t, h, slog.LevelError)
		h.SetLevel(slog.LevelWarn)
		requireThreshold(t, h, slog.LevelWarn)
		current = slog.LevelDebug // 被取代的 Leveler 不再生效。
		requireThreshold(t, h, slog.LevelWarn)
	})
}

func TestHandlerSetTemplate(t *testing.T) {
	unsetTestEnv(t, "Source")
	ctx := context.Background()
	var out bytes.Buffer
	h := NewHandler(WithWriter(&out), WithTemplate("${Message:l}"))
	log := slog.New(h).With("app", "gate") // 先于 SetTemplate 派生。
	grouped := log.WithGroup("frame")
	emit := func(l *slog.Logger) string {
		out.Reset()
		l.Info("hello", "n", 1)
		return out.String()
	}

	if got, want := emit(log), "hello app=\"gate\" n=1\n"; got != want {
		t.Fatalf("initial text output = %q, want %q", got, want)
	}

	h.SetTemplate(`{"message":${Message}}`)
	requireJSON(t, emit(log), `{"message":"hello","app":"gate","n":1}`)
	requireJSON(t, emit(grouped), `{"message":"hello","app":"gate","frame":{"n":1}}`)

	h.SetTemplate("") // 空模板恢复默认模板。
	out.Reset()
	if err := log.Handler().Handle(ctx, testRecord(slog.Int("n", 1))); err != nil {
		t.Fatal(err)
	}
	if got, want := out.String(), "2024-01-02T03:04:05.678Z INFO [] message=\"hello\" app=\"gate\" n=1\n"; got != want {
		t.Fatalf("default template output = %q, want %q", got, want)
	}

	h.SetTemplate("${Message:l}")
	if got, want := emit(log), "hello app=\"gate\" n=1\n"; got != want {
		t.Fatalf("restored text output = %q, want %q", got, want)
	}
}

// SetTemplate 与 WithTemplate 共用编译规则，同一模板的输出必须一致，
// 包括空模板、JSON 与文本编码的选择，以及无效 JSON 的文本回退。
func TestHandlerSetTemplateMatchesWithTemplate(t *testing.T) {
	unsetTestEnv(t, "Source")
	r := testRecord(slog.String("network", "tcp"), slog.Group("frame", slog.Int("seq", 2)))
	for _, template := range []string{
		"",
		DefaultTemplate,
		"${Time:15:04:05} [${Level:u3}] ${Message:l}",
		`{"time":${TimeUnixMilli},"message":${Message}}`,
		`{${Time},"Level":${Level}}`,
		"${Message:l}${NewLine}details",
	} {
		t.Run(template, func(t *testing.T) {
			want := renderRecord(t, r, WithTemplate(template))
			var out bytes.Buffer
			h := NewHandler(WithWriter(&out), WithTemplate(`{"before":${Message}}`))
			h.SetTemplate(template)
			if err := h.Handle(context.Background(), r); err != nil {
				t.Fatal(err)
			}
			if out.String() != want {
				t.Fatalf("SetTemplate output = %q, WithTemplate output = %q", out.String(), want)
			}
		})
	}
}

func TestHandlerSetWriter(t *testing.T) {
	ctx := context.Background()
	var first, second testWriter
	h := NewHandler(WithWriter(&first), WithTemplate("${Message:l}"))
	child := h.WithAttrs([]slog.Attr{slog.String("app", "gate")}) // 先于 SetWriter 派生。
	if err := h.Handle(ctx, testRecord()); err != nil {
		t.Fatal(err)
	}
	h.SetWriter(&second)
	for _, handler := range []slog.Handler{h, child} {
		if err := handler.Handle(ctx, testRecord()); err != nil {
			t.Fatal(err)
		}
	}
	if first.String() != "hello\n" || first.writes != 1 {
		t.Fatalf("replaced writer = %q after %d writes, want one record", first.String(), first.writes)
	}
	if want := "hello\nhello app=\"gate\"\n"; second.String() != want || second.writes != 2 {
		t.Fatalf("new writer = %q after %d writes, want %q in two writes", second.String(), second.writes, want)
	}
	h.SetWriter(nil)
	if h.state.writer != os.Stdout {
		t.Fatalf("SetWriter(nil) writer = %v, want os.Stdout", h.state.writer)
	}
}

func TestHandlerSetErrorHandler(t *testing.T) {
	ctx := context.Background()
	failure := errors.New("write failed")
	h := NewHandler(WithWriter(writeFunc(func([]byte) (int, error) { return 0, failure })), WithTemplate("${Message:l}"))
	child := h.WithGroup("frame") // 先于 SetErrorHandler 派生。
	handle := func() {
		t.Helper()
		if err := child.Handle(ctx, testRecord()); !errors.Is(err, failure) {
			t.Fatalf("Handle error = %v, want %v", err, failure)
		}
	}

	var first, second []error
	handle() // 未设置回调时仍返回错误。
	h.SetErrorHandler(func(err error) { first = append(first, err) })
	handle()
	h.SetErrorHandler(func(err error) { second = append(second, err) })
	handle()
	h.SetErrorHandler(nil)
	handle()
	if len(first) != 1 || len(second) != 1 || !errors.Is(first[0], failure) || !errors.Is(second[0], failure) {
		t.Fatalf("callbacks: first=%v, second=%v; want one write error each", first, second)
	}
}

// 回调在写锁之外执行，因此可以在回调内替换 Writer 来恢复输出。
func TestHandlerErrorHandlerMayCallSetters(t *testing.T) {
	ctx := context.Background()
	var recovered bytes.Buffer
	h := NewHandler(WithWriter(writeFunc(func([]byte) (int, error) { return 0, errors.New("broken") })), WithTemplate("${Message:l}"))
	h.SetErrorHandler(func(error) {
		h.SetWriter(&recovered)
		h.SetErrorHandler(nil)
	})
	done := make(chan error, 1)
	go func() { done <- h.Handle(ctx, testRecord()) }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("first write unexpectedly succeeded")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("error callback deadlocked while calling SetWriter")
	}
	if err := h.Handle(ctx, testRecord()); err != nil || recovered.String() != "hello\n" {
		t.Fatalf("after recovery: output = %q, error = %v", recovered.String(), err)
	}
}

// retirableWriter 统计被标记为已退役后仍收到的写入，用来验证 SetWriter 返回后旧 Writer 不再被调用。
type retirableWriter struct {
	bytes.Buffer
	retired    atomic.Bool
	violations atomic.Int64
}

func (w *retirableWriter) Write(p []byte) (int, error) {
	if w.retired.Load() {
		w.violations.Add(1)
	}
	return w.Buffer.Write(p)
}

// 写日志的同时不断调整全部设置：不能出现数据竞争，每条记录必须完整地来自某一个模板，
// 每条记录恰好写入一次，且 SetWriter 返回后旧 Writer 不会再被写入。
func TestHandlerSettersUnderConcurrentLogging(t *testing.T) {
	const loggers, records = 8, 250
	ctx := context.Background()
	templates := [...]string{"A ${Message:l}", `{"t":"B","message":${Message}}`}
	levels := [...]slog.Level{slog.LevelDebug, slog.LevelError}
	var first, second retirableWriter
	h := NewHandler(WithWriter(&first), WithTemplate(templates[0]))

	var logging sync.WaitGroup
	for id := range loggers {
		child := h.WithAttrs([]slog.Attr{slog.Int("id", id)})
		logging.Go(func() {
			for range records {
				child.Enabled(ctx, slog.LevelInfo) // 与调级并发读取阈值。
				if err := child.Handle(ctx, testRecord()); err != nil {
					t.Error(err)
					return
				}
			}
		})
	}

	stop := make(chan struct{})
	var setting sync.WaitGroup
	repeat := func(step func(i int)) {
		setting.Go(func() {
			for i := 0; ; i++ {
				select {
				case <-stop:
					return
				default:
				}
				step(i)
				runtime.Gosched()
			}
		})
	}
	repeat(func(i int) { h.SetTemplate(templates[i%len(templates)]) })
	repeat(func(i int) { h.SetLevel(levels[i%len(levels)]) })
	repeat(func(i int) {
		if i%2 == 0 {
			h.SetErrorHandler(func(error) {})
		} else {
			h.SetErrorHandler(nil)
		}
	})
	current, next := &first, &second // 仅由下面这个 goroutine 访问。
	repeat(func(int) {
		next.retired.Store(false) // 安装前先恢复，避免把合法写入算作违规。
		h.SetWriter(next)
		current.retired.Store(true) // 此后任何写入 current 都违反保证。
		current, next = next, current
	})
	logging.Wait()
	close(stop)
	setting.Wait()

	total := 0
	for _, w := range []*retirableWriter{&first, &second} {
		if n := w.violations.Load(); n != 0 {
			t.Errorf("%d writes reached a writer after SetWriter returned", n)
		}
		text := w.String()
		if text == "" {
			continue
		}
		for line := range strings.SplitSeq(strings.TrimSuffix(text, "\n"), "\n") {
			total++
			if strings.HasPrefix(line, "A hello id=") {
				continue
			}
			var record struct {
				T, Message string
				ID         int
			}
			if err := json.Unmarshal([]byte(line), &record); err != nil || record.T != "B" || record.Message != "hello" {
				t.Errorf("record %q is not a complete output of either template: %v", line, err)
			}
		}
	}
	if total != loggers*records {
		t.Errorf("records written = %d, want %d", total, loggers*records)
	}
}
