package xslog

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

type trackedWriteCloser struct {
	bytes.Buffer
	closes int
	err    error
}

func (w *trackedWriteCloser) Close() error { w.closes++; return w.err }

func TestSetupUsesDefaultTemplate(t *testing.T) {
	preserveDefaultLogger(t)
	unsetTestEnv(t, "Source")
	for _, opts := range [][]Option{nil, {WithTemplate("")}, {WithTemplate("${Message:l}"), WithTemplate("")}} {
		var out bytes.Buffer
		if err := Setup(append(opts, WithWriter(&out))...); err != nil {
			t.Fatal(err)
		}
		if err := slog.Default().Handler().Handle(context.Background(), testRecord()); err != nil {
			t.Fatal(err)
		}
		if want := "2024-01-02T03:04:05.678Z INFO [] message=\"hello\"\n"; out.String() != want {
			t.Fatalf("default Setup output = %q, want %q", out.String(), want)
		}
	}
}

// Setup 只读取传入 Leveler 的初始级别，之后由 SetLevel 调整。
// 重复 Setup 原地更新全局 Handler：此前派生的 Logger 随之使用新的 Writer、模板和级别。
func TestSetupReconfiguresGlobalHandler(t *testing.T) {
	preserveDefaultLogger(t)
	var first, second bytes.Buffer
	var supplied slog.LevelVar
	supplied.Set(slog.LevelWarn)
	if err := Setup(WithWriter(&first), WithLevel(&supplied), WithTemplate("${Message:l}")); err != nil {
		t.Fatal(err)
	}
	old := slog.Default()
	derived := slog.With("module", "gate")
	supplied.Set(slog.LevelDebug) // Setup 不跟随传入的 LevelVar。
	slog.Info("filtered")
	slog.Warn("warn")
	SetLevel(slog.LevelDebug)
	slog.Debug("debug")
	if first.String() != "warn\ndebug\n" {
		t.Fatal(first.String())
	}
	if err := Setup(WithWriter(&second), WithLevel(slog.LevelError), WithTemplate("${Message:l}!")); err != nil {
		t.Fatal(err)
	}
	old.Warn("old filtered")
	derived.Error("derived")
	slog.Error("error")
	if first.String() != "warn\ndebug\n" || second.String() != "derived! module=\"gate\"\nerror!\n" {
		t.Fatalf("first=%q, second=%q", first.String(), second.String())
	}
	SetLevel(slog.LevelDebug)
	old.Debug("old debug")
	if !strings.HasSuffix(second.String(), "old debug!\n") {
		t.Fatal("previous Loggers do not follow SetLevel")
	}
}

// 包级的 Set 函数作用于全局 Handler：已经派生的 Logger 随之变化。
func TestPackageSettersApplyToGlobalHandler(t *testing.T) {
	preserveDefaultLogger(t)
	var first, second bytes.Buffer
	if err := Setup(WithWriter(&first), WithLevel(slog.LevelWarn), WithTemplate("${Message:l}")); err != nil {
		t.Fatal(err)
	}
	log := slog.With("app", "gate")
	log.Info("dropped")
	SetLevel(slog.LevelInfo)
	log.Info("one")
	SetTemplate(`{"message":${Message}}`)
	log.Info("two")
	SetWriter(&second)
	log.Info("three")
	if want := "one app=\"gate\"\n{\"message\":\"two\",\"app\":\"gate\"}\n"; first.String() != want {
		t.Fatalf("first = %q, want %q", first.String(), want)
	}
	if want := "{\"message\":\"three\",\"app\":\"gate\"}\n"; second.String() != want {
		t.Fatalf("second = %q, want %q", second.String(), want)
	}

	failure := errors.New("write failed")
	var reported []error
	SetWriter(writeFunc(func([]byte) (int, error) { return 0, failure }))
	SetErrorHandler(func(err error) { reported = append(reported, err) })
	log.Info("four")
	SetErrorHandler(nil)
	log.Info("five")
	if len(reported) != 1 || !errors.Is(reported[0], failure) {
		t.Fatalf("reported errors = %v, want one %v", reported, failure)
	}
}

