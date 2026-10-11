package knowledge

// 飞书 docx 块结构 → Markdown 渲染。
//
// 块模型依据 /open-apis/docx/v1/documents/:document_id/blocks 响应:
// 块按 block_type 区分,富文本统一为 elements 数组(text_run/mention_doc/mention_user/equation)。
// 支持的块类型(其余类型输出占位注释):
//
//	1 page(根) 2 text 3-11 heading1-9 12 bullet 13 ordered 14 code 15 quote
//	16 todo 18 callout 21 divider 26 image 33 quote_container(子块加引用前缀)
//
// 注:块树以 parent_id/children 关联,列表嵌套层级按树深度缩进;代码块 language 为
// 数字枚举,此处只映射常用项,未知值输出无语言标注的围栏。

import (
	"fmt"
	"strings"
)

// 飞书 docx block_type 枚举(仅列本实现涉及的)。
const (
	feishuBlockPage     = 1
	feishuBlockText     = 2
	feishuBlockHeading1 = 3
	feishuBlockHeading9 = 11
	feishuBlockBullet   = 12
	feishuBlockOrdered  = 13
	feishuBlockCode     = 14
	feishuBlockQuote    = 15
	feishuBlockTodo     = 16
	feishuBlockCallout  = 18
	feishuBlockDivider  = 21
	feishuBlockImage    = 26
	feishuBlockQuoteBox = 33 // quote_container
)

// feishuBlock 文档块(各类型 payload 同构,按 block_type 取对应字段)。
type feishuBlock struct {
	BlockID   string   `json:"block_id"`
	BlockType int      `json:"block_type"`
	ParentID  string   `json:"parent_id"`
	Children  []string `json:"children"`

	Text     *feishuTextBlock    `json:"text"`
	Heading1 *feishuTextBlock    `json:"heading1"`
	Heading2 *feishuTextBlock    `json:"heading2"`
	Heading3 *feishuTextBlock    `json:"heading3"`
	Heading4 *feishuTextBlock    `json:"heading4"`
	Heading5 *feishuTextBlock    `json:"heading5"`
	Heading6 *feishuTextBlock    `json:"heading6"`
	Heading7 *feishuTextBlock    `json:"heading7"`
	Heading8 *feishuTextBlock    `json:"heading8"`
	Heading9 *feishuTextBlock    `json:"heading9"`
	Bullet   *feishuTextBlock    `json:"bullet"`
	Ordered  *feishuTextBlock    `json:"ordered"`
	Code     *feishuTextBlock    `json:"code"`
	Quote    *feishuTextBlock    `json:"quote"`
	Todo     *feishuTextBlock    `json:"todo"`
	Callout  *feishuCalloutBlock `json:"callout"`
	Image    *feishuImageBlock   `json:"image"`
}

// feishuTextBlock 富文本块公共载体(elements + 样式)。
type feishuTextBlock struct {
	Elements []feishuElement `json:"elements"`
	Style    *feishuBlkStyle `json:"style"`
}

// feishuBlkStyle 块级样式(todo 的 done、code 的 language)。
type feishuBlkStyle struct {
	Done     bool `json:"done"`
	Language int  `json:"language"`
}

// feishuCalloutBlock 高亮块。
type feishuCalloutBlock struct {
	Elements []feishuElement `json:"elements"`
	EmojiID  string          `json:"emoji_id"`
}

// feishuImageBlock 图片块(token 不能直接访问,需素材下载接口,见 feishu.go 注释)。
type feishuImageBlock struct {
	Token string `json:"token"`
}

// feishuElement 行内元素。
type feishuElement struct {
	TextRun     *feishuTextRun    `json:"text_run"`
	MentionDoc  *feishuMentionDoc `json:"mention_doc"`
	MentionUser *struct {
		UserID string `json:"user_id"`
	} `json:"mention_user"`
	Equation *struct {
		Content string `json:"content"`
	} `json:"equation"`
}

