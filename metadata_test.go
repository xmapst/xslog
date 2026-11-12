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
		testSourceLocation(frame.File, frame.Line), frame.File, filepath.Base(frame.File), frame.Line, "TestTemplateCallerMetadata")
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
		"function": "TestTemplateCallerMetadata", "package": "github.com/xmapst/xslog", "File": "business-file", "Line": -1,
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
	unsetTestEnv(t, "TimeUnix")
	unsetTestEnv(t, "TimeUnixMilli")
	r := testRecord()
	r.Time = time.Time{}
	for _, tc := range []struct{ template, want string }{
		{`{"line":"${Line}","keep":1}`, `{"keep":1}`},
		{`{"array":[0,"${Line}",2]}`, `{"array":[0,2]}`},
		{`{"nested":["prefix-${Line}-suffix",null,"${Line}${Line}"]}`, `{"nested":[null]}`},
		{`{"${Line}":"ignored","keep":1}`, `{"keep":1}`},
		{`{"prefix-${Line}":${Line},"keep":1}`, `{"keep":1}`},
		{`{"literal":"null","${Line}":"${Line}","keep":"literal null"}`, `{"literal":"null","keep":"literal null"}`},
		{`{"time":"${TimeUnix}","keep":1}`, `{"keep":1}`},
		{`{"array":[0,"${TimeUnixMilli}",2]}`, `{"array":[0,2]}`},
	} {
		t.Run(tc.template, func(t *testing.T) {
			requireJSON(t, renderRecord(t, r, WithTemplate(tc.template)), tc.want)
		})
	}
}

func TestHandlerCallerMetadataPointsToCaller(t *testing.T) {
	var out bytes.Buffer
	logger := slog.New(NewHandler(WithWriter(&out), WithTemplate("${File}|${FileName}|${Line}|${Function}|${Package}")))
	_, file, line, _ := runtime.Caller(0)
	logger.Info("caller")
	want := fmt.Sprintf("%s|%s|%d|TestHandlerCallerMetadataPointsToCaller|github.com/xmapst/xslog\n", file, filepath.Base(file), line+1)
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

// 时间戳是独立的数值占位符，不是 Time 的格式：输出 int64 数字，文本和 JSON 中都不加引号，
// 名称不区分大小写，任何动词和格式都被忽略（与 Line、PID 一样），只有对齐有效。
func TestTemplateTimestampPlaceholders(t *testing.T) {
	timestamps := []struct{ name, want string }{
		{"TimeUnix", "1704164645"},
		{"TimeUnixMilli", "1704164645678"},
		{"TimeUnixMicro", "1704164645678901"},
		{"TimeUnixNano", "1704164645678901234"},
	}
	for _, name := range []string{"Time", "TimeUnix", "TimeUnixMilli", "TimeUnixMicro", "TimeUnixNano"} {
		unsetTestEnv(t, name)
		unsetTestEnv(t, strings.ToLower(name))
	}

	for _, tc := range timestamps {
		t.Run(tc.name, func(t *testing.T) {
			r := testRecord(slog.Int(tc.name, -1))
			// 三种写法、任意大小写，业务字段不影响内置占位符；文本模板中业务字段追加在末尾。
			for _, template := range []string{"${" + tc.name + "}", "%" + tc.name + "%", "${" + strings.ToUpper(tc.name) + "}", "${" + strings.ToLower(tc.name) + "}"} {
				if got, want := renderRecord(t, r, WithTemplate(template)), tc.want+" "+tc.name+"=-1\n"; got != want {
					t.Fatalf("%s = %q, want %q", template, got, want)
				}
			}
			requireJSON(t, renderRecord(t, r, WithTemplate(`{"at":${`+tc.name+`}}`)), `{"at":`+tc.want+`,"`+tc.name+`":-1}`)
			requireJSON(t, renderRecord(t, r, WithTemplate(`{"at":"${`+tc.name+`}"}`)), `{"at":"`+tc.want+`","`+tc.name+`":-1}`)

			// 任何动词和格式都被忽略：不加引号，不转换，Go 布局也不适用。
			for _, format := range []string{"l", "lj", "j", "u", "w", "t3", "u3", "lu", "ju", "d", "f", "t", "2006-01-02"} {
				placeholder := "${" + tc.name + ":" + format + "}"
				if got := renderRecord(t, testRecord(), WithTemplate(placeholder)); got != tc.want+"\n" {
					t.Errorf("%s = %q, want %q", placeholder, got, tc.want+"\n")
				}
				requireJSON(t, renderRecord(t, testRecord(), WithTemplate(`{"v":`+placeholder+`}`)), `{"v":`+tc.want+`}`)
			}

			// 对齐有效。
			if got, want := renderRecord(t, testRecord(), WithTemplate("[${"+tc.name+",24}][${"+tc.name+",-24}]")),
				fmt.Sprintf("[%24s][%-24s]\n", tc.want, tc.want); got != want {
				t.Fatalf("alignment = %q, want %q", got, want)
			}
		})
	}

	t.Run("与时区无关", func(t *testing.T) {
		r := testRecord()
		r.Time = r.Time.In(time.FixedZone("UTC+8", 8*3600))
		for _, tc := range timestamps {
			if got := renderRecord(t, r, WithTemplate("${"+tc.name+"}")); got != tc.want+"\n" {
				t.Errorf("%s = %q, want %q", tc.name, got, tc.want+"\n")
			}
		}
	})

	t.Run("早于 1970 年的时间是负数", func(t *testing.T) {
		r := testRecord()
		r.Time = time.Unix(-1, 0)
		if got := renderRecord(t, r, WithTemplate("${TimeUnix}|${TimeUnixMilli}")); got != "-1|-1000\n" {
			t.Fatalf("negative timestamps = %q", got)
		}
	})

	// 记录没有时间时按缺失处理，与 Time 一致：文本省略，JSON 省略成员，并按占位符自己的名称回退环境变量。
	t.Run("缺少时间", func(t *testing.T) {
		zero := testRecord()
		zero.Time = time.Time{}
		if got := renderRecord(t, zero, WithTemplate("[${TimeUnix}][${TimeUnixNano}]")); got != "[][]\n" {
			t.Fatalf("missing timestamps = %q", got)
		}
		requireJSON(t, renderRecord(t, zero, WithTemplate(`{"at":${TimeUnixMilli},"keep":1}`)), `{"keep":1}`)

		// 回退只看占位符自己的名称：环境变量 Time 对 TimeUnix 没有影响。
		t.Setenv("Time", "from-time")
		if got := renderRecord(t, zero, WithTemplate("[${TimeUnix}]")); got != "[]\n" {
			t.Fatalf("Time environment variable leaked into TimeUnix: %q", got)
		}
		t.Setenv("TimeUnixMilli", "1700000000000")
		if got := renderRecord(t, zero, WithTemplate("${TimeUnixMilli}|${TimeUnixMilli:l}|${TimeUnixMilli:d}")); got != "\"1700000000000\"|1700000000000|1700000000000\n" {
			t.Fatalf("environment fallback = %q", got)
		}
		// 回退值是字符串，按字符串编码，不嵌入环境变量中的 JSON；类型动词可以解析为数值。
		requireJSON(t, renderRecord(t, zero, WithTemplate(`{"at":${TimeUnixMilli},"num":${TimeUnixMilli:d}}`)), `{"at":"1700000000000","num":1700000000000}`)
		t.Setenv("TimeUnixMilli", `{"from":"environment"}`)
		requireJSON(t, renderRecord(t, zero, WithTemplate(`{"at":${TimeUnixMilli}}`)), `{"at":"{\"from\":\"environment\"}"}`)
	})
}
