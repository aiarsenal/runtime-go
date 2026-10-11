package knowledge

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

// loadFeishuFixture 读 testdata/feishu_blocks.json(飞书 blocks 接口的 data.items 形态)。
func loadFeishuFixture(t *testing.T) []feishuBlock {
	t.Helper()
	raw, err := os.ReadFile("testdata/feishu_blocks.json")
	if err != nil {
		t.Fatalf("读 fixture: %v", err)
	}
	var blocks []feishuBlock
	if err := json.Unmarshal(raw, &blocks); err != nil {
		t.Fatalf("解析 fixture: %v", err)
	}
	return blocks
}

func TestFeishuBlocksToMarkdown(t *testing.T) {
	blocks := loadFeishuFixture(t)
	got := feishuBlocksToMarkdown(blocks, "项目文档")
	want := `# 项目文档

# **项目说明**

这是*正文*[带链接](https://example.com/doc)与` + "`代码`" + `

- 第一点
- 第二点
  - 嵌套子项

1. 第一步
2. 第二步

` + "```go" + `
println("hi")
` + "```" + `

> 引用一句话

- [x] 已完成事项
- [ ] 待办事项

> 高亮提示

---

[图片:token=img_v2_xyz(需通过飞书素材接口获取)]
`
	if strings.TrimSpace(got) != strings.TrimSpace(want) {
		t.Fatalf("markdown 不匹配。\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

func TestFeishuRawContentToMarkdown(t *testing.T) {
	got := feishuRawContentToMarkdown("第一段第一行\n第一段第二行\n\n第二段\n", "标题")
	want := "# 标题\n\n第一段第一行\n第一段第二行\n\n第二段\n"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

// TestFeishuProviderHTTP 用 httptest 走一遍 token → 列表 → 元信息 → 块 的完整链路。
func TestFeishuProviderHTTP(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/open-apis/auth/v3/tenant_access_token/internal", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":0,"msg":"ok","tenant_access_token":"t-test","expire":7200}`))
	})
	mux.HandleFunc("/open-apis/drive/v1/files", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("folder_token") != "FldX" {
			http.Error(w, "bad folder", http.StatusBadRequest)
			return
		}
		if got := r.Header.Get("Authorization"); got != "Bearer t-test" {
			t.Errorf("Authorization = %q", got)
		}
		_, _ = w.Write([]byte(`{"code":0,"data":{"files":[{"name":"项目文档","token":"DOC1","type":"docx","url":"https://x/docx/DOC1","created_time":"1698000000000","modified_time":"1698003600000"}],"has_more":false}}`))
	})
	mux.HandleFunc("/open-apis/docx/v1/documents/DOC1", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"code":0,"data":{"document":{"document_id":"DOC1","title":"项目文档"}}}`))
	})
	mux.HandleFunc("/open-apis/docx/v1/documents/DOC1/blocks", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"code":0,"data":{"items":[
			{"block_id":"pg","block_type":1,"parent_id":"","children":["t"]},
			{"block_id":"t","block_type":2,"parent_id":"pg","text":{"elements":[{"text_run":{"content":"正文内容"}}]}}
		],"has_more":false}}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	p := NewFeishuProvider(FeishuConfig{AppID: "a", AppSecret: "b", FolderToken: "FldX", Endpoint: srv.URL})

	metas, err := p.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(metas) != 1 {
		t.Fatalf("List 返回 %d 条, want 1", len(metas))
	}
	if metas[0].ID != "DOC1" || metas[0].Title != "项目文档" {
		t.Fatalf("meta = %+v", metas[0])
	}
	wantTime := time.UnixMilli(1698003600000)
	if !metas[0].UpdatedAt.Equal(wantTime) {
		t.Fatalf("UpdatedAt = %v, want %v", metas[0].UpdatedAt, wantTime)
	}

	doc, err := p.Fetch(context.Background(), "DOC1")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if doc.Title != "项目文档" {
		t.Fatalf("Title = %q", doc.Title)
	}
	if !strings.Contains(string(doc.Content), "# 项目文档") || !strings.Contains(string(doc.Content), "正文内容") {
		t.Fatalf("Content = %q", doc.Content)
	}
	if doc.Meta["content_source"] != "blocks" {
		t.Fatalf("content_source = %q", doc.Meta["content_source"])
	}
}

// TestFeishuRawContentFallback blocks 接口失败时回退 raw_content。
func TestFeishuRawContentFallback(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/open-apis/auth/v3/tenant_access_token/internal", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"code":0,"tenant_access_token":"t-test","expire":7200}`))
	})
	mux.HandleFunc("/open-apis/docx/v1/documents/DOC1", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"code":0,"data":{"document":{"document_id":"DOC1","title":"文档"}}}`))
	})
	mux.HandleFunc("/open-apis/docx/v1/documents/DOC1/blocks", func(w http.ResponseWriter, r *http.Request) {
		// 模拟无 blocks 权限
		http.Error(w, `{"code":99991668,"msg":"permission denied"}`, http.StatusForbidden)
	})
	mux.HandleFunc("/open-apis/docx/v1/documents/DOC1/raw_content", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"code":0,"data":{"content":"第一行\n第二行"}}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	p := NewFeishuProvider(FeishuConfig{AppID: "a", AppSecret: "b", Endpoint: srv.URL})
	doc, err := p.Fetch(context.Background(), "DOC1")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if doc.Meta["content_source"] != "raw_content" {
		t.Fatalf("content_source = %q, want raw_content", doc.Meta["content_source"])
	}
	if !strings.Contains(string(doc.Content), "第一行") || !strings.Contains(string(doc.Content), "第二行") {
		t.Fatalf("Content = %q", doc.Content)
	}
}
