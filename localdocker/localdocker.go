// Package localdocker 实现 runtime.Adapter，通过本地 Docker daemon 管理实例。
//
// 容器名约定：aiarsenal-<instanceID>；卷名：aiarsenal-vol-<volumeID>。
// 实例 id 直接用 docker container 名（含 aiarsenal- 前缀），无需额外映射表。
//
// 适用：单机/测试环境，Docker daemon 已在本机或可经 DOCKER_HOST 访问。
package localdocker

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/api/types/volume"
	"github.com/docker/docker/client"
	"github.com/docker/docker/pkg/stdcopy"
	"github.com/docker/go-connections/nat"

	"github.com/aiarsenal/runtime-go"
)

const (
	namePrefix = "aiarsenal-"
	volPrefix  = "aiarsenal-vol-"
	// providerName provider 标识。
	providerName = "localdocker"
)

func init() {
	runtime.Register(providerName, FromConfig)
}

// Config localdocker 适配器配置。
type Config struct {
	// DockerHost 覆盖默认连接（空则用 DOCKER_HOST 环境变量 / 默认 unix socket）。
	DockerHost string
	// Network 指定容器接入的 docker 网络（空为默认 bridge）。
	Network string
}

// FromConfig 从通用 Config 构造 localdocker Adapter。
func FromConfig(cfg runtime.Config) (runtime.Adapter, error) {
	host, _ := cfg.Params["dockerHost"].(string)
	net, _ := cfg.Params["network"].(string)
	return New(Config{DockerHost: host, Network: net})
}

