package knowledge

import (
	"strings"
	"testing"
)

func TestSlugify(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"项目说明文档", "项目说明文档"},
		{"第 1 章:检索与排序(v2)", "第-1-章-检索与排序-v2"},
		{"Hello, World! 设计文档", "hello-world-设计文档"},
		{"  多个   空格  ", "多个-空格"},
		{"a/b\\c:d", "a-b-c-d"},
		{"UPPER-case", "upper-case"},
		{"!!!", "doc"}, // 全部被丢弃 → 回退
		{"", "doc"},
		{"--首尾--连字符--", "首尾-连字符"},
	}
	for _, c := range cases {
		if got := Slugify(c.in, 0); got != c.want {
			t.Errorf("Slugify(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestSlugifyMaxLength(t *testing.T) {
	long := strings.Repeat("长", 100)
	got := Slugify(long, 10)
	if n := len([]rune(got)); n != 10 {
		t.Fatalf("截断后 rune 数 = %d, want 10", n)
	}
	// 截断后不应以 '-' 结尾
	if strings.HasSuffix(got, "-") {
		t.Fatalf("截断后不应以 '-' 结尾: %q", got)
	}
}

func TestUniquify(t *testing.T) {
	taken := map[string]bool{}
	if got := Uniquify("文档", taken); got != "文档" {
		t.Fatalf("首次 = %q, want 文档", got)
	}
	if got := Uniquify("文档", taken); got != "文档-2" {
		t.Fatalf("第二次 = %q, want 文档-2", got)
	}
	if got := Uniquify("文档", taken); got != "文档-3" {
		t.Fatalf("第三次 = %q, want 文档-3", got)
	}
	if got := Uniquify("文档", taken); got != "文档-4" {
		t.Fatalf("第四次 = %q, want 文档-4", got)
	}
	// taken 集合应记录全部占用的名字
	for _, want := range []string{"文档", "文档-2", "文档-3", "文档-4"} {
		if !taken[want] {
			t.Errorf("taken 缺少 %q", want)
		}
	}
}
