package xslog

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestTemplateText(t *testing.T) {
	for _, tc := range []struct{ name, template, want string }{
		{"metadata", "${Time} ${Level} ${Message}", "2024-01-02T03:04:05.678Z INFO \"hello\"\n"},
		{"percent", "%Time% [%Level%] %Message%", "2024-01-02T03:04:05.678Z [INFO] \"hello\"\n"},
		{"case", "${tImE:15:04} ${lEvEl} ${mEsSaGe:l}", "03:04 INFO hello\n"},
		{"time format", "${Time:2006-01-02 15:04:05.000000}", "2024-01-02 03:04:05.678901\n"},
		{"unix", "${TimeUnix}", "1704164645\n"},
		{"milliseconds", "${TimeUnixMilli}", "1704164645678\n"},
		{"microseconds", "${TimeUnixMicro}", "1704164645678901\n"},
		{"nanoseconds", "${TimeUnixNano}", "1704164645678901234\n"},
		{"right align", "[${Level,6:u3}]", "[   INF]\n"},
		{"left align", "[${Level,-6:w3}]", "[inf   ]\n"},
		{"literal", "{Level} {{Message}} $(Time) $Message", "{Level} {{Message}} $(Time) $Message\n"},
		{"literal newline", "${Message:l}\n\n", "hello\n\n"},
		{"JSON value in text", "message=${Message:j}", "message=\"hello\"\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := renderRecord(t, testRecord(), WithTemplate(tc.template)); got != tc.want {
				t.Fatalf("output = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestTemplateTimeHasNoTimestampAlias(t *testing.T) {
	for _, name := range []string{"Timestamp", "timestamp", "TIMESTAMP", "tImEsTaMp"} {
		t.Run(name, func(t *testing.T) {
			unsetTestEnv(t, name)
			r := testRecord()
			if got := renderRecord(t, r, WithTemplate("${"+name+"}|%"+name+"%")); got != "|\n" {
				t.Fatalf("removed alias still resolves metadata: %q", got)
			}
			requireJSON(t, renderRecord(t, r, WithTemplate(`{"time":${Time},"absent":${`+name+`}}`)),
				`{"time":"2024-01-02T03:04:05.678Z"}`)
			t.Setenv(name, "environment-time")
			got := renderRecord(t, r, WithTemplate("${Time}|${"+name+":l}|%"+name+"%"))
			if want := "2024-01-02T03:04:05.678Z|environment-time|\"environment-time\"\n"; got != want {
				t.Fatalf("environment fallback = %q, want %q", got, want)
			}
			requireJSON(t, renderRecord(t, r, WithTemplate(`{"time":${Time},"environment":${`+name+`}}`)),
				`{"time":"2024-01-02T03:04:05.678Z","environment":"environment-time"}`)
		})
	}
}

func TestTemplateLevelFormats(t *testing.T) {
	for _, tc := range []struct {
		level slog.Level
		want  string
	}{
		{slog.LevelDebug, "DBG/dbg/DE/DEBUG"},
		{slog.LevelInfo, "INF/inf/IN/INFO"},
		{slog.LevelWarn, "WRN/wrn/WN/WARN"},
		{slog.LevelError, "ERR/err/ER/ERROR"},
		{slog.LevelInfo + 1, "INF/inf/IN/INFO+1"},
	} {
		t.Run(tc.level.String(), func(t *testing.T) {
			r := testRecord()
			r.Level = tc.level
			got := renderRecord(t, r, WithTemplate("${Level:u3}/${Level:w3}/${Level:u2}/${Level:other}"))
			if got != tc.want+"\n" {
				t.Fatalf("output = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestTemplateMessageEncoding(t *testing.T) {
	r := testRecord()
	r.Message = "quoted \"text\"\n\t世界\x1b"
	for _, format := range []string{"", ":l", ":lj", ":j"} {
		t.Run(format, func(t *testing.T) {
			got := renderRecord(t, r, WithTemplate("${Message"+format+"}"))
			if strings.ContainsRune(got, '\x1b') || (format != ":j" && strings.Count(got, "\n") != 1) {
				t.Fatalf("unsafe or multiline text message: %q", got)
			}
			if format == ":j" {
				requireJSON(t, got, `["quoted \"text\"","\t世界\u001b"]`)
			} else if format == "" && !strings.HasPrefix(got, `"quoted \"text\"`) {
				t.Fatalf("message is not quoted: %q", got)
			} else if (format == ":l" || format == ":lj") && !strings.HasPrefix(got, `quoted "text"`) {
				t.Fatalf("literal message = %q", got)
			}
		})
	}
}

func TestTemplateAttributesAreAutomatic(t *testing.T) {
	r := testRecord(slog.String("user", "alice"), slog.Int("count", 2), slog.Bool("ready", true), slog.Group("frame", slog.Int("seq", 7)))
	got := renderRecord(t, r, WithTemplate("[${Level:u3}] ${Message:l}"))
	if want := "[INF] hello user=\"alice\" count=2 ready=true frame.seq=7\n"; got != want {
		t.Fatalf("text = %q, want %q", got, want)
	}
	for _, template := range []string{`{}`, " { \n}\r\n", `{"level":${Level},"event":{"message":${Message}}}`} {
		t.Run(template, func(t *testing.T) {
			got := renderRecord(t, r, WithTemplate(template))
			want := `{"user":"alice","count":2,"ready":true,"frame":{"seq":7}}`
			if strings.Contains(template, "level") {
				want = `{"level":"INFO","event":{"message":"hello"},"user":"alice","count":2,"ready":true,"frame":{"seq":7}}`
			}
			requireJSON(t, got, want)
			if !strings.HasSuffix(got, "\n") || strings.HasSuffix(got, "\n\n") {
				t.Fatalf("record ending = %q", got)
			}
		})
	}
}

func TestTemplateDoesNotLookUpBusinessAttributes(t *testing.T) {
	for _, name := range []string{"user", "frame.seq", "Properties", "Attrs", "Properties.user", "Attrs.user"} {
		unsetTestEnv(t, name)
	}
	r := testRecord(slog.String("user", "alice"), slog.Group("frame", slog.Int("seq", 7)))
	got := renderRecord(t, r, WithTemplate("${user}|${frame.seq}|%Properties%|%Attrs%|${Properties.user}|${Attrs.user}"))
	if want := "||||| user=\"alice\" frame.seq=7\n"; got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
	requireJSON(t, renderRecord(t, r, WithTemplate(`{"selected":${user},"data":${Properties}}`)), `{"user":"alice","frame":{"seq":7}}`)
}

func TestTemplateEnvironmentFallback(t *testing.T) {
	t.Setenv("XSLOG_TEST_ENV", "first")
	t.Setenv("Message", "environment message")
	var out bytes.Buffer
	h := NewHandler(WithWriter(&out), WithTemplate("${XSLOG_TEST_ENV:l}|${Message:l}"))
	r := testRecord(slog.String("XSLOG_TEST_ENV", "business"), slog.String("Message", "business message"))
	for _, value := range []string{"first", "second", ""} {
		t.Setenv("XSLOG_TEST_ENV", value)
		out.Reset()
		if err := h.Handle(context.Background(), r); err != nil {
			t.Fatal(err)
		}
		want := value + "|hello XSLOG_TEST_ENV=\"business\" Message=\"business message\"\n"
		if out.String() != want {
			t.Fatalf("output = %q, want %q", out.String(), want)
		}
	}
	t.Setenv("Source", "fallback.go:1")
	t.Setenv("Time", "fallback-time")
	r = testRecord()
	r.Time = time.Time{}
	if got := renderRecord(t, r, WithTemplate("${Time:l} ${Source}")); got != "fallback-time fallback.go:1\n" {
		t.Fatal(got)
	}
	t.Setenv("XSLOG_TEST_ENV", `${Message} %Level%`)
	if got := renderRecord(t, testRecord(), WithTemplate("${XSLOG_TEST_ENV:l}")); got != "${Message} %Level%\n" {
		t.Fatalf("environment was recursively expanded: %q", got)
	}
}

func TestTemplateMissingJSONValuesAreOmitted(t *testing.T) {
	unsetTestEnv(t, "XSLOG_TEST_MISSING")
	unsetTestEnv(t, "Source")
	t.Setenv("XSLOG_TEST_EMPTY", "")
	for _, tc := range []struct{ name, template, want string }{
		{"only", `{"missing":${XSLOG_TEST_MISSING}}`, `{}`},
		{"first", `{"missing":${XSLOG_TEST_MISSING},"message":${Message}}`, `{"message":"hello"}`},
		{"middle", `{"before":1,"missing":${XSLOG_TEST_MISSING},"after":2}`, `{"before":1,"after":2}`},
		{"last", `{"message":${Message},"missing":${XSLOG_TEST_MISSING}}`, `{"message":"hello"}`},
		{"nested", `{"outer":{"source":${Source},"message":${Message}},"missing":${XSLOG_TEST_MISSING}}`, `{"outer":{"message":"hello"}}`},
		{"all nested", `{"outer":{"a":${Source},"b":${XSLOG_TEST_MISSING}}}`, `{"outer":{}}`},
		{"arrays", `{"values":[${XSLOG_TEST_MISSING},1,${Source},2,${XSLOG_TEST_MISSING}]}`, `{"values":[1,2]}`},
		{"all array", `{"values":[${XSLOG_TEST_MISSING},${Source}]}`, `{"values":[]}`},
		{"real null", `{"actual":null,"missing":${XSLOG_TEST_MISSING},"empty":${XSLOG_TEST_EMPTY}}`, `{"actual":null,"empty":""}`},
		{"alignment", `{"missing":${XSLOG_TEST_MISSING,16},"source":${Source,-16},"message":${Message}}`, `{"message":"hello"}`},
		{"duplicates", `{"same":null,"same":${XSLOG_TEST_MISSING},"message":${Message}}`, `{"same":null,"message":"hello"}`},
		{"escaped key", `{"x,\"y":${XSLOG_TEST_MISSING},"keep":{"x":null}}`, `{"keep":{"x":null}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := renderRecord(t, testRecord(), WithTemplate(tc.template))
			requireJSON(t, got, tc.want)
			if tc.name == "duplicates" && strings.Count(got, `"same":`) != 1 {
				t.Fatalf("missing duplicate member was retained: %s", got)
			}
		})
	}
	r := testRecord(slog.Any("actual", nil), slog.Int("count", 3))
	requireJSON(t, renderRecord(t, r, WithTemplate(`{"missing":${XSLOG_TEST_MISSING}}`)), `{"actual":null,"count":3}`)
	if got := renderRecord(t, testRecord(), WithTemplate("${XSLOG_TEST_MISSING:j}")); got != "\n" {
		t.Fatalf("missing text value = %q", got)
	}
}

func TestTemplateJSONEncoding(t *testing.T) {
	r := testRecord(slog.Time("deadline", testTime()), slog.String("stack", "first\r\n\r\nlast\r\n"))
	r.Message = "line1\nline2\"\x1b"
	for _, tc := range []struct{ placeholder, want string }{
		{"TimeUnix", "1704164645"},
		{"TimeUnixMilli", "1704164645678"},
		{"TimeUnixMicro", "1704164645678901"},
		{"TimeUnixNano", "1704164645678901234"},
	} {
		t.Run(tc.placeholder, func(t *testing.T) {
			got := renderRecord(t, r, WithTemplate(`{"at":${`+tc.placeholder+`},"clock":${Time:15:04},"message":${Message},"severity":${Level:w3}}`))
			requireJSON(t, got, `{"at":`+tc.want+`,"clock":"03:04","message":["line1","line2\"\u001b"],"severity":"inf","deadline":"2024-01-02T03:04:05.678Z","stack":["first","","last"]}`)
		})
	}
}

func TestTemplateInvalidJSONUsesText(t *testing.T) {
	for _, tc := range []struct{ template, want string }{
		{`{${Time},"Level":${Level}}`, `{2024-01-02T03:04:05.678Z,"Level":INFO}`},
		{`{%Time%,"Level":%Level%}`, `{2024-01-02T03:04:05.678Z,"Level":INFO}`},
		{" { \n${Time:15:04:05},\"Level\":${Level}}", " { \n03:04:05,\"Level\":INFO}"},
		{`{${Message}}`, `{"hello"}`},
		{`{"message":"${Message}"}`, `{"message":""hello""}`},
		{`{"message":"%Message%"}`, `{"message":""hello""}`},
		{`{"message":${Message},}`, `{"message":"hello",}`},
		{`{"message":${Message}`, `{"message":"hello"`},
		{`{"message":${Message}} trailing`, `{"message":"hello"} trailing`},
		{`{"message":${Message}} {}`, `{"message":"hello"} {}`},
		{`{"before":"escaped \" ${Message}"}`, `{"before":"escaped \" "hello""}`},
		{`{"x":[1,]}`, `{"x":[1,]}`},
	} {
		t.Run(tc.template, func(t *testing.T) {
			var out testWriter
			var observed error
			h := NewHandler(WithWriter(&out), WithTemplate(tc.template), WithErrorHandler(func(err error) { observed = err }))
			if err := h.Handle(context.Background(), testRecord(slog.Int("count", 2))); err != nil {
				t.Fatal(err)
			}
			if want := tc.want + " count=2\n"; out.String() != want || observed != nil || out.writes != 1 {
				t.Fatalf("output = %q, callback = %v, writes = %d; want %q in one write", out.String(), observed, out.writes, want)
			}
		})
	}
}

func TestTemplateEncodingIsChosenByJSONValidation(t *testing.T) {
	for _, tc := range []struct {
		name, template string
		json           bool
		want           string
	}{
		{"empty object", `{}`, true, `{"count":2}`},
		{"object with whitespace", " \t{\n}\r\n", true, `{"count":2}`},
		{"metadata", `{"message":${Message}}`, true, `{"message":"hello","count":2}`},
		{"dynamic key", `{${Level}:1}`, true, `{"INFO":1,"count":2}`},
		{"quoted numeric value", `{"epoch":"${TimeUnix}"}`, true, `{"epoch":"1704164645","count":2}`},
		{"duplicate keys", `{"n":1,"n":2}`, true, `{"n":2,"count":2}`},
		{"malformed member", `{${Time},"Level":${Level}}`, false, `{2024-01-02T03:04:05.678Z,"Level":INFO} count=2`},
		{"text brace styles", `{Level} {{Message}}`, false, `{Level} {{Message}} count=2`},
		{"non-object array", `[1,2]`, false, `[1,2] count=2`},
		{"non-object number", `42`, false, `42 count=2`},
		{"non-object string", `"plain"`, false, `"plain" count=2`},
		{"non-JSON whitespace", "\u00a0{}", false, "\u00a0{} count=2"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			h := NewHandler(WithWriter(&out), WithTemplate(tc.template))
			compiled, ok := h.currentLayout().(*outputTemplate)
			if !ok || compiled.json != tc.json {
				t.Fatalf("layout = %#v, want JSON mode %v", h.currentLayout(), tc.json)
			}
			if err := h.Handle(context.Background(), testRecord(slog.Int("count", 2))); err != nil {
				t.Fatal(err)
			}
			if tc.json {
				requireJSON(t, out.String(), tc.want)
			} else if out.String() != tc.want+"\n" {
				t.Fatalf("text = %q, want %q", out.String(), tc.want+"\n")
			}
		})
	}
}

func TestTemplateEncodingDoesNotDependOnEnvironment(t *testing.T) {
	t.Setenv("XSLOG_TEST_STYLE", `{"from":"environment"}`)
	text := WithTemplate("${XSLOG_TEST_STYLE}")
	if got := renderRecord(t, testRecord(slog.Int("count", 2)), text); got != "{\"from\":\"environment\"} count=2\n" {
		t.Fatalf("environment changed the template encoding: %q", got)
	}
	object := WithTemplate(`{"data":${XSLOG_TEST_STYLE}}`)
	for _, value := range []string{"plain", "", `{"nested":true}`} {
		t.Setenv("XSLOG_TEST_STYLE", value)
		got := renderRecord(t, testRecord(slog.Int("count", 2)), object)
		want := `{"data":"plain","count":2}`
		switch value {
		case "":
			want = `{"data":"","count":2}`
		case `{"nested":true}`:
			want = `{"data":{"nested":true},"count":2}`
		}
		requireJSON(t, got, want)
	}
}

func TestTemplateRejectsInvalidRuntimeJSON(t *testing.T) {
	t.Setenv("TimeUnix", "not-a-number")
	var out testWriter
	var observed error
	h := NewHandler(WithWriter(&out), WithTemplate(`{"epoch":"${TimeUnix}"}`), WithErrorHandler(func(err error) { observed = err }))
	r := testRecord()
	r.Time = time.Time{}
	if err := h.Handle(context.Background(), r); !errors.Is(err, ErrInvalidJSONLayout) {
		t.Fatalf("error = %v, want ErrInvalidJSONLayout", err)
	}
	if !errors.Is(observed, ErrInvalidJSONLayout) || out.writes != 0 {
		t.Fatalf("callback = %v, writes = %d", observed, out.writes)
	}
}

func TestTemplateSourceAndAppendPrefix(t *testing.T) {
	var pcs [1]uintptr
	runtime.Callers(1, pcs[:])
	frame, _ := runtime.CallersFrames(pcs[:]).Next()
	r := testRecord()
	r.PC = pcs[0]
	wantSource := testSourceLocation(frame.File, frame.Line)
	if got := renderRecord(t, r, WithTemplate("${Source}")); got != wantSource+"\n" {
		t.Fatalf("source = %q, want %q", got, wantSource)
	}
	if got := renderRecord(t, r); !strings.Contains(got, " ["+wantSource+"] message=") {
		t.Fatalf("default source = %q", got)
	}
	for _, template := range []string{"${Message:l}", `{"message":${Message},"missing":${XSLOG_TEST_PREFIX_ABSENT}}`} {
		unsetTestEnv(t, "XSLOG_TEST_PREFIX_ABSENT")
		var layout outputTemplate
		layout.compile(template)
		got, err := layout.Append([]byte("prefix:"), &recordValues{record: r})
		if err != nil || !strings.HasPrefix(string(got), "prefix:") {
			t.Fatalf("append = %q, %v", got, err)
		}
		if strings.HasPrefix(template, "{") {
			requireJSON(t, string(got[len("prefix:"):]), `{"message":"hello"}`)
		}
	}
}

func TestTemplateEmptyRestoresDefault(t *testing.T) {
	unsetTestEnv(t, "Source")
	got := renderRecord(t, testRecord(), WithTemplate("${Message:l}"), WithTemplate(""))
	if want := "2024-01-02T03:04:05.678Z INFO [] message=\"hello\"\n"; got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
	got = renderRecord(t, testRecord(), WithTemplate(""), WithTemplate("${Message:l}"))
	if got != "hello\n" {
		t.Fatal(got)
	}
}

func FuzzTemplateCompilation(f *testing.F) {
	for _, template := range []string{
		"", "text ${Message:l}", `{"message":${Message}}`, `{${Time},"Level":${Level}}`,
		`{"epoch":"${TimeUnix}"}`, `{"at":${TimeUnixMilli}}`, `{"message":"${Message}"}`, "\u00a0{}", `{}\n`,
		`{"file":${File},"line":${Line},"pid":${PID}}`, `{"line":"${Line}","pid":"${PID}"}`,
		`{"${Line}":"${PID}"}`, `${FileName}:${Line} ${Function} ${Package} ${Hostname} ${Executable}`,
		`${NewLine}{"message":${Message}}${NewLine}`, `{"line":${Line}}${NewLine}${NewLine,4}`,
	} {
		f.Add(template)
	}
	f.Fuzz(func(t *testing.T, template string) {
		var compiled outputTemplate
		compiled.compile(template)
		out, err := compiled.Append([]byte("prefix:"), jsonLayoutProbe{})
		if err != nil || !strings.HasPrefix(string(out), "prefix:") {
			t.Fatalf("configuration-time classification cannot render its probe: template=%q, output=%q, error=%v", template, out, err)
		}
	})
}
