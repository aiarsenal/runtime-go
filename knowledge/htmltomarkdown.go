package knowledge

// 轻量 HTML → Markdown 转换(标准库实现,不引入 x/net/html)。
//
// 服务于钉钉文档块(块 content 为 HTML 片段)与腾讯文档 docx 导出兜底等场景。
// 定位是「够用的行内转换」而非完整 HTML 解析:
//   - 支持的行内标签:br、img(src/alt)、a(href)、strong/b、em/i、u、s/strike/del、code;
//     其余标签剥壳保留文本;
//   - 未闭合/错配标签尽力容错:闭合标签在栈里找不到匹配时忽略,不在 EOF 处强补;
//   - 实体解码用标准库 html.UnescapeString。

import (
	"html"
	"strings"
)

// mdTagStyle 行内标签的 Markdown 开/闭标记。
var mdTagStyle = map[string][2]string{
	"strong": {"**", "**"},
	"b":      {"**", "**"},
	"em":     {"*", "*"},
	"i":      {"*", "*"},
	"u":      {"<u>", "</u>"},
	"s":      {"~~", "~~"},
	"strike": {"~~", "~~"},
	"del":    {"~~", "~~"},
	"code":   {"`", "`"},
}

// htmlInlineToMarkdown 把行内 HTML 片段转 Markdown 文本。
func htmlInlineToMarkdown(s string) string {
	var b strings.Builder
	var stack []string     // 打开的样式标签名(小写)
	var openLinks []string // 待闭合的 a 标签 href(空串=无 href 链接)
	codeDepth := 0         // <code> 嵌套深度(内文本的反引号要转义)
	writeText := func(t string) {
		if codeDepth > 0 {
			t = strings.ReplaceAll(t, "`", "'")
		}
		b.WriteString(t)
	}
	for s != "" {
		lt := strings.IndexByte(s, '<')
		if lt < 0 {
			writeText(html.UnescapeString(s))
			break
		}
		if lt > 0 {
			writeText(html.UnescapeString(s[:lt]))
		}
		end := strings.IndexByte(s[lt:], '>')
		if end < 0 {
			// 残缺标签当纯文本
			writeText(html.UnescapeString(s[lt:]))
			break
		}
		tag := strings.ToLower(strings.TrimSpace(s[lt : lt+end+1]))
		s = s[lt+end+1:]

		switch {
		case strings.HasPrefix(tag, "<br"):
			b.WriteString("\n")
		case strings.HasPrefix(tag, "<img"):
			if src, alt := htmlImg(tag); src != "" {
				b.WriteString("![" + alt + "](" + src + ")")
			}
		case !strings.HasPrefix(tag, "</") && htmlTagName(tag) == "a":
			href := htmlAttr(tag, "href")
			openLinks = append(openLinks, href)
			if href != "" {
				b.WriteString("[")
			}
		case strings.HasPrefix(tag, "</") && htmlTagName(tag) == "a":
			if n := len(openLinks); n > 0 {
				href := openLinks[n-1]
				openLinks = openLinks[:n-1]
				if href != "" {
					b.WriteString("](" + href + ")")
				}
			}
		case strings.HasPrefix(tag, "</"):
			name := htmlTagName(tag)
			if _, ok := mdTagStyle[name]; ok {
				var closing []string
				stack, closing, _ = closeTo(stack, name)
				// 被跨过的未闭合标签也要补闭合,保持标记配对。
				for _, n := range closing {
					if n == "code" {
						codeDepth--
					}
					if style, ok := mdTagStyle[n]; ok {
						b.WriteString(style[1])
					}
				}
			}
		default:
			name := htmlTagName(tag)
			if name == "code" {
				codeDepth++
			}
			if style, ok := mdTagStyle[name]; ok {
				stack = append(stack, name)
				b.WriteString(style[0])
			}
			// 其余开标签剥壳
		}
	}
	// EOF 兜底:栈里未闭合的样式标签按开序逆序补闭合标记,避免悬空标记。
	for i := len(stack) - 1; i >= 0; i-- {
		if style, ok := mdTagStyle[stack[i]]; ok {
			b.WriteString(style[1])
		}
	}
	return b.String()
}

// closeTo 在栈中自顶向下找 name;命中时弹出该项及其上方所有未闭合标签。
// 返回 (新栈, 按闭合顺序排列的被弹出标签, 是否命中)。
func closeTo(stack []string, name string) ([]string, []string, bool) {
	for i := len(stack) - 1; i >= 0; i-- {
		if stack[i] == name {
			closing := make([]string, 0, len(stack)-i)
			for j := len(stack) - 1; j >= i; j-- {
				closing = append(closing, stack[j])
			}
			return stack[:i], closing, true
		}
	}
	return stack, nil, false
}

// htmlTagName 提取标签名(开/闭标签通用,如 <span class="x"> → span,</p> → p)。
func htmlTagName(tag string) string {
	t := strings.TrimPrefix(tag, "<")
	t = strings.TrimPrefix(t, "/") // 闭合标签
	if i := strings.IndexAny(t, " \t\n/>"); i >= 0 {
		t = t[:i]
	}
	return strings.ToLower(t)
}

