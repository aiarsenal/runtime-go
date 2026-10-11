package knowledge

// 腾讯文档 Provider。
//
// 所依据的开放平台 API(docs.qq.com/open,OAuth2 用户 access_token,请求头 Access-Token):
//
//	1. GET  /openapi/drive/v2/files?folder_id=&page_size=&page_token=          文件列表(分页)
//	2. GET  /openapi/drive/v2/files/{fileID}/metadata                          文件元信息(标题)
//	3. POST /openapi/drive/v2/files/{fileID}/export                            发起异步导出任务
//	                                                                           exportType:1 原文 2 docx 3 xlsx 4 csv 5 pdf
//	4. GET  /openapi/drive/v2/files/{fileID}/export/operation/{operationId}    轮询导出任务,完成后返回下载 URL
//	5. GET  <下载 URL>                                                         取 docx 二进制 → 内置 OOXML 提取器转 Markdown
//
// 能力受限(诚实说明,调用失败请优先对照本节):
//   - 导出是异步任务且按文档类型/应用权限受限:旧版 doc、部分企业空间文件可能无导出权限,
//     失败时平台错误原样上抛;
//   - 本实现取 exportType=2(docx),用标准库 zip+xml 做 OOXML 提取,仅保留标题/段落/列表/
//     基础表格结构,图片与富样式降级(见 tencentdoc_docx.go);
//   - 仅能访问 access_token 对应授权范围内的文件,接口有 QPS 限流。

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"
)

const tdocDefaultEndpoint = "https://docs.qq.com"

// TencentDocProvider 腾讯文档 provider。
type TencentDocProvider struct {
	cfg    TencentDocConfig
	client *apiClient
}

// NewTencentDocProvider 构造 tdoc provider。
func NewTencentDocProvider(cfg TencentDocConfig) *TencentDocProvider {
	return &TencentDocProvider{
		cfg:    cfg,
		client: newAPIClient(firstNonEmpty(cfg.Endpoint, tdocDefaultEndpoint), cfg.TimeoutSec),
	}
}

// Name 实现 Provider。
func (p *TencentDocProvider) Name() string { return "tdoc" }

// authHeader 返回带 Access-Token 的请求头。
func (p *TencentDocProvider) authHeader() map[string]string {
	return map[string]string{"Access-Token": p.cfg.AccessToken}
}

// List 实现 Provider:列出 FolderId(空=根目录)下的文件。
func (p *TencentDocProvider) List(ctx context.Context) ([]DocumentMeta, error) {
	var out []DocumentMeta
	pageToken := ""
	for {
		url := p.client.base + "/openapi/drive/v2/files?page_size=100"
		if p.cfg.FolderId != "" {
			url += "&folder_id=" + p.cfg.FolderId
		}
		if pageToken != "" {
			url += "&page_token=" + pageToken
		}
		var resp struct {
			Files         []tdocFile `json:"files"`
			NextPageToken string     `json:"next_page_token"`
			Data          struct {
				Files         []tdocFile `json:"files"`
				NextPageToken string     `json:"next_page_token"`
			} `json:"data"`
		}
		if err := apiJSON(ctx, p.client.hc, "GET", url, p.authHeader(), nil, &resp); err != nil {
			return nil, fmt.Errorf("knowledge/tdoc: 列文件: %w(确认 access_token 有效且已授权云端文档读取 scope)", err)
		}
		files, nt := resp.Files, resp.NextPageToken
		if len(files) == 0 {
			files = resp.Data.Files
			nt = firstNonEmpty(resp.Data.NextPageToken, nt)
		}
		for _, f := range files {
			if f.Type == "folder" || f.ID == "" {
				continue
			}
			out = append(out, DocumentMeta{
				ID:        f.ID,
				Title:     firstNonEmpty(f.Title, f.Name, f.ID),
				Space:     firstNonEmpty(p.cfg.FolderId, "root"),
				UpdatedAt: f.updatedAt(),
			})
		}
		if nt == "" {
			return out, nil
		}
		pageToken = nt
	}
}

// tdocFile 文件条目(字段名做多形态兼容)。
type tdocFile struct {
	ID           string `json:"id"`
	Title        string `json:"title"`
	Name         string `json:"name"`
	Type         string `json:"type"` // doc / sheet / slide / smart_sheet / folder / file ...
	URL          string `json:"url"`
	ModifyTime   string `json:"modifyTime"`
	ModifiedTime string `json:"modified_time"`
	ModifyTime2  string `json:"modify_time"`
	CreateTime   string `json:"createTime"`
}

// updatedAt 解析更新时间(RFC3339 / 数字秒或毫秒)。
func (f tdocFile) updatedAt() time.Time {
	for _, v := range []string{f.ModifyTime, f.ModifiedTime, f.ModifyTime2, f.CreateTime} {
		if v == "" {
			continue
		}
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			return t
		}
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 0 {
			if n > 1e12 { // 毫秒
				return time.UnixMilli(n)
			}
			return time.Unix(n, 0)
		}
	}
	return time.Time{}
}

