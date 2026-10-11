package knowledge

import (
	"fmt"
	"hash/fnv"
	"strings"
	"unicode"
)

// 本文件提供 pull-all 落盘时的文件名 slug 化与防重。

const defaultSlugMaxLen = 60

// Slugify 把文档标题转成安全文件名:
//   - 空白与常见分隔符折叠为单个 '-',其余标点丢弃;
//   - 保留 Unicode 字母(含中文)与数字,ASCII 大写转小写;
//   - 按 rune 截断到 maxLen(<=0 用默认 60),首尾 '-' 去掉;
//   - 结果为空时回退 "doc"。
//
// 例:"第 1 章:检索与排序(v2)" → "第1章-检索与排序-v2"。
func Slugify(title string, maxLen int) string {
	if maxLen <= 0 {
		maxLen = defaultSlugMaxLen
	}
	var b strings.Builder
	lastDash := true // 起始侧的 '-' 不写
	for _, r := range strings.TrimSpace(title) {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			if r >= 'A' && r <= 'Z' {
				r = r + ('a' - 'A')
			}
			b.WriteRune(r)
			lastDash = false
		case unicode.IsSpace(r) || r == '-' || r == '_' || r == '.' || r == '/' ||
			r == '\\' || r == ':' || r == ',' || r == ';' || r == '(' || r == ')' ||
			r == '[' || r == ']' || r == '、' || r == '·':
			if !lastDash {
				b.WriteByte('-')
				lastDash = true
			}
		default:
			// 其他标点/符号丢弃
		}
	}
	out := strings.Trim(b.String(), "-")
	// 按 rune 截断后再去尾 '-',避免截断半个词或留下悬空连字符
	runes := []rune(out)
	if len(runes) > maxLen {
		out = string(runes[:maxLen])
		out = strings.Trim(out, "-")
	}
	if out == "" {
		out = "doc"
	}
	return out
}

// Uniquify 在 taken(已占用文件名集合,不含扩展名)上给 base 去重:
// 依次尝试 base、base-2、base-3…;冲突超过 100 次后改加 8 位 fnv 短哈希。
// 占用的名字会写回 taken。
func Uniquify(base string, taken map[string]bool) string {
	if !taken[base] {
		taken[base] = true
		return base
	}
	for i := 2; i < 100; i++ {
		cand := fmt.Sprintf("%s-%d", base, i)
		if !taken[cand] {
			taken[cand] = true
			return cand
		}
	}
	h := fnv.New32a()
	_, _ = h.Write([]byte(base))
	cand := fmt.Sprintf("%s-%08x", base, h.Sum32())
	taken[cand] = true
	return cand
}
