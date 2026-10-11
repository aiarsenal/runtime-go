package knowledge

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// loadNotionFixture 读 testdata/notion_blocks.json(blocks/children 响应的 results 形态)。
func loadNotionFixture(t *testing.T) []notionBlock {
	t.Helper()
	raw, err := os.ReadFile("testdata/notion_blocks.json")
	if err != nil {
		t.Fatalf("读 fixture: %v", err)
	}
	var blocks []notionBlock
	if err := json.Unmarshal(raw, &blocks); err != nil {
		t.Fatalf("解析 fixture: %v", err)
	}
	return blocks
}

// fakeChildren 模拟 children 接口:bulleted 项带子项,table 带两行。
func fakeChildren(t *testing.T) func(context.Context, string) ([]notionBlock, error) {
	t.Helper()
	return func(_ context.Context, blockID string) ([]notionBlock, error) {
		switch blockID {
		case "b1":
			return []notionBlock{{
				Object: "block", ID: "b1a", Type: "bulleted_list_item",
				BulletedListItem: &notionTextBlock{
					RichText: []notionRichText{{Type: "text", PlainText: "子项"}},
				},
			}}, nil
		case "tbl1":
			cell := func(s string) []notionRichText {
				return []notionRichText{{Type: "text", PlainText: s}}
			}
			return []notionBlock{
				{Object: "block", ID: "r1", Type: "table_row", TableRow: &notionTableRow{Cells: [][]notionRichText{cell("列一"), cell("列二")}}},
				{Object: "block", ID: "r2", Type: "table_row", TableRow: &notionTableRow{Cells: [][]notionRichText{cell("a"), cell("b")}}},
			}, nil
		default:
			return nil, nil
		}
	}
}

func TestNotionBlocksToMarkdown(t *testing.T) {
	blocks := loadNotionFixture(t)
	got, err := notionBlocksToMarkdown(context.Background(), blocks, fakeChildren(t))
	if err != nil {
		t.Fatalf("notionBlocksToMarkdown: %v", err)
	}
	want := strings.Join([]string{
		"# **概览**",
		"",
		"普通段落，带 [链接](https://notion.so) 和 `行内代码`。",
		"",
		"- 父项",
		"  - 子项",
		"",
		"1. 第一",
		"2. 第二",
		"",
		"- [x] 已完成",
		"- [ ] 未完成",
		"",
		"> 引用一句话",
		"",
		"```go",
		`fmt.Println("hi")`,
		"```",
		"",
		"> 💡 高亮提示",
		"",
		"![截图](https://example.com/a.png)",
		"",
		"[参考资料](https://example.com/page)",
		"",
		"[子页面](https://www.notion.so/cp1)(子页面,可单独拉取)",
		"",
		"---",
		"",
		"$$",
		"E = mc^2",
		"$$",
		"",
		"| 列一 | 列二 |",
		"| --- | --- |",
		"| a | b |",
	}, "\n") + "\n"

	if strings.TrimSpace(got) != strings.TrimSpace(want) {
		t.Fatalf("markdown 不匹配。\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

func TestNotionRichTextStyles(t *testing.T) {
	rts := []notionRichText{
		{Type: "text", PlainText: "组合", Annotations: &struct {
			Bold          bool `json:"bold"`
			Italic        bool `json:"italic"`
			Strikethrough bool `json:"strikethrough"`
			Underline     bool `json:"underline"`
			Code          bool `json:"code"`
		}{Bold: true, Italic: true}},
		{Type: "text", PlainText: "删除", Annotations: &struct {
			Bold          bool `json:"bold"`
			Italic        bool `json:"italic"`
			Strikethrough bool `json:"strikethrough"`
			Underline     bool `json:"underline"`
			Code          bool `json:"code"`
		}{Strikethrough: true, Underline: true}},
	}
	got := notionRichTextMarkdown(rts)
	want := "***组合***<u>~~删除~~</u>" // 下划线在最外层(应用顺序 bold→italic→strike→underline)
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestNotionMentionAndEquation(t *testing.T) {
	rts := []notionRichText{
		{Type: "mention", PlainText: "@张三", Mention: &struct {
			Type string `json:"type"`
			Page *struct {
				ID string `json:"id"`
			} `json:"page"`
			User *struct {
				Name string `json:"name"`
			} `json:"user"`
			Date *struct {
				Start string `json:"start"`
				End   string `json:"end"`
			} `json:"date"`
		}{Type: "user", User: &struct {
			Name string `json:"name"`
		}{Name: "李四"}}},
		{Type: "equation", Equation: &struct {
			Expression string `json:"expression"`
		}{Expression: "a^2+b^2=c^2"}},
	}
	got := notionRichTextMarkdown(rts)
	want := "@李四$a^2+b^2=c^2$"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}