// feishuTextRun 文本片段 + 行内样式。
type feishuTextRun struct {
	Content string           `json:"content"`
	Style   *feishuTextStyle `json:"text_element_style"`
}

// feishuTextStyle 行内样式。
type feishuTextStyle struct {
	Bold          bool `json:"bold"`
	Italic        bool `json:"italic"`
	Strikethrough bool `json:"strikethrough"`
	Underline     bool `json:"underline"`
	InlineCode    bool `json:"inline_code"`
	Link          *struct {
		URL string `json:"url"`
	} `json:"link"`
}

// feishuMentionDoc 文档/网页 @ 引用。
type feishuMentionDoc struct {
	Title string `json:"title"`
	URL   string `json:"url"`
}

// feishuCodeLanguages 代码块 language 数字枚举 → 围栏语言标注(常用项,非全量)。
var feishuCodeLanguages = map[int]string{
	7: "bash", 12: "css", 22: "go", 24: "html", 28: "json", 29: "java",
	30: "javascript", 39: "markdown", 43: "php", 49: "python", 52: "ruby",
	53: "rust", 56: "sql", 57: "scala", 60: "shell", 61: "swift",
	63: "typescript", 66: "xml", 67: "yaml",
}

// payloadOf 取块对应类型的富文本载荷与类型码。
func (b *feishuBlock) payloadOf() (*feishuTextBlock, int) {
	switch b.BlockType {
	case feishuBlockText:
		return b.Text, b.BlockType
	case feishuBlockHeading1:
		return b.Heading1, b.BlockType
	case 4:
		return b.Heading2, b.BlockType
	case 5:
		return b.Heading3, b.BlockType
	case 6:
		return b.Heading4, b.BlockType
	case 7:
		return b.Heading5, b.BlockType
	case 8:
		return b.Heading6, b.BlockType
	case 9:
		return b.Heading7, b.BlockType
	case 10:
		return b.Heading8, b.BlockType
	case feishuBlockHeading9:
		return b.Heading9, b.BlockType
	case feishuBlockBullet:
		return b.Bullet, b.BlockType
	case feishuBlockOrdered:
		return b.Ordered, b.BlockType
	case feishuBlockCode:
		return b.Code, b.BlockType
	case feishuBlockQuote:
		return b.Quote, b.BlockType
	case feishuBlockTodo:
		return b.Todo, b.BlockType
	default:
		return nil, b.BlockType
	}
}

// feishuBlocksToMarkdown 把块列表渲染成 Markdown(首行 "# 标题")。
func feishuBlocksToMarkdown(blocks []feishuBlock, title string) string {
	byID := make(map[string]*feishuBlock, len(blocks))
	var roots []*feishuBlock
	for i := range blocks {
		b := &blocks[i]
		byID[b.BlockID] = b
		if b.BlockType == feishuBlockPage || b.ParentID == "" {
			roots = append(roots, b)
		}
	}
	r := &feishuRenderer{byID: byID}
	var sb strings.Builder
	if title != "" {
		sb.WriteString("# " + title + "\n\n")
	}
	for _, root := range roots {
		r.renderChildren(&sb, root, 0)
	}
	out := strings.TrimRight(sb.String(), "\n") + "\n"
	return collapseBlankLines(out)
}

// feishuRenderer 渲染上下文:块索引 + 子块顺序。
type feishuRenderer struct {
	byID map[string]*feishuBlock
}

// renderChildren 按父块 children 顺序渲染子块(无 children 时回退文档顺序里 parent 匹配的块)。
func (r *feishuRenderer) renderChildren(sb *strings.Builder, parent *feishuBlock, depth int) {
	var kids []*feishuBlock
	if len(parent.Children) > 0 {
		for _, id := range parent.Children {
			if b, ok := r.byID[id]; ok {
				kids = append(kids, b)
			}
		}
	} else {
		return
	}
	r.renderSlice(sb, kids, depth)
}

