package xslog

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestHandlerAutomaticNewline(t *testing.T) {
	for _, body := range []string{"plain", "plain\n", "plain\r\n", "plain\n\n", "first\nsecond", ""} {
		t.Run(fmt.Sprintf("%q", body), func(t *testing.T) {
			var out testWriter
			h := NewHandler(WithWriter(&out), withTestLayout(layoutFunc(func(dst []byte, _ templateValues) ([]byte, error) {
				return append(dst, body...), nil
			})))
			if err := h.Handle(context.Background(), testRecord()); err != nil {
				t.Fatal(err)
			}
			want := body
			if !strings.HasSuffix(want, "\n") {
				want += "\n"
			}
			if out.String() != want || out.writes != 1 {
				t.Fatalf("output = %q, writes = %d; want %q in one write", out.String(), out.writes, want)
			}
		})
	}
}

func TestHandlerDefaultTemplate(t *testing.T) {
	unsetTestEnv(t, "Time")
	unsetTestEnv(t, "Source")
	r := testRecord(slog.String("network", "tcp"), slog.Int("port", 8080))
	if got, want := renderRecord(t, r), "2024-01-02T03:04:05.678Z INFO [] message=\"hello\" network=\"tcp\" port=8080\n"; got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
	r.Time = time.Time{}
	if got, want := renderRecord(t, r), " INFO [] message=\"hello\" network=\"tcp\" port=8080\n"; got != want {
		t.Fatalf("missing metadata = %q, want %q", got, want)
	}
}

func TestDefaultOutputUsesTemplateRendering(t *testing.T) {
	unsetTestEnv(t, "Source")
	r := testRecord(slog.String("stack", "first\nsecond"), slog.Int("count", 2))
	want := renderRecord(t, r, WithTemplate(DefaultTemplate))
	for _, opts := range [][]Option{nil, {WithTemplate("")}, {WithTemplate(DefaultTemplate)}, {WithTemplate("${Message:l}"), WithTemplate("")}} {
		var out testWriter
		h := NewHandler(append(opts, WithWriter(&out))...)
		if compiled, ok := h.currentLayout().(*outputTemplate); !ok || compiled.json {
			t.Fatalf("default renderer = %#v, want a text outputTemplate", h.currentLayout())
		}
		if err := h.Handle(context.Background(), r); err != nil || out.String() != want || out.writes != 1 {
			t.Fatalf("default output = %q, error = %v, writes = %d; want %q", out.String(), err, out.writes, want)
		}
	}
}

func TestDefaultTemplateUsesEnvironmentFallback(t *testing.T) {
	t.Setenv("Time", "environment-time")
	t.Setenv("Source", "environment.go:1")
	r := testRecord()
	r.Time = time.Time{}
	if got, want := renderRecord(t, r), "\"environment-time\" INFO [environment.go:1] message=\"hello\"\n"; got != want {
		t.Fatalf("default fallback = %q, want %q", got, want)
	}
}

func TestHandlerLevelOptions(t *testing.T) {
	ctx := context.Background()
	for _, opts := range [][]Option{nil, {nil}, {WithLevel(nil)}, {WithLevel(slog.LevelError), WithLevel(nil)}} {
		h := NewHandler(opts...)
		if !h.Enabled(ctx, slog.LevelDebug) || h.Enabled(ctx, slog.LevelDebug-1) {
			t.Fatal("default level is not Debug")
		}
	}
	var out bytes.Buffer
	var level slog.LevelVar
	level.Set(slog.LevelWarn)
	log := slog.New(NewHandler(WithWriter(&out), WithLevel(&level), WithTemplate("${Message:l}")))
	log.Info("filtered")
	log.Warn("warn")
	level.Set(slog.LevelDebug)
	log.Debug("debug")
	if got := out.String(); got != "warn\ndebug\n" {
		t.Fatalf("dynamic levels = %q", got)
	}
	level.Set(slog.LevelError)
	if log.Enabled(ctx, slog.LevelWarn) || !log.Enabled(ctx, slog.LevelError) {
		t.Fatal("LevelVar updates are not observed")
	}
}

