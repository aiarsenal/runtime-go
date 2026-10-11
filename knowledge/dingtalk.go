package knowledge

// 钉钉 Provider(钉盘文件列表 + 钉钉文档内容)。
//
// 所依据的开放平台 API(open.dingtalk.com,企业内部应用):
//
//	1. POST https://api.dingtalk.com/v1.0/oauth2/accessToken          appKey/appSecret 换 accessToken
//	2. POST /v2.0/storage/dentries/lists                              钉盘:列出文件夹内文件(分页)
//	3. GET  /v1.0/doc/documents/{documentId}                          文档基本信息(标题)
//	4. GET  /v1.0/doc/documents/{documentId}/blocks                   文档块数据(块 content 为 HTML 片段)
//
// 权限与限制(注释注明):
//   - 企业内部应用需开通「云盘/文档」相关权限;storage/dentries 系列为重度接口,QPS 限制 2 次/秒,
//     且在企业内开通可能需要管理员审批——未开通时接口返回 403/权限类错误,本实现原样上抛;
//   - 请求头统一 x-acs-dingtalk-access-token;operatorId(操作人 unionId)在部分场景必填,
//     由 DINGTALK_UNION_ID 提供(可选);
//   - 钉盘条目(dentry)只有「钉钉在线文档」才有文档内容(文档接口的 documentId 即 dentryUuid);
//     上传的本地文件(pdf/docx 等)没有文档内容,Fetch 明确报错。
//
// Markdown 化策略:块数据 blockType 决定结构(段落/标题/列表/引用/代码/待办),
// 块 content 为 HTML 片段,用内置轻量 HTML→Markdown 转换(htmlInlineToMarkdown)处理行内内容。

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"
)

const dingtalkDefaultEndpoint = "https://api.dingtalk.com"

// DingTalkProvider 钉钉文档 provider。
type DingTalkProvider struct {
	cfg    DingTalkConfig
	client *apiClient
	tokens *tokenCache // AccessToken 直接给定时缓存退化为透传
}

// NewDingTalkProvider 构造 dingtalk provider。
func NewDingTalkProvider(cfg DingTalkConfig) *DingTalkProvider {
	p := &DingTalkProvider{
		cfg:    cfg,
		client: newAPIClient(firstNonEmpty(cfg.Endpoint, dingtalkDefaultEndpoint), cfg.TimeoutSec),
	}
	if cfg.AccessToken != "" {
		// 直接持有 accessToken:refresh 直接透传,有效期给 24h(实际不再触发)。
		tok := cfg.AccessToken
		p.tokens = &tokenCache{refresh: func(context.Context) (string, int64, error) { return tok, 86400, nil }}
	} else {
		p.tokens = &tokenCache{refresh: p.refreshToken}
	}
	return p
}

// Name 实现 Provider。
func (p *DingTalkProvider) Name() string { return "dingtalk" }

// refreshToken 企业内部应用 accessToken。
func (p *DingTalkProvider) refreshToken(ctx context.Context) (string, int64, error) {
	var out struct {
		AccessToken string `json:"accessToken"`
		ExpireIn    int64  `json:"expireIn"`
	}
	err := apiJSON(ctx, p.client.hc, "POST", p.client.base+"/v1.0/oauth2/accessToken", nil, map[string]string{
		"appKey":    p.cfg.AppKey,
		"appSecret": p.cfg.AppSecret,
	}, &out)
	if err != nil {
		return "", 0, fmt.Errorf("knowledge/dingtalk: 获取 accessToken: %w", err)
	}
	if out.AccessToken == "" {
		return "", 0, fmt.Errorf("knowledge/dingtalk: 获取 accessToken 失败: 响应无 token(检查 appKey/appSecret)")
	}
	return out.AccessToken, out.ExpireIn, nil
}

// authHeader 返回 API 请求头。
func (p *DingTalkProvider) authHeader(ctx context.Context) (map[string]string, error) {
	tok, err := p.tokens.Token(ctx)
	if err != nil {
		return nil, err
	}
	return map[string]string{"x-acs-dingtalk-access-token": tok}, nil
}

// dingDentry 钉盘条目(字段名取 v2.0 storage 接口口径,type 兼容部分版本)。
type dingDentry struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	DentryType  string `json:"dentryType"` // folder / file
	Type        string `json:"type"`       // 部分版本用该字段
	MimeType    string `json:"mimeType"`
	ModifiedAt  string `json:"modifiedAt"`
	ModifiedAt2 string `json:"modifiedTime"` // 兼容字段
	CreatedAt   string `json:"createdAt"`
}

// isFolder 判断是否文件夹。
func (d dingDentry) isFolder() bool {
	t := strings.ToLower(firstNonEmpty(d.DentryType, d.Type))
	return t == "folder" || t == "dir"
}

