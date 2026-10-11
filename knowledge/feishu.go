package knowledge

// 飞书(Lark)Provider。
//
// 所依据的开放平台 API(均为 open.feishu.cn 开放平台 v1 稳定版接口,自建应用 tenant_access_token 鉴权):
//
//	1. POST /open-apis/auth/v3/tenant_access_token/internal        自建应用获取 tenant_access_token
//	2. GET  /open-apis/drive/v1/files?folder_token=...            云空间:列出文件夹内文件(分页)
//	3. GET  /open-apis/docx/v1/documents/:document_id             文档元信息(标题)
//	4. GET  /open-apis/docx/v1/documents/:document_id/blocks      文档块结构(分页,page_size≤500)
//	5. GET  /open-apis/docx/v1/documents/:document_id/raw_content 文档纯文本(兜底,无块类型信息)
//
// 权限与前置条件(注释注明,调用失败多为这几类):
//   - 应用需开通「云文档」只读权限(drive:drive:readonly / docx:document:readonly 等 scope);
//   - 目标文件夹需把应用添加为协作者,否则 files 接口返回 1063/230002 类错误;
//   - 旧版文档(type=doc)走 /doc/v2 系列接口,本实现不支持,Fetch 明确报错;仅支持新版 docx。
//
// Markdown 化策略:raw_content 只返回纯文本、不含标题/列表层级,因此以 blocks 接口
// (块类型 + 富文本元素)为主渲染标题/列表/代码/引用/待办/高亮块,blocks 拉取失败时
// 回退 raw_content 纯文本。

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"
)

const feishuDefaultEndpoint = "https://open.feishu.cn"

// FeishuProvider 飞书文档 provider。
type FeishuProvider struct {
	cfg    FeishuConfig
	client *apiClient
	tokens *tokenCache
}

// NewFeishuProvider 构造 feishu provider。
func NewFeishuProvider(cfg FeishuConfig) *FeishuProvider {
	p := &FeishuProvider{
		cfg:    cfg,
		client: newAPIClient(firstNonEmpty(cfg.Endpoint, feishuDefaultEndpoint), cfg.TimeoutSec),
	}
	p.tokens = &tokenCache{refresh: p.refreshToken}
	return p
}

// Name 实现 Provider。
func (p *FeishuProvider) Name() string { return "feishu" }

// newAPIClient 构造 apiClient(base 去尾部 /)。
func newAPIClient(base string, timeoutSec int) *apiClient {
	return &apiClient{hc: newHTTPClient(timeoutSec), base: strings.TrimSuffix(base, "/")}
}

// refreshToken 取 tenant_access_token(有效期 expire 秒)。
func (p *FeishuProvider) refreshToken(ctx context.Context) (string, int64, error) {
	var out struct {
		Code              int    `json:"code"`
		Msg               string `json:"msg"`
		TenantAccessToken string `json:"tenant_access_token"`
		Expire            int64  `json:"expire"`
	}
	err := apiJSON(ctx, p.client.hc, "POST", p.client.base+"/open-apis/auth/v3/tenant_access_token/internal", nil, map[string]string{
		"app_id":     p.cfg.AppID,
		"app_secret": p.cfg.AppSecret,
	}, &out)
	if err != nil {
		return "", 0, fmt.Errorf("knowledge/feishu: 获取 tenant_access_token: %w", err)
	}
	if out.Code != 0 {
		return "", 0, fmt.Errorf("knowledge/feishu: 获取 tenant_access_token 失败: code=%d msg=%s(检查 app_id/app_secret 与应用权限)", out.Code, out.Msg)
	}
	if out.TenantAccessToken == "" {
		return "", 0, fmt.Errorf("knowledge/feishu: 获取 tenant_access_token 失败: 响应无 token")
	}
	return out.TenantAccessToken, out.Expire, nil
}