func TestHandlerAttributesAndGroups(t *testing.T) {
	for _, template := range []string{"${Message:l}", `{"message":${Message}}`} {
		t.Run(template, func(t *testing.T) {
			var out bytes.Buffer
			root := slog.New(NewHandler(WithWriter(&out), WithTemplate(template))).With("app", "gate")
			parent := root.WithGroup("frame").With("fixed", 1)
			left, right := parent.With("child", "left"), parent.With("child", "right")
			left.WithGroup("inner").Info("hello", "seq", 2)
			right.Info("hello", "seq", 3)
			parent.WithGroup("unused").Info("hello")
			root.Info("hello")
			lines := strings.Split(strings.TrimSuffix(out.String(), "\n"), "\n")
			if len(lines) != 4 {
				t.Fatal(out.String())
			}
			if strings.HasPrefix(template, "{") {
				for i, want := range []string{
					`{"message":"hello","app":"gate","frame":{"fixed":1,"child":"left","inner":{"seq":2}}}`,
					`{"message":"hello","app":"gate","frame":{"fixed":1,"child":"right","seq":3}}`,
					`{"message":"hello","app":"gate","frame":{"fixed":1}}`,
					`{"message":"hello","app":"gate"}`,
				} {
					requireJSON(t, lines[i], want)
				}
			} else {
				want := "hello app=\"gate\" frame.fixed=1 frame.child=\"left\" frame.inner.seq=2\n" +
					"hello app=\"gate\" frame.fixed=1 frame.child=\"right\" frame.seq=3\n" +
					"hello app=\"gate\" frame.fixed=1\nhello app=\"gate\"\n"
				if out.String() != want {
					t.Fatalf("groups = %q, want %q", out.String(), want)
				}
			}
		})
	}
}

func TestHandlerNormalizesAttributes(t *testing.T) {
	r := testRecord(slog.Attr{}, slog.String("", "discard"), slog.Group("empty"),
		slog.Group("ignored", slog.String("", "discard"), slog.Group("empty")),
		slog.Group("", slog.Int("inline", 1)), slog.Any("nil", nil))
	requireJSON(t, renderRecord(t, r, WithTemplate(`{}`)), `{"inline":1,"nil":null}`)
	var out bytes.Buffer
	h := NewHandler(WithWriter(&out), WithTemplate(`{}`))
	derived := h.WithAttrs([]slog.Attr{slog.Attr{}, slog.Group("empty")}).WithGroup("").WithGroup("empty")
	if err := derived.Handle(context.Background(), testRecord()); err != nil {
		t.Fatal(err)
	}
	requireJSON(t, out.String(), `{}`)
}

type countingValue struct{ calls *atomic.Int64 }

func (v countingValue) LogValue() slog.Value { return slog.Int64Value(v.calls.Add(1)) }

type countingStringer struct{ calls *atomic.Int64 }

func (v countingStringer) String() string { return fmt.Sprintf("value-%d", v.calls.Add(1)) }

func TestHandlerResolvesValuesAtTheRightTime(t *testing.T) {
	var bound, dynamic, stringer atomic.Int64
	var out bytes.Buffer
	log := slog.New(NewHandler(WithWriter(&out), WithTemplate(`{}`))).With(
		slog.Any("bound", countingValue{&bound}), slog.Any("text", countingStringer{&stringer}))
	for range 2 {
		log.Info("hello", slog.Any("dynamic", countingValue{&dynamic}))
	}
	if bound.Load() != 1 || stringer.Load() != 1 || dynamic.Load() != 2 {
		t.Fatalf("calls: bound=%d, stringer=%d, dynamic=%d", bound.Load(), stringer.Load(), dynamic.Load())
	}
	lines := strings.Split(strings.TrimSuffix(out.String(), "\n"), "\n")
	for i, line := range lines {
		requireJSON(t, line, fmt.Sprintf(`{"bound":1,"text":"value-1","dynamic":%d}`, i+1))
	}
}

func TestHandlerLayoutReceivesTypedAttributes(t *testing.T) {
	checked := false
	got := renderRecord(t, testRecord(slog.Time("deadline", testTime()), slog.Group("frame", slog.Int("seq", 2))),
		withTestLayout(layoutFunc(func(dst []byte, values templateValues) ([]byte, error) {
			for _, name := range []string{"deadline", "frame.seq", "Properties", "Attrs"} {
				if _, ok := values.Lookup(name); ok {
					t.Errorf("business value %q was exposed as metadata", name)
				}
			}
			value, found := values.Lookup("Time")
			attrs := values.Attrs()
			if !found || value.Kind() != slog.KindTime || len(attrs) != 2 || attrs[0].Value.Kind() != slog.KindTime || attrs[1].Value.Kind() != slog.KindGroup {
				t.Fatalf("metadata/attributes lost types: %v, %v", value, attrs)
			}
			checked = true
			return append(dst, "typed"...), nil
		})))
	if !checked || got != "typed\n" {
		t.Fatalf("output = %q, checked = %v", got, checked)
	}
}

func TestHandlerErrors(t *testing.T) {
	ctx := context.Background()
	want := errors.New("failed")
	for _, phase := range []string{"layout", "writer"} {
		t.Run(phase, func(t *testing.T) {
			var out testWriter
			var observed error
			opts := []Option{WithWriter(&out), WithErrorHandler(func(err error) { observed = err })}
			if phase == "layout" {
				opts = append(opts, withTestLayout(layoutFunc(func(dst []byte, _ templateValues) ([]byte, error) {
					return append(dst, "partial"...), want
				})))
			} else {
				opts = append(opts, WithWriter(writeFunc(func([]byte) (int, error) { return 0, want })))
			}
			if err := NewHandler(opts...).Handle(ctx, testRecord()); !errors.Is(err, want) || !errors.Is(observed, want) {
				t.Fatalf("error = %v, callback = %v", err, observed)
			}
			if out.writes != 0 {
				t.Fatal("failed layout wrote partial output")
			}
		})
	}
}

