package xslog

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestTemplateCallerMetadata(t *testing.T) {
	var pcs [1]uintptr
	runtime.Callers(1, pcs[:])
	frame, _ := runtime.CallersFrames(pcs[:]).Next()
	r := testRecord()
	r.PC = pcs[0]
	for _, name := range []string{"Source", "File", "FileName", "Line", "Function", "Package"} {
		t.Setenv(name, "environment-"+name)
	}
	want := fmt.Sprintf("%s|%s|%s|%d|%s|github.com/xmapst/xslog\n",
		testSourceLocation(frame.File, frame.Line), frame.File, filepath.Base(frame.File), frame.Line, frame.Function)
	for _, template := range []string{
		"${Source}|${File}|${FileName}|${Line}|${Function}|${Package}",
		"%Source%|%File%|%FileName%|%Line%|%Function%|%Package%",
		"${sOuRcE}|${fIlE}|${fIlEnAmE}|${lInE}|${fUnCtIoN}|${pAcKaGe}",
	} {
		if got := renderRecord(t, r, WithTemplate(template)); got != want {
			t.Fatalf("caller output = %q, want %q", got, want)
		}
	}
	if got, want := renderRecord(t, r, WithTemplate("[${Line,8}]/[${FileName,-24}]")),
		fmt.Sprintf("[%8d]/[%-24s]\n", frame.Line, filepath.Base(frame.File)); got != want {
		t.Fatalf("caller alignment = %q, want %q", got, want)
	}
	r.AddAttrs(slog.String("File", "business-file"), slog.Int("Line", -1))
	wantJSON, err := json.Marshal(map[string]any{
		"source": testSourceLocation(frame.File, frame.Line), "file": frame.File,
		"fileName": filepath.Base(frame.File), "line": frame.Line, "quotedLine": fmt.Sprint(frame.Line),
		"function": frame.Function, "package": "github.com/xmapst/xslog", "File": "business-file", "Line": -1,
	})
	if err != nil {
		t.Fatal(err)
	}
	requireJSON(t, renderRecord(t, r, WithTemplate(`{"source":${Source},"file":${File},"fileName":${FileName},"line":${Line},"quotedLine":"${Line}","function":${Function},"package":${Package}}`)), string(wantJSON))
}

func TestTemplateCallerMetadataMissingAndFallback(t *testing.T) {
	for _, name := range []string{"File", "FileName", "Line", "Function", "Package"} {
		t.Run(name, func(t *testing.T) {
			unsetTestEnv(t, name)
			lower := strings.ToLower(name)
			unsetTestEnv(t, lower)
			r := testRecord()
			if got := renderRecord(t, r, WithTemplate("${"+name+"}|%"+name+"%")); got != "|\n" {
				t.Fatalf("missing caller value = %q", got)
			}
			requireJSON(t, renderRecord(t, r, WithTemplate(`{"missing":${`+name+`},"array":[${`+name+`}],"keep":null}`)), `{"array":[],"keep":null}`)
			t.Setenv(name, "fallback-"+name)
			if got := renderRecord(t, r, WithTemplate("${"+name+":l}")); got != "fallback-"+name+"\n" {
				t.Fatalf("caller fallback = %q", got)
			}
			requireJSON(t, renderRecord(t, r, WithTemplate(`{"value":${`+name+`}}`)), `{"value":"fallback-`+name+`"}`)
			if got := renderRecord(t, r, WithTemplate("${"+lower+"}")); got != "\n" {
				t.Fatalf("environment name was normalized: %q", got)
			}
			t.Setenv(name, `{"from":"environment"}`)
			requireJSON(t, renderRecord(t, r, WithTemplate(`{"value":${`+name+`}}`)), `{"value":"{\"from\":\"environment\"}"}`)
			t.Setenv(name, "")
			requireJSON(t, renderRecord(t, r, WithTemplate(`{"value":${`+name+`}}`)), `{"value":""}`)
		})
	}
	unsetTestEnv(t, "File")
	if got := renderRecord(t, testRecord(slog.String("File", "business-file")), WithTemplate("${File}")); got != "File=\"business-file\"\n" {
		t.Fatalf("business field became caller metadata: %q", got)
	}
}

