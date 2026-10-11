package knowledge

import (
	"testing"
)

func TestConfigFromEnv(t *testing.T) {
	t.Setenv("FEISHU_APP_ID", "cli_a")
	t.Setenv("FEISHU_APP_SECRET", "s1")
	t.Setenv("FEISHU_FOLDER_TOKEN", "FldX")
	t.Setenv("DINGTALK_APP_KEY", "k")
	t.Setenv("DINGTALK_APP_SECRET", "s2")
	t.Setenv("DINGTALK_SPACE_ID", "sp1")
	t.Setenv("TDOC_ACCESS_TOKEN", "t1")
	t.Setenv("TDOC_FOLDER_ID", "F1")
	t.Setenv("NOTION_TOKEN", "secret_x")
	t.Setenv("NOTION_ROOT_PAGE", "page-root")

	cfg := ConfigFromEnv()
	if !cfg.Feishu.Enabled() || cfg.Feishu.AppID != "cli_a" || cfg.Feishu.FolderToken != "FldX" {
		t.Fatalf("feishu 配置错误: %+v", cfg.Feishu)
	}
	if !cfg.DingTalk.Enabled() || cfg.DingTalk.SpaceId != "sp1" {
		t.Fatalf("dingtalk 配置错误: %+v", cfg.DingTalk)
	}
	if !cfg.TencentDoc.Enabled() || cfg.TencentDoc.FolderId != "F1" {
		t.Fatalf("tdoc 配置错误: %+v", cfg.TencentDoc)
	}
	if !cfg.Notion.Enabled() || cfg.Notion.RootPage != "page-root" {
		t.Fatalf("notion 配置错误: %+v", cfg.Notion)
	}
}

func TestConfigFromJSONOverlay(t *testing.T) {
	// 环境变量打底
	t.Setenv("FEISHU_APP_ID", "cli_from_env")
	t.Setenv("FEISHU_APP_SECRET", "secret_from_env")
	t.Setenv("NOTION_TOKEN", "notion_from_env")

	// JSON 只覆盖出现的字段:feishu.app_secret 覆盖,app_id 沿用环境变量;notion.token 沿用。
	data := []byte(`{
		"feishu": {"app_secret": "secret_from_json", "folder_token": "FldJSON"},
		"notion": {"root_page": "root-from-json"}
	}`)
	cfg, err := ConfigFromJSON(data)
	if err != nil {
		t.Fatalf("ConfigFromJSON: %v", err)
	}
	if cfg.Feishu.AppID != "cli_from_env" {
		t.Errorf("app_id 应沿用环境变量, got %q", cfg.Feishu.AppID)
	}
	if cfg.Feishu.AppSecret != "secret_from_json" {
		t.Errorf("app_secret 应被 JSON 覆盖, got %q", cfg.Feishu.AppSecret)
	}
	if cfg.Feishu.FolderToken != "FldJSON" {
		t.Errorf("folder_token 应来自 JSON, got %q", cfg.Feishu.FolderToken)
	}
	if cfg.Notion.Token != "notion_from_env" {
		t.Errorf("notion.token 应沿用环境变量, got %q", cfg.Notion.Token)
	}
	if cfg.Notion.RootPage != "root-from-json" {
		t.Errorf("notion.root_page 应来自 JSON, got %q", cfg.Notion.RootPage)
	}
}

func TestConfigFromJSONBad(t *testing.T) {
	if _, err := ConfigFromJSON([]byte("{not-json")); err == nil {
		t.Fatal("非法 JSON 应报错")
	}
}

func TestNewManagerEmptyConfig(t *testing.T) {
	if _, err := NewManager(Config{}); err == nil {
		t.Fatal("空配置应报错")
	}
}

func TestNewManagerProviders(t *testing.T) {
	m, err := NewManager(Config{
		Feishu: FeishuConfig{AppID: "a", AppSecret: "b", FolderToken: "f"},
		Notion: NotionConfig{Token: "t"},
	})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	got := m.Providers()
	if len(got) != 2 || got[0] != "feishu" || got[1] != "notion" {
		t.Fatalf("Providers() = %v, want [feishu notion]", got)
	}
	if _, err := m.Provider("tdoc"); err == nil {
		t.Fatal("未启用的 provider 应报错")
	}
}
