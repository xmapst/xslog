package xslog

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"slices"
	"sync"
)

// Handler 根据模板渲染记录，管理写入、级别、属性归属和并发同步。
// 输出编码和时间格式由模板决定，记录末尾自动补齐 LF 换行。
type Handler struct {
	mu      *sync.Mutex
	writer  io.Writer
	level   slog.Leveler
	onError ErrorHandler
	layout  layout
	process processInfo
	groups  []attributeGroup
	attrs   []slog.Attr
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

// newHandler 根据已应用的选项构建 Handler，未设置模板时编译默认模板。
func (o options) newHandler() *Handler {
	if o.layout == nil {
		WithTemplate("")(&o)
	}
	return &Handler{
		mu:      &sync.Mutex{},
		writer:  o.output(),
		level:   o.level,
		onError: o.errorHandler,
		layout:  o.layout,
		process: (processInfo{}).load(),
		groups:  []attributeGroup{{}},
	}
}

// Enabled 报告日志级别是否达到当前阈值。
func (h *Handler) Enabled(_ context.Context, level slog.Level) bool {
	return level >= h.level.Level()
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
	buf, err = h.layout.Append(buf, &values)
	if err != nil {
		return h.report(err)
	}
	if len(buf) == 0 || buf[len(buf)-1] != '\n' {
		buf = append(buf, '\n')
	}
	return h.report(h.write(buf))
}

// write 在共享锁下写入记录，确保同一 Handler 派生出的写入不会交错。
func (h *Handler) write(buf []byte) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	n, err := h.writer.Write(buf)
	if err == nil && n != len(buf) {
		return io.ErrShortWrite
	}
	return err
}

// report 调用已配置的错误回调，并返回原错误。
func (h *Handler) report(err error) error {
	if err != nil && h.onError != nil {
		h.onError(err)
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