// New 构造一个 localdocker 适配器。
func New(cfg Config) (runtime.Adapter, error) {
	opts := []client.Opt{client.WithAPIVersionNegotiation()}
	if cfg.DockerHost != "" {
		opts = append(opts, client.WithHost(cfg.DockerHost))
	}
	cli, err := client.NewClientWithOpts(opts...)
	if err != nil {
		return nil, fmt.Errorf("localdocker: new client: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := cli.Ping(ctx); err != nil {
		_ = cli.Close()
		return nil, fmt.Errorf("localdocker: ping daemon: %w", err)
	}
	return &Adapter{cli: cli, network: cfg.Network}, nil
}

// Adapter 本地 Docker 适配器。
type Adapter struct {
	cli     *client.Client
	network string
}

func (a *Adapter) Provider() string { return providerName }

// containerName 由 instance id 推导容器名。
func containerName(instanceID string) string { return namePrefix + instanceID }

// volumeName 由 volume id 推导卷名。
func volumeName(volumeID string) string { return volPrefix + volumeID }

// Create 拉取镜像并创建容器（不启动）。返回实例 id（= 容器名）。
func (a *Adapter) Create(ctx context.Context, spec runtime.InstanceSpec) (string, error) {
	if spec.Image == "" {
		return "", errors.New("localdocker: spec.Image required")
	}
	instanceID := spec.Name
	if instanceID == "" {
		return "", errors.New("localdocker: spec.Name required (used as instance id)")
	}
	cname := containerName(instanceID)

	// 已存在则幂等返回。
	if exists, err := a.containerExists(ctx, cname); err != nil {
		return "", err
	} else if exists {
		return instanceID, nil
	}

	// 拉取镜像（缺失时）。
	if _, err := a.cli.ImagePull(ctx, spec.Image, image.PullOptions{}); err != nil {
		// 已存在镜像会快速返回；这里忽略 "already exists" 类错误继续创建。
		if !strings.Contains(err.Error(), "not found") && !isImagePresent(ctx, a.cli, spec.Image) {
			return "", fmt.Errorf("localdocker: pull %s: %w", spec.Image, err)
		}
	}

	// 端口暴露。
	exposedPorts, portBindings := buildPorts(spec.Ports)

	// 挂载。
	mounts := buildMounts(spec.Volumes)

	// 环境变量。
	env := make([]string, 0, len(spec.Env))
	for k, v := range spec.Env {
		env = append(env, k+"="+v)
	}

	ccResp, err := a.cli.ContainerCreate(ctx, &container.Config{
		Image:        spec.Image,
		Cmd:          spec.Cmd,
		Env:          env,
		ExposedPorts: exposedPorts,
		Labels:       withPrefix(spec.Labels),
	}, &container.HostConfig{
		PortBindings: portBindings,
		Mounts:       mounts,
		Resources:    buildResources(spec.CPU, spec.MemoryMB),
		RestartPolicy: container.RestartPolicy{
			Name: "unless-stopped",
		},
	}, &network.NetworkingConfig{
		EndpointsConfig: a.endpointConfig(),
	}, nil, cname)
	if err != nil {
		return "", fmt.Errorf("localdocker: create container: %w", err)
	}

	// 启动容器。
	if err := a.cli.ContainerStart(ctx, ccResp.ID, container.StartOptions{}); err != nil {
		_ = a.cli.ContainerRemove(ctx, ccResp.ID, container.RemoveOptions{Force: true})
		return "", fmt.Errorf("localdocker: start container: %w", err)
	}
	return instanceID, nil
}

func (a *Adapter) Start(ctx context.Context, instanceID string) error {
	c, err := a.findContainer(ctx, instanceID)
	if err != nil {
		return err
	}
	return a.cli.ContainerStart(ctx, c.ID, container.StartOptions{})
}

func (a *Adapter) Stop(ctx context.Context, instanceID string) error {
	c, err := a.findContainer(ctx, instanceID)
	if err != nil {
		return err
	}
	return a.cli.ContainerStop(ctx, c.ID, container.StopOptions{Timeout: toPtr(30)})
}

func (a *Adapter) Restart(ctx context.Context, instanceID string) error {
	c, err := a.findContainer(ctx, instanceID)
	if err != nil {
		return err
	}
	return a.cli.ContainerRestart(ctx, c.ID, container.StopOptions{Timeout: toPtr(30)})
}

func (a *Adapter) Remove(ctx context.Context, instanceID string) error {
	c, err := a.findContainer(ctx, instanceID)
	if err != nil {
		if runtime.IsNotFound(err) {
			return nil // 幂等
		}
		return err
	}
	return a.cli.ContainerRemove(ctx, c.ID, container.RemoveOptions{Force: true, RemoveVolumes: false})
}

func (a *Adapter) Status(ctx context.Context, instanceID string) (*runtime.Status, error) {
	c, err := a.findContainer(ctx, instanceID)
	if err != nil {
		return nil, err
	}
	insp, err := a.cli.ContainerInspect(ctx, c.ID)
	if err != nil {
		return nil, err
	}
	st := &runtime.Status{
		InstanceID:  instanceID,
		ContainerID: c.ID,
		State:       runtime.StateCreating,
	}
	if insp.State != nil {
		if insp.State.Running {
			st.State = runtime.StateRunning
		} else if insp.State.Status == "exited" {
			st.State = runtime.StateStopped
		}
		if insp.State.Error != "" {
			st.Error = insp.State.Error
		}
		if insp.State.StartedAt != "" {
			if t, err := time.Parse(time.RFC3339Nano, insp.State.StartedAt); err == nil && !t.IsZero() {
				st.StartedAt = &t
			}
		}
	}
	if insp.NetworkSettings != nil {
		for _, net := range insp.NetworkSettings.Networks {
			if net.IPAddress != "" {
				st.IP = net.IPAddress
				break
			}
		}
	}
	st.Ports = inspectPorts(insp)
	return st, nil
}

// Exec 在容器内执行命令。
func (a *Adapter) Exec(ctx context.Context, instanceID string, opts runtime.ExecOpts) ([]byte, error) {
	c, err := a.findContainer(ctx, instanceID)
	if err != nil {
		return nil, err
	}
	if len(opts.Cmd) == 0 {
		return nil, errors.New("localdocker: exec requires Cmd")
	}
	env := make([]string, 0, len(opts.Env))
	for k, v := range opts.Env {
		env = append(env, k+"="+v)
	}
	execCfg := container.ExecOptions{
		Cmd:          opts.Cmd,
		Env:          env,
		WorkingDir:   opts.WorkDir,
		User:         opts.User,
		AttachStdout: true,
		AttachStderr: true,
	}
	if opts.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, opts.Timeout)
		defer cancel()
	}
	execID, err := a.cli.ContainerExecCreate(ctx, c.ID, execCfg)
	if err != nil {
		return nil, err
	}
	hijack, err := a.cli.ContainerExecAttach(ctx, execID.ID, container.ExecAttachOptions{})
	if err != nil {
		return nil, err
	}
	defer hijack.Close()

	var out bytes.Buffer
	if _, err := stdcopy.StdCopy(&out, &out, hijack.Reader); err != nil {
		return nil, err
	}
	insp, err := a.cli.ContainerExecInspect(ctx, execID.ID)
	if err != nil {
		return out.Bytes(), err
	}
	_ = insp
	return out.Bytes(), nil
}

