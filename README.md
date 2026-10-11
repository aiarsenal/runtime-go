# runtime-go

统一的 Agent 运行时适配 SDK（Go）。无论 agent 实例跑在**本地 Docker、本机进程、腾讯云、阿里云还是 Kubernetes**，调用方只与同一个 `Adapter` 接口对话，底层 provider 透明可换。

> 控制面采用命令队列模型（心跳 + 幂等 + 状态机）；本 SDK 只负责「一条命令对底层资源的具体执行」，不涉及命令调度。

## 为什么需要它

Agent 控制平面要管理 agent 实例的生命周期、shell、文件、备份。但实例可能跑在不同地方：

| 场景 | provider |
|---|---|
| 单机/测试环境 | `localdocker`（本机 Docker） |
| 桌面客户端嵌入式 | `localprocess`（本机 spawn 子进程） |
| 生产弹性 | `tencent`（腾讯云 ECI/CVM）、`aliyun`（阿里云 ECI/ECS） |
| 已有 K8s 集群 | `k8s`（client-go） |

每换一种底层不希望改业务代码。`runtime-go` 把差异收敛到一个接口后面。

## 接口

```go
type Adapter interface {
    Provider() string

    // 生命周期
    Create(ctx, spec InstanceSpec) (instanceID, error)
    Start(ctx, instanceID) error
    Stop(ctx, instanceID) error
    Restart(ctx, instanceID) error
    Remove(ctx, instanceID) error
    Status(ctx, instanceID) (*Status, error)

    // 访问
    Exec(ctx, instanceID, ExecOpts) ([]byte, error)
    Logs(ctx, instanceID, LogOpts) (io.ReadCloser, error)
    Shell(ctx, instanceID, ShellOpts) (ReadWriteCloser, error)  // 交互式 PTY

    // 文件（工作区）
    ListFiles / UploadFile / DownloadFile / Mkdir / RenameFile / DeleteFile

    // 存储
    CreateVolume / RemoveVolume / Backup

    // 健康
    Inspect / Metrics

    Close() error
}
```

状态枚举：`0=creating 1=running 2=stopped 3=error 4=deleting`。

## 用法

```go
import (
    "github.com/aiarsenal/runtime-go"
    _ "github.com/aiarsenal/runtime-go/localdocker"  // 注册 provider
)

adapter, _ := runtime.New(runtime.Config{
    Type: "localdocker",
    Params: map[string]any{},
})
defer adapter.Close()

id, _ := adapter.Create(ctx, runtime.InstanceSpec{
    Name: "agent-1", Type: "webtop",
    Image: "alpine:latest", Cmd: []string{"sleep", "3600"},
    CPU: 2, MemoryMB: 2048,
    Ports: []runtime.PortMap{{HostPort: 3000, ContainerPort: 3000}},
})

out, _ := adapter.Exec(ctx, id, runtime.ExecOpts{Cmd: []string{"echo", "hi"}})
adapter.Remove(ctx, id)
```

切换到腾讯云只改 `Config`：

```go
adapter, _ := runtime.New(runtime.Config{
    Type: "tencent",
    Params: map[string]any{
        "mode": "cvm", "region": "ap-guangzhou",
        "secretId": "...", "secretKey": "...",
    },
})
```

跑示例（需本机 Docker）：

```bash
go run ./cmd/example
```

## Provider 状态

| Provider | 状态 | 说明 |
|---|---|---|
| `localdocker` | ✅ 完整可用 | docker SDK，端到端验证：Create/Start/Stop/Restart/Remove/Status/Exec/Logs/Shell/文件/Volume/Backup/Inspect/Metrics |
| `localprocess` | ✅ 完整可用 | 本机子进程模型（移植自 desktop 客户端），Shell 不支持 |
| `k8s` | 🟡 骨架 | client-go，方法签名齐全，待填充 |
| `tencent` | 🔶 CVM 可用 | 腾讯云 SDK 已接入：CVM 生命周期（Create/Start/Stop/Restart/Remove/Status、公网 IP、UserData）已实现，ECI 与 Exec/文件等待填充 |
| `aliyun` | 🟡 骨架 | 阿里云 ECI/ECS，方法签名齐全，待接 SDK |
| `storage` | ✅ 可用 | Volume/Backup 存储后端：本地磁盘 + S3 兼容对象存储（OSS / COS / MinIO / AWS S3） |