// updatedAt 解析更新时间(RFC3339 优先,数字毫秒兜底)。
func (d dingDentry) updatedAt() time.Time {
	for _, v := range []string{d.ModifiedAt, d.ModifiedAt2, d.CreatedAt} {
		if v == "" {
			continue
		}
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			return t
		}
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 0 {
			return time.UnixMilli(n)
		}
	}
	return time.Time{}
}

// List 实现 Provider:列出 SpaceId/FolderId 下文件(分页;接口限 QPS=2,分页串行即天然限速)。
func (p *DingTalkProvider) List(ctx context.Context) ([]DocumentMeta, error) {
	if p.cfg.SpaceId == "" {
		return nil, fmt.Errorf("knowledge/dingtalk: 未配置 DINGTALK_SPACE_ID,无法列钉盘文件")
	}
	folder := firstNonEmpty(p.cfg.FolderId, "0") // 根目录约定 id=0
	var out []DocumentMeta
	nextToken := ""
	for {
		header, err := p.authHeader(ctx)
		if err != nil {
			return nil, err
		}
		body := map[string]any{
			"spaceId":        p.cfg.SpaceId,
			"parentDentryId": folder,
			"maxResults":     200,
			"orderBy":        "modifiedTime",
			"orderType":      "DESC",
		}
		if p.cfg.UnionId != "" {
			body["unionId"] = p.cfg.UnionId
		}
		if nextToken != "" {
			body["nextToken"] = nextToken
		}
		var resp struct {
			Result struct {
				Dentries  []dingDentry `json:"dentries"`
				NextToken string       `json:"nextToken"`
			} `json:"result"`
			Dentries  []dingDentry `json:"dentries"` // 兼容无 result 包裹的形态
			NextToken string       `json:"nextToken"`
		}
		url := p.client.base + "/v2.0/storage/dentries/lists"
		if err := apiJSON(ctx, p.client.hc, "POST", url, header, body, &resp); err != nil {
			return nil, fmt.Errorf("knowledge/dingtalk: 列钉盘文件: %w(确认应用已开通云盘 storage 只读权限;该接口可能需要企业内审批)", err)
		}
		list := resp.Result.Dentries
		nt := resp.Result.NextToken
		if len(list) == 0 {
			list = resp.Dentries
			nt = firstNonEmpty(resp.NextToken, nt)
		}
		for _, d := range list {
			if d.isFolder() || d.ID == "" {
				continue
			}
			out = append(out, DocumentMeta{
				ID:        d.ID,
				Title:     d.Name,
				Space:     p.cfg.SpaceId,
				UpdatedAt: d.updatedAt(),
			})
		}
		if nt == "" {
			return out, nil
		}
		nextToken = nt
	}
}

// dingBlock 文档块。
type dingBlock struct {
	BlockID   string `json:"blockId"`
	BlockType string `json:"blockType"` // paragraph/heading1-9/bulletList/orderedList/codeBlock/quote/todoList/image/table/...
	Content   string `json:"content"`   // HTML 片段
	Checked   *bool  `json:"checked"`   // todoList 勾选态(部分版本才有)
}

