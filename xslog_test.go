package xslog

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"
)

func record() slog.Record {
	return slog.NewRecord(timeNow(), slog.LevelInfo, "hello", 0)
}

func timeNow() (t time.Time) { return time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC) }

func TestJSONHandlerSkipsEmptyKeyValue(t *testing.T) {
	var out bytes.Buffer
	h := NewJSONHandler(WithWriter(&out))
	r := record()
	r.Add(slog.String("", "bad"), slog.Int("ok", 1))
	if err := h.Handle(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if _, ok := got[""]; ok {
		t.Fatal("empty key was emitted")
	}
	if got["ok"] != float64(1) {
		t.Fatalf("ok = %#v", got["ok"])
	}
}

func TestConsoleHandlerSkipsEmptyKeyValue(t *testing.T) {
	var out bytes.Buffer
	h := NewConsoleHandler(WithWriter(&out))
	r := record()
	r.Add(slog.String("", "bad"), slog.Int("ok", 1))
	if err := h.Handle(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), `""=`) {
		t.Fatal("empty key was emitted")
	}
	if !strings.Contains(out.String(), "ok=1") {
		t.Fatal(out.String())
	}
}

func TestJSONHeaderPanicIsContained(t *testing.T) {
	var out bytes.Buffer
	h := NewJSONHandler(WithWriter(&out), WithJSONHeader(func() []HeaderField { panic("bad") }))
	if err := h.Handle(context.Background(), record()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "header_error") {
		t.Fatal(out.String())
	}
}

type failWriter struct{ err error }

func (w failWriter) Write([]byte) (int, error) { return 0, w.err }

func TestWriterErrorCallback(t *testing.T) {
	want := errors.New("write failed")
	var got error
	h := NewJSONHandler(WithWriter(failWriter{want}), WithErrorHandler(func(err error) { got = err }))
	if err := h.Handle(context.Background(), record()); !errors.Is(err, want) {
		t.Fatal(err)
	}
	if !errors.Is(got, want) {
		t.Fatalf("callback error = %v", got)
	}
}

func TestSetupConcurrentDoesNotRace(t *testing.T) {
	var wg sync.WaitGroup
	for range 20 {
		wg.Add(3)
		go func() { defer wg.Done(); _ = Setup(WithWriter(&bytes.Buffer{})) }()
		go func() { defer wg.Done(); _ = Setup(WithWriter(&bytes.Buffer{})) }()
		go func() { defer wg.Done(); _ = Close() }()
	}
	wg.Wait()
}
