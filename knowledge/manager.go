package knowledge

import (
	"context"
	"fmt"
	"sync"
)

// Manager 是知识生态接入的统一入口:按 provider 名取实现,批量拉取带并发控制。
// 与 runtime/storage 的注册表模式不同,这里按配置直接构造(四个 provider 都是本包内置,
// 无需跨包注册),凭据没配的平台不进入 Manager。
type Manager struct {
	providers map[string]Provider
}

// NewManager 按 Config 构造启用的 provider(凭据齐全才启用)。
// 一个都没配时返回错误并列出各平台所需环境变量。
func NewManager(cfg Config) (*Manager, error) {
	m := &Manager{providers: map[string]Provider{}}
	if cfg.Feishu.Enabled() {
		m.providers["feishu"] = NewFeishuProvider(cfg.Feishu)
	}
	if cfg.DingTalk.Enabled() {
		m.providers["dingtalk"] = NewDingTalkProvider(cfg.DingTalk)
	}
	if cfg.TencentDoc.Enabled() {
		m.providers["tdoc"] = NewTencentDocProvider(cfg.TencentDoc)
	}
	if cfg.Notion.Enabled() {
		m.providers["notion"] = NewNotionProvider(cfg.Notion)
	}
	if len(m.providers) == 0 {
		return nil, fmt.Errorf("knowledge: %s", envHelp)
	}
	return m, nil
}

// Providers 返回已启用的 provider 名(排序)。
func (m *Manager) Providers() []string {
	out := make([]string, 0, len(m.providers))
	for name := range m.providers {
		out = append(out, name)
	}
	return sortStrings(out)
}

// Provider 取指定 provider;未启用返回错误。
func (m *Manager) Provider(name string) (Provider, error) {
	p, ok := m.providers[name]
	if !ok {
		return nil, fmt.Errorf("knowledge: provider %q 未启用(可用: %v;启用需配置对应凭据)", name, m.Providers())
	}
	return p, nil
}

// List 列出指定平台的文档清单。
func (m *Manager) List(ctx context.Context, name string) ([]DocumentMeta, error) {
	p, err := m.Provider(name)
	if err != nil {
		return nil, err
	}
	return p.List(ctx)
}

// Fetch 拉取指定平台的一篇文档。
func (m *Manager) Fetch(ctx context.Context, name, id string) (*Document, error) {
	p, err := m.Provider(name)
	if err != nil {
		return nil, err
	}
	return p.Fetch(ctx, id)
}

// FetchAll 列出 provider 全部文档并并发拉取,每篇成功后回调 handle。
// 并发度 concurrency<=0 时取 4。
//
// 错误语义为 errgroup(fail-fast):任一次 List/Fetch/handle 返回错误,即取消派生
// context、停止派发剩余任务,Wait 返回首个错误;已写出的结果保留,未拉取的直接跳过。
func (m *Manager) FetchAll(ctx context.Context, name string, concurrency int, handle func(*Document) error) error {
	p, err := m.Provider(name)
	if err != nil {
		return err
	}
	metas, err := p.List(ctx)
	if err != nil {
		return fmt.Errorf("knowledge: %s list: %w", name, err)
	}
	return FetchMetas(ctx, p, metas, concurrency, handle)
}

// FetchMetas 对给定清单并发 Fetch(同样 fail-fast),供调用方自己控制清单来源
// (如 pull-all 只拉指定 id 子集)。
func FetchMetas(ctx context.Context, p Provider, metas []DocumentMeta, concurrency int, handle func(*Document) error) error {
	if concurrency <= 0 {
		concurrency = 4
	}
	g, gctx := newGroup(ctx)
	sem := make(chan struct{}, concurrency) // 并发闸
	for i := range metas {
		meta := metas[i]
		select {
		case sem <- struct{}{}:
		case <-gctx.Done():
			return g.Wait() // 已有失败/取消:停止派发
		}
		g.Go(func() error {
			defer func() { <-sem }()
			if err := gctx.Err(); err != nil {
				return err
			}
			doc, err := p.Fetch(gctx, meta.ID)
			if err != nil {
				return fmt.Errorf("knowledge: %s fetch %q(%s): %w", p.Name(), meta.Title, meta.ID, err)
			}
			if doc == nil {
				return fmt.Errorf("knowledge: %s fetch %q(%s): provider 返回空文档", p.Name(), meta.Title, meta.ID)
			}
			if doc.Title == "" {
				doc.Title = meta.Title
			}
			if doc.Space == "" {
				doc.Space = meta.Space
			}
			if doc.UpdatedAt.IsZero() {
				doc.UpdatedAt = meta.UpdatedAt
			}
			if handle != nil {
				return handle(doc)
			}
			return nil
		})
	}
	return g.Wait()
}

// ---- 手写 errgroup ----

// group 是 golang.org/x/sync/errgroup.Group 语义的最小手写实现(零第三方依赖):
// Go 并发跑任务;任一任务返回非 nil 错误时记录首个错误并 cancel 派生 context;
// Wait 阻塞至全部任务结束并返回该错误。
type group struct {
	cancel context.CancelFunc
	wg     sync.WaitGroup
	mu     sync.Mutex
	err    error
}

func newGroup(ctx context.Context) (*group, context.Context) {
	gctx, cancel := context.WithCancel(ctx)
	return &group{cancel: cancel}, gctx
}

// Go 启动一个任务。
func (g *group) Go(f func() error) {
	g.wg.Add(1)
	go func() {
		defer g.wg.Done()
		if err := f(); err != nil {
			g.mu.Lock()
			if g.err == nil {
				g.err = err
			}
			g.mu.Unlock()
			g.cancel()
		}
	}()
}

// Wait 等待全部任务结束,返回首个非 nil 错误(无错返回 nil)。
func (g *group) Wait() error {
	g.wg.Wait()
	g.cancel()
	return g.err
}