// Logs 返回容器日志流。
func (a *Adapter) Logs(ctx context.Context, instanceID string, opts runtime.LogOpts) (io.ReadCloser, error) {
	c, err := a.findContainer(ctx, instanceID)
	if err != nil {
		return nil, err
	}
	since := ""
	if !opts.Since.IsZero() {
		since = opts.Since.Format(time.RFC3339)
	}
	return a.cli.ContainerLogs(ctx, c.ID, container.LogsOptions{
		ShowStdout: true,
		ShowStderr: true,
		Follow:     opts.Follow,
		Tail:       opts.Tail,
		Since:      since,
	})
}

// Shell 附加交互式终端（PTY）。返回的 ReadWriteCloser 直连容器 stdin/stdout。
func (a *Adapter) Shell(ctx context.Context, instanceID string, opts runtime.ShellOpts) (runtime.ReadWriteCloser, error) {
	c, err := a.findContainer(ctx, instanceID)
	if err != nil {
		return nil, err
	}
	cmd := opts.Cmd
	if len(cmd) == 0 {
		cmd = []string{"/bin/sh"}
	}
	env := make([]string, 0, len(opts.Env))
	for k, v := range opts.Env {
		env = append(env, k+"="+v)
	}
	execCfg := container.ExecOptions{
		Cmd:          cmd,
		Env:          env,
		AttachStdin:  true,
		AttachStdout: true,
		AttachStderr: true,
		Tty:          true,
	}
	execID, err := a.cli.ContainerExecCreate(ctx, c.ID, execCfg)
	if err != nil {
		return nil, err
	}
	hijack, err := a.cli.ContainerExecAttach(ctx, execID.ID, container.ExecAttachOptions{Tty: true})
	if err != nil {
		return nil, err
	}
	// 设置初始窗口大小。
	if opts.Width > 0 && opts.Height > 0 {
		_ = a.cli.ContainerExecResize(ctx, execID.ID, container.ResizeOptions{
			Height: uint(opts.Height),
			Width:  uint(opts.Width),
		})
	}
	return &hijackStream{hijack: hijack, cli: a.cli, execID: execID.ID}, nil
}

// ---- 文件（工作区）----

func (a *Adapter) ListFiles(ctx context.Context, instanceID, p string) ([]runtime.FileEntry, error) {
	c, err := a.findContainer(ctx, instanceID)
	if err != nil {
		return nil, err
	}
	// BusyBox/coreutils 通用：-1 单列、-A 含隐藏（不含 . ..）、-p 目录加 / 后缀。
	out, err := a.execRaw(ctx, c.ID, []string{"ls", "-1Ap", p})
	if err != nil {
		return nil, err
	}
	entries := []runtime.FileEntry{}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		line = strings.TrimRight(line, "\r")
		if line == "" {
			continue
		}
		isDir := strings.HasSuffix(line, "/")
		name := strings.TrimSuffix(line, "/")
		entries = append(entries, runtime.FileEntry{
			Name:  name,
			Path:  path.Join(p, name),
			IsDir: isDir,
		})
	}
	return entries, nil
}