func TestTemplateProcessMetadata(t *testing.T) {
	for _, name := range []string{"PID", "Hostname", "Executable"} {
		t.Setenv(name, "fallback-"+name)
	}
	t.Setenv("pId", "fallback-PID")
	t.Setenv("hOsTnAmE", "fallback-Hostname")
	t.Setenv("eXeCuTaBlE", "fallback-Executable")
	host, err := os.Hostname()
	if err != nil || host == "" {
		host = "fallback-Hostname"
	}
	executable, err := os.Executable()
	if err != nil || executable == "" {
		executable = "fallback-Executable"
	}
	for _, template := range []string{
		"${PID}|${Hostname}|${Executable}", "%PID%|%Hostname%|%Executable%", "${pId}|${hOsTnAmE}|${eXeCuTaBlE}",
	} {
		if got, want := renderRecord(t, testRecord(), WithTemplate(template)), fmt.Sprintf("%d|%s|%s\n", os.Getpid(), host, executable); got != want {
			t.Fatalf("process output = %q, want %q", got, want)
		}
	}
	want, err := json.Marshal(map[string]any{"pid": os.Getpid(), "quotedPID": fmt.Sprint(os.Getpid()), "host": host, "executable": executable})
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	h := NewHandler(WithWriter(&out), WithTemplate(`{"pid":${PID},"quotedPID":"${PID}","host":${Hostname},"executable":${Executable}}`))
	derived := h.WithGroup("unused")
	for _, handler := range []slog.Handler{h, derived} {
		out.Reset()
		if err := handler.Handle(context.Background(), testRecord()); err != nil {
			t.Fatal(err)
		}
		requireJSON(t, out.String(), string(want))
	}
}

func TestTemplateMissingProcessMetadataFallback(t *testing.T) {
	for _, name := range []string{"PID", "Hostname", "Executable"} {
		unsetTestEnv(t, name)
	}
	var template outputTemplate
	template.compile(`{"pid":${PID},"hostname":${Hostname},"executable":${Executable},"keep":null}`)
	values := &recordValues{record: testRecord()}
	out, err := template.Append(nil, values)
	if err != nil {
		t.Fatal(err)
	}
	requireJSON(t, string(out), `{"keep":null}`)
	for _, value := range []string{"123", `{"nested":true}`, ""} {
		for _, name := range []string{"PID", "Hostname", "Executable"} {
			t.Setenv(name, value)
		}
		out, err = template.Append(nil, values)
		if err != nil {
			t.Fatal(err)
		}
		want, err := json.Marshal(map[string]any{"pid": value, "hostname": value, "executable": value, "keep": nil})
		if err != nil {
			t.Fatal(err)
		}
		requireJSON(t, string(out), string(want))
	}
}

func TestHandlerProcessMetadataSnapshot(t *testing.T) {
	var out bytes.Buffer
	h := NewHandler(WithWriter(&out), WithTemplate(`{"pid":${PID},"hostname":${Hostname},"executable":${Executable}}`))
	// 模拟 Handler 创建时的进程快照，渲染和派生时不应重新读取系统信息。
	h.process = processInfo{pid: 123, hostname: "snapshot-host", executable: "/snapshot/program"}
	derived := h.WithGroup("scope").WithAttrs([]slog.Attr{slog.String("app", "gate")})
	for _, tc := range []struct {
		handler slog.Handler
		want    string
	}{
		{h, `{"pid":123,"hostname":"snapshot-host","executable":"/snapshot/program"}`},
		{derived, `{"pid":123,"hostname":"snapshot-host","executable":"/snapshot/program","scope":{"app":"gate"}}`},
	} {
		out.Reset()
		if err := tc.handler.Handle(context.Background(), testRecord()); err != nil {
			t.Fatal(err)
		}
		requireJSON(t, out.String(), tc.want)
	}
}

func TestTemplateMissingQuotedNumericValues(t *testing.T) {
	unsetTestEnv(t, "Line")
	unsetTestEnv(t, "Time")
	r := testRecord()
	r.Time = time.Time{}
	for _, tc := range []struct{ template, want string }{
		{`{"line":"${Line}","keep":1}`, `{"keep":1}`},
		{`{"array":[0,"${Line}",2]}`, `{"array":[0,2]}`},
		{`{"nested":["prefix-${Line}-suffix",null,"${Line}${Line}"]}`, `{"nested":[null]}`},
		{`{"${Line}":"ignored","keep":1}`, `{"keep":1}`},
		{`{"prefix-${Line}":${Line},"keep":1}`, `{"keep":1}`},
		{`{"literal":"null","${Line}":"${Line}","keep":"literal null"}`, `{"literal":"null","keep":"literal null"}`},
		{`{"time":"${Time:unix}","keep":1}`, `{"keep":1}`},
	} {
		t.Run(tc.template, func(t *testing.T) {
			requireJSON(t, renderRecord(t, r, WithTemplate(tc.template)), tc.want)
		})
	}
}

