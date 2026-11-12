package xslog

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"slices"
	"sync"
	"sync/atomic"
)

// Handler 根据模板渲染记录，管理写入、级别、属性归属和并发同步。
// 输出编码和时间格式由模板决定，记录末尾自动补齐 LF 换行。
//
// 模板、级别、Writer 和错误回调可在运行时通过 [Handler.SetTemplate]、[Handler.SetLevel]、
// [Handler.SetWriter] 和 [Handler.SetErrorHandler] 调整，这些方法可并发调用。
// 经 WithAttrs、WithGroup 派生的实例与原实例共享这些设置，调整后对双方立即生效，
// 已保存的 [slog.Logger] 无需重建。
type Handler struct {
	// state 与全部派生实例共享，复制 Handler 时只复制指针。
	state   *handlerState
	process processInfo
	groups  []attributeGroup
	attrs   []slog.Attr
}

// handlerState 保存 Handler 及其派生实例共享的可变设置。
// 级别、布局和错误回调以原子指针发布，记录路径只加载一次；
// writer 由 mu 保护，mu 同时串行化写入，因此 SetWriter 返回后旧 Writer 不会再被调用。
type handlerState struct {
	mu      sync.Mutex
	writer  io.Writer
	level   atomic.Pointer[slog.Leveler]
	layout  atomic.Pointer[layout]
	onError atomic.Pointer[ErrorHandler]
}

// handlerSettings 是 Handler 的全部可变设置，创建 Handler 和 Setup 整体更新时使用。
type handlerSettings struct {
	writer  io.Writer
	level   slog.Leveler
	layout  layout
	onError ErrorHandler
}

// configure 整体替换设置。各项逐一生效，期间并发的记录可能混用新旧设置，但每条记录都是完整的。
func (s *handlerState) configure(v handlerSettings) {
	s.mu.Lock()
	s.writer = v.writer
	s.mu.Unlock()
	s.level.Store(new(v.level))
	s.setLayout(v.layout)
	s.setErrorHandler(v.onError)
}

// setLayout 安装新的布局，之后开始的记录使用它渲染。
func (s *handlerState) setLayout(l layout) { s.layout.Store(new(l)) }

// setErrorHandler 安装错误回调，nil 表示不再通知。
func (s *handlerState) setErrorHandler(fn ErrorHandler) {
	if fn == nil {
		s.onError.Store(nil)
		return
	}
	s.onError.Store(new(fn))
}

// attributeGroup 保存一个 WithGroup 层级及在该层绑定的属性。
// 合并记录属性时保留分组归属，并省略空组。
type attributeGroup struct {
	name  string
	attrs []slog.Attr
}

// NewHandler 创建使用 [DefaultTemplate] 的 Handler。
// 可通过 [WithTemplate] 指定输出模板。
func NewHandler(opts ...Option) *Handler { return (options{}).apply(opts).newHandler() }

// newHandler 根据已应用的选项构建 Handler。
func (o options) newHandler() *Handler {
	h := &Handler{
		state:   new(handlerState),
		process: (processInfo{}).load(),
		groups:  []attributeGroup{{}},
	}
	h.state.configure(o.settings())
	return h
}

// settings 返回已应用选项对应的 Handler 设置，未设置模板时编译默认模板。
func (o options) settings() handlerSettings {
	if o.layout == nil {
		WithTemplate("")(&o)
	}
	return handlerSettings{writer: o.output(), level: o.level, layout: o.layout, onError: o.errorHandler}
}

// Enabled 报告日志级别是否达到当前阈值。
func (h *Handler) Enabled(_ context.Context, level slog.Level) bool {
	return level >= (*h.state.level.Load()).Level()
}

