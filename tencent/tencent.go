// Package tencent 实现 runtime.Adapter，通过腾讯云管理 agent 实例。
//
// 当前实现：CVM（云服务器）后端。CVM 能装桌面(KDE)、能 SSH，覆盖 openclaw gateway +
// webtop 桌面全部场景。ECI（弹性容器）后端为骨架，待后续补。
//
// 配置（Config.Params）：
//
//	mode            : "cvm"（必填；"eci" 见 eci.go，待补）
//	region          : 地域，如 ap-guangzhou（必填）
//	secretId        : 腾讯云 API SecretId（必填，或 TENCENTCLOUD_SECRET_ID）
//	secretKey       : 腾讯云 API SecretKey（必填，或 TENCENTCLOUD_SECRET_KEY）
//	vpcId           : VPC Id
//	subnetId        : 子网 Id
//	securityGroupId : 安全组 Id
//	imageId         : 默认镜像 Id（CVM 镜像 Id，如 img-xxx）
//	instanceType    : 默认机型（如 SA1.MEDIUM4），可被 spec 覆盖
//	loginKey        : SSH 登录密钥对 Id（KeyId，如 skey-xxx），用于 Exec/Shell
//	loginUser       : SSH 登录用户（默认 ubuntu）
//
// 实例命名约定：CVM 实例名 = aiarsenal-<instanceID>（与 localdocker 容器名一致）。
// 反查：按实例名 Filter DescribeInstances 拿 CVM InstanceId。
package tencent

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/common"
	"github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/common/profile"
	tccvm "github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/cvm/v20170312"

	"github.com/aiarsenal/runtime-go"
)

const providerName = "tencent"

// ErrNotImplemented 该后端/方法尚未实现。
var ErrNotImplemented = errors.New("tencent: not yet implemented for this backend")

func init() {
	runtime.Register(providerName, FromConfig)
}

// Config 腾讯云适配器配置。
type Config struct {
	Mode                string // cvm
	Region              string
	Zone                string // ap-beijing-6
	SecretID            string
	SecretKey           string
	VpcID               string
	SubnetID            string
	SecurityGroupID     string
	DefaultImageID      string
	DefaultInstanceType string
	BandwidthMbps       int // 公网出带宽上限 Mbps（默认 5）；>0 分配公网 IP，0 不分配
	LoginKey            string // SSH 密钥对 KeyId
	LoginUser           string // SSH 登录用户（默认 ubuntu）
}

// FromConfig 从通用 Config 构造 tencent Adapter。
func FromConfig(cfg runtime.Config) (runtime.Adapter, error) {
	c := Config{
		Mode:                str(cfg.Params["mode"]),
		Region:              str(cfg.Params["region"]),
		Zone:                str(cfg.Params["zone"]),
		SecretID:            str(cfg.Params["secretId"]),
		SecretKey:           str(cfg.Params["secretKey"]),
		VpcID:               str(cfg.Params["vpcId"]),
		SubnetID:            str(cfg.Params["subnetId"]),
		SecurityGroupID:     str(cfg.Params["securityGroupId"]),
		DefaultImageID:      str(cfg.Params["imageId"]),
		DefaultInstanceType: str(cfg.Params["instanceType"]),
		BandwidthMbps:       toInt(cfg.Params["bandwidthMbps"]),
		LoginKey:            str(cfg.Params["loginKey"]),
		LoginUser:           str(cfg.Params["loginUser"]),
	}
	if c.SecretID == "" {
		c.SecretID = os.Getenv("TENCENTCLOUD_SECRET_ID")
	}
	if c.SecretKey == "" {
		c.SecretKey = os.Getenv("TENCENTCLOUD_SECRET_KEY")
	}
	if c.Mode == "" {
		c.Mode = "cvm"
	}
	if c.LoginUser == "" {
		c.LoginUser = "ubuntu"
	}
	if c.Mode != "cvm" {
		return nil, fmt.Errorf("tencent: mode %q not yet supported (only cvm)", c.Mode)
	}
	if c.Region == "" || c.SecretID == "" || c.SecretKey == "" {
		return nil, errors.New("tencent: region/secretId/secretKey required")
	}
	return &Adapter{cfg: c}, nil
}

