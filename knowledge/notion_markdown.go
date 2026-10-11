package knowledge

// Notion 块模型 → Markdown 渲染器(四个平台里最完整的一个)。
//
// 块结构依据 GET /v1/blocks/{block_id}/children(Notion-Version 2022-06-28):
// 每块有 type 字段,同名字段承载载荷;has_children=true 时子块需再调 children 接口。
//
// 支持的块类型:
//
//	paragraph / heading_1-3 / bulleted_list_item / numbered_list_item / to_do /
//	quote / code / callout / toggle / divider / image / bookmark / link_preview /
//	embed / child_page / child_database / equation / table(+table_row)
//
// 行内富文本(annotations)支持 bold/italic/strikethrough/underline/code 与链接;
// mention 支持 page/user/date;行内 equation 输出 $...$。

import (
	"context"
	"fmt"
	"strings"
)

// notionBlock Notion 块。
type notionBlock struct {
	Object         string `json:"object"`
	ID             string `json:"id"`
	Type           string `json:"type"`
	HasChildren    bool   `json:"has_children"`
	CreatedTime    string `json:"created_time"`
	LastEditedTime string `json:"last_edited_time"`
	HasMore        bool   `json:"-"` // children 响应分页标记(非块字段)

	Paragraph        *notionTextBlock  `json:"paragraph"`
	Heading1         *notionTextBlock  `json:"heading_1"`
	Heading2         *notionTextBlock  `json:"heading_2"`
	Heading3         *notionTextBlock  `json:"heading_3"`
	BulletedListItem *notionTextBlock  `json:"bulleted_list_item"`
	NumberedListItem *notionTextBlock  `json:"numbered_list_item"`
	ToDo             *notionTextBlock  `json:"to_do"`
	Quote            *notionTextBlock  `json:"quote"`
	Code             *notionTextBlock  `json:"code"`
	Callout          *notionTextBlock  `json:"callout"`
	Toggle           *notionTextBlock  `json:"toggle"`
	Equation         *notionEquation   `json:"equation"`
	Divider          *struct{}         `json:"divider"`
	Image            *notionFileBlock  `json:"image"`
	Bookmark         *notionURLBlock   `json:"bookmark"`
	LinkPreview      *notionURLBlock   `json:"link_preview"`
	Embed            *notionURLBlock   `json:"embed"`
	ChildPage        *notionChildRef   `json:"child_page"`
	ChildDatabase    *notionChildRef   `json:"child_database"`
	TableRow         *notionTableRow   `json:"table_row"`
	Table            *notionTableBlock `json:"table"`
}

// notionTextBlock 富文本类块公共载体。
type notionTextBlock struct {
	RichText []notionRichText `json:"rich_text"`
	Color    string           `json:"color"`
	Checked  bool             `json:"checked"`
	Language string           `json:"language"`
	Icon     *struct {
		Type  string `json:"type"`
		Emoji string `json:"emoji"`
	} `json:"icon"`
}

// notionEquation 块级公式。
type notionEquation struct {
	Expression string `json:"expression"`
}

// notionFileBlock 图片/文件块。
type notionFileBlock struct {
	Type     string `json:"type"` // external / file
	External *struct {
		URL string `json:"url"`
	} `json:"external"`
	File *struct {
		URL        string `json:"url"`
		ExpiryTime string `json:"expiry_time"`
	} `json:"file"`
	Caption []notionRichText `json:"caption"`
}

// url 取图片实际地址(external 直链;file 为临时 S3 链接,有过期时间)。
func (f *notionFileBlock) url() string {
	if f == nil {
		return ""
	}
	if f.External != nil {
		return f.External.URL
	}
	if f.File != nil {
		return f.File.URL
	}
	return ""
}

// alt 取图片说明。
func (f *notionFileBlock) alt() string {
	if f == nil {
		return ""
	}
	return notionRichTextPlain(f.Caption)
}

// notionURLBlock 链接类块(bookmark/link_preview/embed)。
type notionURLBlock struct {
	URL     string           `json:"url"`
	Caption []notionRichText `json:"caption"`
}

// notionChildRef child_page / child_database 的标题。
type notionChildRef struct {
	Title string `json:"title"`
}

// notionTableRow 表格行。
type notionTableRow struct {
	Cells [][]notionRichText `json:"cells"`
}