func TestSetupFailuresPreserveLoggerAndFile(t *testing.T) {
	preserveDefaultLogger(t)
	path := filepath.Join(t.TempDir(), "active.log")
	if err := Setup(WithFile(path, WithRotationHours(0)), WithTemplate("${Message:l}")); err != nil {
		t.Fatal(err)
	}
	previous, file := slog.Default(), std.file.Load()
	newPath := filepath.Join(t.TempDir(), "not-created", "app.log")
	for _, opts := range [][]Option{
		{WithFile(" \t")},
		{WithFile(newPath, WithMaxSizeMB(-1))},
	} {
		if err := Setup(opts...); err == nil {
			t.Fatal("invalid Setup succeeded")
		}
		if slog.Default() != previous || std.file.Load() != file {
			t.Fatal("failed Setup replaced the logger or current file")
		}
	}
	if _, err := os.Stat(filepath.Dir(newPath)); !os.IsNotExist(err) {
		t.Fatalf("failed Setup created its target directory: %v", err)
	}
	slog.Info("still active")
	if err := Close(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "still active\n" || slog.Default() != previous {
		t.Fatalf("active file = %q, error = %v", data, err)
	}
}

func TestSetupAcceptsInvalidJSONAsText(t *testing.T) {
	preserveDefaultLogger(t)
	path := filepath.Join(t.TempDir(), "text.log")
	previous := slog.Default()
	if err := Setup(WithFile(path, WithRotationHours(0)), WithTemplate(`{${Time},"Level":${Level}}`)); err != nil {
		t.Fatal(err)
	}
	if slog.Default() == previous {
		t.Fatal("text fallback did not install the new Logger")
	}
	if err := slog.Default().Handler().Handle(context.Background(), testRecord(slog.Int("count", 2))); err != nil {
		t.Fatal(err)
	}
	if err := Close(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "{2024-01-02T03:04:05.678Z,\"Level\":INFO} count=2\n" {
		t.Fatalf("file = %q, error = %v", data, err)
	}
}

func TestSetupWriterAndFileOptionOrder(t *testing.T) {
	preserveDefaultLogger(t)
	var out bytes.Buffer
	unused := filepath.Join(t.TempDir(), "unused", "app.log")
	if err := Setup(WithFile(unused), WithWriter(&out), WithTemplate("${Message:l}")); err != nil {
		t.Fatal(err)
	}
	slog.Info("writer")
	if out.String() != "writer\n" {
		t.Fatal(out.String())
	}
	if _, err := os.Stat(filepath.Dir(unused)); !os.IsNotExist(err) {
		t.Fatalf("overridden file was opened: %v", err)
	}
	path := filepath.Join(t.TempDir(), "active.log")
	if err := Setup(WithWriter(&out), WithFile(path, WithRotationHours(0)), WithTemplate("${Message:l}")); err != nil {
		t.Fatal(err)
	}
	slog.Info("file")
	if err := Close(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "file\n" {
		t.Fatalf("file = %q, error = %v", data, err)
	}
}

func TestSetupDoesNotCloseCallerWriter(t *testing.T) {
	preserveDefaultLogger(t)
	writer := new(trackedWriteCloser)
	if err := Setup(WithWriter(writer), WithTemplate("${Message:l}")); err != nil {
		t.Fatal(err)
	}
	slog.Info("hello")
	for range 2 {
		if err := Close(); err != nil {
			t.Fatal(err)
		}
	}
	if err := Setup(WithWriter(io.Discard)); err != nil {
		t.Fatal(err)
	}
	if writer.closes != 0 || writer.String() != "hello\n" {
		t.Fatalf("caller writer: closes=%d, data=%q", writer.closes, writer.String())
	}
}

func TestManagerClosesPreviousFileAndReportsCloseErrors(t *testing.T) {
	preserveDefaultLogger(t)
	for _, closeErr := range []error{nil, errors.New("close failed")} {
		old := &trackedWriteCloser{err: closeErr}
		m := &manager{handler: NewHandler()}
		m.file.Store(&FileOutput{WriteCloser: old})
		var out bytes.Buffer
		if err := m.setup((options{}).apply([]Option{WithWriter(&out), WithTemplate("${Message:l}")})); err != nil {
			t.Fatal(err)
		}
		if old.closes != 1 || m.file.Load() != nil {
			t.Fatalf("previous file close count = %d", old.closes)
		}
		if closeErr != nil && (!strings.Contains(out.String(), "close previous log file") || !strings.Contains(out.String(), closeErr.Error())) {
			t.Fatalf("close warning = %q", out.String())
		}
		if err := m.close(); err != nil || old.closes != 1 {
			t.Fatalf("repeated close = %v, count = %d", err, old.closes)
		}
	}
}

func TestSetupConcurrentLifecycle(t *testing.T) {
	preserveDefaultLogger(t)
	var wg sync.WaitGroup
	errs := make(chan error, 40)
	for range 20 {
		wg.Go(func() { errs <- Setup(WithWriter(io.Discard), WithTemplate("${Message:l}")) })
		wg.Go(func() { errs <- Close() })
		wg.Go(func() { SetLevel(slog.LevelDebug); slog.Info("concurrent") })
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestWriterDefaults(t *testing.T) {
	if got := (options{}).output(); got != os.Stdout {
		t.Fatal("default output is not stdout")
	}
	o := (options{}).apply([]Option{WithFile("unused"), WithWriter(nil)})
	if o.output() != os.Stdout || o.hasFile {
		t.Fatal("WithWriter(nil) did not override file output")
	}
}