// Adapter 腾讯云 CVM 适配器。
type Adapter struct{ cfg Config }

// newClient 构造 CVM 客户端。
func (a *Adapter) newClient() (*tccvm.Client, error) {
	cred := common.NewCredential(a.cfg.SecretID, a.cfg.SecretKey)
	cpf := profile.NewClientProfile()
	cpf.Language = "en-US"
	return tccvm.NewClient(cred, a.cfg.Region, cpf)
}

// instanceName CVM 实例名（与 localdocker 容器名约定一致）。
func instanceName(instanceID string) string { return "aiarsenal-" + instanceID }

func (a *Adapter) Provider() string { return providerName }

// Create 创建 CVM 实例（RunInstances）。返回 instanceID（aiarsenal biz_id）。
func (a *Adapter) Create(ctx context.Context, spec runtime.InstanceSpec) (string, error) {
	if spec.Name == "" {
		return "", errors.New("tencent: spec.Name required (instance id)")
	}
	cli, err := a.newClient()
	if err != nil {
		return "", err
	}
	imageID := spec.Image
	if imageID == "" {
		imageID = a.cfg.DefaultImageID
	}
	if imageID == "" {
		return "", errors.New("tencent: image required (spec.Image or config imageId)")
	}
	instType := ""
	if spec.Labels != nil {
		instType = spec.Labels["instanceType"] // CVM 机型，如 SA1.MEDIUM4
	}
	if instType == "" {
		instType = a.cfg.DefaultInstanceType
	}
	if instType == "" {
		return "", errors.New("tencent: instanceType required (spec.Labels[instanceType] or config instanceType)")
	}

	req := tccvm.NewRunInstancesRequest()
	req.InstanceType = common.StringPtr(instType)
	req.ImageId = common.StringPtr(imageID)
	req.InstanceName = common.StringPtr(instanceName(spec.Name))
	req.InstanceCount = common.Int64Ptr(1)
	// Placement（Zone 必填）
	zone := a.cfg.Zone
	if zone == "" {
		zone = a.cfg.Region // 退化：region 当 zone 用（腾讯云 zone 形如 ap-beijing-6）
	}
	req.Placement = &tccvm.Placement{Zone: common.StringPtr(zone)}
	if a.cfg.VpcID != "" {
		req.VirtualPrivateCloud = &tccvm.VirtualPrivateCloud{
			VpcId:    common.StringPtr(a.cfg.VpcID),
			SubnetId: common.StringPtr(a.cfg.SubnetID),
		}
	}
	if a.cfg.SecurityGroupID != "" {
		req.SecurityGroupIds = common.StringPtrs([]string{a.cfg.SecurityGroupID})
	}
	if a.cfg.LoginKey != "" {
		req.LoginSettings = &tccvm.LoginSettings{KeyIds: common.StringPtrs([]string{a.cfg.LoginKey})}
	}
	diskSize := int64(spec.DiskGB)
	if diskSize <= 0 {
		diskSize = 50
	}
	req.SystemDisk = &tccvm.SystemDisk{
		DiskType: common.StringPtr("CLOUD_PREMIUM"),
		DiskSize: common.Int64Ptr(diskSize),
	}
	// 公网：按流量后付费 + 分配公网 IP（带宽 >0 才允许分配）。带宽可被 spec.Labels 覆盖。
	bw := a.cfg.BandwidthMbps
	if bw <= 0 {
		bw = 5
	}
	if spec.Labels != nil {
		if n := atoi(spec.Labels["bandwidthMbps"]); n > 0 {
			bw = n
		}
	}
	req.InternetAccessible = &tccvm.InternetAccessible{
		InternetChargeType:      common.StringPtr("TRAFFIC_POSTPAID_BY_HOUR"),
		InternetMaxBandwidthOut: common.Int64Ptr(int64(bw)),
		PublicIpAssigned:        common.BoolPtr(true),
	}
	req.HostName = common.StringPtr(spec.Name)
	req.TagSpecification = []*tccvm.TagSpecification{
		{ResourceType: common.StringPtr("instance"), Tags: []*tccvm.Tag{
			{Key: common.StringPtr("aiarsenal-instance-id"), Value: common.StringPtr(spec.Name)},
		}},
	}
	if len(spec.Env) > 0 {
		req.UserData = common.StringPtr(buildUserDataB64(spec.Env))
	}

	resp, err := cli.RunInstances(req)
	if err != nil {
		return "", fmt.Errorf("tencent RunInstances: %w", err)
	}
	if resp.Response == nil || len(resp.Response.InstanceIdSet) == 0 {
		return "", errors.New("tencent RunInstances: no instance id returned")
	}
	return spec.Name, nil
}