// SetLevel 调整级别阈值，对此 Handler 及其派生实例立即生效。
//
// 当前使用经 [WithLevel] 传入的 *[slog.LevelVar] 时，SetLevel 更新该变量，
// 共享它的所有 Handler 同步变化。使用固定级别或自定义 [slog.Leveler] 时，
// 以固定的 l 取代原 Leveler，此后对原 Leveler 的修改不再影响此 Handler。
// 全局 Handler 使用固定级别，包级 [SetLevel] 即调用它的 SetLevel。
func (h *Handler) SetLevel(l slog.Level) {
	if v, ok := (*h.state.level.Load()).(*slog.LevelVar); ok {
		v.Set(l)
		return
	}
	h.state.level.Store(new(slog.Leveler(l)))
}

// SetTemplate 编译并安装新的输出模板，对此 Handler 及其派生实例生效。
//
// 语法和编码规则与 [WithTemplate] 相同：空模板使用 [DefaultTemplate]，
// 输出编码可能在文本与 JSON 之间切换。每条记录只读取一次模板，不会混用新旧模板；
// 调用时已开始渲染的记录仍使用旧模板。
func (h *Handler) SetTemplate(template string) {
	h.state.setLayout(newTemplate(template))
}

// SetWriter 替换输出目标，nil 表示标准输出，对此 Handler 及其派生实例生效。
//
// 替换与写入互斥：SetWriter 返回后旧 Writer 不会再被调用，调用方可以安全地关闭它。
// Handler 不会关闭任何 Writer，其生命周期由调用方管理。
// 不要在 Writer 的 Write 方法内调用 SetWriter，此时写锁由当前写入持有，会造成死锁。
func (h *Handler) SetWriter(w io.Writer) {
	if w == nil {
		w = os.Stdout
	}
	h.state.mu.Lock()
	defer h.state.mu.Unlock()
	h.state.writer = w
}

// SetErrorHandler 替换布局和 Writer 错误的观察回调，nil 表示不再通知，
// 对此 Handler 及其派生实例生效。回调在写锁之外执行，可以调用本 Handler 的任何方法；
// 与 [WithErrorHandler] 一样，回调可能被并发调用。
func (h *Handler) SetErrorHandler(fn ErrorHandler) {
	h.state.setErrorHandler(fn)
}

// WithAttrs 在当前分组绑定属性，解析 LogValuer，并返回不影响其他派生实例的 Handler。
func (h *Handler) WithAttrs(attrs []slog.Attr) slog.Handler {
	resolved := make([]slog.Attr, 0, len(attrs))
	for _, attr := range attrs {
		resolved = h.appendResolvedAttr(resolved, attr)
	}
	if len(resolved) == 0 {
		return h
	}
	next := *h
	next.groups = slices.Clone(h.groups)
	last := &next.groups[len(next.groups)-1]
	last.attrs = append(slices.Clip(last.attrs), resolved...)
	next.attrs = next.bindAttrs(nil)
	return &next
}

// WithGroup 返回将后续属性放入指定分组的 Handler，空名称返回当前实例。
func (h *Handler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}
	next := *h
	next.groups = append(slices.Clip(h.groups), attributeGroup{name: name})
	return &next
}

// bindAttrs 按分组归属合并绑定属性和当前记录的属性。
func (h *Handler) bindAttrs(dynamic []slog.Attr) []slog.Attr {
	last := len(h.groups) - 1
	attrs := append(slices.Clip(h.groups[last].attrs), dynamic...)
	for i := last; i > 0; i-- {
		parent := h.groups[i-1].attrs
		if len(attrs) == 0 {
			attrs = parent
			continue
		}
		attrs = append(slices.Clip(parent), slog.Attr{Key: h.groups[i].name, Value: slog.GroupValue(attrs...)})
	}
	return attrs
}

