package knowledge

import (
	"strings"
	"testing"
)

func TestHTMLInlineToMarkdown(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"纯文本", "纯文本"},
		{"<strong>粗</strong>与<em>斜</em>", "**粗**与*斜*"},
		{"<b>b</b><i>i</i>", "**b***i*"},
		{`<a href="https://example.com">链接</a>`, "[链接](https://example.com)"},
		{"<code>a`b</code>", "`a'b`"}, // 行内代码中的反引号替换为单引号
		{"第一行<br>第二行", "第一行\n第二行"},
		{`<img src="https://x/i.png" alt="图">`, "![图](https://x/i.png)"},
		{`<img src='https://x/i.png'>`, "![](https://x/i.png)"},
		{"a&amp;b&lt;c&gt;d&quot;e&nbsp;f", "a&b<c>d\"e f"},
		{"<span style=\"color:red\">红字</span>", "红字"}, // 不支持的标签剥壳
		{"<strong>未闭合", "**未闭合**"},                    // EOF 补闭合
		{"<strong><em>x</strong>y", "***x***y"},       // 错配闭合:中间标签逆序补齐
	}
	for _, c := range cases {
		if got := htmlInlineToMarkdown(c.in); got != c.want {
			t.Errorf("htmlInlineToMarkdown(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestHTMLCodeText(t *testing.T) {
	got := htmlCodeText("<pre><span>if (a &lt; b) {</span>\n    go()</pre>")
	want := "if (a < b) {\n    go()"
	if strings.TrimSpace(got) != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestHTMLBlockToMarkdown(t *testing.T) {
	got := htmlBlockToMarkdown("<h1>标题</h1><p>段落<strong>粗</strong></p><ul><li>项一</li><li>项二</li></ul><hr>")
	want := strings.Join([]string{
		"# 标题",
		"",
		"段落**粗**",
		"",
		"- 项一",
		"- 项二",
		"",
		"---",
	}, "\n") + "\n"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestHTMLAttr(t *testing.T) {
	if got := htmlAttr(`<a class="x" href="https://a">`, "href"); got != "https://a" {
		t.Fatalf("双引号 = %q", got)
	}
	if got := htmlAttr("<a href='https://b'>", "href"); got != "https://b" {
		t.Fatalf("单引号 = %q", got)
	}
	if got := htmlAttr("<a href=https://c>", "href"); got != "https://c" {
		t.Fatalf("裸值 = %q", got)
	}
	if got := htmlAttr("<a>", "href"); got != "" {
		t.Fatalf("缺失 = %q", got)
	}
}