// notionTableBlock 表格容器(行在 children 的 table_row 块里)。
type notionTableBlock struct {
	TableWidth      int  `json:"table_width"`
	HasColumnHeader bool `json:"has_column_header"`
	HasRowHeader    bool `json:"has_row_header"`
}

// notionRichText 行内富文本。
type notionRichText struct {
	Type      string `json:"type"` // text / mention / equation
	PlainText string `json:"plain_text"`
	Href      string `json:"href"`
	Text      *struct {
		Content string `json:"content"`
		Link    *struct {
			URL string `json:"url"`
		} `json:"link"`
	} `json:"text"`
	Mention *struct {
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
	} `json:"mention"`
	Equation *struct {
		Expression string `json:"expression"`
	} `json:"equation"`
	Annotations *struct {
		Bold          bool `json:"bold"`
		Italic        bool `json:"italic"`
		Strikethrough bool `json:"strikethrough"`
		Underline     bool `json:"underline"`
		Code          bool `json:"code"`
	} `json:"annotations"`
}

// notionRichTextPlain 富文本数组 → 纯文本。
func notionRichTextPlain(rts []notionRichText) string {
	var parts []string
	for _, rt := range rts {
		if rt.PlainText != "" {
			parts = append(parts, rt.PlainText)
		}
	}
	return strings.Join(parts, "")
}

// notionRichTextMarkdown 富文本数组 → Markdown(行内样式)。
func notionRichTextMarkdown(rts []notionRichText) string {
	var b strings.Builder
	for _, rt := range rts {
		b.WriteString(notionRichTextOne(rt))
	}
	return b.String()
}

// notionRichTextOne 单段富文本。
func notionRichTextOne(rt notionRichText) string {
	text := rt.PlainText
	href := rt.Href
	if rt.Text != nil && rt.Text.Link != nil && rt.Text.Link.URL != "" {
		href = rt.Text.Link.URL
	}
	switch rt.Type {
	case "mention":
		if m := rt.Mention; m != nil {
			switch m.Type {
			case "user":
				if m.User != nil && m.User.Name != "" {
					text = "@" + m.User.Name
				}
			case "date":
				if m.Date != nil {
					text = m.Date.Start
				}
			case "page":
				text = strings.TrimSpace(text)
				if text == "" {
					text = "页面"
				}
			}
		}
	case "equation":
		if rt.Equation != nil {
			return "$" + rt.Equation.Expression + "$"
		}
	}
	if text == "" {
		return ""
	}
	a := rt.Annotations
	if a != nil && a.Code {
		return "`" + strings.ReplaceAll(text, "`", "'") + "`"
	}
	if href != "" {
		text = "[" + text + "](" + href + ")"
	}
	if a != nil {
		if a.Bold {
			text = "**" + text + "**"
		}
		if a.Italic {
			text = "*" + text + "*"
		}
		if a.Strikethrough {
			text = "~~" + text + "~~"
		}
		if a.Underline {
			text = "<u>" + text + "</u>"
		}
	}
	return text
}

// notionRenderer 渲染上下文。fetchChildren 为块子块懒加载器(见 /v1/blocks/{id}/children);
// 传 nil 时 has_children 的块输出占位注释。
type notionRenderer struct {
	ctx           context.Context
	fetchChildren func(ctx context.Context, blockID string) ([]notionBlock, error)
}

// notionBlocksToMarkdown 顶层入口:渲染一组块(块间空行,列表项之间单换行)。
func notionBlocksToMarkdown(ctx context.Context, blocks []notionBlock, fetchChildren func(context.Context, string) ([]notionBlock, error)) (string, error) {
	r := &notionRenderer{ctx: ctx, fetchChildren: fetchChildren}
	var sb strings.Builder
	if err := r.renderSlice(&sb, blocks, 0); err != nil {
		return "", err
	}
	return tidyMarkdown(sb.String()), nil
}

