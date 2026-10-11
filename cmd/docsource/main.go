// docsource —— 知识生态文档拉取 CLI。
//
// 从飞书 / 钉钉 / 腾讯文档 / Notion 拉取文档清单与正文,统一输出 Markdown。
// 配置默认读环境变量,--config config.json 覆盖(段内出现的字段才覆盖,缺省沿用环境变量)。
//
// 用法:
//
//	docsource providers                              列出已配置的 provider
//	docsource list --provider feishu                 列出文档清单(TSV)
//	docsource fetch --provider notion --id <id> [--out file.md]
//	                                                 拉取单篇(--out 缺省输出到 stdout)
//	docsource pull-all --out ./docs [--provider X] [--concurrency 4]
//	                                                 全量拉到本地 markdown 目录(文件名 slug 化防重)
//
// 全局 flag:--config path(四个子命令都支持)。
//
// 退出码:0 成功;1 运行错误(拉取失败/凭据缺失等);2 用法错误(未知子命令/缺参数)。
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/aiarsenal/runtime-go/knowledge"
)

const (
	exitOK    = 0
	exitErr   = 1
	exitUsage = 2
)

const usageText = `docsource —— 知识生态文档拉取 CLI

用法:
  docsource providers                                  列出已配置的 provider 与所需凭据
  docsource list --provider <name>                     列出文档清单(TSV: id/title/space/updated)
  docsource fetch --provider <name> --id <id> [--out <file.md>]
                                                       拉取单篇文档(--out 缺省输出 stdout)
  docsource pull-all --out <dir> [--provider <name>] [--concurrency N] [--config config.json]
                                                       全量拉取到本地 markdown 目录

provider:feishu / dingtalk / tdoc / notion(仅凭据齐全的可用)

凭据(环境变量或 --config config.json):
  feishu   FEISHU_APP_ID / FEISHU_APP_SECRET [/FEISHU_FOLDER_TOKEN] [/FEISHU_RECURSE_DEPTH]
  dingtalk DINGTALK_ACCESS_TOKEN(或 DINGTALK_APP_KEY/DINGTALK_APP_SECRET)/ DINGTALK_SPACE_ID [/DINGTALK_FOLDER_ID]
  tdoc     TDOC_ACCESS_TOKEN [/TDOC_FOLDER_ID]
  notion   NOTION_TOKEN [/NOTION_ROOT_PAGE]

config.json 示例:
  {
    "feishu":  {"app_id": "cli_xxx", "app_secret": "xxx", "folder_token": "FldcnXXX"},
    "notion":  {"token": "secret_xxx", "root_page": "32-char-page-id"}
  }
`

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	if len(args) == 0 {
		fmt.Fprint(os.Stderr, usageText)
		return exitUsage
	}
	cmd, rest := args[0], args[1:]
	switch cmd {
	case "providers":
		return cmdProviders(rest)
	case "list":
		return cmdList(rest)
	case "fetch":
		return cmdFetch(rest)
	case "pull-all":
		return cmdPullAll(rest)
	case "help", "-h", "--help":
		fmt.Print(usageText)
		return exitOK
	default:
		fmt.Fprintf(os.Stderr, "docsource: 未知子命令 %q\n\n%s", cmd, usageText)
		return exitUsage
	}
}

// fs 建一个统一风格的 FlagSet,并挂上各子命令公共的 --config。
func fs(name string, configPath *string) *flag.FlagSet {
	f := flag.NewFlagSet(name, flag.ContinueOnError)
	f.SetOutput(os.Stderr)
	f.StringVar(configPath, "config", "", "配置文件(JSON),覆盖环境变量中的对应字段")
	return f
}

// loadConfig 按 --config(可空)加载配置:环境变量打底,JSON 覆盖。
func loadConfig(configPath string) (knowledge.Config, error) {
	if configPath == "" {
		return knowledge.ConfigFromEnv(), nil
	}
	raw, err := os.ReadFile(configPath)
	if err != nil {
		return knowledge.Config{}, fmt.Errorf("读配置文件: %w", err)
	}
	return knowledge.ConfigFromJSON(raw)
}

// newManager 构造 Manager(配置无任何平台凭据时报错)。
func newManager(configPath string) (*knowledge.Manager, error) {
	cfg, err := loadConfig(configPath)
	if err != nil {
		return nil, err
	}
	return knowledge.NewManager(cfg)
}

// cmdProviders 列出已配置的 provider。
func cmdProviders(args []string) int {
	var configPath string
	f := fs("providers", &configPath)
	if err := f.Parse(args); err != nil {
		return exitUsage
	}
	m, err := newManager(configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "docsource: %v\n", err)
		return exitErr
	}
	for _, name := range m.Providers() {
		fmt.Println(name)
	}
	return exitOK
}

