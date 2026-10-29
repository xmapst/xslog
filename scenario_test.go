package xslog

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/json/jsontext"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
)

// expectedJSONString 使用标准 JSON 解码器和逐行拆分建立独立的值语义参照。
func expectedJSONString(t testing.TB, input string, embedded bool) any {
	t.Helper()
	if embedded && len(input) >= 2 && (input[0] == '{' || input[0] == '[') && json.Valid([]byte(input)) {
		decoder := json.NewDecoder(strings.NewReader(input))
		decoder.UseNumber()
		var value any
		if err := decoder.Decode(&value); err != nil {
			t.Fatal(err)
		}
		return value
	}
	body := strings.TrimRight(input, "\r\n")
	if strings.Contains(body, "\n") {
		var lines []any
		for line := range strings.SplitSeq(body, "\n") {
			lines = append(lines, string([]rune(strings.TrimSuffix(line, "\r"))))
		}
		return lines
	}
	return string([]rune(input))
}

func TestJSONMultilineAndEmbeddedValueMatrix(t *testing.T) {
	for _, value := range []string{
		"", " ", "123", "false", "null", "\n", "\n\n", "\r\n\r\n", "one\n", "one\n\n", "\none", "\none\n",
		"one\r\ntwo\r\n\r\n", "one\n\ntwo", "one\rtwo", "a\t\"\\\x00\u202e\nb\U000e0001", "\xff\xfe\n世界\xff",
		`{"s":"line1\nline2"}`, "{\n\"a\": [1,2]\n}\n", "[\n1,\n2\n]", `{"x":1,"x":2}`, " {}", "{} trailing",
	} {
		t.Run(strconv.Quote(value), func(t *testing.T) {
			r := testRecord(slog.String("value", value))
			r.Message = value
			got := renderRecord(t, r, WithTemplate(`{"message":${Message}}`))
			decoder := json.NewDecoder(strings.NewReader(got))
			decoder.UseNumber()
			var decoded map[string]any
			if err := decoder.Decode(&decoded); err != nil {
				t.Fatal(err)
			}
			want := map[string]any{"message": expectedJSONString(t, value, false), "value": expectedJSONString(t, value, true)}
			if !reflect.DeepEqual(decoded, want) {
				t.Fatalf("decoded=%#v want=%#v; output=%q", decoded, want, got)
			}
		})
	}
}

func TestTextMultilineAlignmentAndGrouping(t *testing.T) {
	r := testRecord(slog.Group("frame", slog.String("s", "a\nb"), slog.Int("seq", 7)),
		slog.String("long key", "x\r\n\r\ny\r\n"), slog.String("n", "123"), slog.Bool("b", true))
	want := "hello frame.seq=7 n=\"123\" b=true\n" +
		"    frame.s    │ a\n" +
		"               │ b\n" +
		"    \"long key\" │ x\n" +
		"               │ \n" +
		"               │ y\n"
	if got := renderRecord(t, r, WithTemplate("${Message:l}")); got != want {
		t.Fatalf("text=%q want=%q", got, want)
	}
}

func TestGroupsCanShareBackingSlicesWithoutCycles(t *testing.T) {
	attrs := []slog.Attr{slog.Int("first", 1), slog.Int("second", 2)}
	attrs[1] = slog.Attr{Key: "subset", Value: slog.GroupValue(attrs[:1]...)}
	r := testRecord(slog.Attr{Key: "outer", Value: slog.GroupValue(attrs...)})
	requireJSON(t, renderRecord(t, r, WithTemplate(`{}`)), `{"outer":{"first":1,"subset":{"first":1}}}`)
	// 同一组在两个分支复用也不构成循环。
	shared := slog.GroupValue(slog.Int("n", 3))
	r = testRecord(slog.Attr{Key: "left", Value: shared}, slog.Attr{Key: "right", Value: shared})
	requireJSON(t, renderRecord(t, r, WithTemplate(`{}`)), `{"left":{"n":3},"right":{"n":3}}`)
}

type selfValuer struct{}

func (v selfValuer) LogValue() slog.Value { return slog.AnyValue(v) }

type nilError struct{ text string }

func (e *nilError) Error() string { return e.text }

type nilStringer struct{ text string }

func (s *nilStringer) String() string { return s.text }

func TestUnencodableAndPanickingValuesPreserveOtherFields(t *testing.T) {
	for _, value := range []any{
		make(chan int), func() {}, complex(1, 2), (*nilError)(nil), (*nilStringer)(nil),
		arbitraryPanicMarshaler{nil}, arbitraryPanicMarshaler{errors.New("payload")}, selfValuer{},
		struct{ N float64 }{math.NaN()}, failingMarshaler{}, panicMarshaler{},
	} {
		t.Run(fmt.Sprintf("%T/%v", value, reflect.TypeOf(value)), func(t *testing.T) {
			for _, template := range []string{"${Message:l}", "{}"} {
				got := renderRecord(t, testRecord(slog.Int("before", 1), slog.Any("bad", value), slog.Int("after", 2)), WithTemplate(template))
				if !strings.Contains(got, "before") || !strings.Contains(got, "after") || strings.Contains(got, "partial") {
					t.Fatalf("value corrupted other fields: %q", got)
				}
				if template == "{}" {
					var object map[string]any
					if err := json.Unmarshal([]byte(got), &object); err != nil || object["before"] != float64(1) || object["after"] != float64(2) {
						t.Fatalf("fields=%v error=%v", object, err)
					}
				}
			}
		})
	}
}

