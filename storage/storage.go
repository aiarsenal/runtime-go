// Package storage 提供对象存储抽象，覆盖 本地磁盘 / S3 兼容（阿里云 OSS / 腾讯云 COS / MinIO / AWS S3）。
// 与 runtime（实例运行时）正交，使用独立的 provider registry，不碰 runtime.Register。
package storage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"
)

// Storage 对象存储接口。key 为相对路径（如 skill/{skillId}/{version}/{file}），各 provider 自行解析为物理位置。
type Storage interface {
	// Provider 返回标识（local / s3）。
	Provider() string
	// Save 写入 key（已存在则覆盖），返回写入字节数。
	Save(ctx context.Context, key string, r io.Reader) (int64, error)
	// Open 打开 key 返回读取流（调用方负责 Close）；不存在返回 ErrNotFound。
	Open(ctx context.Context, key string) (io.ReadCloser, error)
	// Delete 删除 key；不存在视为成功（幂等）。
	Delete(ctx context.Context, key string) error
	// Stat 返回对象元信息；不存在返回 ErrNotFound。
	Stat(ctx context.Context, key string) (*ObjectInfo, error)
	// Close 释放底层连接（s3 无状态可 no-op）。
	Close() error
}

// ObjectInfo 对象元信息。
type ObjectInfo struct {
	Key          string
	Size         int64
	ContentType  string
	LastModified time.Time
}

// Config 构造某个 storage provider 所需的配置。调用方填 Type + 各 provider 要求的 Params，再调 New。
type Config struct {
	Type   string         // provider 名：local / s3
	Params map[string]any // provider 特有参数（见各 provider 包的 FromConfig）
}

// Factory 按 Config 构造 Storage。各 provider 包在 init() 里注册自己。
type Factory func(cfg Config) (Storage, error)

var (
	mu        sync.RWMutex
	factories = map[string]Factory{}
)

// Register 注册一个 provider 工厂。通常在 provider 包的 init() 中调用。重复注册同名 provider 将 panic。
func Register(name string, f Factory) {
	mu.Lock()
	defer mu.Unlock()
	if _, exists := factories[name]; exists {
		panic(fmt.Sprintf("storage: provider %q already registered", name))
	}
	factories[name] = f
}

// New 按 Config.Type 构造对应 provider 的 Storage。
func New(cfg Config) (Storage, error) {
	mu.RLock()
	f, ok := factories[cfg.Type]
	mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("storage: unknown provider %q (registered: %v)", cfg.Type, Registered())
	}
	return f(cfg)
}

// Registered 返回已注册的 provider 名列表。
func Registered() []string {
	mu.RLock()
	defer mu.RUnlock()
	out := make([]string, 0, len(factories))
	for k := range factories {
		out = append(out, k)
	}
	return out
}

// ErrNotFound 对象不存在。
var ErrNotFound = errors.New("storage: object not found")

// IsNotFound 判断是否「对象不存在」错误。
func IsNotFound(err error) bool { return errors.Is(err, ErrNotFound) }

// errUnsafeKey key 含非法路径段（路径穿越保护）。
var errUnsafeKey = errors.New("storage: key contains illegal path segment")