func (a *Adapter) UploadFile(ctx context.Context, instanceID, p string, content io.Reader, mode int64) error {
	c, err := a.findContainer(ctx, instanceID)
	if err != nil {
		return err
	}
	base := path.Base(p)
	dir := path.Dir(p)
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	hdr := &tar.Header{
		Name: base,
		Mode: mode,
	}
	data, err := io.ReadAll(content)
	if err != nil {
		return err
	}
	hdr.Size = int64(len(data))
	if err := tw.WriteHeader(hdr); err != nil {
		return err
	}
	if _, err := tw.Write(data); err != nil {
		return err
	}
	if err := tw.Close(); err != nil {
		return err
	}
	return a.cli.CopyToContainer(ctx, c.ID, dir, &buf, container.CopyToContainerOptions{})
}

func (a *Adapter) DownloadFile(ctx context.Context, instanceID, p string) (io.ReadCloser, error) {
	c, err := a.findContainer(ctx, instanceID)
	if err != nil {
		return nil, err
	}
	rc, _, err := a.cli.CopyFromContainer(ctx, c.ID, p)
	if err != nil {
		return nil, err
	}
	// 解 tar 包返回第一个成员的内容。
	return untarSingle(rc)
}

func (a *Adapter) Mkdir(ctx context.Context, instanceID, p string) error {
	c, err := a.findContainer(ctx, instanceID)
	if err != nil {
		return err
	}
	_, err = a.execRaw(ctx, c.ID, []string{"mkdir", "-p", p})
	return err
}

func (a *Adapter) RenameFile(ctx context.Context, instanceID, oldPath, newPath string) error {
	c, err := a.findContainer(ctx, instanceID)
	if err != nil {
		return err
	}
	_, err = a.execRaw(ctx, c.ID, []string{"mv", oldPath, newPath})
	return err
}

func (a *Adapter) DeleteFile(ctx context.Context, instanceID, p string) error {
	c, err := a.findContainer(ctx, instanceID)
	if err != nil {
		return err
	}
	_, err = a.execRaw(ctx, c.ID, []string{"rm", "-rf", p})
	return err
}

// ---- 存储 ----

func (a *Adapter) CreateVolume(ctx context.Context, instanceID, mountPath string, sizeGB int) (string, error) {
	c, err := a.findContainer(ctx, instanceID)
	if err != nil {
		return "", err
	}
	// 复用实例 id 作卷 id（一实例一卷足够 v2 场景）。
	volID := instanceID
	vname := volumeName(volID)
	vol, err := a.cli.VolumeCreate(ctx, volume.CreateOptions{
		Name:   vname,
		Driver: "local",
		Labels: map[string]string{"aiarsenal.instance": instanceID},
	})
	if err != nil {
		return "", fmt.Errorf("localdocker: create volume: %w", err)
	}
	_ = sizeGB // local 卷驱动不强制配额；size 仅记录用。
	// 重新创建容器以挂载新卷（docker 不支持给运行中容器加挂载）。
	insp, err := a.cli.ContainerInspect(ctx, c.ID)
	if err != nil {
		return "", err
	}
	if err := a.cli.ContainerStop(ctx, c.ID, container.StopOptions{Timeout: toPtr(15)}); err != nil {
		return "", err
	}
	if err := a.cli.ContainerRemove(ctx, c.ID, container.RemoveOptions{Force: true}); err != nil {
		return "", err
	}
	cfg := insp.Config
	hc := insp.HostConfig
	hc.Mounts = append(hc.Mounts, mount.Mount{
		Type:   mount.TypeVolume,
		Source: vol.Name,
		Target: mountPath,
	})
	cc, err := a.cli.ContainerCreate(ctx, cfg, hc, &network.NetworkingConfig{EndpointsConfig: a.endpointConfig()}, nil, containerName(instanceID))
	if err != nil {
		return "", err
	}
	if err := a.cli.ContainerStart(ctx, cc.ID, container.StartOptions{}); err != nil {
		return "", err
	}
	return volID, nil
}