// Fetch 实现 Provider:发起 docx 导出 → 轮询 → 下载 → OOXML 转 Markdown。
func (p *TencentDocProvider) Fetch(ctx context.Context, id string) (*Document, error) {
	// 0. 元信息(标题;失败不致命)。
	title := id
	var meta struct {
		Data struct {
			File struct {
				Title string `json:"title"`
				Name  string `json:"name"`
			} `json:"file"`
			Title string `json:"title"`
		} `json:"data"`
		Title string `json:"title"`
	}
	metaURL := p.client.base + "/openapi/drive/v2/files/" + id + "/metadata"
	if err := apiJSON(ctx, p.client.hc, "GET", metaURL, p.authHeader(), nil, &meta); err == nil {
		title = firstNonEmpty(meta.Data.File.Title, meta.Data.File.Name, meta.Data.Title, id)
	}

	// 1. 发起导出任务(exportType=2 → docx)。
	var start struct {
		Data struct {
			OperationID string `json:"operationId"`
		} `json:"data"`
		OperationID string `json:"operationId"`
	}
	startURL := p.client.base + "/openapi/drive/v2/files/" + id + "/export"
	if err := apiJSON(ctx, p.client.hc, "POST", startURL, p.authHeader(), map[string]int{"exportType": 2}, &start); err != nil {
		return nil, fmt.Errorf("knowledge/tdoc: 发起导出 %s: %w(该类型/权限可能不支持导出)", id, err)
	}
	opID := firstNonEmpty(start.Data.OperationID, start.OperationID)
	if opID == "" {
		return nil, fmt.Errorf("knowledge/tdoc: 发起导出 %s: 响应无 operationId", id)
	}

	// 2. 轮询任务直到完成(默认上限 90s,间隔 1.5s)。
	dlURL, err := p.pollExport(ctx, id, opID)
	if err != nil {
		return nil, err
	}

	// 3. 下载 docx。
	raw, err := apiRaw(ctx, p.client.hc, "GET", dlURL, nil, nil)
	if err != nil {
		return nil, fmt.Errorf("knowledge/tdoc: 下载导出文件 %s: %w", id, err)
	}

	// 4. OOXML → Markdown。
	md, err := docxToMarkdown(raw, title)
	if err != nil {
		return nil, fmt.Errorf("knowledge/tdoc: 解析导出的 docx %s: %w", id, err)
	}
	return &Document{
		ID:      id,
		Title:   title,
		Space:   firstNonEmpty(p.cfg.FolderId, "root"),
		Content: []byte(md),
		Meta: map[string]string{
			"url":            firstNonEmpty(dlURL, "export:"+opID),
			"content_source": "export_docx",
		},
	}, nil
}

// pollExport 轮询导出任务,返回下载 URL。
func (p *TencentDocProvider) pollExport(ctx context.Context, id, opID string) (string, error) {
	timeout := time.Duration(p.cfg.ExportTimeoutSec) * time.Second
	if timeout <= 0 {
		timeout = 90 * time.Second
	}
	deadline := time.Now().Add(timeout)
	pollURL := p.client.base + "/openapi/drive/v2/files/" + id + "/export/operation/" + opID
	for {
		var resp struct {
			Data struct {
				OperationState string `json:"operationState"`
				State          string `json:"state"`
				URL            string `json:"url"`
				DownloadURL    string `json:"downloadUrl"`
				FileURL        string `json:"fileUrl"`
			} `json:"data"`
			OperationState string `json:"operationState"`
			State          string `json:"state"`
			URL            string `json:"url"`
			DownloadURL    string `json:"downloadUrl"`
			FileURL        string `json:"fileUrl"`
		}
		if err := apiJSON(ctx, p.client.hc, "GET", pollURL, p.authHeader(), nil, &resp); err != nil {
			return "", fmt.Errorf("knowledge/tdoc: 轮询导出任务 %s: %w", opID, err)
		}
		state := strings.ToLower(firstNonEmpty(resp.Data.OperationState, resp.Data.State, resp.OperationState, resp.State))
		dl := firstNonEmpty(resp.Data.URL, resp.Data.DownloadURL, resp.Data.FileURL, resp.URL, resp.DownloadURL, resp.FileURL)
		switch {
		case dl != "" && (state == "" || tdocStateDone(state)):
			return dl, nil
		case tdocStateFailed(state):
			return "", fmt.Errorf("knowledge/tdoc: 导出任务 %s 失败: state=%s(检查文档类型/导出权限)", opID, state)
		}
		if time.Now().After(deadline) {
			return "", fmt.Errorf("knowledge/tdoc: 导出任务 %s 轮询超时(%s),可调大 TDOC 导出超时", opID, state)
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(1500 * time.Millisecond):
		}
	}
}

// tdocStateDone 判断导出任务终态为成功(状态字面量做多形态兼容)。
func tdocStateDone(state string) bool {
	for _, kw := range []string{"success", "finish", "complete", "done", "ok"} {
		if strings.Contains(state, kw) {
			return true
		}
	}
	return false
}

// tdocStateFailed 判断导出任务终态为失败。
func tdocStateFailed(state string) bool {
	for _, kw := range []string{"fail", "error", "cancel", "reject"} {
		if strings.Contains(state, kw) {
			return true
		}
	}
	return false
}