// Start 启动实例（StartInstances）。
func (a *Adapter) Start(ctx context.Context, instanceID string) error {
	cvmID, err := a.lookupCvmID(instanceID)
	if err != nil {
		return err
	}
	cli, err := a.newClient()
	if err != nil {
		return err
	}
	req := tccvm.NewStartInstancesRequest()
	req.InstanceIds = common.StringPtrs([]string{cvmID})
	return retryState("StartInstances", func() error { _, err = cli.StartInstances(req); return err })
}

// Stop 停止实例（StopInstances）。
func (a *Adapter) Stop(ctx context.Context, instanceID string) error {
	cvmID, err := a.lookupCvmID(instanceID)
	if err != nil {
		return err
	}
	cli, err := a.newClient()
	if err != nil {
		return err
	}
	req := tccvm.NewStopInstancesRequest()
	req.InstanceIds = common.StringPtrs([]string{cvmID})
	req.StopType = common.StringPtr("SOFT")
	return retryState("StopInstances", func() error { _, err = cli.StopInstances(req); return err })
}

// Restart 重启实例（RebootInstances）。
func (a *Adapter) Restart(ctx context.Context, instanceID string) error {
	cvmID, err := a.lookupCvmID(instanceID)
	if err != nil {
		return err
	}
	cli, err := a.newClient()
	if err != nil {
		return err
	}
	req := tccvm.NewRebootInstancesRequest()
	req.InstanceIds = common.StringPtrs([]string{cvmID})
	return retryState("RebootInstances", func() error { _, err = cli.RebootInstances(req); return err })
}

// Remove 销毁实例（TerminateInstances）。
func (a *Adapter) Remove(ctx context.Context, instanceID string) error {
	cvmID, err := a.lookupCvmID(instanceID)
	if err != nil {
		return err
	}
	cli, err := a.newClient()
	if err != nil {
		return err
	}
	req := tccvm.NewTerminateInstancesRequest()
	req.InstanceIds = common.StringPtrs([]string{cvmID})
	return retryState("TerminateInstances", func() error { _, err = cli.TerminateInstances(req); return err })
}

// Status 查实例状态（DescribeInstances）→ runtime.Status。
func (a *Adapter) Status(ctx context.Context, instanceID string) (*runtime.Status, error) {
	cvmID, err := a.lookupCvmID(instanceID)
	if err != nil {
		return nil, err
	}
	cli, err := a.newClient()
	if err != nil {
		return nil, err
	}
	req := tccvm.NewDescribeInstancesRequest()
	req.InstanceIds = common.StringPtrs([]string{cvmID})
	resp, err := cli.DescribeInstances(req)
	if err != nil {
		return nil, wrapErr("DescribeInstances", err)
	}
	if resp.Response == nil || len(resp.Response.InstanceSet) == 0 {
		return nil, &runtime.ErrNotFound{InstanceID: instanceID}
	}
	ins := resp.Response.InstanceSet[0]
	st := &runtime.Status{InstanceID: instanceID, ContainerID: cvmID}
	st.State = mapCvmState(sval(ins.InstanceState))
	if len(ins.PublicIpAddresses) > 0 {
		st.IP = sval(ins.PublicIpAddresses[0])
	}
	return st, nil
}

// --- 以下方法后续补（SSH/COS/快照等） ---