骨架 provider 的方法返回 `ErrNotImplemented`，接口契约已定，接入云 SDK 时逐方法填充，调用方零改动。

## 设计要点

- **注册表 + 工厂**：provider 包在 `init()` 里 `runtime.Register(name, factory)`；`runtime.New(Config)` 按 `Type` 实例化。
- **幂等可重试**：`Create` 已存在则幂等返回；`Remove` 不存在则成功。
- **错误约定**：不存在返回 `*runtime.ErrNotFound`（`runtime.IsNotFound(err)` 判断）。
- **命名约定**（localdocker）：容器 `aiarsenal-<instanceID>`，卷 `aiarsenal-vol-<volumeID>`。

## 知识生态接入（docsource CLI）

Agent 需要读知识库文档：飞书、钉钉、腾讯文档、Notion。`knowledge/` 包把四个平台收敛到一个 `Provider` 接口后面，统一拉取文档清单与正文，统一输出 Markdown；`cmd/docsource` 提供开箱即用的 CLI。

零第三方依赖：全部用标准库 `net/http` + `encoding/json` 实现，各平台 open API 直连。

### Provider 状态

| Provider | 状态 | 依赖的开放平台接口 | 说明与限制 |
|---|---|---|---|
| `feishu` | ✅ 可用 | `POST /open-apis/auth/v3/tenant_access_token/internal`（租户 token）→ `GET /open-apis/drive/v1/files`（文件夹清单，递归）→ `GET /open-apis/docx/v1/documents/:id/blocks`（块结构）→ 兜底 `GET /open-apis/docx/v1/documents/:id/raw_content`（纯文本） | 仅支持新版 docx 文档；应用需开通「云文档」权限并被添加为文件夹协作者（或知识库成员）；图片仅输出占位（token 需素材下载接口另取） |
| `dingtalk` | ✅ 可用 | `POST /v1.0/oauth2/accessToken` → `POST /v2.0/storage/dentries/lists`（节点清单）→ `GET /v1.0/document/documents/:docId`（元信息）→ `GET /v1.0/document/documents/:docId/blocks`（块内容，HTML） | 需企业内部应用开通「文档」权限并经管理员审批后生效；接口 QPS 限流 2 次/秒；仅在线文档可拉（表格/思维导图等不支持）；HTML 块经内置轻量转换器转 Markdown |
| `tdoc` | ✅ 可用 | `GET /openapi/drive/v2/files`（文件夹清单）→ `POST /openapi/document/export`（异步导出，`exportType=2` docx）→ `GET /openapi/operation?id=...`（轮询任务）→ 下载 docx → 内置 OOXML 解析转 Markdown | 腾讯文档开放平台不提供直出 Markdown 接口，走「导出 docx + 本地解析」路线；导出为异步任务（默认轮询 90s）；智能表格/幻灯片等类型不可导出；Access-Token 为用户态凭据（OAuth 流程外部获取） |
| `notion` | ✅ 可用 | `POST /v1/search`（工作区检索）或 `GET /v1/blocks/:id/children`（RootPage 递归）→ `GET /v1/pages/:id` + `GET /v1/blocks/:id/children`（正文） | `Notion-Version: 2022-06-28`；integration 只能看到被显式授权的页面（页面右上角 Connections 里添加）；两种列表模式：工作区 search 或 `NOTION_ROOT_PAGE` 指定根页面递归；块树 → Markdown 渲染四个平台中最完整（标题/列表/to_do/引用/代码/callout/toggle/图片/书签/子页面/公式/表格） |

