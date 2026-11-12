package xslog

import (
	"encoding/json"
	"errors"
	"log/slog"
	"math"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
)

type errorAndStringer struct{}

func (errorAndStringer) Error() string  { return "error text" }
func (errorAndStringer) String() string { return "stringer text" }

type panicStringer struct{}

func (panicStringer) String() string { panic("stringer") }

type failingMarshaler struct{}

func (failingMarshaler) MarshalJSON() ([]byte, error) {
	return []byte(`{"partial":`), errors.New("marshal failed")
}

type panicMarshaler struct{}

func (panicMarshaler) MarshalJSON() ([]byte, error) { panic("marshal") }

type panicValuer struct{}

func (panicValuer) LogValue() slog.Value { panic("valuer") }

func TestTextAttributeTypes(t *testing.T) {
	r := testRecord(slog.String("string", "hello"), slog.Int64("signed", -2), slog.Uint64("unsigned", 3),
		slog.Float64("float", 1.25), slog.Bool("bool", true), slog.Duration("duration", 1500*time.Millisecond),
		slog.Time("time", testTime()), slog.Any("nil", nil), slog.Any("bytes", []byte("abc")),
		slog.Any("binary", []byte{0xff, 0x00}), slog.Any("error", errorAndStringer{}))
	got := renderRecord(t, r, WithTemplate("${Message:l}"))
	want := "hello string=\"hello\" signed=-2 unsigned=3 float=1.25 bool=true duration=\"1.5s\" time=\"2024-01-02T03:04:05.678Z\" nil=<nil> bytes=\"abc\" binary=0xff00 error=\"error text\"\n"
	if got != want {
		t.Fatalf("text = %q, want %q", got, want)
	}
}

func TestJSONAttributeTypes(t *testing.T) {
	r := testRecord(slog.Int64("signed", -2), slog.Uint64("unsigned", math.MaxUint64), slog.Float64("float", 1.25),
		slog.Float64("nan", math.NaN()), slog.Float64("inf", math.Inf(1)), slog.Float64("negativeInf", math.Inf(-1)),
		slog.Bool("bool", false), slog.Duration("duration", 1500*time.Millisecond), slog.Time("time", testTime()),
		slog.Any("nil", nil), slog.Any("bytes", []byte("abc")), slog.Any("binary", []byte{0xff, 0}),
		slog.Any("error", errorAndStringer{}))
	requireJSON(t, renderRecord(t, r, WithTemplate(`{}`)), `{"signed":-2,"unsigned":18446744073709551615,"float":1.25,"nan":"NaN","inf":"+Inf","negativeInf":"-Inf","bool":false,"duration":"1.5s","time":"2024-01-02T03:04:05.678Z","nil":null,"bytes":"abc","binary":"0xff00","error":"error text"}`)
}

func TestTextKeysAndMultilineAttributes(t *testing.T) {
	r := testRecord(slog.String("a b", "text"), slog.Int("a=b", 1), slog.String("a\"b", "quoted \"value\""),
		slog.String("control\nkey", "tab\tvalue"), slog.String("stack", "first\r\n\r\nlast\r\n"))
	got := renderRecord(t, r, WithTemplate("${Message:l}"))
	want := "hello \"a b\"=\"text\" \"a=b\"=1 \"a\\\"b\"=\"quoted \\\"value\\\"\" control\\u000akey=\"tab  value\"\n" +
		"    stack │ first\n          │ \n          │ last\n"
	if got != want {
		t.Fatalf("text = %q, want %q", got, want)
	}
}

func TestEmbeddedJSONAndMultiline(t *testing.T) {
	for _, tc := range []struct{ name, value, json string }{
		{"object", "{ \"key\" : [1, 2] }", `{"key":[1,2]}`},
		{"array", "[true, null]", `[true,null]`},
		{"number text", "42", `"42"`},
		{"boolean text", "true", `"true"`},
		{"invalid", "{bad}", `"{bad}"`},
		{"extra", "{} trailing", `"{} trailing"`},
		{"single trailing LF", "one\n", `"one\n"`},
		{"multiple lines", "one\r\n\r\ntwo\r\n", `["one","","two"]`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := testRecord(slog.String("value", tc.value))
			requireJSON(t, renderRecord(t, r, WithTemplate(`{}`)), `{"value":`+tc.json+`}`)
			text := renderRecord(t, r, WithTemplate("${Message:l}"))
			if tc.name == "object" && text != "hello value={\"key\":[1,2]}\n" {
				t.Fatalf("embedded JSON text = %q", text)
			}
		})
	}
}