func TestTemplateJSONTailWhitespaceAndControls(t *testing.T) {
	for _, template := range []string{
		"{}${NewLine}  ", "${NewLine}{}${NewLine} \t\r\n", "{${NewLine}}${NewLine,-4}  ",
		"{\"message\":${Message},${NewLine}\"line\":${Line}} ${NewLine}  ${NewLine}",
	} {
		t.Run(template, func(t *testing.T) {
			unsetTestEnv(t, "Line")
			var out testWriter
			h := NewHandler(WithWriter(&out), WithTemplate(template))
			if !h.layout.(*outputTemplate).json {
				t.Fatal("valid JSON whitespace selected text mode")
			}
			if err := h.Handle(context.Background(), testRecord(slog.Int("field", 1))); err != nil || out.writes != 1 {
				t.Fatalf("error=%v writes=%d", err, out.writes)
			}
			if !json.Valid(out.Bytes()) || !strings.HasSuffix(out.String(), "\n") || !bytes.Contains(out.Bytes(), []byte(`"field":1`)) {
				t.Fatalf("invalid tail or attributes: %q", out.String())
			}
		})
	}
}

func TestHandlerSkipsEmptyDescendantGroups(t *testing.T) {
	var out bytes.Buffer
	h := NewHandler(WithWriter(&out), WithTemplate(`{}`)).WithGroup("outer").WithAttrs([]slog.Attr{slog.Int("n", 1)}).WithGroup("empty").WithGroup("alsoEmpty")
	if err := h.Handle(context.Background(), testRecord(slog.Attr{}, slog.Group("discard"))); err != nil {
		t.Fatal(err)
	}
	requireJSON(t, out.String(), `{"outer":{"n":1}}`)
	out.Reset()
	if err := h.WithAttrs([]slog.Attr{slog.Int("x", 2)}).WithGroup("lastEmpty").Handle(context.Background(), testRecord()); err != nil {
		t.Fatal(err)
	}
	requireJSON(t, out.String(), `{"outer":{"n":1,"empty":{"alsoEmpty":{"x":2}}}}`)
}

func TestGroupBindingWithoutRecordAttributes(t *testing.T) {
	h := NewHandler().WithAttrs([]slog.Attr{slog.Int("root", 1)}).WithGroup("first").WithAttrs([]slog.Attr{slog.Int("n", 2)}).WithGroup("empty").(*Handler)
	attrs := h.bindAttrs(nil)
	got := (jsonEncoder{}).appendValue(nil, slog.GroupValue(attrs...))
	requireJSON(t, string(got), `{"root":1,"first":{"n":2}}`)
}

func TestEncodersNormalizeEmptyAndUnnamedGroups(t *testing.T) {
	attrs := []slog.Attr{slog.Attr{}, slog.String("", "discard"), slog.Group("empty"),
		slog.Group("shell", slog.String("", "discard"), slog.Group("empty")),
		slog.Group("", slog.Int("inline", 1)), slog.Group("named", slog.Group("", slog.Int("n", 2)))}
	encoder := jsonEncoder{}
	buf := []byte(`{"prefix":true`)
	sep := byte(',')
	for _, attr := range attrs {
		buf, sep = encoder.appendAttr(buf, attr, sep)
	}
	buf = append(buf, '}')
	requireJSON(t, string(buf), `{"prefix":true,"inline":1,"named":{"n":2}}`)
	requireJSON(t, string(encoder.appendValue(nil, slog.GroupValue(attrs...))), `{"inline":1,"named":{"n":2}}`)
	var fields []textAttr
	text := textEncoder{}
	for _, attr := range attrs {
		fields = text.flatten(fields, "outer", attr)
	}
	want := []textAttr{{key: "outer.inline", text: "1"}, {key: "outer.named.n", text: "2"}}
	if !reflect.DeepEqual(fields, want) {
		t.Fatalf("flattened=%#v want=%#v", fields, want)
	}
}

func TestDirectAnyEncodingKeepsTextSemantics(t *testing.T) {
	for _, tc := range []struct {
		value any
		want  string
	}{
		{"plain", `"plain"`}, {errorAndStringer{}, `"error text"`}, {&nilStringer{text: "first\nsecond"}, `["first","second"]`},
	} {
		requireJSON(t, string((jsonEncoder{}).appendAny(nil, tc.value)), tc.want)
	}
	if text, quoted := (textEncoder{}).renderAny("plain"); text != "plain" || !quoted {
		t.Fatalf("direct string=%q quoted=%v", text, quoted)
	}
}

