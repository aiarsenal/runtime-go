package knowledge

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"testing"
)

// fakeProvider 测试桩。
type fakeProvider struct {
	name  string
	metas []DocumentMeta
	fetch func(ctx context.Context, id string) (*Document, error)

	mu      sync.Mutex
	fetched []string // 实际执行 Fetch 的 id(含失败)
	fetchN  int
	listErr error
}

func (f *fakeProvider) Name() string { return f.name }

func (f *fakeProvider) List(ctx context.Context) ([]DocumentMeta, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.metas, nil
}

func (f *fakeProvider) Fetch(ctx context.Context, id string) (*Document, error) {
	f.mu.Lock()
	f.fetchN++
	f.fetched = append(f.fetched, id)
	f.mu.Unlock()
	return f.fetch(ctx, id)
}

func mkMeta(id string) DocumentMeta { return DocumentMeta{ID: id, Title: "标题-" + id, Space: "s"} }

func TestFetchMetasConcurrent(t *testing.T) {
	ids := []string{"d1", "d2", "d3", "d4", "d5", "d6", "d7", "d8"}
	p := &fakeProvider{
		name:  "fake",
		metas: []DocumentMeta{},
		fetch: func(_ context.Context, id string) (*Document, error) {
			return &Document{ID: id, Title: "标题-" + id, Content: []byte("# " + id)}, nil
		},
	}
	for _, id := range ids {
		p.metas = append(p.metas, mkMeta(id))
	}

	var mu sync.Mutex
	var got []string
	err := FetchMetas(context.Background(), p, p.metas, 3, func(doc *Document) error {
		mu.Lock()
		got = append(got, doc.ID)
		mu.Unlock()
		return nil
	})
	if err != nil {
		t.Fatalf("FetchMetas: %v", err)
	}
	if len(got) != len(ids) {
		t.Fatalf("拉到 %d 篇, want %d", len(got), len(ids))
	}
	sort.Strings(got)
	for i, id := range ids {
		if got[i] != id {
			t.Fatalf("ids = %v, 漏了 %s", got, id)
		}
	}
	if p.fetchN != len(ids) {
		t.Fatalf("fetch 次数 = %d, want %d", p.fetchN, len(ids))
	}
}

// TestFetchMetasFailFast 并发度 1 时,第 2 篇失败:只拉 2 篇,返回首个错误(errgroup 语义)。
func TestFetchMetasFailFast(t *testing.T) {
	boom := errors.New("boom-on-d2")
	p := &fakeProvider{
		name: "fake",
		metas: []DocumentMeta{
			mkMeta("d1"), mkMeta("d2"), mkMeta("d3"), mkMeta("d4"), mkMeta("d5"),
		},
		fetch: func(_ context.Context, id string) (*Document, error) {
			if id == "d2" {
				return nil, boom
			}
			return &Document{ID: id}, nil
		},
	}

	var mu sync.Mutex
	var got []string
	err := FetchMetas(context.Background(), p, p.metas, 1, func(doc *Document) error {
		mu.Lock()
		got = append(got, doc.ID)
		mu.Unlock()
		return nil
	})
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want boom-on-d2", err)
	}
	if len(got) != 1 || got[0] != "d1" {
		t.Fatalf("成功回调 %v, want [d1](fail-fast 不再继续)", got)
	}
	if p.fetchN != 2 {
		t.Fatalf("fetch 次数 = %d, want 2", p.fetchN)
	}
}

// TestFetchMetasContextCancel 并发拉取中取消 context:错误上抛,不再派发新任务。
func TestFetchMetasContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	p := &fakeProvider{
		name:  "fake",
		metas: []DocumentMeta{mkMeta("a"), mkMeta("b"), mkMeta("c"), mkMeta("d")},
		fetch: func(fctx context.Context, id string) (*Document, error) {
			if id == "b" {
				cancel()
			}
			if fctx.Err() != nil {
				return nil, fctx.Err()
			}
			return &Document{ID: id}, nil
		},
	}
	err := FetchMetas(ctx, p, p.metas, 1, func(*Document) error { return nil })
	if err == nil {
		t.Fatal("取消后应返回错误")
	}
	if p.fetchN == len(p.metas) {
		t.Fatalf("取消后仍执行了全部 %d 次 fetch", p.fetchN)
	}
}

func TestFetchAllListError(t *testing.T) {
	p := &fakeProvider{
		name:    "fake",
		listErr: errors.New("list down"),
		fetch:   func(context.Context, string) (*Document, error) { return nil, nil },
	}
	m := &Manager{providers: map[string]Provider{"fake": p}}
	err := m.FetchAll(context.Background(), "fake", 2, func(*Document) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "list down") {
		t.Fatalf("err = %v, want list 错误", err)
	}
}

func TestFetchMetasUnknownProvider(t *testing.T) {
	m, err := NewManager(Config{Notion: NotionConfig{Token: "t"}})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	if _, err := m.Fetch(context.Background(), "feishu", "x"); err == nil {
		t.Fatal("未启用 provider 的 Fetch 应报错")
	}
	if _, err := m.List(context.Background(), "feishu"); err == nil {
		t.Fatal("未启用 provider 的 List 应报错")
	}
}

func TestSlugFileNameForPullAll(t *testing.T) {
	// 模拟 pull-all 的文件名分配:同标题多篇防重 + slug 空标题回退。
	taken := map[string]bool{}
	var names []string
	for _, title := range []string{"会议纪要", "会议纪要", "会议纪要", "", "设计/评审:结论"} {
		base := Slugify(firstNonEmpty(title, "doc-id"), 0)
		names = append(names, Uniquify(base, taken))
	}
	want := []string{"会议纪要", "会议纪要-2", "会议纪要-3", "doc-id", "设计-评审-结论"}
	for i := range want {
		if names[i] != want[i] {
			t.Fatalf("names[%d] = %q, want %q(全部: %v)", i, names[i], want[i], names)
		}
	}
}

func TestTokenCacheRefresh(t *testing.T) {
	calls := 0
	tc := &tokenCache{refresh: func(context.Context) (string, int64, error) {
		calls++
		if calls > 2 {
			return "", 0, fmt.Errorf("too many refreshes")
		}
		return fmt.Sprintf("tok-%d", calls), 7200, nil
	}}
	for i := 0; i < 5; i++ {
		tok, err := tc.Token(context.Background())
		if err != nil {
			t.Fatalf("Token: %v", err)
		}
		if tok != "tok-1" {
			t.Fatalf("tok = %q, want tok-1(缓存生效)", tok)
		}
	}
	if calls != 1 {
		t.Fatalf("refresh 次数 = %d, want 1", calls)
	}
}