// renderSlice 渲染一组同层块;决定块间空行(同类型列表项之间单换行,其余空行)
// 与有序列表计数(列表类型切换即重新计数)。
func (r *feishuRenderer) renderSlice(sb *strings.Builder, kids []*feishuBlock, depth int) {
	prevKind := ""
	orderedNum := 0
	for _, b := range kids {
		_, typeCode := b.payloadOf()
		kind := feishuListKind(typeCode)
		if typeCode == feishuBlockOrdered {
			if prevKind == kind {
				orderedNum++
			} else {
				orderedNum = 1
			}
		} else {
			orderedNum = 0
		}
		text := r.renderBlock(b, depth, orderedNum)
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
		sb.WriteString(text)
		prevKind = kind
	}
}

// feishuListKind 块类型 → 列表种类名(空串=非列表)。
func feishuListKind(typeCode int) string {
	switch typeCode {
	case feishuBlockBullet:
		return "bullet"
	case feishuBlockOrdered:
		return "ordered"
	case feishuBlockTodo:
		return "todo"
	default:
		return ""
	}
}

// renderBlock 渲染单个块(不含块间分隔)。
func (r *feishuRenderer) renderBlock(b *feishuBlock, depth, orderedNum int) string {
	payload, typeCode := b.payloadOf()
	switch typeCode {
	case feishuBlockPage:
		return "" // 根块本身无内容
	case feishuBlockText:
		return r.renderWithChildren(b, indentLines(feishuElementsToMarkdown(payload.Elements), listIndent(depth)), depth)
	case feishuBlockHeading1, 4, 5, 6, 7, 8, 9, 10, feishuBlockHeading9:
		level := typeCode - feishuBlockHeading1 + 1
		return strings.Repeat("#", level) + " " + feishuElementsToMarkdown(payload.Elements)
	case feishuBlockBullet:
		return r.renderWithChildren(b, indentLines("- "+feishuElementsToMarkdown(payload.Elements), listIndent(depth)), depth)
	case feishuBlockOrdered:
		return r.renderWithChildren(b, indentLines(fmt.Sprintf("%d. %s", orderedNum, feishuElementsToMarkdown(payload.Elements)), listIndent(depth)), depth)
	case feishuBlockCode:
		lang := ""
		if payload.Style != nil {
			lang = feishuCodeLanguages[payload.Style.Language]
		}
		return "```" + lang + "\n" + feishuElementsToMarkdown(payload.Elements) + "\n```"
	case feishuBlockQuote:
		return r.renderWithChildren(b, "> "+feishuElementsToMarkdown(payload.Elements), depth)
	case feishuBlockTodo:
		mark := "[ ]"
		if payload.Style != nil && payload.Style.Done {
			mark = "[x]"
		}
		return r.renderWithChildren(b, indentLines("- "+mark+" "+feishuElementsToMarkdown(payload.Elements), listIndent(depth)), depth)
	case feishuBlockCallout:
		text := feishuElementsToMarkdown(b.Callout.Elements)
		var inner strings.Builder
		r.renderChildren(&inner, b, 0)
		out := "> " + text
		if inner.Len() > 0 {
			out += "\n" + indentLines(strings.TrimRight(inner.String(), "\n"), "> ")
		}
		return out
	case feishuBlockDivider:
		return "---"
	case feishuBlockImage:
		if b.Image == nil || b.Image.Token == "" {
			return "[图片]"
		}
		// 飞书图片 token 非公开 URL,需素材下载接口,本工具不拉取图片素材,输出占位。
		return fmt.Sprintf("[图片:token=%s(需通过飞书素材接口获取)]", b.Image.Token)
	case feishuBlockQuoteBox:
		// 引用容器:容器自身无文本,子块逐行加 "> " 前缀。
		var inner strings.Builder
		r.renderChildren(&inner, b, 0)
		if inner.Len() == 0 {
			return ""
		}
		return indentLines(strings.TrimRight(inner.String(), "\n"), "> ")
	default:
		return fmt.Sprintf("<!-- 未支持的飞书块类型: %d -->", typeCode)
	}
}