// htmlAttr 从标签文本中取属性值(单/双引号或裸值)。
func htmlAttr(tag, name string) string {
	lower := strings.ToLower(tag)
	n := strings.ToLower(name)
	for _, quote := range []byte{'"', '\''} {
		key := n + "=" + string(quote)
		i := strings.Index(lower, key)
		if i < 0 {
			continue
		}
		rest := tag[i+len(key):]
		j := strings.IndexByte(rest, quote)
		if j < 0 {
			return rest
		}
		return rest[:j]
	}
	// 裸值:attr=value(到空白或 > 为止)
	key := n + "="
	if i := strings.Index(lower, key); i >= 0 {
		rest := tag[i+len(key):]
		j := strings.IndexAny(rest, " \t>")
		if j < 0 {
			return rest
		}
		return rest[:j]
	}
	return ""
}

// htmlImg 提取第一个 img 标签的 src/alt(传整个片段时取首个)。
func htmlImg(s string) (src, alt string) {
	lower := strings.ToLower(s)
	i := strings.Index(lower, "<img")
	if i < 0 {
		return "", ""
	}
	rest := s[i:]
	if j := strings.IndexByte(rest, '>'); j >= 0 {
		rest = rest[:j+1]
	} else {
		rest = rest + ">"
	}
	return htmlAttr(rest, "src"), strings.TrimSpace(htmlAttr(rest, "alt"))
}

// htmlCodeText 提取代码块 HTML 的纯文本(剥全部标签,保留换行)。
func htmlCodeText(s string) string {
	var b strings.Builder
	for s != "" {
		lt := strings.IndexByte(s, '<')
		if lt < 0 {
			b.WriteString(html.UnescapeString(s))
			break
		}
		if lt > 0 {
			b.WriteString(html.UnescapeString(s[:lt]))
		}
		end := strings.IndexByte(s[lt:], '>')
		if end < 0 {
			break
		}
		tag := strings.ToLower(s[lt : lt+end+1])
		s = s[lt+end+1:]
		if strings.HasPrefix(tag, "<br") {
			b.WriteString("\n")
		}
	}
	return strings.Trim(b.String(), "\n")
}

// htmlBlockTags 块级标签集合(命中才做块级处理,其余一律并入行内缓冲)。
var htmlBlockTags = map[string]bool{
	"p": true, "h1": true, "h2": true, "h3": true, "h4": true, "h5": true,
	"h6": true, "h7": true, "h8": true, "h9": true,
	"li": true, "ul": true, "ol": true, "blockquote": true, "pre": true,
	"hr": true, "br": true, "img": true, "div": true, "table": true,
	"tr": true, "td": true, "th": true, "section": true, "article": true,
}

// htmlBlockToMarkdown 整篇 HTML → Markdown 的粗转换(块级标签转结构,行内片段交给行内转换)。
// 供钉钉 blocks 接口「整篇 HTML」兼容形态使用。
func htmlBlockToMarkdown(s string) string {
	var b strings.Builder
	var inl strings.Builder // 待行内转换的缓冲(文本 + 行内标签)
	flush := func() {
		if inl.Len() > 0 {
			b.WriteString(htmlInlineToMarkdown(inl.String()))
			inl.Reset()
		}
	}
	for s != "" {
		lt := strings.IndexByte(s, '<')
		if lt < 0 {
			inl.WriteString(s)
			break
		}
		if lt > 0 {
			inl.WriteString(s[:lt])
		}
		end := strings.IndexByte(s[lt:], '>')
		if end < 0 {
			inl.WriteString(s[lt:])
			break
		}
		tag := strings.ToLower(strings.TrimSpace(s[lt : lt+end+1]))
		raw := s[lt : lt+end+1]
		s = s[lt+end+1:]
		name := htmlTagName(tag)
		if !htmlBlockTags[name] {
			// 行内标签(strong/em/code/a/u/s...)原样入缓冲,与相邻文本一起转换
			inl.WriteString(raw)
			continue
		}
		flush()
		closing := strings.HasPrefix(tag, "</")
		switch name {
		case "br":
			b.WriteString("\n")
		case "li":
			if !closing {
				b.WriteString("\n- ")
			}
		case "h1", "h2", "h3", "h4", "h5", "h6", "h7", "h8", "h9":
			if closing {
				b.WriteString("\n\n")
			} else {
				b.WriteString("\n\n" + strings.Repeat("#", int(name[1]-'0')) + " ")
			}
		case "p":
			if closing {
				b.WriteString("\n\n")
			}
		case "blockquote":
			if closing {
				b.WriteString("\n")
			} else {
				b.WriteString("> ")
			}
		case "pre":
			// 代码块整体围栏;行内的 <code> 由行内转换处理,这里不再重复起围栏。
			if closing {
				b.WriteString("\n")
			} else {
				b.WriteString("```\n")
			}
		case "img":
			if !closing {
				if src, alt := htmlImg(tag); src != "" {
					b.WriteString("![" + alt + "](" + src + ")")
				}
			}
		case "hr":
			if !closing {
				b.WriteString("\n\n---\n\n")
			}
		}
	}
	flush()
	return tidyMarkdown(b.String())
}

// tidyMarkdown 收尾清理:去首尾空白、行尾空格、压缩 3+ 连续空行。
func tidyMarkdown(s string) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = strings.TrimRight(l, " \t")
	}
	out := strings.Trim(strings.Join(lines, "\n"), "\n")
	for strings.Contains(out, "\n\n\n") {
		out = strings.ReplaceAll(out, "\n\n\n", "\n\n")
	}
	if out != "" {
		out += "\n"
	}
	return out
}
