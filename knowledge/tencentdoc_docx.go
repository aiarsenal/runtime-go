package knowledge

// OOXML(docx)→ Markdown 提取器,服务于腾讯文档导出(exportType=2)产物。
//
// 只用标准库 archive/zip + encoding/xml。定位是「够用的正文提取」:
//   - 段落:标题样式(Heading1-6 / 数字样式 1-6 / 中文「标题 N」)→ # 标题;
//   - 列表:含 numPr 的段落 → "- "(编号细节不还原,统一无序;注释注明);
//   - 表格:tbl → Markdown 表格(首行作表头;复杂合并单元格降级);
//   - 图片:输出占位;字体加粗等行内样式不还原(走 w:t 纯文本)。
// 非目标:完整样式保真。

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// xNode 通用 XML 节点(递归捕获任意 OOXML 结构)。
type xNode struct {
	XMLName xml.Name
	Attrs   []xml.Attr `xml:",any,attr"`
	Text    string     `xml:",chardata"`
	Nodes   []*xNode   `xml:",any"`
}

// child 按本地名取子节点(取首个)。
func (n *xNode) child(local string) *xNode {
	for _, c := range n.Nodes {
		if c.XMLName.Local == local {
			return c
		}
	}
	return nil
}

// childAll 按本地名取全部子节点。
func (n *xNode) childAll(local string) []*xNode {
	var out []*xNode
	for _, c := range n.Nodes {
		if c.XMLName.Local == local {
			out = append(out, c)
		}
	}
	return out
}

// attr 按本地名取属性值。
func (n *xNode) attr(local string) string {
	for _, a := range n.Attrs {
		if a.Name.Local == local {
			return a.Value
		}
	}
	return ""
}

// docxToMarkdown 从 docx 二进制提取 word/document.xml 并转 Markdown。
func docxToMarkdown(raw []byte, title string) (string, error) {
	zr, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		return "", fmt.Errorf("打开 zip: %w", err)
	}
	var docXML []byte
	for _, f := range zr.File {
		if f.Name == "word/document.xml" {
			rc, err := f.Open()
			if err != nil {
				return "", fmt.Errorf("打开 document.xml: %w", err)
			}
			docXML, err = io.ReadAll(rc)
			rc.Close()
			if err != nil {
				return "", fmt.Errorf("读取 document.xml: %w", err)
			}
			break
		}
	}
	if docXML == nil {
		return "", fmt.Errorf("docx 中无 word/document.xml(可能不是 Word 文档)")
	}
	var root xNode
	if err := xml.Unmarshal(docXML, &root); err != nil {
		return "", fmt.Errorf("解析 document.xml: %w", err)
	}
	body := root.child("body")
	if body == nil {
		return "", fmt.Errorf("document.xml 无 body")
	}

	var sb strings.Builder
	if title != "" {
		sb.WriteString("# " + title + "\n\n")
	}
	writeBodyNodes(&sb, body)
	return tidyMarkdown(sb.String()), nil
}

// writeBodyNodes 按文档顺序处理 body 下的段落与表格(sdt 等容器递归);
// 同为列表项的相邻段落之间单换行,其余空行。
func writeBodyNodes(sb *strings.Builder, n *xNode) {
	prevList := false
	for _, c := range n.Nodes {
		switch c.XMLName.Local {
		case "p":
			t, isList := docxParaToMarkdown(c)
			if t == "" {
				continue
			}
			if sb.Len() > 0 {
				if prevList && isList {
					sb.WriteString("\n")
				} else {
					sb.WriteString("\n\n")
				}
			}
			sb.WriteString(t)
			prevList = isList
		case "tbl":
			if t := docxTableToMarkdown(c); t != "" {
				if sb.Len() > 0 {
					sb.WriteString("\n\n")
				}
				sb.WriteString(t)
			}
			prevList = false
		case "sdt", "sdtContent", "tu": // 容器节点递归(tu 为部分腾讯文档导出产物)
			writeBodyNodes(sb, c)
		}
	}
}

// docxParaToMarkdown 单段 → Markdown(标题样式 / 列表 / 普通段落);返回 (文本, 是否列表项)。
func docxParaToMarkdown(p *xNode) (string, bool) {
	text := docxParaText(p)
	if strings.TrimSpace(text) == "" {
		return "", false
	}
	ppr := p.child("pPr")
	level := 0
	isList := false
	if ppr != nil {
		if st := ppr.child("pStyle"); st != nil {
			level = docxHeadingLevel(st.attr("val"))
		}
		if ppr.child("numPr") != nil {
			isList = true
		}
	}
	switch {
	case level > 0:
		return strings.Repeat("#", level) + " " + text, false
	case isList:
		return "- " + text, true
	default:
		return text, false
	}
}

// docxHeadingLevel 样式值 → 标题级别(0=非标题)。兼容 "Heading1"/"heading 2"/"3"/"标题 4" 等。
func docxHeadingLevel(val string) int {
	v := strings.ToLower(strings.TrimSpace(val))
	v = strings.TrimPrefix(v, "heading")
	v = strings.TrimPrefix(v, "标题")
	v = strings.TrimSpace(v)
	n, err := strconv.Atoi(v)
	if err != nil || n < 1 {
		return 0
	}
	if n > 6 {
		return 6
	}
	return n
}

// docxParaText 收集段落全部 w:t 文本(按文档顺序),w:br → 换行,w:tab → 空格。
func docxParaText(p *xNode) string {
	var sb strings.Builder
	var walk func(n *xNode)
	walk = func(n *xNode) {
		switch n.XMLName.Local {
		case "t":
			sb.WriteString(n.Text)
		case "br":
			sb.WriteString("\n")
		case "tab":
			sb.WriteString("    ")
		}
		for _, c := range n.Nodes {
			walk(c)
		}
	}
	for _, c := range p.Nodes {
		walk(c)
	}
	return strings.TrimSpace(sb.String())
}

// docxTableToMarkdown 表格 → Markdown 表(首行作表头;GFM 要求表头,故首行无论语义都作表头)。
func docxTableToMarkdown(tbl *xNode) string {
	var rows [][]string
	var walkRows func(n *xNode)
	walkRows = func(n *xNode) {
		for _, c := range n.Nodes {
			switch c.XMLName.Local {
			case "tr":
				var row []string
				for _, tc := range c.childAll("tc") {
					var cellParts []string
					for _, p := range tc.childAll("p") {
						if t := docxParaText(p); t != "" {
							cellParts = append(cellParts, t)
						}
					}
					row = append(row, strings.Join(cellParts, " "))
				}
				if len(row) > 0 {
					rows = append(rows, row)
				}
			case "tbl": // 嵌套表格:不展开,占位
			default:
				walkRows(c)
			}
		}
	}
	walkRows(tbl)
	if len(rows) == 0 {
		return ""
	}
	width := 0
	for _, r := range rows {
		if len(r) > width {
			width = len(r)
		}
	}
	var sb strings.Builder
	for i, r := range rows {
		for len(r) < width {
			r = append(r, "")
		}
		sb.WriteString("| " + strings.Join(r, " | ") + " |\n")
		if i == 0 {
			sb.WriteString("|" + strings.Repeat(" --- |", width) + "\n")
		}
	}
	return strings.TrimRight(sb.String(), "\n")
}
