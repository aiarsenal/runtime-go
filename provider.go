package runtime

import (
	"fmt"
	"sync"
)

// Config 是构造某个 provider Adapter 所需的配置。
// 调用方只需填 Type（provider 名）与对应 provider 包要求的字段，再调 New。
type Config struct {
	Type   string         // provider 名：localdocker / localprocess / tencent / aliyun / k8s
	Params map[string]any // provider 特有参数（见各 provider 包的 FromConfig）
}

// Factory 按 Config 构造 Adapter。各 provider 包在 init() 里注册自己。
type Factory func(cfg Config) (Adapter, error)

var (
	mu        sync.RWMutex
	factories = map[string]Factory{}
)

// Register 注册一个 provider 工厂。通常在 provider 包的 init() 中调用。
// 重复注册同名 provider 将 panic（编程错误）。
func Register(name string, f Factory) {
	mu.Lock()
	defer mu.Unlock()
	if _, exists := factories[name]; exists {
		panic(fmt.Sprintf("runtime: provider %q already registered", name))
	}
	factories[name] = f
}

// New 按 Config.Type 构造对应 provider 的 Adapter。
func New(cfg Config) (Adapter, error) {
	mu.RLock()
	f, ok := factories[cfg.Type]
	mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("runtime: unknown provider %q (registered: %v)", cfg.Type, Registered())
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
