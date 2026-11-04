package xslog

import (
	"bytes"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
)

func TestAdapterMethodFamilies(t *testing.T) {
	for _, tc := range []struct {
		name, level string
		call        func(*Adapter)
	}{
		{"Print", "INFO", func(a *Adapter) { a.Print("value ", 7) }},
		{"Printf", "INFO", func(a *Adapter) { a.Printf("value %d", 7) }},
		{"Println", "INFO", func(a *Adapter) { a.Println("value", 7) }},
		{"Debug", "DEBUG", func(a *Adapter) { a.Debug("value ", 7) }},
		{"Debugf", "DEBUG", func(a *Adapter) { a.Debugf("value %d", 7) }},
		{"Debugln", "DEBUG", func(a *Adapter) { a.Debugln("value", 7) }},
		{"Info", "INFO", func(a *Adapter) { a.Info("value ", 7) }},
		{"Infof", "INFO", func(a *Adapter) { a.Infof("value %d", 7) }},
		{"Infoln", "INFO", func(a *Adapter) { a.Infoln("value", 7) }},
		{"Warn", "WARN", func(a *Adapter) { a.Warn("value ", 7) }},
		{"Warnf", "WARN", func(a *Adapter) { a.Warnf("value %d", 7) }},
		{"Warnln", "WARN", func(a *Adapter) { a.Warnln("value", 7) }},
		{"Warning", "WARN", func(a *Adapter) { a.Warning("value ", 7) }},
		{"Warningf", "WARN", func(a *Adapter) { a.Warningf("value %d", 7) }},
		{"Warningln", "WARN", func(a *Adapter) { a.Warningln("value", 7) }},
		{"Error", "ERROR", func(a *Adapter) { a.Error("value ", 7) }},
		{"Errorf", "ERROR", func(a *Adapter) { a.Errorf("value %d", 7) }},
		{"Errorln", "ERROR", func(a *Adapter) { a.Errorln("value", 7) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			a := NewAdapter(slog.New(NewHandler(WithWriter(&out), WithTemplate("${Level}:${Message:l}"))))
			tc.call(a)
			if want := tc.level + ":value 7\n"; out.String() != want {
				t.Fatalf("output = %q, want %q", out.String(), want)
			}
		})
	}
}

func TestAdapterStructuredMethods(t *testing.T) {
	for _, tc := range []struct {
		level string
		call  func(*Adapter)
	}{
		{"DEBUG", func(a *Adapter) { a.Debugw("event", "answer", 42, "dangling") }},
		{"INFO", func(a *Adapter) { a.Infow("event", "answer", 42, "dangling") }},
		{"WARN", func(a *Adapter) { a.Warnw("event", "answer", 42, "dangling") }},
		{"ERROR", func(a *Adapter) { a.Errorw("event", "answer", 42, "dangling") }},
	} {
		t.Run(tc.level, func(t *testing.T) {
			var out bytes.Buffer
			a := NewAdapter(slog.New(NewHandler(WithWriter(&out), WithTemplate(`{"level":${Level},"message":${Message}}`))))
			tc.call(a)
			requireJSON(t, out.String(), fmt.Sprintf(`{"level":%q,"message":"event","answer":42,"!BADKEY":"dangling"}`, tc.level))
		})
	}
}

func TestAdapterFilteringAvoidsFormatting(t *testing.T) {
	var calls atomic.Int64
	var out bytes.Buffer
	var level slog.LevelVar
	level.Set(slog.LevelInfo)
	a := NewAdapter(slog.New(NewHandler(WithWriter(&out), WithLevel(&level))))
	value := countingStringer{&calls}
	a.Debugf("%s", value)
	a.Debug(value)
	a.Debugln(value)
	a.Debugw("filtered", "value", value)
	if calls.Load() != 0 || out.Len() != 0 {
		t.Fatalf("disabled logging called String %d times", calls.Load())
	}
	if a.V(1) || !a.V(0) || !a.V(-1) {
		t.Fatal("verbosity does not map to Debug/Info")
	}
	level.Set(slog.LevelDebug)
	if !a.V(1) {
		t.Fatal("verbosity did not observe dynamic level")
	}
	level.Set(slog.LevelWarn)
	if a.V(0) || a.V(1) {
		t.Fatal("verbosity ignored Warn threshold")
	}
}

func TestAdapterNilLoggerFollowsSetup(t *testing.T) {
	preserveDefaultLogger(t)
	var first, second bytes.Buffer
	a := NewAdapter(nil)
	if err := Setup(WithWriter(&first), WithTemplate("${Message:l}")); err != nil {
		t.Fatal(err)
	}
	a.Info("first")
	if err := Setup(WithWriter(&second), WithTemplate("${Message:l}")); err != nil {
		t.Fatal(err)
	}
	a.Println("second")
	if first.String() != "first\n" || second.String() != "second\n" {
		t.Fatalf("first=%q, second=%q", first.String(), second.String())
	}
}