func (a *Adapter) RemoveVolume(ctx context.Context, volumeID string) error {
	return a.cli.VolumeRemove(ctx, volumeName(volumeID), true)
}

func (a *Adapter) Backup(ctx context.Context, instanceID, name string) (*runtime.Backup, error) {
	c, err := a.findContainer(ctx, instanceID)
	if err != nil {
		return nil, err
	}
	rc, _, err := a.cli.CopyFromContainer(ctx, c.ID, "/")
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	// 写到宿主机临时文件（生产可换对象存储）。
	f, err := os.CreateTemp("", "aiarsenal-backup-"+name+"-*.tar")
	if err != nil {
		return nil, err
	}
	n, err := io.Copy(f, rc)
	f.Close()
	if err != nil {
		return nil, err
	}
	return &runtime.Backup{
		BackupID:  name,
		SizeGB:    int(n / (1 << 30)),
		Location:  f.Name(),
		Status:    "completed",
		CreatedAt: time.Now(),
	}, nil
}

// ---- 健康 ----

func (a *Adapter) Inspect(ctx context.Context, instanceID string) (*runtime.InstanceDetail, error) {
	st, err := a.Status(ctx, instanceID)
	if err != nil {
		return nil, err
	}
	c, err := a.findContainer(ctx, instanceID)
	if err != nil {
		return nil, err
	}
	insp, err := a.cli.ContainerInspect(ctx, c.ID)
	if err != nil {
		return nil, err
	}
	d := &runtime.InstanceDetail{Status: *st}
	if insp.Config != nil {
		d.Image = insp.Config.Image
		d.Env = envSliceToMap(insp.Config.Env)
	}
	if insp.HostConfig != nil {
		if insp.HostConfig.NanoCPUs > 0 {
			d.CPU = int(insp.HostConfig.NanoCPUs / 1e9)
		}
		if insp.HostConfig.Memory > 0 {
			d.MemoryMB = int(insp.HostConfig.Memory / (1024 * 1024))
		}
	}
	d.ProviderInfo = map[string]string{"driver": "docker", "container": c.ID}
	for _, mnt := range insp.Mounts {
		d.Volumes = append(d.Volumes, runtime.VolumeMount{
			VolumeID:  mnt.Name,
			MountPath: mnt.Destination,
			ReadOnly:  !mnt.RW,
		})
	}
	return d, nil
}

func (a *Adapter) Metrics(ctx context.Context, instanceID string) (*runtime.Metrics, error) {
	c, err := a.findContainer(ctx, instanceID)
	if err != nil {
		return nil, err
	}
	stats, err := a.cli.ContainerStats(ctx, c.ID, false)
	if err != nil {
		return nil, err
	}
	defer stats.Body.Close()
	return parseStats(stats.Body, instanceID)
}

func (a *Adapter) Close() error { return a.cli.Close() }

// ==================== 辅助 ====================

func (a *Adapter) findContainer(ctx context.Context, instanceID string) (container.Summary, error) {
	list, err := a.cli.ContainerList(ctx, container.ListOptions{
		All: true,
		Filters: filters.NewArgs(filters.KeyValuePair{
			Key: "name", Value: containerName(instanceID),
		}),
	})
	if err != nil {
		return container.Summary{}, err
	}
	if len(list) == 0 {
		return container.Summary{}, &runtime.ErrNotFound{InstanceID: instanceID}
	}
	return list[0], nil
}

func (a *Adapter) containerExists(ctx context.Context, cname string) (bool, error) {
	list, err := a.cli.ContainerList(ctx, container.ListOptions{
		All: true,
		Filters: filters.NewArgs(filters.KeyValuePair{
			Key: "name", Value: cname,
		}),
	})
	if err != nil {
		return false, err
	}
	return len(list) > 0, nil
}