// Handle 渲染记录并补齐末尾 LF 后，在派生 Handler 共享的锁下调用 Writer。
// 布局失败时不调用 Writer；布局和写入错误会返回并交给 [WithErrorHandler]。
func (h *Handler) Handle(_ context.Context, r slog.Record) error {
	attrs := h.attrs
	if r.NumAttrs() > 0 {
		dynamic := make([]slog.Attr, 0, r.NumAttrs())
		r.Attrs(func(attr slog.Attr) bool {
			dynamic = h.appendResolvedAttr(dynamic, attr)
			return true
		})
		if len(dynamic) > 0 {
			attrs = h.bindAttrs(dynamic)
		}
	}
	values := recordValues{record: r, attrs: attrs, process: h.process}
	bp := bufPool.Get().(*[]byte)
	buf := (*bp)[:0]
	defer func() {
		if cap(buf) <= maxPooledBuf {
			*bp = buf
			bufPool.Put(bp)
		}
	}()
	var err error
	buf, err = h.currentLayout().Append(buf, &values)
	if err != nil {
		return h.report(err)
	}
	if len(buf) == 0 || buf[len(buf)-1] != '\n' {
		buf = append(buf, '\n')
	}
	return h.report(h.write(buf))
}

// currentLayout 返回当前安装的布局。Handle 每条记录只调用一次，使整条记录使用同一份布局。
func (h *Handler) currentLayout() layout { return *h.state.layout.Load() }

// write 在共享锁下写入记录，确保同一 Handler 派生出的写入不会交错。
func (h *Handler) write(buf []byte) error {
	s := h.state
	s.mu.Lock()
	defer s.mu.Unlock()
	n, err := s.writer.Write(buf)
	if err == nil && n != len(buf) {
		return io.ErrShortWrite
	}
	return err
}

// report 调用当前的错误回调，并返回原错误。
func (h *Handler) report(err error) error {
	if err != nil {
		if fn := h.state.onError.Load(); fn != nil {
			(*fn)(err)
		}
	}
	return err
}

// appendResolvedAttr 解析属性，过滤空属性并展开无名组。
// 直接作为属性值的 error 和 fmt.Stringer 在此取为文本，供布局使用。
func (h *Handler) appendResolvedAttr(dst []slog.Attr, attr slog.Attr) []slog.Attr {
	resolver := attrResolver{remaining: maxResolvedLogValuers}
	return resolver.append(dst, attr)
}

// attrResolver 限制单个属性树中的 LogValuer 值数量，并跟踪当前分组路径。
// 分组深度和 LogValuer 的解析数量受限，普通平铺属性的数量不受限制。
type attrResolver struct {
	path      [][]slog.Attr
	remaining int
}

// append 解析并规范化一个属性，分组循环或超出解析限制时使用占位文本。
// 分组深度和 LogValuer 值数量分别受 maxAttrGroupDepth、maxResolvedLogValuers 限制。
func (r *attrResolver) append(dst []slog.Attr, attr slog.Attr) []slog.Attr {
	if attr.Value.Kind() == slog.KindLogValuer {
		if r.remaining == 0 {
			attr.Value = slog.StringValue("<log value resolution limit exceeded>")
		} else {
			r.remaining--
			attr.Value = attr.Value.Resolve()
		}
	}
	if attr.Value.Kind() == slog.KindGroup {
		group := attr.Value.Group()
		if len(group) == 0 {
			return dst
		}
		for _, ancestor := range r.path {
			if len(ancestor) == len(group) && &ancestor[0] == &group[0] {
				return r.append(dst, slog.String(attr.Key, "<log group cycle>"))
			}
		}
		if len(r.path) >= maxAttrGroupDepth {
			return r.append(dst, slog.String(attr.Key, "<log group depth exceeded>"))
		}
		r.path = append(r.path, group)
		var children []slog.Attr
		for _, child := range group {
			children = r.append(children, child)
		}
		r.path = r.path[:len(r.path)-1]
		if len(children) == 0 {
			return dst
		}
		if attr.Key == "" {
			return append(dst, children...)
		}
		attr.Value = slog.GroupValue(children...)
	} else {
		if attr.Key == "" {
			return dst
		}
		// renderAny 捕获外部文本方法的 panic，取值结果在本次记录或绑定中复用。
		if attr.Value.Kind() == slog.KindAny {
			switch attr.Value.Any().(type) {
			case error, fmt.Stringer:
				text, _ := (textEncoder{}).renderAny(attr.Value.Any())
				attr.Value = slog.StringValue(text)
			}
		}
	}
	return append(dst, attr)
}