// Fetch 实现 Provider:钉钉在线文档 → Markdown。
func (p *DingTalkProvider) Fetch(ctx context.Context, id string) (*Document, error) {
	header, err := p.authHeader(ctx)
	if err != nil {
		return nil, err
	}
	op := p.operatorQuery()

	// 1. 文档基本信息(标题)。
	title := id
	var info struct {
		Title  string `json:"title"`
		Result struct {
			Title string `json:"title"`
		} `json:"result"`
		Name string `json:"name"`
	}
	infoURL := p.client.base + "/v1.0/doc/documents/" + id + op
	if err := apiJSON(ctx, p.client.hc, "GET", infoURL, header, nil, &info); err != nil {
		return nil, fmt.Errorf("knowledge/dingtalk: 取文档信息 %s: %w(仅在线文档有内容;上传的本地文件不支持)", id, err)
	}
	title = firstNonEmpty(info.Title, info.Result.Title, info.Name, id)

	// 2. 分页拉块。
	var blocks []dingBlock
	nextToken := ""
	for {
		url := p.client.base + "/v1.0/doc/documents/" + id + "/blocks?maxResults=100" + op
		if nextToken != "" {
			url += "&nextToken=" + nextToken
		}
		var resp struct {
			Result struct {
				Blocks    []dingBlock `json:"blocks"`
				NextToken string      `json:"nextToken"`
			} `json:"result"`
			Blocks    []dingBlock `json:"blocks"`
			NextToken string      `json:"nextToken"`
			Content   string      `json:"content"` // 兼容:部分形态直接回整篇 HTML
		}
		if err := apiJSON(ctx, p.client.hc, "GET", url, header, nil, &resp); err != nil {
			return nil, fmt.Errorf("knowledge/dingtalk: 拉取文档块 %s: %w", id, err)
		}
		if resp.Content != "" && len(resp.Result.Blocks) == 0 && len(resp.Blocks) == 0 {
			// 整篇 HTML 兼容形态:按整体转 Markdown。
			md := tidyMarkdown(htmlBlockToMarkdown(resp.Content))
			return p.buildDoc(id, title, md, "content"), nil
		}
		blocks = append(blocks, resp.Result.Blocks...)
		if len(resp.Result.Blocks) == 0 {
			blocks = append(blocks, resp.Blocks...)
			nextToken = firstNonEmpty(resp.NextToken, resp.Result.NextToken)
		} else {
			nextToken = resp.Result.NextToken
		}
		if nextToken == "" {
			break
		}
	}

	var sb strings.Builder
	sb.WriteString("# " + title + "\n\n")
	prevKind := ""
	orderedNum := 0
	for _, b := range blocks {
		kind := dingListKind(b.BlockType)
		if kind == "ordered" {
			if prevKind == kind {
				orderedNum++
			} else {
				orderedNum = 1
			}
		} else {
			orderedNum = 0
		}
		text := dingBlockToMarkdown(b, orderedNum)
		if text == "" {
			continue
		}
		if sb.Len() > 0 {
			if kind != "" && kind == prevKind {
				sb.WriteString("\n")
			} else {
				sb.WriteString("\n\n")
			}
		}
		sb.WriteString(strings.TrimLeft(text, "\n"))
		prevKind = kind
	}
	return p.buildDoc(id, title, tidyMarkdown(sb.String()), "blocks"), nil
}

// dingListKind 块类型 → 列表种类名(空串=非列表)。
func dingListKind(blockType string) string {
	switch blockType {
	case "bulletList", "bullet":
		return "bullet"
	case "orderedList", "ordered":
		return "ordered"
	case "todoList", "todo":
		return "todo"
	default:
		return ""
	}
}

// buildDoc 组装 Document。
func (p *DingTalkProvider) buildDoc(id, title, content, source string) *Document {
	return &Document{
		ID:      id,
		Title:   title,
		Space:   p.cfg.SpaceId,
		Content: []byte(content),
		Meta: map[string]string{
			"document_id":    id,
			"content_source": source,
		},
	}
}

// operatorQuery 拼 operatorId 查询串(配置了 UnionId 才带)。
func (p *DingTalkProvider) operatorQuery() string {
	if p.cfg.UnionId == "" {
		return ""
	}
	return "&operatorId=" + p.cfg.UnionId
}

// dingBlockToMarkdown 单块 → Markdown(orderedNum 为连续有序列表计数,非有序块传 0)。
func dingBlockToMarkdown(b dingBlock, orderedNum int) string {
	inner := htmlInlineToMarkdown(b.Content)
	inner = strings.TrimSpace(inner)
	switch b.BlockType {
	case "paragraph", "text":
		return inner
	case "heading1", "heading2", "heading3", "heading4", "heading5",
		"heading6", "heading7", "heading8", "heading9":
		return strings.Repeat("#", dingHeadingLevel(b.BlockType)) + " " + inner
	case "bulletList", "bullet":
		return "- " + inner
	case "orderedList", "ordered":
		n := orderedNum
		if n <= 0 {
			n = 1
		}
		return fmt.Sprintf("%d. %s", n, inner)
	case "todoList", "todo":
		mark := "[ ]"
		if b.Checked != nil && *b.Checked {
			mark = "[x]"
		}
		return "- " + mark + " " + inner
	case "codeBlock", "code":
		return "```\n" + htmlCodeText(b.Content) + "\n```"
	case "quote", "blockquote":
		return "> " + inner
	case "image":
		if src, alt := htmlImg(b.Content); src != "" {
			return "![" + alt + "](" + src + ")"
		}
		return "[图片]"
	case "table":
		return "<!-- 钉钉表格块:暂不支持 Markdown 化,内容略 -->"
	case "":
		return inner
	default:
		if inner == "" {
			return ""
		}
		return fmt.Sprintf("<!-- 未支持的钉钉块类型: %s -->\n%s", b.BlockType, inner)
	}
}

// dingHeadingLevel headingN → N。
func dingHeadingLevel(t string) int {
	n := strings.TrimPrefix(t, "heading")
	v, err := strconv.Atoi(n)
	if err != nil || v < 1 {
		return 1
	}
	if v > 6 {
		return 6 // Markdown 只有 6 级标题
	}
	return v
}
