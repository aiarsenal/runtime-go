package knowledge

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// 本文件是各 provider 共用的轻量 HTTP 工具:JSON 请求/响应、带状态码的错误包装、
// token 缓存。全部基于标准库 net/http,零第三方依赖。

const defaultTimeoutSec = 30

// newHTTPClient 按 TimeoutSec(0=默认 30s)构造 http.Client。
func newHTTPClient(timeoutSec int) *http.Client {
	if timeoutSec <= 0 {
		timeoutSec = defaultTimeoutSec
	}
	return &http.Client{Timeout: time.Duration(timeoutSec) * time.Second}
}

// apiJSON 执行一次 JSON API 调用:body 非 nil 时序列化为请求体;响应 2xx 时解析 JSON 到 out。
// 非 2xx 返回含状态码与响应体前 512 字节的错误,便于定位平台侧报错。
func apiJSON(ctx context.Context, client *http.Client, method, url string, header map[string]string, body any, out any) error {
	var rdr io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("knowledge: 序列化请求体失败: %w", err)
		}
		rdr = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, rdr)
	if err != nil {
		return fmt.Errorf("knowledge: 构造请求失败: %w", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json; charset=utf-8")
	}
	for k, v := range header {
		req.Header.Set(k, v)
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("knowledge: %s %s 请求失败: %w", method, url, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20)) // 32MB 上限,防异常大响应
	if err != nil {
		return fmt.Errorf("knowledge: %s %s 读响应失败: %w", method, url, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("knowledge: %s %s 返回 HTTP %d: %s", method, url, resp.StatusCode, excerpt(data))
	}
	if out == nil || len(data) == 0 {
		return nil
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("knowledge: %s %s 解析响应 JSON 失败: %w(原文: %s)", method, url, err, excerpt(data))
	}
	return nil
}

// apiRaw 执行一次请求并返回原始响应体(下载导出文件用)。
func apiRaw(ctx context.Context, client *http.Client, method, url string, header map[string]string, body []byte) ([]byte, error) {
	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, rdr)
	if err != nil {
		return nil, fmt.Errorf("knowledge: 构造请求失败: %w", err)
	}
	for k, v := range header {
		req.Header.Set(k, v)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("knowledge: %s %s 请求失败: %w", method, url, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 128<<20)) // 128MB 上限
	if err != nil {
		return nil, fmt.Errorf("knowledge: %s %s 读响应失败: %w", method, url, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("knowledge: %s %s 返回 HTTP %d: %s", method, url, resp.StatusCode, excerpt(data))
	}
	return data, nil
}

// excerpt 截取响应体前 512 字节做错误摘录,换行压成空格。
func excerpt(data []byte) string {
	s := strings.ReplaceAll(string(data), "\n", " ")
	s = strings.TrimSpace(s)
	if len(s) > 512 {
		return s[:512] + "..."
	}
	return s
}

// apiClient 是 base URL + http.Client 的组合,各 provider 共用,避免重复写 URL 拼接。
type apiClient struct {
	hc   *http.Client
	base string // 已去尾部 /
}

// tokenCache 平台 access token 的线程安全缓存:过期前 60s 视为失效,过期后调 refresh 重取。
// refresh 返回 (token, 有效期秒数)。
type tokenCache struct {
	mu      sync.Mutex
	token   string
	expires time.Time
	refresh func(ctx context.Context) (string, int64, error)
}

func (t *tokenCache) Token(ctx context.Context) (string, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.token != "" && time.Now().Before(t.expires.Add(-60*time.Second)) {
		return t.token, nil
	}
	tok, ttl, err := t.refresh(ctx)
	if err != nil {
		return "", err
	}
	if ttl <= 0 {
		ttl = 7200
	}
	t.token = tok
	t.expires = time.Now().Add(time.Duration(ttl) * time.Second)
	return tok, nil
}

// firstNonEmpty 返回第一个非空串。
func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