func TestHandlerCallbackAndWriterPanicReleaseLock(t *testing.T) {
	for _, panicFirst := range []bool{false, true} {
		t.Run(fmt.Sprint(panicFirst), func(t *testing.T) {
			var calls atomic.Int64
			var h *Handler
			writer := writeFunc(func(p []byte) (int, error) {
				if calls.Add(1) == 1 {
					if panicFirst {
						panic("writer panic")
					}
					return 0, errors.New("first write")
				}
				return len(p), nil
			})
			h = NewHandler(WithWriter(writer), WithErrorHandler(func(error) {
				_ = h.Handle(context.Background(), testRecord())
			}))
			done := make(chan any, 1)
			go func() {
				defer func() { done <- recover() }()
				_ = h.Handle(context.Background(), testRecord())
			}()
			select {
			case recovered := <-done:
				if panicFirst && recovered != "writer panic" {
					t.Fatalf("panic = %v", recovered)
				}
				if !panicFirst && recovered != nil {
					t.Fatalf("unexpected callback panic: %v", recovered)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("writer lock was held during callback")
			}
			if panicFirst {
				go func() { done <- h.Handle(context.Background(), testRecord()) }()
				select {
				case err := <-done:
					if err != nil {
						t.Fatal(err)
					}
				case <-time.After(2 * time.Second):
					t.Fatal("writer panic left the lock held")
				}
			}
			if calls.Load() != 2 {
				t.Fatalf("write calls = %d, want 2", calls.Load())
			}
		})
	}
}

func TestHandlerLargeRecordsDoNotLeakIntoLaterRecords(t *testing.T) {
	var out bytes.Buffer
	h := NewHandler(WithWriter(&out), WithTemplate("${Message:l}"))
	r := testRecord()
	r.Message = strings.Repeat("x", maxPooledBuf+1)
	for i := range maxPooledAttrs + 1 {
		r.AddAttrs(slog.Int(fmt.Sprintf("field%d", i), i))
	}
	if err := h.Handle(context.Background(), r); err != nil || !strings.HasPrefix(out.String(), r.Message+" field0=0") {
		t.Fatalf("large record: %v", err)
	}
	out.Reset()
	if err := h.Handle(context.Background(), testRecord()); err != nil || out.String() != "hello\n" {
		t.Fatalf("subsequent output = %q, error = %v", out.String(), err)
	}
}

func TestHandlerConcurrentChildrenWriteWholeRecords(t *testing.T) {
	const workers, records = 16, 20
	var pcs [1]uintptr
	runtime.Callers(1, pcs[:])
	frame, _ := runtime.CallersFrames(pcs[:]).Next()
	r := testRecord()
	r.PC = pcs[0]
	var out testWriter // 依赖 Handler 的共享锁串行化写入。
	h := NewHandler(WithWriter(&out), WithTemplate(`{"message":${Message},"file":${File},"fileName":${FileName},"line":${Line},"function":${Function},"package":${Package},"pid":${PID}}`)).WithAttrs([]slog.Attr{slog.String("app", "gate")}).WithGroup("worker")
	var wg sync.WaitGroup
	errs := make(chan error, workers)
	for id := range workers {
		child := h.WithAttrs([]slog.Attr{slog.Int("id", id)})
		wg.Go(func() {
			for range records {
				if err := child.Handle(context.Background(), r); err != nil {
					errs <- err
					return
				}
			}
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSuffix(out.String(), "\n"), "\n")
	if len(lines) != workers*records || out.writes != len(lines) {
		t.Fatalf("records = %d, writes = %d", len(lines), out.writes)
	}
	counts := make([]int, workers)
	for _, line := range lines {
		var record struct {
			Message, App, File, FileName, Function, Package string
			Line, PID                                       int
			Worker                                          struct{ ID int }
		}
		if err := json.Unmarshal([]byte(line), &record); err != nil || record.Message != "hello" || record.App != "gate" || record.Worker.ID < 0 || record.Worker.ID >= workers {
			t.Fatalf("invalid concurrent record %q: %v", line, err)
		}
		if record.File != frame.File || record.FileName != "handler_test.go" || record.Line != frame.Line || record.Function != "TestHandlerConcurrentChildrenWriteWholeRecords" || record.Package != "github.com/xmapst/xslog" || record.PID != os.Getpid() {
			t.Fatalf("invalid concurrent metadata: %s", line)
		}
		counts[record.Worker.ID]++
	}
	for id, count := range counts {
		if count != records {
			t.Fatalf("worker %d wrote %d records, want %d", id, count, records)
		}
	}
}