### 凭据获取

- **飞书**：[飞书开放平台](https://open.feishu.cn) → 开发者后台 → 创建企业自建应用 → 凭据与基础信息拿 `App ID` / `App Secret` → 权限管理开通「云文档」相关 scope（`docs:doc:readonly` 等）→ 文档所在文件夹添加应用为协作者；`FOLDER_TOKEN` 取文件夹 URL 路径中的 token。
- **钉钉**：[钉钉开放平台](https://open.dingtalk.com) → 创建企业内部应用 → 拿 `AppKey` / `AppSecret`（或直接用已签发的 `accessToken`）→ 申请「文档」API 权限并等待企业审批；`SPACE_ID` 从文档空间 URL 获取。
- **腾讯文档**：[腾讯文档开放平台](https://docs.qq.com/open) → 创建应用 → OAuth2 授权拿用户态 `access_token`；`FOLDER_ID` 从文件夹 URL 获取。
- **Notion**：[notion.so/my-integrations](https://www.notion.so/my-integrations) → 创建 Internal Integration → 拿 `Internal Integration Secret`（`ntn_` 开头）→ 在目标页面右上角 `•••` → Connections 添加该 integration；`NOTION_ROOT_PAGE` 填根页面 id（32 位）。

### 环境变量

| Provider | 环境变量 |
|---|---|
| `feishu` | `FEISHU_APP_ID` / `FEISHU_APP_SECRET` [/`FEISHU_FOLDER_TOKEN`] [/`FEISHU_RECURSE_DEPTH`] |
| `dingtalk` | `DINGTALK_ACCESS_TOKEN`（或 `DINGTALK_APP_KEY`/`DINGTALK_APP_SECRET`）[/`DINGTALK_SPACE_ID`] [/`DINGTALK_FOLDER_ID`] |
| `tdoc` | `TDOC_ACCESS_TOKEN` [/`TDOC_FOLDER_ID`] |
| `notion` | `NOTION_TOKEN` [/`NOTION_ROOT_PAGE`] |

或用 `--config config.json`（JSON 段内出现的字段覆盖环境变量，缺省沿用环境变量）：

```json
{
  "feishu": {"app_id": "cli_xxx", "app_secret": "xxx", "folder_token": "FldcnXXX"},
  "notion": {"token": "ntn_xxx", "root_page": "32-char-page-id"}
}
```

### CLI 用法

```bash
go install github.com/aiarsenal/runtime-go/cmd/docsource@latest

docsource providers                      # 列出已配置的 provider（凭据齐全才出现）
docsource list --provider feishu         # 文档清单（TSV: id/title/space/updated）
docsource fetch --provider notion --id <page-id> --out doc.md
docsource pull-all --out ./docs          # 全量拉到本地 markdown 目录
docsource pull-all --out ./docs --provider feishu --concurrency 8
docsource pull-all --out ./docs --config config.json
```

`pull-all` 文件名按标题 slug 化（保留中文），重名自动 `-2`/`-3` 防重；任一篇失败即整体停止（errgroup 语义），已写出的文件保留。退出码：`0` 成功 / `1` 运行错误 / `2` 用法错误。

### Go API 用法

```go
import "github.com/aiarsenal/runtime-go/knowledge"

mgr, _ := knowledge.NewManager(knowledge.ConfigFromEnv()) // 或 knowledge.Config{...}

metas, _ := mgr.List(ctx, "notion")             // 文档清单
doc, _ := mgr.Fetch(ctx, "notion", metas[0].ID) // 单篇正文(doc.Content 为 Markdown 字节)

// 并发全量拉取(手写 errgroup 语义:fail-fast + context 取消传播)
_ = mgr.FetchAll(ctx, "feishu", 4, func(doc *knowledge.Document) error {
    return os.WriteFile(doc.ID+".md", doc.Content, 0o644)
})
```

## License

MIT
