package knowledge

// Notion Provider。
//
// 所依据的 API(https://api.notion.com/v1,请求头 Notion-Version: 2022-06-28,integration token 鉴权):
//
//	1. POST /v1/search               列出集成可见页面(注意:search 无按父页面过滤的参数)
//	2. GET  /v1/pages/{page_id}      页面属性(取标题/父级/最后编辑时间)
//	3. GET  /v1/blocks/{id}/children 分页拉取块(has_children 时逐层懒加载)
//
// List 两种模式:
//   - 配置了 RootPage(NOTION_ROOT_PAGE):从该页面沿「子页面块」DFS 展开(深度 MaxDepth,
//     默认 3;总量上限 MaxPages,默认 500),适合整站拉取;根页面本身也进清单;
//   - 未配置:全工作区 search(仅返回集成被授权可见、未归档页面)。
//
// 限制(诚实说明):
//   - 集成只能在「已把页面连接给该集成」的范围内取数,看不到未授权页面;
//   - file 型图片/附件 URL 为临时 S3 链接,拉下来的 Markdown 里的链接会过期。

import (
	"context"
	"fmt"
	"time"
)

const (
	notionDefaultEndpoint = "https://api.notion.com"
	notionAPIVersion      = "2022-06-28"
)

// NotionProvider Notion provider。
type NotionProvider struct {
	cfg    NotionConfig
	client *apiClient
}

// NewNotionProvider 构造 notion provider。
func NewNotionProvider(cfg NotionConfig) *NotionProvider {
	return &NotionProvider{
		cfg:    cfg,
		client: newAPIClient(firstNonEmpty(cfg.Endpoint, notionDefaultEndpoint), cfg.TimeoutSec),
	}
}

// Name 实现 Provider。
func (p *NotionProvider) Name() string { return "notion" }

// headers API 公共请求头。
func (p *NotionProvider) headers() map[string]string {
	return map[string]string{
		"Authorization":  "Bearer " + p.cfg.Token,
		"Notion-Version": notionAPIVersion,
	}
}

// notionPage 页面对象(仅取需要的字段)。
type notionPage struct {
	ID             string         `json:"id"`
	URL            string         `json:"url"`
	CreatedTime    string         `json:"created_time"`
	LastEditedTime string         `json:"last_edited_time"`
	Archived       bool           `json:"archived"`
	InTrash        bool           `json:"in_trash"`
	Parent         map[string]any `json:"parent"`
	Properties     map[string]struct {
		Type  string           `json:"type"`
		Title []notionRichText `json:"title"`
	} `json:"properties"`
}

// title 从 properties 中取 title 型属性文本。
func (pg *notionPage) title() string {
	for _, prop := range pg.Properties {
		if prop.Type == "title" {
			return notionRichTextPlain(prop.Title)
		}
	}
	return ""
}

// updatedAt 解析 last_edited_time(ISO8601)。
func (pg *notionPage) updatedAt() time.Time {
	if pg.LastEditedTime == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339, pg.LastEditedTime)
	if err != nil {
		return time.Time{}
	}
	return t
}

// space 描述父级(page_id / database_id / workspace)。
func (pg *notionPage) space() string {
	if len(pg.Parent) == 0 {
		return "workspace"
	}
	if t, ok := pg.Parent["type"].(string); ok {
		if id, ok := pg.Parent[t+"_id"].(string); ok {
			return t + ":" + id
		}
		return t
	}
	return "workspace"
}

// List 实现 Provider。
func (p *NotionProvider) List(ctx context.Context) ([]DocumentMeta, error) {
	if p.cfg.RootPage != "" {
		return p.listFromRoot(ctx)
	}
	return p.listBySearch(ctx)
}

// listBySearch 全工作区搜索页面。
func (p *NotionProvider) listBySearch(ctx context.Context) ([]DocumentMeta, error) {
	var out []DocumentMeta
	cursor := ""
	for {
		body := map[string]any{
			"page_size": 100,
			"filter":    map[string]any{"property": "object", "value": "page"},
		}
		if cursor != "" {
			body["start_cursor"] = cursor
		}
		var resp struct {
			Results    []notionPage `json:"results"`
			HasMore    bool         `json:"has_more"`
			NextCursor string       `json:"next_cursor"`
		}
		if err := apiJSON(ctx, p.client.hc, "POST", p.client.base+"/v1/search", p.headers(), body, &resp); err != nil {
			return nil, fmt.Errorf("knowledge/notion: search: %w(确认 token 有效且页面已连接给该集成)", err)
		}
		for _, pg := range resp.Results {
			if pg.Archived || pg.InTrash {
				continue
			}
			out = append(out, DocumentMeta{
				ID:        pg.ID,
				Title:     pg.title(),
				Space:     pg.space(),
				UpdatedAt: pg.updatedAt(),
			})
		}
		if !resp.HasMore || resp.NextCursor == "" {
			return out, nil
		}
		cursor = resp.NextCursor
	}
}