// authHeader 返回带 Bearer token 的请求头。
func (p *FeishuProvider) authHeader(ctx context.Context) (map[string]string, error) {
	tok, err := p.tokens.Token(ctx)
	if err != nil {
		return nil, err
	}
	return map[string]string{"Authorization": "Bearer " + tok}, nil
}

// feishuResp 飞书开放平台响应外壳:code=0 成功;业务数据在顶层 data 对象内。
type feishuResp struct {
	Code int    `json:"code"`
	Msg  string `json:"msg"`
}

// List 实现 Provider:列出 FolderToken(必填)下的文档,RecurseDepth>0 时递归子文件夹。
func (p *FeishuProvider) List(ctx context.Context) ([]DocumentMeta, error) {
	if p.cfg.FolderToken == "" {
		return nil, fmt.Errorf("knowledge/feishu: 未配置 FEISHU_FOLDER_TOKEN,无法列文件夹")
	}
	var out []DocumentMeta
	seen := map[string]bool{}
	if err := p.listFolder(ctx, p.cfg.FolderToken, 0, &out, seen); err != nil {
		return nil, err
	}
	return out, nil
}

// listFolder 递归列文件夹(单层只含文件;folder 类型按 RecurseDepth 展开)。
func (p *FeishuProvider) listFolder(ctx context.Context, folderToken string, depth int, out *[]DocumentMeta, seen map[string]bool) error {
	if seen[folderToken] {
		return nil // 防环
	}
	seen[folderToken] = true
	pageToken := ""
	for {
		header, err := p.authHeader(ctx)
		if err != nil {
			return err
		}
		url := p.client.base + "/open-apis/drive/v1/files?folder_token=" + folderToken + "&page_size=50"
		if pageToken != "" {
			url += "&page_token=" + pageToken
		}
		var resp struct {
			feishuResp
			Data struct {
				Files []struct {
					Name         string `json:"name"`
					Token        string `json:"token"`
					Type         string `json:"type"`
					URL          string `json:"url"`
					OwnerID      string `json:"owner_id"`
					CreatedTime  string `json:"created_time"`
					ModifiedTime string `json:"modified_time"`
				} `json:"files"`
				HasMore   bool   `json:"has_more"`
				PageToken string `json:"page_token"`
			} `json:"data"`
		}
		if err := apiJSON(ctx, p.client.hc, "GET", url, header, nil, &resp); err != nil {
			return fmt.Errorf("knowledge/feishu: 列文件夹 %s: %w(确认应用已被添加为该文件夹协作者且有云文档只读权限)", folderToken, err)
		}
		if resp.Code != 0 {
			return fmt.Errorf("knowledge/feishu: 列文件夹 %s 失败: code=%d msg=%s", folderToken, resp.Code, resp.Msg)
		}
		for _, f := range resp.Data.Files {
			if f.Type == "folder" {
				if depth < p.cfg.RecurseDepth {
					if err := p.listFolder(ctx, f.Token, depth+1, out, seen); err != nil {
						return err
					}
				}
				continue
			}
			*out = append(*out, DocumentMeta{
				ID:        f.Token,
				Title:     f.Name,
				Space:     folderToken,
				UpdatedAt: feishuMsTime(f.ModifiedTime),
			})
		}
		if !resp.Data.HasMore || resp.Data.PageToken == "" {
			return nil
		}
		pageToken = resp.Data.PageToken
	}
}

// feishuMsTime 解析飞书毫秒时间戳字符串。
func feishuMsTime(ms string) time.Time {
	n, err := strconv.ParseInt(ms, 10, 64)
	if err != nil || n <= 0 {
		return time.Time{}
	}
	return time.UnixMilli(n)
}