// cmdList 列出文档清单,TSV 输出:id \t title \t space \t updated。
func cmdList(args []string) int {
	var configPath, provider string
	f := fs("list", &configPath)
	f.StringVar(&provider, "provider", "", "provider 名:feishu/dingtalk/tdoc/notion(必填)")
	if err := f.Parse(args); err != nil {
		return exitUsage
	}
	if provider == "" {
		fmt.Fprintln(os.Stderr, "docsource: list 需要 --provider")
		return exitUsage
	}
	m, err := newManager(configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "docsource: %v\n", err)
		return exitErr
	}
	metas, err := m.List(context.Background(), provider)
	if err != nil {
		fmt.Fprintf(os.Stderr, "docsource: %v\n", err)
		return exitErr
	}
	for _, md := range metas {
		updated := "-"
		if !md.UpdatedAt.IsZero() {
			updated = md.UpdatedAt.Format("2006-01-02 15:04:05")
		}
		title := strings.ReplaceAll(md.Title, "\t", " ")
		space := strings.ReplaceAll(md.Space, "\t", " ")
		fmt.Printf("%s\t%s\t%s\t%s\n", md.ID, title, space, updated)
	}
	fmt.Fprintf(os.Stderr, "docsource: 共 %d 篇\n", len(metas))
	return exitOK
}

// cmdFetch 拉取单篇文档:--out 写文件,否则输出 stdout。
func cmdFetch(args []string) int {
	var configPath, provider, id, out string
	f := fs("fetch", &configPath)
	f.StringVar(&provider, "provider", "", "provider 名(必填)")
	f.StringVar(&id, "id", "", "文档 id(必填,list 命令的第一列)")
	f.StringVar(&out, "out", "", "输出文件路径(缺省 stdout)")
	if err := f.Parse(args); err != nil {
		return exitUsage
	}
	if provider == "" || id == "" {
		fmt.Fprintln(os.Stderr, "docsource: fetch 需要 --provider 与 --id")
		return exitUsage
	}
	m, err := newManager(configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "docsource: %v\n", err)
		return exitErr
	}
	doc, err := m.Fetch(context.Background(), provider, id)
	if err != nil {
		fmt.Fprintf(os.Stderr, "docsource: %v\n", err)
		return exitErr
	}
	if out == "" {
		_, _ = os.Stdout.Write(doc.Content)
		return exitOK
	}
	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "docsource: 建目录失败: %v\n", err)
		return exitErr
	}
	if err := os.WriteFile(out, doc.Content, 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "docsource: 写文件失败: %v\n", err)
		return exitErr
	}
	fmt.Printf("%s\t%d bytes\t%s\n", out, len(doc.Content), doc.Title)
	return exitOK
}

// cmdPullAll 全量拉取到本地目录:文件名 slug 化,重名自动 -2/-3,首个失败即取消(fail-fast)。
func cmdPullAll(args []string) int {
	var configPath, provider, outDir string
	concurrency := 4
	f := fs("pull-all", &configPath)
	f.StringVar(&provider, "provider", "", "provider 名(缺省=全部已配置的)")
	f.StringVar(&outDir, "out", "", "输出目录(必填)")
	f.IntVar(&concurrency, "concurrency", 4, "并发拉取数")
	if err := f.Parse(args); err != nil {
		return exitUsage
	}
	if outDir == "" {
		fmt.Fprintln(os.Stderr, "docsource: pull-all 需要 --out <dir>")
		return exitUsage
	}
	if concurrency <= 0 {
		concurrency = 4
	}
	m, err := newManager(configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "docsource: %v\n", err)
		return exitErr
	}
	providers := []string{}
	if provider != "" {
		if _, err := m.Provider(provider); err != nil {
			fmt.Fprintf(os.Stderr, "docsource: %v\n", err)
			return exitUsage
		}
		providers = append(providers, provider)
	} else {
		providers = m.Providers()
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "docsource: 建目录失败: %v\n", err)
		return exitErr
	}

	ctx := context.Background()
	total, failed := 0, 0
	taken := map[string]bool{} // 已占用的文件名(不含扩展名),跨 provider 防重
	for _, name := range providers {
		written := 0
		err := m.FetchAll(ctx, name, concurrency, func(doc *knowledge.Document) error {
			base := knowledge.Slugify(firstNonEmpty(doc.Title, doc.ID), 0)
			filename := knowledge.Uniquify(base, taken) + ".md"
			path := filepath.Join(outDir, filename)
			if err := os.WriteFile(path, doc.Content, 0o644); err != nil {
				return fmt.Errorf("写文件 %s: %w", path, err)
			}
			written++
			total++
			fmt.Printf("%s\t%d bytes\t%s\n", path, len(doc.Content), doc.Title)
			return nil
		})
		if err != nil {
			fmt.Fprintf(os.Stderr, "docsource: %s 拉取中断(已写出 %d 篇): %v\n", name, written, err)
			failed++
			break // fail-fast:errgroup 语义,首个错误即停止
		}
		fmt.Fprintf(os.Stderr, "docsource: %s 完成,共 %d 篇\n", name, written)
	}
	if failed > 0 {
		return exitErr
	}
	fmt.Fprintf(os.Stderr, "docsource: 全部完成,共 %d 篇 → %s\n", total, outDir)
	return exitOK
}

// firstNonEmpty 与包内同名语义(本地小工具,避免导出)。
func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