// listFromRoot 从根页面 DFS 子页面树。
func (p *NotionProvider) listFromRoot(ctx context.Context) ([]DocumentMeta, error) {
	maxDepth := p.cfg.MaxDepth
	if maxDepth <= 0 {
		maxDepth = 3
	}
	maxPages := p.cfg.MaxPages
	if maxPages <= 0 {
		maxPages = 500
	}
	rootID := p.cfg.RootPage
	out := make([]DocumentMeta, 0, 16)
	seen := map[string]bool{}

	// 根页面本身进清单。
	if pg, err := p.getPage(ctx, rootID); err == nil {
		out = append(out, DocumentMeta{ID: pg.ID, Title: pg.title(), Space: pg.space(), UpdatedAt: pg.updatedAt()})
		seen[pg.ID] = true
	}
	var walk func(ctx context.Context, pageID string, depth int) error
	walk = func(ctx context.Context, pageID string, depth int) error {
		if depth > maxDepth || len(out) >= maxPages {
			return nil
		}
		blocks, err := p.getBlockChildren(ctx, pageID)
		if err != nil {
			// 无内容权限的页面跳过,不中断整棵树。
			return nil
		}
		for _, b := range blocks {
			if b.Type != "child_page" && b.Type != "child_database" {
				continue
			}
			if seen[b.ID] || len(out) >= maxPages {
				continue
			}
			seen[b.ID] = true
			title := "未命名"
			if b.ChildPage != nil && b.ChildPage.Title != "" {
				title = b.ChildPage.Title
			} else if b.ChildDatabase != nil && b.ChildDatabase.Title != "" {
				title = b.ChildDatabase.Title
			}
			out = append(out, DocumentMeta{ID: b.ID, Title: title, Space: "root:" + p.cfg.RootPage})
			if b.Type == "child_page" {
				if err := walk(ctx, b.ID, depth+1); err != nil {
					return err
				}
			}
		}
		return nil
	}
	if err := walk(ctx, rootID, 1); err != nil {
		return out, err
	}
	return out, nil
}

// getPage 取页面属性。
func (p *NotionProvider) getPage(ctx context.Context, pageID string) (*notionPage, error) {
	var pg notionPage
	url := p.client.base + "/v1/pages/" + pageID
	if err := apiJSON(ctx, p.client.hc, "GET", url, p.headers(), nil, &pg); err != nil {
		return nil, err
	}
	return &pg, nil
}

// getBlockChildren 分页拉取块子块。
func (p *NotionProvider) getBlockChildren(ctx context.Context, blockID string) ([]notionBlock, error) {
	var all []notionBlock
	cursor := ""
	for {
		url := p.client.base + "/v1/blocks/" + blockID + "/children?page_size=100"
		if cursor != "" {
			url += "&start_cursor=" + cursor
		}
		var resp struct {
			Results    []notionBlock `json:"results"`
			HasMore    bool          `json:"has_more"`
			NextCursor string        `json:"next_cursor"`
		}
		if err := apiJSON(ctx, p.client.hc, "GET", url, p.headers(), nil, &resp); err != nil {
			return nil, err
		}
		all = append(all, resp.Results...)
		if !resp.HasMore || resp.NextCursor == "" {
			return all, nil
		}
		cursor = resp.NextCursor
	}
}

// Fetch 实现 Provider:页面 → Markdown(块树递归渲染)。
func (p *NotionProvider) Fetch(ctx context.Context, id string) (*Document, error) {
	pg, err := p.getPage(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("knowledge/notion: 取页面 %s: %w(确认页面已连接给该集成)", id, err)
	}
	title := pg.title()
	if title == "" {
		title = id
	}
	blocks, err := p.getBlockChildren(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("knowledge/notion: 拉取页面块 %s: %w", id, err)
	}
	body, err := notionBlocksToMarkdown(ctx, blocks, p.getBlockChildren)
	if err != nil {
		return nil, fmt.Errorf("knowledge/notion: 渲染页面 %s: %w", id, err)
	}
	content := "# " + title + "\n\n" + body
	return &Document{
		ID:        id,
		Title:     title,
		Space:     pg.space(),
		UpdatedAt: pg.updatedAt(),
		Content:   []byte(content),
		Meta: map[string]string{
			"url":     pg.URL,
			"updated": pg.LastEditedTime,
		},
	}, nil
}

// 编译期接口断言。
var (
	_ Provider = (*FeishuProvider)(nil)
	_ Provider = (*DingTalkProvider)(nil)
	_ Provider = (*TencentDocProvider)(nil)
	_ Provider = (*NotionProvider)(nil)
)