// Fetch 实现 Provider:docx 文档 → Markdown。
func (p *FeishuProvider) Fetch(ctx context.Context, id string) (*Document, error) {
	header, err := p.authHeader(ctx)
	if err != nil {
		return nil, err
	}
	// 1. 文档元信息(标题);code!=0 只在致命类型不符时才失败——这里按响应判断。
	title := id
	var meta struct {
		feishuResp
		Data struct {
			Document struct {
				DocumentID string `json:"document_id"`
				Title      string `json:"title"`
			} `json:"document"`
		} `json:"data"`
	}
	metaURL := p.client.base + "/open-apis/docx/v1/documents/" + id
	if err := apiJSON(ctx, p.client.hc, "GET", metaURL, header, nil, &meta); err != nil {
		return nil, fmt.Errorf("knowledge/feishu: 取文档元信息 %s: %w(仅支持新版 docx;旧版 doc/表格等类型不支持)", id, err)
	}
	if meta.Code != 0 {
		return nil, fmt.Errorf("knowledge/feishu: 取文档元信息 %s 失败: code=%d msg=%s(仅支持新版 docx)", id, meta.Code, meta.Msg)
	}
	if meta.Data.Document.Title != "" {
		title = meta.Data.Document.Title
	}

	// 2. 块结构 → Markdown;blocks 拉取失败回退 raw_content 纯文本。
	source := "blocks"
	var content string
	blocks, berr := p.fetchBlocks(ctx, header, id)
	if berr == nil {
		content = feishuBlocksToMarkdown(blocks, title)
	} else {
		source = "raw_content"
		content, err = p.fetchRawContent(ctx, header, id)
		if err != nil {
			return nil, fmt.Errorf("knowledge/feishu: 拉取文档 %s 失败(blocks: %v;%v)", id, berr, err)
		}
	}

	doc := &Document{
		ID:      id,
		Title:   title,
		Space:   p.cfg.FolderToken,
		Content: []byte(content),
		Meta: map[string]string{
			"url":            p.client.base + "/docx/" + id,
			"content_source": source,
		},
	}
	if berr == nil {
		doc.Meta["block_count"] = strconv.Itoa(len(blocks))
	}
	return doc, nil
}

// fetchRawContent 兜底:拉取文档纯文本并按段落 Markdown 化。
func (p *FeishuProvider) fetchRawContent(ctx context.Context, header map[string]string, docID string) (string, error) {
	var resp struct {
		feishuResp
		Data struct {
			Content string `json:"content"`
		} `json:"data"`
	}
	url := p.client.base + "/open-apis/docx/v1/documents/" + docID + "/raw_content"
	if err := apiJSON(ctx, p.client.hc, "GET", url, header, nil, &resp); err != nil {
		return "", fmt.Errorf("raw_content: %w", err)
	}
	if resp.Code != 0 {
		return "", fmt.Errorf("raw_content: code=%d msg=%s", resp.Code, resp.Msg)
	}
	return feishuRawContentToMarkdown(resp.Data.Content, ""), nil
}

// fetchBlocks 分页拉取文档全部块。
func (p *FeishuProvider) fetchBlocks(ctx context.Context, header map[string]string, docID string) ([]feishuBlock, error) {
	var all []feishuBlock
	pageToken := ""
	for {
		url := p.client.base + "/open-apis/docx/v1/documents/" + docID + "/blocks?page_size=500"
		if pageToken != "" {
			url += "&page_token=" + pageToken
		}
		var resp struct {
			feishuResp
			Data struct {
				Items     []feishuBlock `json:"items"`
				HasMore   bool          `json:"has_more"`
				PageToken string        `json:"page_token"`
			} `json:"data"`
		}
		if err := apiJSON(ctx, p.client.hc, "GET", url, header, nil, &resp); err != nil {
			return nil, err
		}
		if resp.Code != 0 {
			return nil, fmt.Errorf("blocks: code=%d msg=%s", resp.Code, resp.Msg)
		}
		all = append(all, resp.Data.Items...)
		if !resp.Data.HasMore || resp.Data.PageToken == "" {
			return all, nil
		}
		pageToken = resp.Data.PageToken
	}
}