func TestMissingValueOmissionRejectsMalformedInput(t *testing.T) {
	for _, tc := range []struct {
		input   string
		missing []int
	}{
		{"", []int{0}}, {`{null:0}`, []int{0}}, {`{"a":`, []int{0}},
		{`{"a":"unterminated}`, []int{0}}, {`{"a":null}`, []int{99}}, {`{"a":null}`, []int{-1}},
	} {
		input := "prefix:" + tc.input
		got, err := (&outputTemplate{}).omitMissing([]byte(input), len("prefix:"), tc.missing)
		if !errors.Is(err, ErrInvalidJSONLayout) || string(got) != input {
			t.Fatalf("omission(%q,%v)=%q,%v", tc.input, tc.missing, got, err)
		}
	}
}

func TestHandlerPreservesDuplicateAttributeOrder(t *testing.T) {
	var out bytes.Buffer
	h := NewHandler(WithWriter(&out), WithTemplate(`{"n":0}`)).WithAttrs([]slog.Attr{slog.Int("n", 1)})
	if err := h.Handle(context.Background(), testRecord(slog.Int("n", 2), slog.Int("n", 3))); err != nil {
		t.Fatal(err)
	}
	if got, want := out.String(), "{\"n\":0,\"n\":1,\"n\":2,\"n\":3}\n"; got != want {
		t.Fatalf("duplicate order=%q want=%q", got, want)
	}
}

func TestTimeLayoutCannotInjectTextControlSequences(t *testing.T) {
	got := renderRecord(t, testRecord(), WithTemplate("${Time:2006\n\x1b\\\"\u202e}"))
	if got != "2024\\u000a\\u001b\\\"\\u202e\n" {
		t.Fatalf("time controls=%q", got)
	}
}

type streamingMarshaler struct{ failure string }

func (v streamingMarshaler) MarshalJSONTo(encoder *jsontext.Encoder) error {
	if err := encoder.WriteToken(jsontext.BeginObject); err != nil {
		return err
	}
	if err := encoder.WriteToken(jsontext.String("partial")); err != nil {
		return err
	}
	if v.failure == "panic" {
		panic("streaming marshal")
	}
	return errors.New("streaming marshal failed")
}

func TestStreamingMarshalerFailuresDoNotLeakPartialJSON(t *testing.T) {
	for _, failure := range []string{"error", "panic"} {
		for _, template := range []string{"${Message:l}", "{}"} {
			got := renderRecord(t, testRecord(slog.Any("broken", streamingMarshaler{failure}), slog.Int("after", 7)), WithTemplate(template))
			if strings.Contains(got, "partial") || !strings.Contains(got, "after") {
				t.Fatalf("streaming failure leaked partial JSON: %q", got)
			}
			if template == "{}" && !json.Valid([]byte(got)) {
				t.Fatalf("invalid JSON after streaming failure: %q", got)
			}
		}
	}
}

func TestTypedNilsAndCompositeStringsKeepTheirEncoding(t *testing.T) {
	r := testRecord(slog.Any("nil", nil), slog.Any("bytes", []byte(nil)),
		slog.Any("slice", []string(nil)), slog.Any("map", map[string]int(nil)), slog.Any("pointer", (*cyclicNode)(nil)),
		slog.Any("strings", []string{"first\nsecond", "{\"n\":1}"}),
		slog.Any("stringer", &nilStringer{text: `{"n":1}`}), slog.Any("raw", json.RawMessage(`{"raw":true}`)))
	requireJSON(t, renderRecord(t, r, WithTemplate("{}")), `{"nil":null,"bytes":"","slice":[],"map":{},"pointer":null,"strings":["first\nsecond","{\"n\":1}"],"stringer":{"n":1},"raw":{"raw":true}}`)
}

func TestTimeZonesExtremeYearsLevelsAndNumbers(t *testing.T) {
	for _, timestamp := range []time.Time{
		testTime().In(time.FixedZone("offset", 5*3600+30*60)), time.Date(0, 1, 1, 0, 0, 0, 0, time.UTC),
		time.Date(12_000, 12, 31, 23, 59, 59, 999_999_999, time.UTC), time.Unix(-1, 999_999_999),
	} {
		for _, level := range []slog.Level{slog.LevelDebug - 3, slog.LevelInfo + 1, slog.LevelError + 99} {
			r := slog.NewRecord(timestamp, level, "", 0)
			r.AddAttrs(slog.Int64("min", math.MinInt64), slog.Uint64("max", math.MaxUint64), slog.Float64("negativeZero", math.Copysign(0, -1)))
			got := renderRecord(t, r, WithTemplate(`{"time":${Time:2006-01-02T15:04:05.999999999Z07:00},"level":${Level},"message":${Message}}`))
			var decoded struct {
				Time, Level, Message string
				Min                  int64
				Max                  uint64
				NegativeZero         float64
			}
			if err := json.Unmarshal([]byte(got), &decoded); err != nil || decoded.Time != timestamp.Format("2006-01-02T15:04:05.999999999Z07:00") || decoded.Level != level.String() || decoded.Message != "" || decoded.Min != math.MinInt64 || decoded.Max != math.MaxUint64 || !math.Signbit(decoded.NegativeZero) {
				t.Fatalf("extreme scalars=%q decoded=%+v error=%v", got, decoded, err)
			}
		}
	}
}