func TestHandlerCallerMetadataPointsToCaller(t *testing.T) {
	var out bytes.Buffer
	logger := slog.New(NewHandler(WithWriter(&out), WithTemplate("${File}|${FileName}|${Line}|${Function}|${Package}")))
	pc, file, line, _ := runtime.Caller(0)
	logger.Info("caller")
	want := fmt.Sprintf("%s|%s|%d|%s|github.com/xmapst/xslog\n", file, filepath.Base(file), line+1, runtime.FuncForPC(pc).Name())
	if out.String() != want {
		t.Fatalf("slog caller = %q, want %q", out.String(), want)
	}
}

func TestTemplateNewLineControl(t *testing.T) {
	t.Setenv("NewLine", "environment")
	for _, tc := range []struct{ template, want string }{
		{"${Message:l}${NewLine}next", "hello\nnext\n"},
		{"${Message:l}%NewLine%next", "hello\nnext\n"},
		{"${Message:l}${nEwLiNe}next", "hello\nnext\n"},
		{"${NewLine}", "\n"},
		{"${Message:l}${NewLine}", "hello\n"},
		{"${Message:l}${NewLine}${NewLine}", "hello\n\n"},
		{`{"message":${Message},${NewLine}"count":1${NewLine}}`, "{\"message\":\"hello\",\n\"count\":1\n}\n"},
		{`${NewLine}{"message":${Message}}${NewLine}`, "\n{\"message\":\"hello\"}\n"},
		{`{"message":${Message}}${NewLine}${NewLine}`, "{\"message\":\"hello\"}\n\n"},
		{`{"message":${Message}}${NewLine,3}`, "{\"message\":\"hello\"}  \n"},
	} {
		t.Run(tc.template, func(t *testing.T) {
			var out testWriter
			h := NewHandler(WithWriter(&out), WithTemplate(tc.template))
			if err := h.Handle(context.Background(), testRecord()); err != nil {
				t.Fatal(err)
			}
			if out.String() != tc.want || out.writes != 1 {
				t.Fatalf("NewLine output = %q, writes = %d; want %q in one write", out.String(), out.writes, tc.want)
			}
		})
	}
	r := testRecord(slog.String("NewLine", "business"))
	if got := renderRecord(t, r, WithTemplate("${Message:l}${NewLine}")); got != "hello\nNewLine=\"business\"\n" {
		t.Fatalf("NewLine business field = %q", got)
	}
	got := renderRecord(t, r, WithTemplate(`{"message":${Message}}${NewLine}${NewLine}`))
	requireJSON(t, got, `{"message":"hello","NewLine":"business"}`)
	if !strings.HasSuffix(got, "\n\n") || strings.HasSuffix(got, "\n\n\n") {
		t.Fatalf("JSON NewLine suffix = %q", got)
	}
}

func TestTemplateJSONMultilineStrings(t *testing.T) {
	for _, tc := range []struct{ input, want string }{
		{"one\ntwo", `["one","two"]`},
		{"first\r\n\r\nlast\r\n\n", `["first","","last"]`},
		{"single\n", `"single\n"`},
		{"single", `"single"`},
	} {
		t.Run(tc.input, func(t *testing.T) {
			r := testRecord()
			r.Message = tc.input
			values := &recordValues{record: r}
			for _, name := range []string{"Message", "Source", "File", "FileName", "Function", "Package", "PID", "Hostname", "Executable", "Line", "XSLOG_TEST_MULTILINE"} {
				t.Setenv(name, tc.input)
				var template outputTemplate
				template.compile(`{"value":${` + name + `}}`)
				got, err := template.Append(nil, values)
				if err != nil {
					t.Fatal(err)
				}
				requireJSON(t, string(got), `{"value":`+tc.want+`}`)
			}
			requireJSON(t, renderRecord(t, r, WithTemplate(`{"message":${Message}}`)), `{"message":`+tc.want+`}`)
		})
	}
	requireJSON(t, renderRecord(t, testRecord(), WithTemplate("{\"time\":${Time:2006-01-02\n15:04}}")), `{"time":["2024-01-02","03:04"]}`)
	var out bytes.Buffer
	h := NewHandler(WithWriter(&out), WithTemplate(`{"host":${Hostname},"executable":${Executable}}`))
	h.process = processInfo{hostname: "first\nsecond", executable: "first\nsecond"}
	if err := h.Handle(context.Background(), testRecord()); err != nil {
		t.Fatal(err)
	}
	requireJSON(t, out.String(), `{"host":["first","second"],"executable":["first","second"]}`)
}