// execRaw 不带 timeout 的内部 exec（供 ListFiles/Mkdir 等用）。
func (a *Adapter) execRaw(ctx context.Context, containerID string, cmd []string) ([]byte, error) {
	execID, err := a.cli.ContainerExecCreate(ctx, containerID, container.ExecOptions{
		Cmd:          cmd,
		AttachStdout: true,
		AttachStderr: true,
	})
	if err != nil {
		return nil, err
	}
	hijack, err := a.cli.ContainerExecAttach(ctx, execID.ID, container.ExecAttachOptions{})
	if err != nil {
		return nil, err
	}
	defer hijack.Close()
	var out bytes.Buffer
	if _, err := stdcopy.StdCopy(&out, &out, hijack.Reader); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

func (a *Adapter) endpointConfig() map[string]*network.EndpointSettings {
	if a.network == "" {
		return nil
	}
	return map[string]*network.EndpointSettings{
		a.network: {},
	}
}

func buildPorts(ports []runtime.PortMap) (map[nat.Port]struct{}, map[nat.Port][]nat.PortBinding) {
	exposed := map[nat.Port]struct{}{}
	bindings := map[nat.Port][]nat.PortBinding{}
	for _, p := range ports {
		proto := p.Protocol
		if proto == "" {
			proto = "tcp"
		}
		cp := nat.Port(fmt.Sprintf("%d/%s", p.ContainerPort, proto))
		exposed[cp] = struct{}{}
		bindings[cp] = []nat.PortBinding{{HostPort: strconv.Itoa(p.HostPort)}}
	}
	return exposed, bindings
}

func buildMounts(vols []runtime.VolumeMount) []mount.Mount {
	out := make([]mount.Mount, 0, len(vols))
	for _, v := range vols {
		m := mount.Mount{Target: v.MountPath, ReadOnly: v.ReadOnly}
		if v.VolumeID != "" {
			m.Type = mount.TypeVolume
			m.Source = volumeName(v.VolumeID)
		} else {
			// 无指定卷：临时用匿名 volume（实例删则随容器删）。
			m.Type = mount.TypeVolume
		}
		out = append(out, m)
	}
	return out
}

func buildResources(cpu, memMB int) container.Resources {
	r := container.Resources{}
	if cpu > 0 {
		r.NanoCPUs = int64(cpu) * 1e9
	}
	if memMB > 0 {
		r.Memory = int64(memMB) * 1024 * 1024
	}
	return r
}

func withPrefix(labels map[string]string) map[string]string {
	out := make(map[string]string, len(labels)+1)
	out["aiarsenal.managed"] = "true"
	for k, v := range labels {
		out[k] = v
	}
	return out
}

func inspectPorts(insp container.InspectResponse) []runtime.PortMap {
	if insp.NetworkSettings == nil {
		return nil
	}
	out := []runtime.PortMap{}
	for port, bindings := range insp.NetworkSettings.Ports {
		if bindings == nil {
			continue
		}
		cp, _ := parseDockerPort(string(port))
		for _, b := range bindings {
			hp, _ := strconv.Atoi(b.HostPort)
			out = append(out, runtime.PortMap{HostPort: hp, ContainerPort: cp})
		}
	}
	return out
}

func envSliceToMap(envs []string) map[string]string {
	m := map[string]string{}
	for _, e := range envs {
		if k, v, ok := strings.Cut(e, "="); ok {
			m[k] = v
		}
	}
	return m
}

func isImagePresent(ctx context.Context, cli *client.Client, ref string) bool {
	sum, err := cli.ImageList(ctx, image.ListOptions{
		Filters: filters.NewArgs(filters.KeyValuePair{Key: "reference", Value: ref}),
	})
	if err != nil {
		return false
	}
	return len(sum) > 0
}

func toPtr(i int) *int { return &i }
