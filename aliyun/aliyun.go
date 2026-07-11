// Package aliyun 实现 runtime.Adapter，通过阿里云管理 agent 实例。
//
// 支持两种后端（按 Config.Mode 选择）：
//   - eci（弹性容器实例）：无服务器容器。
//   - ecs（云服务器）：常驻虚拟机，适合 webtop 桌面。
//
// ⚠️ 本包为接口骨架：实现了 Adapter 全部方法签名，底层云 API 待接入
// 阿里云 Go SDK V2（github.com/alibabacloud-go/*）。Create/Start 等返回 ErrNotImplemented。
//
// 配置（Config.Params）：
//
//	mode          : "eci" | "ecs"（必填）
//	regionId      : 地域，如 cn-hangzhou（必填）
//	accessKeyId   : 阿里云 AccessKey Id（或环境变量 ALIBABA_CLOUD_ACCESS_KEY_ID）
//	accessKeySecret : 阿里云 AccessKey Secret（或环境变量 ALIBABA_CLOUD_ACCESS_KEY_SECRET）
//	vpcId / vswitchId / securityGroupId : 网络配置
//	imageId       : 默认镜像 Id
package aliyun

import (
	"context"
	"errors"
	"io"
	"os"

	"github.com/aiarsenal/runtime-go"
)

const providerName = "aliyun"

// ErrNotImplemented 云 API 尚未接入。
var ErrNotImplemented = errors.New("aliyun: cloud API not yet implemented (skeleton adapter)")

func init() {
	runtime.Register(providerName, FromConfig)
}

// Config 阿里云适配器配置。
type Config struct {
	Mode             string // eci / ecs
	RegionID         string
	AccessKeyID      string
	AccessKeySecret  string
	VpcID            string
	VSwitchID        string
	SecurityGroupID  string
	DefaultImageID   string
}

// FromConfig 从通用 Config 构造 aliyun Adapter。
func FromConfig(cfg runtime.Config) (runtime.Adapter, error) {
	c := Config{
		Mode:            str(cfg.Params["mode"]),
		RegionID:        str(cfg.Params["regionId"]),
		AccessKeyID:     str(cfg.Params["accessKeyId"]),
		AccessKeySecret: str(cfg.Params["accessKeySecret"]),
		VpcID:           str(cfg.Params["vpcId"]),
		VSwitchID:       str(cfg.Params["vswitchId"]),
		SecurityGroupID: str(cfg.Params["securityGroupId"]),
		DefaultImageID:  str(cfg.Params["imageId"]),
	}
	if c.AccessKeyID == "" {
		c.AccessKeyID = os.Getenv("ALIBABA_CLOUD_ACCESS_KEY_ID")
	}
	if c.AccessKeySecret == "" {
		c.AccessKeySecret = os.Getenv("ALIBABA_CLOUD_ACCESS_KEY_SECRET")
	}
	if c.Mode == "" || c.RegionID == "" {
		return nil, errors.New("aliyun: mode and regionId required")
	}
	return &Adapter{cfg: c}, nil
}

// Adapter 阿里云适配器（骨架）。
type Adapter struct{ cfg Config }

func (a *Adapter) Provider() string { return providerName }

func (a *Adapter) Create(ctx context.Context, spec runtime.InstanceSpec) (string, error) {
	// TODO: 接入阿里云 SDK V2
	//   eci → eci20180808.CreateContainerGroup
	//   ecs → ecs20140526.RunInstances
	return "", ErrNotImplemented
}

func (a *Adapter) Start(ctx context.Context, instanceID string) error {
	// ecs → StartInstance；eci 无停启概念（删除重建）。
	return ErrNotImplemented
}
func (a *Adapter) Stop(ctx context.Context, instanceID string) error    { return ErrNotImplemented }
func (a *Adapter) Restart(ctx context.Context, instanceID string) error { return ErrNotImplemented }
func (a *Adapter) Remove(ctx context.Context, instanceID string) error  { return ErrNotImplemented }
func (a *Adapter) Status(ctx context.Context, instanceID string) (*runtime.Status, error) {
	return nil, ErrNotImplemented
}

func (a *Adapter) Exec(ctx context.Context, instanceID string, opts runtime.ExecOpts) ([]byte, error) {
	// TODO: ecs 经 云助手 RunCommand；eci 经 ExecContainerCommand。
	return nil, ErrNotImplemented
}
func (a *Adapter) Logs(ctx context.Context, instanceID string, opts runtime.LogOpts) (io.ReadCloser, error) {
	return nil, ErrNotImplemented
}
func (a *Adapter) Shell(ctx context.Context, instanceID string, opts runtime.ShellOpts) (runtime.ReadWriteCloser, error) {
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
	// TODO: ecs → CreateDisk；eci → 挂载 ESSD。
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