func TestCompositeValuesAndExternalMethodFailures(t *testing.T) {
	value := struct {
		Name   string `json:"name"`
		Error  error  `json:"error"`
		Hidden string `json:"-"`
	}{"世界\u202e", errors.New("nested"), "secret"}
	r := testRecord(slog.Any("object", value), slog.Any("broken", failingMarshaler{}),
		slog.Any("panic", panicMarshaler{}), slog.Any("stringer", panicStringer{}), slog.Any("valuer", panicValuer{}))
	for _, template := range []string{`${Message:l}`, `{}`} {
		t.Run(template, func(t *testing.T) {
			got := renderRecord(t, r, WithTemplate(template))
			if strings.Contains(got, "secret") || strings.Contains(got, "partial") || !strings.Contains(got, "<log value panicked: marshal>") || !strings.Contains(got, "<log value panicked: stringer>") {
				t.Fatalf("failed value corrupted output: %q", got)
			}
			if strings.ContainsRune(got, '\u202e') {
				t.Fatalf("composite value retained an unprintable character: %q", got)
			}
			if template == `{}` {
				var object map[string]json.RawMessage
				if err := json.Unmarshal([]byte(got), &object); err != nil {
					t.Fatal(err)
				}
				requireJSON(t, string(object["object"]), `{"name":"世界\u202e","error":"nested"}`)
			}
		})
	}
}

func TestJSONStringsEscapeControlCharacters(t *testing.T) {
	encoder := jsonEncoder{}
	for _, input := range []string{"ASCII \" \\", "\n\r\t\b\f\x00\x1b\x7f", "世界\u202e\u200b\U000e0001", "bad\xffutf8"} {
		t.Run(input, func(t *testing.T) {
			encoded := encoder.appendJSONString(nil, input)
			encodedBytes := encoder.appendJSONString(nil, []byte(input))
			if string(encoded) != string(encodedBytes) || !json.Valid(encoded) {
				t.Fatalf("invalid encoding: %q / %q", encoded, encodedBytes)
			}
			var decoded string
			if err := json.Unmarshal(encoded, &decoded); err != nil || decoded != strings.ToValidUTF8(input, "\ufffd") {
				t.Fatalf("roundtrip = %q, error = %v", decoded, err)
			}
			for _, unsafe := range []string{"\x1b", "\x7f", "\u202e", "\u200b", "\U000e0001"} {
				if strings.Contains(string(encoded), unsafe) {
					t.Fatalf("unprintable character escaped output: %q", encoded)
				}
			}
		})
	}
}

// 时间属性总是按布局格式化为字符串：JSON 中是字符串，文本中带引号；Unix 时间戳不是格式。
func TestEncoderTimeFormats(t *testing.T) {
	for _, tc := range []struct {
		format timeFormat
		want   string
	}{
		{"", "2024-01-02T03:04:05.678Z"},
		{defaultTimeLayout, "2024-01-02T03:04:05.678Z"},
		{"15:04:05", "03:04:05"},
		{"time=15h", "time=03h"},
	} {
		t.Run(string(tc.format), func(t *testing.T) {
			got := string((jsonEncoder{time: tc.format}).appendValue(nil, slog.TimeValue(testTime())))
			requireJSON(t, got, strconv.Quote(tc.want))
			text, block, quoted := (textEncoder{time: tc.format}).render(slog.TimeValue(testTime()))
			if text != tc.want || block || !quoted {
				t.Fatalf("text time = %q, block=%v, quoted=%v", text, block, quoted)
			}
		})
	}
}

func FuzzJSONAttributeString(f *testing.F) {
	for _, seed := range []string{"", "plain", "\n\t\x1b", "{\"a\":1}", `{"x":1,"x":2}`, "one\r\ntwo\n", "世界\xff\xfe", "\n\n", " ", `"plain"`} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, value string) {
		r := testRecord(slog.String("value", value))
		r.Message = value
		got := renderRecord(t, r, WithTemplate(`{"message":${Message}}`))
		decoder := json.NewDecoder(strings.NewReader(got))
		decoder.UseNumber()
		var decoded map[string]any
		if err := decoder.Decode(&decoded); err != nil {
			t.Fatalf("value %q produced invalid JSON %q: %v", value, got, err)
		}
		want := map[string]any{"message": expectedJSONString(t, value, false), "value": expectedJSONString(t, value, true)}
		if !reflect.DeepEqual(decoded, want) {
			t.Fatalf("value %q decoded=%#v want=%#v; output=%q", value, decoded, want, got)
		}
	})
}
