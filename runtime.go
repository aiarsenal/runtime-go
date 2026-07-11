// Package runtime 提供统一的 Agent 运行时适配接口。
//
// 无论 agent 实例跑在本地 Docker、本机进程、腾讯云、阿里云还是 K8s，
// 调用方都只与本包的 Adapter 接口对话，底层 provider 透明可换。
//
// 设计目标：
//   - 控制面协议沿用 ClawManager 的命令队列模型（心跳 + 幂等 + 状态机）；
//     本包只负责「一条命令对底层资源的具体执行」，不涉及命令调度。
//   - 接口覆盖生命周期 / 访问(exec·logs·shell·文件) / 存储(volume·backup) / 健康。
//   - 每个 provider 实现同一接口，api 层零改动切换底层。
package runtime

import (
	"context"
	"io"
	"time"
)

// Adapter 是所有 runtime provider 必须实现的统一接口。
// 实现方应保证：方法幂等可重试；不存在时返回 ErrNotFound；底层不可用时返回包装错误。
type Adapter interface {
	// Provider 返回 provider 标识（localdocker / localprocess / tencent / aliyun / k8s）。
	Provider() string

	// ---- 生命周期 ----

	// Create 按规格创建一个实例（容器/进程/云主机），返回实例业务 id。
	Create(ctx context.Context, spec InstanceSpec) (string, error)
	// Start 启动已停止的实例。
	Start(ctx context.Context, instanceID string) error
	// Stop 停止实例（保留资源，可再 Start）。
	Stop(ctx context.Context, instanceID string) error
	// Restart 重启实例。
	Restart(ctx context.Context, instanceID string) error
	// Remove 删除实例及其关联资源（按 Cascade 级联）。
	Remove(ctx context.Context, instanceID string) error
	// Status 查询实例当前状态。
	Status(ctx context.Context, instanceID string) (*Status, error)

	// ---- 访问 ----

	// Exec 在实例内执行命令，同步返回 stdout/stderr 合并输出。
	Exec(ctx context.Context, instanceID string, opts ExecOpts) ([]byte, error)
	// Logs 返回实例日志流（调用方负责关闭）。
	Logs(ctx context.Context, instanceID string, opts LogOpts) (io.ReadCloser, error)
	// Shell 附加交互式终端；返回的双向流连接到实例的 PTY。调用方负责关闭。
	Shell(ctx context.Context, instanceID string, opts ShellOpts) (ReadWriteCloser, error)

	// ---- 文件（工作区） ----

	ListFiles(ctx context.Context, instanceID, path string) ([]FileEntry, error)
	UploadFile(ctx context.Context, instanceID, path string, content io.Reader, mode int64) error
	DownloadFile(ctx context.Context, instanceID, path string) (io.ReadCloser, error)
	Mkdir(ctx context.Context, instanceID, path string) error
	RenameFile(ctx context.Context, instanceID, oldPath, newPath string) error
	DeleteFile(ctx context.Context, instanceID, path string) error

	// ---- 存储 ----

	// CreateVolume 创建持久卷并挂载到实例的 mountPath（已存在则幂等返回）。
	CreateVolume(ctx context.Context, instanceID, mountPath string, sizeGB int) (string, error)
	// RemoveVolume 删除持久卷。
	RemoveVolume(ctx context.Context, volumeID string) error
	// Backup 创建实例快照/备份，返回备份标识与位置。
	Backup(ctx context.Context, instanceID, name string) (*Backup, error)

	// ---- 健康 ----

	// Inspect 返回实例完整元数据（镜像/资源/网络/挂载等）。
	Inspect(ctx context.Context, instanceID string) (*InstanceDetail, error)
	// Metrics 返回实例最近资源用量。
	Metrics(ctx context.Context, instanceID string) (*Metrics, error)

	// ---- 管理 ----

	// Close 释放适配器持有的底层连接（如 docker client、云 SDK client）。
	Close() error
}

// InstanceSpec 创建实例规格。
type InstanceSpec struct {
	Name        string            // 实例名（同 owner 下唯一）
	Type        string            // openclaw / hermes / ubuntu / webtop / custom
	RuntimeType string            // desktop / shell / gateway
	Mode        string            // lite / full
	Image       string            // 镜像引用（localdocker/k8s 用）
	Cmd         []string          // 启动命令（覆盖镜像默认）
	CPU         int               // CPU 核数
	MemoryMB    int               // 内存 MB
	DiskGB      int               // 磁盘 GB
	Env         map[string]string // 环境变量
	Ports       []PortMap         // 端口映射
	Volumes     []VolumeMount     // 挂载
	Labels      map[string]string // 标签/注解
}

// PortMap 端口映射（Host:Container）。
type PortMap struct {
	HostPort      int
	ContainerPort int
	Protocol      string // tcp / udp，空为 tcp
}

// VolumeMount 卷挂载。
type VolumeMount struct {
	VolumeID  string // 已有卷 id；空则按 MountPath 自动创建
	MountPath string
	ReadOnly  bool
}

// State 实例状态枚举（与 aa_agent_instance.status smallint 对齐：0=creating 1=running 2=stopped 3=error 4=deleting）。
type State int

const (
	StateCreating State = 0
	StateRunning  State = 1
	StateStopped  State = 2
	StateError    State = 3
	StateDeleting State = 4
)

// Status 实例状态快照。
type Status struct {
	InstanceID  string
	State       State
	ContainerID string // docker container id / pod name / 云实例 id
	IP          string
	Ports       []PortMap
	StartedAt   *time.Time
	Error       string
}

// InstanceDetail 完整元数据。
type InstanceDetail struct {
	Status
	Image        string
	CPU          int
	MemoryMB     int
	DiskGB       int
	Env          map[string]string
	Volumes      []VolumeMount
	ProviderInfo map[string]string // provider 特有字段（如云实例 region/zone）
}

// ExecOpts 执行命令选项。
type ExecOpts struct {
	Cmd     []string
	Env     map[string]string
	WorkDir string
	Timeout time.Duration // 0 = 不超时
	User    string        // 以哪个用户执行（空为默认）
}

// LogOpts 日志选项。
type LogOpts struct {
	Follow bool
	Tail   string // 行数，如 "100"；空为全部
	Since  time.Time
}

// ShellOpts 交互式终端选项。
type ShellOpts struct {
	Cmd     []string // 默认 /bin/sh 或镜像默认 shell
	Width   int
	Height  int
	Env     map[string]string
}

// FileEntry 文件/目录条目。
type FileEntry struct {
	Name    string
	Path    string
	IsDir   bool
	Size    int64
	Mode    int64
	ModTime time.Time
}

// Backup 备份描述。
type Backup struct {
	BackupID  string
	SizeGB    int
	Location  string // 存储位置（卷路径 / 对象存储 key）
	Status    string // creating / completed / failed
	CreatedAt time.Time
}

// Metrics 资源用量。
type Metrics struct {
	CPUPercent float64
	MemoryMB   float64
	DiskGB     float64
	GPUPercent float64
	UptimeSec  int64
	RecordedAt time.Time
}

// ReadWriteCloser 组合 io.ReadWriteCloser，供 Shell 双向流使用。
type ReadWriteCloser interface {
	io.Reader
	io.Writer
	io.Closer
}

// ErrNotFound 实例不存在。
type ErrNotFound struct {
	InstanceID string
}

func (e *ErrNotFound) Error() string { return "runtime: instance not found: " + e.InstanceID }

// IsNotFound 判断是否为「不存在」错误。
func IsNotFound(err error) bool {
	_, ok := err.(*ErrNotFound)
	return ok
}