// renderSlice 渲染同层块序列(同类型列表项之间单换行,其余块间空行)。
func (r *notionRenderer) renderSlice(sb *strings.Builder, blocks []notionBlock, depth int) error {
	prevKind := ""
	orderedNum := 0
	for _, b := range blocks {
		kind := notionListKind(b.Type)
		if kind == "ordered" {
			if prevKind == kind {
				orderedNum++
			} else {
				orderedNum = 1
			}
		} else {
			orderedNum = 0
		}
		text, err := r.renderBlock(b, depth, orderedNum)
		if err != nil {
			return err
		}
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
	return nil
}

// notionListKind 块类型 → 列表种类名(空串=非列表)。
func notionListKind(blockType string) string {
	switch blockType {
	case "bulleted_list_item":
		return "bullet"
	case "numbered_list_item":
		return "ordered"
	case "to_do":
		return "todo"
	default:
		return ""
	}
}

// renderChildrenInto 拉取并渲染子块:depth 为子块渲染深度(子块自行按 listIndent 缩进),
// prefix 为逐行附加前缀(仅 quote/callout 这类不自缩进的结构用 "> ")。
func (r *notionRenderer) renderChildrenInto(sb *strings.Builder, b *notionBlock, depth int, prefix string) error {
	if !b.HasChildren || r.fetchChildren == nil {
		if b.HasChildren {
			sb.WriteString("\n" + prefix + "<!-- 子块未拉取 -->")
		}
		return nil
	}
	kids, err := r.fetchChildren(r.ctx, b.ID)
	if err != nil {
		return fmt.Errorf("拉取块 %s 子块: %w", b.ID, err)
	}
	var inner strings.Builder
	if err := r.renderSlice(&inner, kids, depth); err != nil {
		return err
	}
	if inner.Len() > 0 {
		sb.WriteString("\n" + indentLines(strings.TrimRight(inner.String(), "\n"), prefix))
	}
	return nil
}

// renderBlock 渲染单块。
func (r *notionRenderer) renderBlock(b notionBlock, depth, orderedNum int) (string, error) {
	indent := listIndent(depth)
	tb := func(t *notionTextBlock) string {
		if t == nil {
			return ""
		}
		return notionRichTextMarkdown(t.RichText)
	}
	switch b.Type {
	case "paragraph":
		var sb strings.Builder
		sb.WriteString(indent + tb(b.Paragraph))
		if err := r.renderChildrenInto(&sb, &b, depth, ""); err != nil {
			return "", err
		}
		return sb.String(), nil
	case "heading_1", "heading_2", "heading_3":
		level := int(b.Type[len("heading_")] - '0') // heading_1 → 1
		return strings.Repeat("#", level) + " " + tb(blockText(b)), nil
	case "bulleted_list_item":
		var sb strings.Builder
		sb.WriteString(indent + "- " + tb(b.BulletedListItem))
		if err := r.renderChildrenInto(&sb, &b, depth+1, ""); err != nil {
			return "", err
		}
		return sb.String(), nil
	case "numbered_list_item":
		var sb strings.Builder
		sb.WriteString(fmt.Sprintf("%s%d. %s", indent, orderedNum, tb(b.NumberedListItem)))
		if err := r.renderChildrenInto(&sb, &b, depth+1, ""); err != nil {
			return "", err
		}
		return sb.String(), nil
	case "to_do":
		mark := "[ ]"
		if b.ToDo != nil && b.ToDo.Checked {
			mark = "[x]"
		}
		var sb strings.Builder
		sb.WriteString(indent + "- " + mark + " " + tb(b.ToDo))
		if err := r.renderChildrenInto(&sb, &b, depth+1, ""); err != nil {
			return "", err
		}
		return sb.String(), nil
	case "quote":
		var sb strings.Builder
		sb.WriteString("> " + tb(b.Quote))
		if err := r.renderChildrenInto(&sb, &b, depth, "> "); err != nil {
			return "", err
		}
		return sb.String(), nil
	case "code":
		lang := ""
		if b.Code != nil {
			lang = notionCodeLanguage(b.Code.Language)
		}
		return "```" + lang + "\n" + tb(b.Code) + "\n```", nil
	case "callout":
		var sb strings.Builder
		icon := ""
		if b.Callout != nil && b.Callout.Icon != nil {
			icon = strings.TrimSpace(b.Callout.Icon.Emoji) + " "
		}
		sb.WriteString("> " + icon + tb(b.Callout))
		if err := r.renderChildrenInto(&sb, &b, depth, "> "); err != nil {
			return "", err
		}
		return sb.String(), nil
	case "toggle":
		var sb strings.Builder
		sb.WriteString("**" + tb(b.Toggle) + "**")
		if err := r.renderChildrenInto(&sb, &b, depth+1, ""); err != nil {
			return "", err
		}
		return sb.String(), nil
	case "divider":
		return "---", nil
	case "image":
		if b.Image == nil || b.Image.url() == "" {
			return "[图片]", nil
		}
		alt := firstNonEmpty(b.Image.alt(), "image")
		// file 型 URL 是临时 S3 链接(有过期时间),写进 Markdown 只作参考。
		return fmt.Sprintf("![%s](%s)", alt, b.Image.url()), nil
	case "bookmark", "link_preview", "embed":
		if b.Bookmark == nil && b.LinkPreview == nil && b.Embed == nil {
			return "", nil
		}
		u := ""
		var caption string
		switch b.Type {
		case "bookmark":
			if b.Bookmark != nil {
				u, caption = b.Bookmark.URL, notionRichTextPlain(b.Bookmark.Caption)
			}
		case "link_preview":
			if b.LinkPreview != nil {
				u, caption = b.LinkPreview.URL, notionRichTextPlain(b.LinkPreview.Caption)
			}
		case "embed":
			if b.Embed != nil {
				u, caption = b.Embed.URL, notionRichTextPlain(b.Embed.Caption)
			}
		}
		if u == "" {
			return "", nil
		}
		label := firstNonEmpty(caption, u)
		return fmt.Sprintf("[%s](%s)", label, u), nil
	case "child_page", "child_database":
		ref := b.ChildPage
		if ref == nil {
			ref = b.ChildDatabase
		}
		title := "未命名"
		if ref != nil && ref.Title != "" {
			title = ref.Title
		}
		// Notion 页面直链约定:https://www.notion.so/<id 去连字符>
		url := "https://www.notion.so/" + strings.ReplaceAll(b.ID, "-", "")
		kind := "子页面"
		if b.Type == "child_database" {
			kind = "子数据库"
		}
		return fmt.Sprintf("[%s](%s)(%s,可单独拉取)", title, url, kind), nil
	case "equation":
		if b.Equation == nil {
			return "", nil
		}
		return "$$\n" + b.Equation.Expression + "\n$$", nil
	case "table":
		if r.fetchChildren == nil {
			return "<!-- 表格子块未拉取 -->", nil
		}
		rows, err := r.fetchChildren(r.ctx, b.ID)
		if err != nil {
			return "", fmt.Errorf("拉取表格 %s 行: %w", b.ID, err)
		}
		return r.renderTableRows(rows), nil
	case "table_row":
		// 渲染器只会在 table 分支内处理行;游离行按表格行输出
		return r.renderTableRows([]notionBlock{b}), nil
	default:
		return fmt.Sprintf("<!-- 未支持的 Notion 块类型: %s -->", b.Type), nil
	}
}

// renderTableRows 渲染 table_row 块序列为 Markdown 表。
func (r *notionRenderer) renderTableRows(rows []notionBlock) string {
	var lines []string
	width := 0
	for _, rb := range rows {
		if rb.TableRow == nil {
			continue
		}
		var cells []string
		for _, c := range rb.TableRow.Cells {
			cells = append(cells, strings.ReplaceAll(notionRichTextMarkdown(c), "|", "\\|"))
		}
		if len(cells) > width {
			width = len(cells)
		}
		lines = append(lines, strings.Join(cells, "\x00"))
	}
	if width == 0 {
		return ""
	}
	var sb strings.Builder
	for i, l := range lines {
		cells := strings.Split(l, "\x00")
		for len(cells) < width {
			cells = append(cells, "")
		}
		sb.WriteString("| " + strings.Join(cells, " | ") + " |\n")
		if i == 0 {
			sb.WriteString("|" + strings.Repeat(" --- |", width) + "\n")
		}
	}
	return strings.TrimRight(sb.String(), "\n")
}

// blockText 取富文本块的兜底载荷。
func blockText(b notionBlock) *notionTextBlock {
	switch b.Type {
	case "heading_1":
		return b.Heading1
	case "heading_2":
		return b.Heading2
	case "heading_3":
		return b.Heading3
	case "code":
		return b.Code
	default:
		return nil
	}
}

// notionCodeLanguage 归一化代码语言标注("plain text" 等输出为空)。
func notionCodeLanguage(l string) string {
	v := strings.ToLower(strings.TrimSpace(l))
	if v == "" || v == "plain text" || v == "plaintext" || v == "plain" {
		return ""
	}
	return v
}
