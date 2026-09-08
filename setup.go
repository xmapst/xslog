package xslog

import (
	"fmt"
	"log/slog"
	"strings"
	"sync/atomic"
)

// manager 持有全局日志的两样可变状态：动态级别，以及 [WithFile] 开出来的那份文件。
//
// 拿住一个需要释放的资源是有代价的——多出一个配套的 [Close]，以及「既给了 Writer
// 又给了路径」该听谁的这条规则（那条在选项层就化掉了，见 [WithFile]）。认下这笔代价
// 是因为它只发生在 [Setup] 这一条路上：那里装的本来就是进程唯一的那个 Logger，
// 一个全局再挂一个文件句柄，要管的生命周期仍然只有一份。自己 New Handler 的那条路
// 仍然一无所持，见 [OpenFile]。
type manager struct {
	// level 是动态日志级别。slog.LevelVar 自带并发安全，且被 Handler 按接口持有
	// （接口里装的是这个指针），因此调级不需要重建 Handler。
	level *slog.LevelVar

	// file 是当前这份日志文件；没走 [WithFile] 时为 nil。
	//
	// 用原子指针而不是「一把锁护着一个字段」：要做的事只有「换上新的、把旧的交出来」
	// 这一个动作，那正是 Swap 的语义，锁在这里只是把它拆成三步再手动拼回去。
	// 装配和关闭也确实常常不在同一个 goroutine 上——运维接口上挂一个热更日志配置的
	// handler 就是这个形状，而进程退出那条路径又几乎总在别处。
	file atomic.Pointer[FileOutput]
}

func init() {
	// 开箱即用：任何二进制只要引入本包，未显式调用 Setup 前也能以标准格式输出到
	// 标准输出，避免因遗漏初始化导致日志退化为 log/slog 默认格式。
	//
	// 这一段覆盖的是「配置还没读到」的那几条日志——配置文件找不到的告警正是其中之一。
	// 级别不在这里单独 Set：newHandler 会把 newOptions(nil) 里的 [DefaultLevel]
	// 落进去，两条路径因此只有一处在写这个 LevelVar。
	slog.SetDefault(slog.New(std.newHandler(newOptions(nil))))
}

// Setup 按选项重建全局默认 Logger（slog.SetDefault）。不传选项即控制台格式、写标准输出。
//
// 要落盘有两种写法：[WithFile] 给一个路径，文件由 Setup 打开、[Close] 关闭；
// 或者自己 [OpenFile] 开一个 Writer 用 [WithWriter] 传进来，那样生命周期仍归调用方。
// 两个都给按选项顺序由后者作数。
//
// 重复调用是安全的：上一次 [WithFile] 开的文件会在新 Logger 装好之后被关掉，
// 因此热更日志配置不必先 [Close] 再 Setup。
//
// [WithLevel] 在这里定的是装配那一刻的阈值：装出来的 Handler 持有的始终是本包那一个
// LevelVar，装完之后调级一律走 [SetLevel]。传自己的 LevelVar 进来也只是给了个起点，
// 之后再 Set 它不会传导到这个 Logger——要那种联动就别走 Setup，直接
// [NewConsoleHandler] / [NewJSONHandler] 把它传进去。
func Setup(opts ...Option) error { return std.setup(newOptions(opts)) }

// Close 关闭 [WithFile] 打开的那份日志文件；没开过就是空操作，可重复调用。
//
// 进程退出前调一次即可：
//
//	if err := xslog.Setup(xslog.WithFile(path)); err != nil {
//		return err
//	}
//	defer xslog.Close()
//
// 关完之后再打的日志不会丢：底下的轮转器对「已关闭还在写」是开-写-关地照写一遍，
// 只是不再轮转。所以这条 defer 摆在哪、和别的清理谁先谁后，都不必特别小心——
// 收尾顺序排错时，最坏的结果也只是最后几行没参与轮转，而不是整段日志没了。
func Close() error { return std.close() }

// SetLevel 动态调整全局日志级别，无需重启进程立即生效，默认级别为 [DefaultLevel]。
//
// 它调的是本包持有的那一个 LevelVar，因此只对 [Setup] 装出来的 Logger 生效；
// 自己 New 出来的 Handler 想跟着变，就把同一个 LevelVar 用 [WithLevel] 传进去。
func SetLevel(l slog.Level) { std.level.Set(l) }

// setup 校验选项后换上新的全局 Logger。
//
// 格式不认识时返回错误而不是回落到默认值：配错一个字就静静换一种格式，
// 要等有人去看日志才发现。
func (m *manager) setup(o options) error {
	switch strings.ToLower(strings.TrimSpace(o.format)) {
	case "", FormatConsole, FormatJSON:
	default:
		return fmt.Errorf("invalid log format %q: want %s or %s", o.format, FormatConsole, FormatJSON)
	}
	// 先把文件开出来：开不了就原样返回，全局 Logger 一个字节都不动。反过来先装后开
	// 的话，一个写错的路径会连上一份能用的日志一起弄没——而那份日志正是用来看
	// 「为什么起不来」的。
	var file *FileOutput
	if strings.TrimSpace(o.filePath) != "" {
		var err error
		if file, err = OpenFile(o.filePath, o.fileOpts...); err != nil {
			return err
		}
		o.writer = file
	}
	slog.SetDefault(slog.New(m.newHandler(o)))
	// 切完再关上一份，不能反过来：中间那几条日志正落在旧文件上。
	if err := m.swapFile(file); err != nil {
		// 装配这件事已经成了，返回错误会让调用方以为没装上。旧文件没关上是句柄泄漏，
		// 不影响新日志往下写，交给日志本身说一声就够——这条会落在刚装好的目的地。
		slog.Warn("close previous log file", slog.Any("error", err))
	}
	return nil
}

// swapFile 换上新的日志文件并关掉旧的，返回的是关旧文件的错误。
//
// 存的是 *FileOutput 而不是 io.Closer：一个 nil 的 *FileOutput 装进接口就成了
// 非 nil 的接口值，「没开文件」会被当成「开了一个」，关的时候空指针。存具体类型，
// 这个坑在类型上就不存在。
func (m *manager) swapFile(next *FileOutput) error {
	prev := m.file.Swap(next)
	if prev == nil {
		return nil
	}
	return prev.Close()
}

// close 关掉当前这份日志文件，并把它从 manager 上摘掉，因此重复调用是空操作。
func (m *manager) close() error { return m.swapFile(nil) }

// newHandler 按格式挑一个 Handler。format 已由调用方校验过，留空即控制台。
func (m *manager) newHandler(o options) slog.Handler {
	// 先把调用方给的阈值落进包内这一个 LevelVar，再把 LevelVar 本身交给 Handler：
	// 两头因此都成立——[Setup] 收到的 [WithLevel] 说话算数，而装完之后 [SetLevel]
	// 仍改得动它。把 o.level 直接传下去会丢掉后一半；像这里曾经那样只传 m.level、
	// 不看 o.level，丢掉的则是前一半：外部 --log.level 怎么写级别都不动。
	m.level.Set(o.level.Level())
	opts := []Option{
		WithLevel(m.level),
		WithWriter(o.output()),
		WithTimeLayout(o.timeLayout),
		WithJSONHeader(o.jsonHeader),
		WithConsoleHeader(o.consoleHeader),
	}
	if strings.EqualFold(strings.TrimSpace(o.format), FormatJSON) {
		return NewJSONHandler(opts...)
	}
	return NewConsoleHandler(opts...)
}
