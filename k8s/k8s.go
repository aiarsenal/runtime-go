// Package k8s 实现 runtime.Adapter，通过 Kubernetes 管理 agent 实例。
//
// 这是 ClawManager 的原生模型：instance = Pod（或 Deployment），经 client-go 操作。
// 适合已有 K8s 集群的部署场景；单机/测试环境用 localdocker 更轻。
//
// ⚠️ 本包为接口骨架：实现了 Adapter 全部方法签名，底层 client-go 调用待接入。
// Create/Start 等返回 ErrNotImplemented。
//
// 配置（Config.Params）：
//
//	kubeconfig : kubeconfig 文件路径（空则用 InClusterConfig 或 ~/.kube/config）
//	namespace  : 默认命名空间（空为 default）
package k8s

import (
	"context"
	"errors"
	"io"

	"github.com/aiarsenal/runtime-go"
)

const providerName = "k8s"

// ErrNotImplemented K8s API 尚未接入。
var ErrNotImplemented = errors.New("k8s: client-go calls not yet implemented (skeleton adapter)")

func init() {
	runtime.Register(providerName, FromConfig)
}

// Config K8s 适配器配置。
type Config struct {
	Kubeconfig string
	Namespace  string
}

// FromConfig 从通用 Config 构造 k8s Adapter。
func FromConfig(cfg runtime.Config) (runtime.Adapter, error) {
	c := Config{
		Kubeconfig: str(cfg.Params["kubeconfig"]),
		Namespace:  str(cfg.Params["namespace"]),
	}
	if c.Namespace == "" {
		c.Namespace = "default"
	}
	return &Adapter{cfg: c}, nil
}

// Adapter K8s 适配器（骨架）。
type Adapter struct{ cfg Config }

func (a *Adapter) Provider() string { return providerName }

func (a *Adapter) Create(ctx context.Context, spec runtime.InstanceSpec) (string, error) {
	// TODO: client-go 创建 Pod/Deployment + Service + PVC。
	return "", ErrNotImplemented
}
func (a *Adapter) Start(ctx context.Context, instanceID string) error {
	// K8s 无「停启」语义：对应 scale replicas 0/1，或删除/重建 Pod。
	return ErrNotImplemented
}
func (a *Adapter) Stop(ctx context.Context, instanceID string) error    { return ErrNotImplemented }
func (a *Adapter) Restart(ctx context.Context, instanceID string) error { return ErrNotImplemented }
func (a *Adapter) Remove(ctx context.Context, instanceID string) error  { return ErrNotImplemented }
func (a *Adapter) Status(ctx context.Context, instanceID string) (*runtime.Status, error) {
	return nil, ErrNotImplemented
}
func (a *Adapter) Exec(ctx context.Context, instanceID string, opts runtime.ExecOpts) ([]byte, error) {
	// TODO: CoreV1().Pods().Exec，经 remotecommand。
	return nil, ErrNotImplemented
}
func (a *Adapter) Logs(ctx context.Context, instanceID string, opts runtime.LogOpts) (io.ReadCloser, error) {
	// TODO: CoreV1().Pods().GetLogs（支持 Follow）。
	return nil, ErrNotImplemented
}
func (a *Adapter) Shell(ctx context.Context, instanceID string, opts runtime.ShellOpts) (runtime.ReadWriteCloser, error) {
	// TODO: remotecommand.SPDY + TerminalSizeQueue。
	return nil, ErrNotImplemented
}
func (a *Adapter) ListFiles(ctx context.Context, instanceID, p string) ([]runtime.FileEntry, error) {
	return nil, ErrNotImplemented
}
func (a *Adapter) UploadFile(ctx context.Context, instanceID, p string, content io.Reader, mode int64) error {
	return ErrNotImplemented
}
func (a *Adapter) DownloadFile(ctx context.Context, instanceID, p string) (io.ReadCloser, error) {
	return nil, ErrNotImplemented
}
func (a *Adapter) Mkdir(ctx context.Context, instanceID, p string) error {
	return ErrNotImplemented
}
func (a *Adapter) RenameFile(ctx context.Context, instanceID, oldPath, newPath string) error {
	return ErrNotImplemented
}
func (a *Adapter) DeleteFile(ctx context.Context, instanceID, p string) error {
	return ErrNotImplemented
}
func (a *Adapter) CreateVolume(ctx context.Context, instanceID, mountPath string, sizeGB int) (string, error) {
	// TODO: 创建 PVC。
	return "", ErrNotImplemented
}
func (a *Adapter) RemoveVolume(ctx context.Context, volumeID string) error { return ErrNotImplemented }
func (a *Adapter) Backup(ctx context.Context, instanceID, name string) (*runtime.Backup, error) {
	return nil, ErrNotImplemented
}
func (a *Adapter) Inspect(ctx context.Context, instanceID string) (*runtime.InstanceDetail, error) {
	return nil, ErrNotImplemented
}
func (a *Adapter) Metrics(ctx context.Context, instanceID string) (*runtime.Metrics, error) {
	return nil, ErrNotImplemented
}
func (a *Adapter) Close() error { return nil }

func str(v any) string {
	if v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}