func TestAdapterSourcePointsToCaller(t *testing.T) {
	for _, call := range []func(*Adapter) runtime.Frame{
		func(a *Adapter) runtime.Frame {
			pc, file, line, _ := runtime.Caller(0)
			a.Info("caller")
			return runtime.Frame{File: file, Line: line + 1, Function: runtime.FuncForPC(pc).Name()}
		},
		func(a *Adapter) runtime.Frame {
			pc, file, line, _ := runtime.Caller(0)
			a.Infof("caller %d", 1)
			return runtime.Frame{File: file, Line: line + 1, Function: runtime.FuncForPC(pc).Name()}
		},
		func(a *Adapter) runtime.Frame {
			pc, file, line, _ := runtime.Caller(0)
			a.Println("caller")
			return runtime.Frame{File: file, Line: line + 1, Function: runtime.FuncForPC(pc).Name()}
		},
		func(a *Adapter) runtime.Frame {
			pc, file, line, _ := runtime.Caller(0)
			a.Infow("caller")
			return runtime.Frame{File: file, Line: line + 1, Function: runtime.FuncForPC(pc).Name()}
		},
	} {
		var out bytes.Buffer
		a := NewAdapter(slog.New(NewHandler(WithWriter(&out), WithTemplate("${Source}|${File}|${FileName}|${Line}|${Function}|${Package}"))))
		frame := call(a)
		want := fmt.Sprintf("%s|%s|%s|%d|%s|github.com/xmapst/xslog\n", testSourceLocation(frame.File, frame.Line), frame.File, filepath.Base(frame.File), frame.Line, strings.TrimPrefix(frame.Function, "github.com/xmapst/xslog."))
		if out.String() != want {
			t.Fatalf("source = %q, want %q", out.String(), want)
		}
	}
}

func TestAdapterPanicMethods(t *testing.T) {
	for _, filtered := range []bool{false, true} {
		for _, call := range []func(*Adapter){
			func(a *Adapter) { a.Panic("panic ", 7) },
			func(a *Adapter) { a.Panicf("panic %d", 7) },
			func(a *Adapter) { a.Panicln("panic", 7) },
		} {
			var out bytes.Buffer
			level := slog.LevelDebug
			if filtered {
				level = slog.LevelError + 1
			}
			a := NewAdapter(slog.New(NewHandler(WithWriter(&out), WithLevel(level), WithTemplate("${Message:l}"))))
			var recovered any
			func() {
				defer func() { recovered = recover() }()
				call(a)
			}()
			want := "panic 7\n"
			if filtered {
				want = ""
			}
			if recovered != "panic 7" || out.String() != want {
				t.Fatalf("panic = %v, output = %q", recovered, out.String())
			}
		}
	}
}

func TestAdapterFatalProcess(t *testing.T) {
	if mode := os.Getenv("XSLOG_ADAPTER_FATAL_HELPER"); mode != "" {
		level := slog.LevelDebug
		if strings.HasSuffix(mode, "-filtered") {
			level = slog.LevelError + 1
		}
		a := NewAdapter(slog.New(NewHandler(WithWriter(os.Stdout), WithLevel(level), WithTemplate("${Message:l}"))))
		defer fmt.Fprintln(os.Stdout, "defer ran")
		switch strings.TrimSuffix(mode, "-filtered") {
		case "Fatal":
			a.Fatal("fatal ", 7)
		case "Fatalf":
			a.Fatalf("fatal %d", 7)
		case "Fatalln":
			a.Fatalln("fatal", 7)
		}
		t.Fatal("Fatal returned")
	}
	for _, name := range []string{"Fatal", "Fatalf", "Fatalln", "Fatal-filtered", "Fatalf-filtered", "Fatalln-filtered"} {
		t.Run(name, func(t *testing.T) {
			cmd := exec.Command(os.Args[0], "-test.run=^TestAdapterFatalProcess$")
			cmd.Env = append(os.Environ(), "XSLOG_ADAPTER_FATAL_HELPER="+name)
			output, err := cmd.Output()
			var exit *exec.ExitError
			if !errors.As(err, &exit) || exit.ExitCode() != 1 {
				t.Fatalf("process error = %v, output = %q", err, output)
			}
			want := "fatal 7\n"
			if strings.HasSuffix(name, "-filtered") {
				want = ""
			}
			if string(output) != want {
				t.Fatalf("Fatal output = %q, want %q", output, want)
			}
		})
	}
}
