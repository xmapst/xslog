package xslog

import "log/slog"

// recordValues 在一次布局渲染期间提供记录元数据和已解析的属性。
type recordValues struct {
	renderer
	record  slog.Record
	attrs   []slog.Attr
	process processInfo
}

// Lookup 按内置名称查找记录元数据，不查找业务属性。
func (v *recordValues) Lookup(name string) (slog.Value, bool) {
	switch v.builtinName(name) {
	case "time":
		return slog.TimeValue(v.record.Time), !v.record.Time.IsZero()
	case "level":
		return slog.StringValue(v.record.Level.String()), true
	case "source":
		source := v.source(v.record.PC).location
		return slog.StringValue(source), source != ""
	case "file":
		file := v.source(v.record.PC).file
		return slog.StringValue(file), file != ""
	case "filename":
		name := v.source(v.record.PC).fileName
		return slog.StringValue(name), name != ""
	case "line":
		line := v.source(v.record.PC).line
		return slog.IntValue(line), line > 0
	case "function":
		function := v.source(v.record.PC).function
		return slog.StringValue(function), function != ""
	case "package":
		pkg := v.source(v.record.PC).pkg
		return slog.StringValue(pkg), pkg != ""
	case "pid":
		return slog.IntValue(v.process.pid), v.process.pid > 0
	case "hostname":
		return slog.StringValue(v.process.hostname), v.process.hostname != ""
	case "executable":
		return slog.StringValue(v.process.executable), v.process.executable != ""
	case "message":
		return slog.StringValue(v.record.Message), true
	case "newline":
		return slog.StringValue("\n"), true
	}
	return slog.Value{}, false
}

// Attrs 返回按分组归属合并、解析后的业务属性。
func (v *recordValues) Attrs() []slog.Attr { return v.attrs }