func (a *Adapter) Exec(ctx context.Context, instanceID string, opts runtime.ExecOpts) ([]byte, error) {
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

func (a *Adapter) Mkdir(ctx context.Context, instanceID, p string) error                     { return ErrNotImplemented }
func (a *Adapter) RenameFile(ctx context.Context, instanceID, oldPath, newPath string) error { return ErrNotImplemented }
func (a *Adapter) DeleteFile(ctx context.Context, instanceID, p string) error                { return ErrNotImplemented }

func (a *Adapter) CreateVolume(ctx context.Context, instanceID, mountPath string, sizeGB int) (string, error) {
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

// --- 内部工具 ---

// lookupCvmID 由 aiarsenal instanceID 反查 CVM InstanceId（按实例名 Filter）。
func (a *Adapter) lookupCvmID(instanceID string) (string, error) {
	cli, err := a.newClient()
	if err != nil {
		return "", err
	}
	req := tccvm.NewDescribeInstancesRequest()
	req.Filters = []*tccvm.Filter{
		{Name: common.StringPtr("instance-name"), Values: common.StringPtrs([]string{instanceName(instanceID)})},
	}
	resp, err := cli.DescribeInstances(req)
	if err != nil {
		return "", wrapErr("DescribeInstances(lookup)", err)
	}
	if resp.Response == nil || len(resp.Response.InstanceSet) == 0 {
		return "", &runtime.ErrNotFound{InstanceID: instanceID}
	}
	return sval(resp.Response.InstanceSet[0].InstanceId), nil
}

// mapCvmState CVM InstanceState → runtime.State（0 creating 1 running 2 stopped 3 error 4 deleting）。
func mapCvmState(s string) runtime.State {
	switch strings.ToUpper(s) {
	case "PENDING":
		return runtime.StateCreating
	case "LAUNCH_FAILED", "TERMINATING_FAILED":
		return runtime.StateError
	case "RUNNING":
		return runtime.StateRunning
	case "STOPPED", "STOPPING":
		return runtime.StateStopped
	case "TERMINATING":
		return runtime.StateDeleting
	default:
		return runtime.StateRunning
	}
}

// buildUserData 生成 cloud-init UserData 脚本（明文）。
func buildUserData(env map[string]string) string {
	var b strings.Builder
	b.WriteString("#!/bin/bash\n")
	b.WriteString("mkdir -p /etc\n")
	b.WriteString("rm -f /etc/aiarsenal-env\n")
	for k, v := range env {
		safe := strings.ReplaceAll(v, "'", "'\\''")
		b.WriteString("echo '" + k + "=" + safe + "' >> /etc/aiarsenal-env\n")
	}
	return b.String()
}

// buildUserDataB64 生成 base64 编码的 UserData（腾讯云 RunInstances 要求 base64）。
func buildUserDataB64(env map[string]string) string {
	return base64.StdEncoding.EncodeToString([]byte(buildUserData(env)))
}

func wrapErr(op string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("tencent %s: %w", op, err)
}

// isTransientStateErr 判断是否为「实例状态过渡中」类可重试错误
// （CVM 的 Start/Stop/Terminate 在实例还在 STARTING/STOPPING/PENDING 时会拒绝）。
func isTransientStateErr(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	return strings.Contains(s, "InstanceStateStart") ||
		strings.Contains(s, "InstanceStateStop") ||
		strings.Contains(s, "InstanceStatePending") ||
		strings.Contains(s, "InternalServerError") ||
		strings.Contains(s, "RequestLimitExceeded")
}

// retryState 对实例状态过渡类错误做指数退避重试（5s→10s→15s，最多 ~90s），
// 消除 CVM 操作的最终一致性竞态。
func retryState(op string, fn func() error) error {
	backoff := 5 * time.Second
	for deadline := time.Now().Add(90 * time.Second); ; {
		err := fn()
		if err == nil {
			return nil
		}
		if !isTransientStateErr(err) || time.Now().After(deadline) {
			return wrapErr(op, err)
		}
		time.Sleep(backoff)
		if backoff < 15*time.Second {
			backoff += 5 * time.Second
		}
	}
}

func str(v any) string {
	if v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

// atoi 容错解析 int（空串/非法返回 0）。
func atoi(s string) int {
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0
		}
		n = n*10 + int(c-'0')
	}
	return n
}

// toInt 从 any 取 int（兼容 int/float64/string，来自 Config.Params 的异构值）。
func toInt(v any) int {
	switch n := v.(type) {
	case int:
		return n
	case int64:
		return int(n)
	case float64:
		return int(n)
	case string:
		return atoi(n)
	}
	return 0
}

// sval 安全解引用 *string。
func sval(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}
