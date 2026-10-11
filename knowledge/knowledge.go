// Package knowledge 提供「知识生态接入」:从飞书 / 钉钉 / 腾讯文档 / Notion 四个平台
// 拉取文档清单与正文,统一转成 Markdown。
//
// 设计与 runtime/storage 一致:Provider 接口 + 各平台独立实现 + Manager 统一入口。
// 各 provider 均为纯 net/http 实现,零第三方 SDK 依赖;所依据的开放平台端点与版本
// 写在各文件头部注释。
package knowledge

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"
)

// DocumentMeta 文档清单条目(List 返回)。
type DocumentMeta struct {
	ID        string    // 平台侧文档 id(fetch --id 用)
	Title     string    // 标题(可能为空,拉取时兜底)
	Space     string    // 所在空间标识:飞书 folder_token / 钉钉 spaceId / 腾讯文档 folder_id / Notion parent 描述
	UpdatedAt time.Time // 平台侧最近更新时间(取不到为零值)
}

// Document 拉取到的完整文档。Content 一律为 UTF-8 Markdown 文本。
type Document struct {
	ID        string
	Title     string
	Space     string
	UpdatedAt time.Time
	Content   []byte
	Meta      map[string]string // provider 特有信息:url / block 数 / 内容来源等
}

// Provider 是各知识平台 adapter 必须实现的统一接口。
type Provider interface {
	// Name 返回 provider 标识:feishu / dingtalk / tdoc / notion。
	Name() string
	// List 列出可拉取的文档清单。
	List(ctx context.Context) ([]DocumentMeta, error)
	// Fetch 按 id 拉取单篇文档正文(Markdown)。
	Fetch(ctx context.Context, id string) (*Document, error)
}

// ---- 配置 ----

// FeishuConfig 飞书(自建应用)。
type FeishuConfig struct {
	AppID       string `json:"app_id"`
	AppSecret   string `json:"app_secret"`
	FolderToken string `json:"folder_token"`
	// RecurseDepth List 时递归进入子文件夹的层数,0=只列一层(默认 0)。
	RecurseDepth int `json:"recurse_depth,omitempty"`
	// Endpoint 覆盖 API 根地址(默认 https://open.feishu.cn,测试/私有化用)。
	Endpoint string `json:"endpoint,omitempty"`
	// TimeoutSec 单次 HTTP 超时秒数,0=默认 30。
	TimeoutSec int `json:"timeout_sec,omitempty"`
}

// Enabled 判断凭据是否齐全。
func (c FeishuConfig) Enabled() bool { return c.AppID != "" && c.AppSecret != "" }

// DingTalkConfig 钉钉(企业内部应用)。AccessToken 与 AppKey/AppSecret 二选一:
// 直接持有 accessToken(如外部刷新)时填 AccessToken;否则填应用三元组走 OAuth 换取。
type DingTalkConfig struct {
	AccessToken string `json:"access_token,omitempty"`
	AppKey      string `json:"app_key,omitempty"`
	AppSecret   string `json:"app_secret,omitempty"`
	// SpaceId 钉盘空间 id(必填,管理后台/钉盘页面可查)。
	SpaceId string `json:"space_id"`
	// FolderId 列举的文件夹 id,空=根目录。
	FolderId string `json:"folder_id,omitempty"`
	// UnionId 操作人 unionId(部分接口的 operatorId 参数,选填)。
	UnionId string `json:"union_id,omitempty"`
	// Endpoint 覆盖 API 根地址(默认 https://api.dingtalk.com)。
	Endpoint string `json:"endpoint,omitempty"`
	// TimeoutSec 单次 HTTP 超时秒数,0=默认 30。
	TimeoutSec int `json:"timeout_sec,omitempty"`
}

// Enabled 判断凭据是否齐全(accessToken 或 appKey+appSecret)。
func (c DingTalkConfig) Enabled() bool {
	return c.AccessToken != "" || (c.AppKey != "" && c.AppSecret != "")
}

// TencentDocConfig 腾讯文档(OAuth2 用户 access_token,见 docs.qq.com/open)。
type TencentDocConfig struct {
	AccessToken string `json:"access_token"`
	// FolderId 列举的文件夹 id,空=根目录。
	FolderId string `json:"folder_id,omitempty"`
	// ExportTimeoutSec 导出任务轮询上限秒数,0=默认 90。
	ExportTimeoutSec int `json:"export_timeout_sec,omitempty"`
	// Endpoint 覆盖 API 根地址(默认 https://docs.qq.com)。
	Endpoint string `json:"endpoint,omitempty"`
	// TimeoutSec 单次 HTTP 超时秒数,0=默认 30。
	TimeoutSec int `json:"timeout_sec,omitempty"`
}

// Enabled 判断凭据是否齐全。
func (c TencentDocConfig) Enabled() bool { return c.AccessToken != "" }