// renderWithChildren 渲染块文本并追加子块(列表项子块缩进一层)。
func (r *feishuRenderer) renderWithChildren(b *feishuBlock, first string, depth int) string {
	var sb strings.Builder
	sb.WriteString(first)
	var inner strings.Builder
	r.renderChildren(&inner, b, depth+1)
	if inner.Len() > 0 {
		sb.WriteString("\n" + strings.TrimRight(inner.String(), "\n"))
	}
	return sb.String()
}

// listIndent 列表嵌套缩进:每层 2 空格(GFM 列表)。
func listIndent(depth int) string {
	if depth <= 0 {
		return ""
	}
	return strings.Repeat("  ", depth)
}

// indentLines 给多行文本每行加前缀(空行不加)。
func indentLines(s, prefix string) string {
	if prefix == "" {
		return s
	}
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		if strings.TrimSpace(l) != "" {
			lines[i] = prefix + l
		}
	}
	return strings.Join(lines, "\n")
}

// feishuElementsToMarkdown 渲染行内元素序列。
func feishuElementsToMarkdown(elems []feishuElement) string {
	var parts []string
	for _, e := range elems {
		if s := feishuElementToMarkdown(e); s != "" {
			parts = append(parts, s)
		}
	}
	return strings.Join(parts, "")
}

// feishuElementToMarkdown 渲染单个行内元素。
func feishuElementToMarkdown(e feishuElement) string {
	switch {
	case e.TextRun != nil:
		return feishuTextRunToMarkdown(*e.TextRun)
	case e.MentionDoc != nil:
		title := firstNonEmpty(e.MentionDoc.Title, "文档")
		if e.MentionDoc.URL == "" {
			return title
		}
		return "[" + title + "](" + e.MentionDoc.URL + ")"
	case e.MentionUser != nil:
		return "@" + e.MentionUser.UserID
	case e.Equation != nil:
		return "$" + e.Equation.Content + "$"
	default:
		return ""
	}
}

// feishuTextRunToMarkdown 应用行内样式:行内代码优先,其次链接,再加粗/斜体/删除线/下划线。
func feishuTextRunToMarkdown(run feishuTextRun) string {
	text := run.Content
	st := run.Style
	if st == nil {
		return text
	}
	if st.InlineCode {
		text = "`" + strings.ReplaceAll(text, "`", "'") + "`"
		return text // 行内代码内不再叠加其他样式
	}
	if st.Link != nil && st.Link.URL != "" {
		text = "[" + text + "](" + st.Link.URL + ")"
	}
	if st.Bold && text != "" {
		text = "**" + text + "**"
	}
	if st.Italic && text != "" {
		text = "*" + text + "*"
	}
	if st.Strikethrough && text != "" {
		text = "~~" + text + "~~"
	}
	if st.Underline && text != "" {
		text = "<u>" + text + "</u>"
	}
	return text
}

// feishuRawContentToMarkdown 把 raw_content 纯文本(按块逐行)转成段落式 Markdown。
func feishuRawContentToMarkdown(content, title string) string {
	// raw_content 每个块一行;连续非空行按段落保留,空行分段。
	var paras []string
	var cur []string
	for _, l := range strings.Split(content, "\n") {
		l = strings.TrimRight(l, " \t")
		if l == "" {
			if len(cur) > 0 {
				paras = append(paras, strings.Join(cur, "\n"))
				cur = nil
			}
			continue
		}
		cur = append(cur, l)
	}
	if len(cur) > 0 {
		paras = append(paras, strings.Join(cur, "\n"))
	}

	var sb strings.Builder
	if title != "" {
		sb.WriteString("# " + title + "\n\n")
	}
	sb.WriteString(strings.Join(paras, "\n\n"))
	if len(paras) > 0 {
		sb.WriteString("\n")
	}
	return sb.String()
}

// collapseBlankLines 把 3 个以上连续换行压缩成 2 个(段落间统一单空行)。
func collapseBlankLines(s string) string {
	for strings.Contains(s, "\n\n\n") {
		s = strings.ReplaceAll(s, "\n\n\n", "\n\n")
	}
	return s
}
