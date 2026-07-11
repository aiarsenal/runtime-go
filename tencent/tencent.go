// Package tencent 实现 runtime.Adapter，通过腾讯云管理 agent 实例。
//
// 支持两种后端（按 Config.Mode 选择）：
//   - eci（弹性容器实例）：无服务器容器，按秒计费，适合短任务/弹性。
//   - cvm（云服务器）：常驻虚拟机，适合 webtop 桌面等长驻场景。
//
// ⚠️ 本包为接口骨架：实现了 Adapter 全部方法签名，但底层云 API 调用待接入
// 腾讯云 Go SDK（github.com/tencentcloud/tencentcloud-sdk-go）。
// 目前 Create/Start 等返回 ErrNotImplemented；接入 SDK 后逐方法填充。
//
// 配置（Config.Params）：
//
//	mode        : "eci" | "cvm"（必填）
//	region      : 地域，如 ap-guangzhou（必填）
//	secretId    : 腾讯云 API SecretId（必填，或走环境变量 TENCENTCLOUD_SECRET_ID）
//	secretKey   : 腾讯云 API SecretKey（必填，或走环境变量 TENCENTCLOUD_SECRET_KEY）
//	vpcId       : VPC Id
//	subnetId    : 子网 Id
//	securityGroupId : 安全组 Id
//	imageId     : 默认镜像 Id（openclaw/webtop 等）
package tencent

import (
	"context"
	"errors"
	"io"
	"os"

	"github.com/aiarsenal/runtime-go"
)

const providerName = "tencent"

// ErrNotImplemented 云 API 尚未接入。
var ErrNotImplemented = errors.New("tencent: cloud API not yet implemented (skeleton adapter)")

func init() {
	runtime.Register(providerName, FromConfig)
}

// Config 腾讯云适配器配置。
type Config struct {
	Mode            string // eci / cvm
	Region          string
	SecretID        string
	SecretKey       string
	VpcID           string
	SubnetID        string
	SecurityGroupID string
	DefaultImageID  string
}

// FromConfig 从通用 Config 构造 tencent Adapter。
func FromConfig(cfg runtime.Config) (runtime.Adapter, error) {
	c := Config{
		Mode:            str(cfg.Params["mode"]),
		Region:          str(cfg.Params["region"]),
		SecretID:        str(cfg.Params["secretId"]),
		SecretKey:       str(cfg.Params["secretKey"]),
		VpcID:           str(cfg.Params["vpcId"]),
		SubnetID:        str(cfg.Params["subnetId"]),
		SecurityGroupID: str(cfg.Params["securityGroupId"]),
		DefaultImageID:  str(cfg.Params["imageId"]),
	}
	if c.SecretID == "" {
		c.SecretID = os.Getenv("TENCENTCLOUD_SECRET_ID")
	}
	if c.SecretKey == "" {
		c.SecretKey = os.Getenv("TENCENTCLOUD_SECRET_KEY")
	}
	if c.Mode == "" || c.Region == "" {
		return nil, errors.New("tencent: mode and region required")
	}
	return &Adapter{cfg: c}, nil
}

// Adapter 腾讯云适配器（骨架）。
type Adapter struct{ cfg Config }

func (a *Adapter) Provider() string { return providerName }

func (a *Adapter) Create(ctx context.Context, spec runtime.InstanceSpec) (string, error) {
	// TODO: 接入 tencentcloud-sdk-go
	//   eci  → eciv20180408.RunInstances
	//   cvm  → cvm20170312.RunInstances
	return "", ErrNotImplemented
}

func (a *Adapter) Start(ctx context.Context, instanceID string) error   { return ErrNotImplemented }
func (a *Adapter) Stop(ctx context.Context, instanceID string) error    { return ErrNotImplemented }
func (a *Adapter) Restart(ctx context.Context, instanceID string) error { return ErrNotImplemented }
func (a *Adapter) Remove(ctx context.Context, instanceID string) error  { return ErrNotImplemented }
func (a *Adapter) Status(ctx context.Context, instanceID string) (*runtime.Status, error) {
	return nil, ErrNotImplemented
}

func (a *Adapter) Exec(ctx context.Context, instanceID string, opts runtime.ExecOpts) ([]byte, error) {
	// TODO: cvm 经 SSH；eci 经 InvokeCommand（腾讯云 TAT）。
	return nil, ErrNotImplemented
}

func (a *Adapter) Logs(ctx context.Context, instanceID string, opts runtime.LogOpts) (io.ReadCloser, error) {
	return nil, ErrNotImplemented
}

func (a *Adapter) Shell(ctx context.Context, instanceID string, opts runtime.ShellOpts) (runtime.ReadWriteCloser, error) {
	// TODO: cvm 经 SSH websocket；eci 不支持常驻 PTY。
	return nil, ErrNotImplemented
}

func (a *Adapter) ListFiles(ctx context.Context, instanceID, p string) ([]runtime.FileEntry, error) {
	// TODO: 经 COS（文件存对象存储）或 SSH。
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
	// TODO: cvm → CreateDisks；eci → 容器实例自带临时盘或挂 CBS。
	return "", ErrNotImplemented
}

func (a *Adapter) RemoveVolume(ctx context.Context, volumeID string) error { return ErrNotImplemented }

func (a *Adapter) Backup(ctx context.Context, instanceID, name string) (*runtime.Backup, error) {
	// TODO: cvm → CreateSnapshot；eci → 备份 CBS。
	return nil, ErrNotImplemented
}

func (a *Adapter) Inspect(ctx context.Context, instanceID string) (*runtime.InstanceDetail, error) {
	return nil, ErrNotImplemented
}

func (a *Adapter) Metrics(ctx context.Context, instanceID string) (*runtime.Metrics, error) {
	// TODO: 接入 云监控 Monitor。
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
