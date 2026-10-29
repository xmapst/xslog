package xslog

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/DeRuina/timberjack"
)

const (
	// CompressionNone 表示不压缩轮转后的历史文件。
	CompressionNone = "none"
	// CompressionGzip 使用 gzip 压缩历史文件。
	CompressionGzip = "gzip"
	// CompressionZstd 使用 zstd 压缩历史文件。
	CompressionZstd = "zstd"
)

const (
	// DefaultMaxSizeMB 是单个日志文件的默认大小上限，单位为 MiB。
	DefaultMaxSizeMB = 50
	// DefaultMaxBackups 是默认保留的历史轮转批次数。
	DefaultMaxBackups = 7
	// DefaultMaxAgeDays 是历史文件的默认保留天数。
	DefaultMaxAgeDays = 7
	// DefaultRotationHours 是默认轮转间隔，单位为小时。
	DefaultRotationHours = 24
)

// FileOption 配置 [OpenFile] 或 [WithFile] 的文件轮转参数。
// 省略选项时使用本包默认值，显式传入的零值交给对应选项处理。
type FileOption func(*fileOptions)

// fileOptions 是文件选项应用后的结果。
type fileOptions struct {
	maxSizeMB     int
	maxBackups    int
	maxAgeDays    int
	rotationHours int
	compression   string
	err           error
}

// newFileOptions 从默认值构建文件选项，并记录参数校验错误。
func (f *FileOutput) newFileOptions(opts []FileOption) fileOptions {
	o := fileOptions{
		maxSizeMB:     DefaultMaxSizeMB,
		maxBackups:    DefaultMaxBackups,
		maxAgeDays:    DefaultMaxAgeDays,
		rotationHours: DefaultRotationHours,
		compression:   CompressionNone,
	}
	for _, opt := range opts {
		if opt != nil {
			opt(&o)
		}
	}
	return o
}

// WithMaxSizeMB 设置单个文件的大小上限，单位为 MiB（1024×1024 字节）。
// 默认值为 [DefaultMaxSizeMB]。
// 负数会在打开文件时报错；0 使用底层轮转器的默认上限。
func WithMaxSizeMB(mb int) FileOption {
	return func(o *fileOptions) {
		if mb < 0 {
			o.err = fmt.Errorf("xslog: max size MB must be non-negative: %d", mb)
			return
		}
		o.maxSizeMB = mb
	}
}

// WithMaxBackups 设置最多保留的历史轮转批次数，默认值为 [DefaultMaxBackups]。
// 0 表示不按批次数清理；负数会在打开文件时报错。
func WithMaxBackups(n int) FileOption {
	return func(o *fileOptions) {
		if n < 0 {
			o.err = fmt.Errorf("xslog: max backups must be non-negative: %d", n)
			return
		}
		o.maxBackups = n
	}
}

// WithMaxAgeDays 设置历史文件的最长保留天数，默认值为 [DefaultMaxAgeDays]。
// 0 表示不按保留天数清理；负数会在打开文件时报错。
func WithMaxAgeDays(days int) FileOption {
	return func(o *fileOptions) {
		if days < 0 {
			o.err = fmt.Errorf("xslog: max age days must be non-negative: %d", days)
			return
		}
		o.maxAgeDays = days
	}
}

// WithRotationHours 设置按时间轮转的间隔，单位为小时，默认值为 [DefaultRotationHours]。
// 0 关闭按时间轮转；负数或无法表示为 time.Duration 的值会在打开文件时报错。
func WithRotationHours(hours int) FileOption {
	return func(o *fileOptions) {
		if hours < 0 {
			o.err = fmt.Errorf("xslog: rotation hours must be non-negative: %d", hours)
			return
		}
		o.rotationHours = hours
	}
}

// WithCompression 设置历史文件的压缩方式，可选 [CompressionNone]、
// [CompressionGzip] 或 [CompressionZstd]，空值表示不压缩。
// 名称忽略大小写及首尾空白，无效值在打开文件时报错。
func WithCompression(kind string) FileOption {
	return func(o *fileOptions) {
		o.compression = kind
	}
}

// FileOutput 通过 [io.WriteCloser] 提供按大小和时间轮转的日志输出。
// [OpenFile] 返回的文件由调用方关闭；[Setup] 通过 [WithFile] 创建的文件由 [Close] 关闭。
type FileOutput struct {
	io.WriteCloser
}

// OpenFile 打开按大小和时间轮转的日志文件，交给 [WithWriter] 使用。
// 调用方负责关闭返回的文件。
//
// 打开前会校验参数、创建缺失的目录并试开文件，提前返回路径或权限错误。
// 轮转器在首次写入时打开持续使用的文件句柄。
func OpenFile(path string, opts ...FileOption) (*FileOutput, error) {
	var f = new(FileOutput)
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("xslog: empty log file path")
	}
	o := f.newFileOptions(opts)
	if o.err != nil {
		return nil, o.err
	}
	if int64(o.rotationHours) > int64(^uint64(0)>>1)/int64(time.Hour) {
		return nil, fmt.Errorf("xslog: rotation hours out of range: %d", o.rotationHours)
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolve log path %q: %w", path, err)
	}
	compression, err := f.resolveCompression(o.compression)
	if err != nil {
		return nil, err
	}
	if err = f.ensureLogDir(filepath.Dir(abs)); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(abs, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open log path %q: %w", abs, err)
	}
	_ = file.Close()
	// 使用当前 Logger 记录解析后的绝对路径。
	slog.Info("logging to file", slog.String("path", abs))
	f.WriteCloser = &timberjack.Logger{
		Filename:   abs,
		MaxSize:    o.maxSizeMB,
		MaxBackups: o.maxBackups,
		MaxAge:     o.maxAgeDays,
		// 0 关闭时间轮转，未指定时使用本包默认间隔。
		RotationInterval: time.Duration(o.rotationHours) * time.Hour,
		Compression:      compression,
		// 备份文件名使用系统本地时间，不影响记录时间的模板格式。
		LocalTime:        true,
		BackupTimeFormat: "2006-01-02-15-04-05",
	}
	return f, nil
}

// ensureLogDir 在目录不存在时记录告警并尝试创建目录。
func (f *FileOutput) ensureLogDir(dir string) error {
	if _, err := os.Stat(dir); !errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	slog.Warn("log directory does not exist, creating it", slog.String("dir", dir))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create log directory %q: %w", dir, err)
	}
	return nil
}

// resolveCompression 规范化并校验压缩方式，空值表示不压缩。
func (f *FileOutput) resolveCompression(kind string) (string, error) {
	switch s := strings.ToLower(strings.TrimSpace(kind)); s {
	case "":
		return CompressionNone, nil
	case CompressionNone, CompressionGzip, CompressionZstd:
		return s, nil
	default:
		return "", fmt.Errorf("invalid log compression %q: want %s / %s / %s",
			kind, CompressionNone, CompressionGzip, CompressionZstd)
	}
}
