package knowledge

import (
	"archive/zip"
	"bytes"
	"strings"
	"testing"
)

// buildTestDocx 在内存中构造一个最小 docx(zip + word/document.xml)。
func buildTestDocx(t *testing.T) []byte {
	t.Helper()
	const doc = `<?xml version="1.0" encoding="UTF-8"?>
<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main">
  <w:body>
    <w:p><w:pPr><w:pStyle w:val="Heading1"/></w:pPr><w:r><w:t>一级标题</w:t></w:r></w:p>
    <w:p><w:r><w:t>普通段落，</w:t><w:t>两段文本</w:t></w:r></w:p>
    <w:p><w:pPr><w:numPr/></w:pPr><w:r><w:t>列表项一</w:t></w:r></w:p>
    <w:p><w:pPr><w:numPr/></w:pPr><w:r><w:t>列表项二</w:t></w:r></w:p>
    <w:p><w:r><w:t>含换行</w:t><w:br/><w:t>第二行</w:t></w:r></w:p>
    <w:tbl>
      <w:tr><w:tc><w:p><w:r><w:t>列一</w:t></w:r></w:p></w:tc><w:tc><w:p><w:r><w:t>列二</w:t></w:r></w:p></w:tc></w:tr>
      <w:tr><w:tc><w:p><w:r><w:t>a</w:t></w:r></w:p></w:tc><w:tc><w:p><w:r><w:t>b</w:t></w:r></w:p></w:tc></w:tr>
    </w:tbl>
    <w:p><w:pPr><w:pStyle w:val="2"/></w:pPr><w:r><w:t>数字样式二级标题</w:t></w:r></w:p>
  </w:body>
</w:document>`

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	f, err := zw.Create("word/document.xml")
	if err != nil {
		t.Fatalf("zip create: %v", err)
	}
	if _, err := f.Write([]byte(doc)); err != nil {
		t.Fatalf("zip write: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("zip close: %v", err)
	}
	return buf.Bytes()
}

func TestDocxToMarkdown(t *testing.T) {
	md, err := docxToMarkdown(buildTestDocx(t), "测试文档")
	if err != nil {
		t.Fatalf("docxToMarkdown: %v", err)
	}
	want := strings.Join([]string{
		"# 测试文档",
		"",
		"# 一级标题",
		"",
		"普通段落，两段文本",
		"",
		"- 列表项一",
		"- 列表项二",
		"",
		"含换行",
		"第二行",
		"",
		"| 列一 | 列二 |",
		"| --- | --- |",
		"| a | b |",
		"",
		"## 数字样式二级标题",
	}, "\n") + "\n"
	if strings.TrimSpace(md) != strings.TrimSpace(want) {
		t.Fatalf("markdown 不匹配。\n--- got ---\n%s\n--- want ---\n%s", md, want)
	}
}

func TestDocxToMarkdownNotDocx(t *testing.T) {
	if _, err := docxToMarkdown([]byte("not a zip"), "x"); err == nil {
		t.Fatal("非 zip 输入应报错")
	}
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	f, _ := zw.Create("readme.txt")
	_, _ = f.Write([]byte("hi"))
	_ = zw.Close()
	if _, err := docxToMarkdown(buf.Bytes(), "x"); err == nil {
		t.Fatal("缺 document.xml 应报错")
	}
}

func TestDocxHeadingLevel(t *testing.T) {
	cases := map[string]int{
		"Heading1":  1,
		"heading 3": 3,
		"2":         2,
		"标题 4":      4,
		"Normal":    0,
		"Heading9":  6, // Markdown 上限 6 级
	}
	for in, want := range cases {
		if got := docxHeadingLevel(in); got != want {
			t.Errorf("docxHeadingLevel(%q) = %d, want %d", in, got, want)
		}
	}
}