// NotionConfig Notion(integration token)。
type NotionConfig struct {
	Token string `json:"token"`
	// RootPage 可选。设置后 List 从该页面出发沿子页面树展开(可控制深度),
	// 而不是全工作区 search(Notion search 不支持按父页面过滤)。
	RootPage string `json:"root_page,omitempty"`
	// MaxDepth RootPage 展开的最大层数,0=默认 3。
	MaxDepth int `json:"max_depth,omitempty"`
	// MaxPages 单次 List 最多返回条数,0=默认 500。
	MaxPages int `json:"max_pages,omitempty"`
	// Endpoint 覆盖 API 根地址(默认 https://api.notion.com)。
	Endpoint string `json:"endpoint,omitempty"`
	// TimeoutSec 单次 HTTP 超时秒数,0=默认 30。
	TimeoutSec int `json:"timeout_sec,omitempty"`
}

// Enabled 判断凭据是否齐全。
func (c NotionConfig) Enabled() bool { return c.Token != "" }

// Config 四平台配置总入口。各段只填启用的平台;JSON 键与各 XxxConfig 的 json tag 一致。
type Config struct {
	Feishu     FeishuConfig     `json:"feishu"`
	DingTalk   DingTalkConfig   `json:"dingtalk"`
	TencentDoc TencentDocConfig `json:"tdoc"`
	Notion     NotionConfig     `json:"notion"`
}

const envHelp = "至少配置一个平台凭据:FEISHU_APP_ID/FEISHU_APP_SECRET[/FEISHU_FOLDER_TOKEN];" +
	"DINGTALK_ACCESS_TOKEN(或 DINGTALK_APP_KEY/DINGTALK_APP_SECRET)[/DINGTALK_SPACE_ID];" +
	"TDOC_ACCESS_TOKEN[/TDOC_FOLDER_ID];NOTION_TOKEN[/NOTION_ROOT_PAGE]"

// ConfigFromEnv 从环境变量读配置(各平台段仅当凭据齐全时启用)。
func ConfigFromEnv() Config {
	return Config{
		Feishu: FeishuConfig{
			AppID:        os.Getenv("FEISHU_APP_ID"),
			AppSecret:    os.Getenv("FEISHU_APP_SECRET"),
			FolderToken:  os.Getenv("FEISHU_FOLDER_TOKEN"),
			RecurseDepth: envInt("FEISHU_RECURSE_DEPTH", 0),
		},
		DingTalk: DingTalkConfig{
			AccessToken: os.Getenv("DINGTALK_ACCESS_TOKEN"),
			AppKey:      os.Getenv("DINGTALK_APP_KEY"),
			AppSecret:   os.Getenv("DINGTALK_APP_SECRET"),
			SpaceId:     os.Getenv("DINGTALK_SPACE_ID"),
			FolderId:    os.Getenv("DINGTALK_FOLDER_ID"),
			UnionId:     os.Getenv("DINGTALK_UNION_ID"),
		},
		TencentDoc: TencentDocConfig{
			AccessToken: os.Getenv("TDOC_ACCESS_TOKEN"),
			FolderId:    os.Getenv("TDOC_FOLDER_ID"),
		},
		Notion: NotionConfig{
			Token:    os.Getenv("NOTION_TOKEN"),
			RootPage: os.Getenv("NOTION_ROOT_PAGE"),
			MaxDepth: envInt("NOTION_MAX_DEPTH", 0),
		},
	}
}

// ConfigFromJSON 解析 config.json 并覆盖到环境变量配置之上:
// 以 ConfigFromEnv() 为底,JSON 中出现的字段(段内出现即覆盖,段内缺省沿用环境变量)优先生效。
func ConfigFromJSON(data []byte) (Config, error) {
	cfg := ConfigFromEnv()
	var sections map[string]json.RawMessage
	if err := json.Unmarshal(data, &sections); err != nil {
		return Config{}, fmt.Errorf("knowledge: 解析配置失败: %w", err)
	}
	// json.Unmarshal 到既有结构体只会覆盖 JSON 里出现的字段,天然实现「缺省沿用」。
	if raw, ok := sections["feishu"]; ok {
		if err := json.Unmarshal(raw, &cfg.Feishu); err != nil {
			return Config{}, fmt.Errorf("knowledge: 解析 feishu 配置失败: %w", err)
		}
	}
	if raw, ok := sections["dingtalk"]; ok {
		if err := json.Unmarshal(raw, &cfg.DingTalk); err != nil {
			return Config{}, fmt.Errorf("knowledge: 解析 dingtalk 配置失败: %w", err)
		}
	}
	if raw, ok := sections["tdoc"]; ok {
		if err := json.Unmarshal(raw, &cfg.TencentDoc); err != nil {
			return Config{}, fmt.Errorf("knowledge: 解析 tdoc 配置失败: %w", err)
		}
	}
	if raw, ok := sections["notion"]; ok {
		if err := json.Unmarshal(raw, &cfg.Notion); err != nil {
			return Config{}, fmt.Errorf("knowledge: 解析 notion 配置失败: %w", err)
		}
	}
	return cfg, nil
}

// KnownProviders 返回全部 provider 名(启用与否取决于凭据)。
func KnownProviders() []string {
	return []string{"feishu", "dingtalk", "tdoc", "notion"}
}

// sortStrings 原地排序并返回,供 provider 名列表输出使用。
func sortStrings(in []string) []string {
	sort.Strings(in)
	return in
}

// envInt 读整型环境变量,缺失/非法用 def。
func envInt(key string, def int) int {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return def
	}
	var n int
	if _, err := fmt.Sscanf(v, "%d", &n); err != nil {
		return def
	}
	return n
}
