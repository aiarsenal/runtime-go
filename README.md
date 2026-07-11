# runtime-go

统一的 Agent 运行时适配 SDK（Go）。无论 agent 实例跑在**本地 Docker、本机进程、腾讯云、阿里云还是 Kubernetes**，调用方只与同一个 `Adapter` 接口对话，底层 provider 透明可换。

> 控制面协议沿用 ClawManager 的命令队列模型（心跳 + 幂等 + 状态机）；本 SDK 只负责「一条命令对底层资源的具体执行」，不涉及命令调度。

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
| `tencent` | 🟡 骨架 | 腾讯云 ECI/CVM，方法签名齐全，待接 SDK |
| `aliyun` | 🟡 骨架 | 阿里云 ECI/ECS，方法签名齐全，待接 SDK |

骨架 provider 的方法返回 `ErrNotImplemented`，接口契约已定，接入云 SDK 时逐方法填充，调用方零改动。

## 设计要点

- **注册表 + 工厂**：provider 包在 `init()` 里 `runtime.Register(name, factory)`；`runtime.New(Config)` 按 `Type` 实例化。
- **幂等可重试**：`Create` 已存在则幂等返回；`Remove` 不存在则成功。
- **错误约定**：不存在返回 `*runtime.ErrNotFound`（`runtime.IsNotFound(err)` 判断）。
- **命名约定**（localdocker）：容器 `aiarsenal-<instanceID>`，卷 `aiarsenal-vol-<volumeID>`。

## License

MIT
