// Package localprocess 实现 runtime.Adapter，在本机直接 spawn 子进程管理 agent。
//
// 这是 desktop 客户端（Tauri app）控制方式的 Go 版本：openclaw/hermes 等作为
// 本机进程运行，适配器持有子进程句柄，Start/Stop/Restart 直接操作进程。
//
// 适用：无 Docker 的本机环境，或桌面客户端嵌入式控制。
// 限制：Exec/Shell/文件接口在此实现里基于工作目录文件系统直接操作（非进程内 exec），
//      适合「进程 + 本地工作区」模型；云场景请用 localdocker / tencent / aliyun。
package localprocess

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/aiarsenal/runtime-go"
)

const providerName = "localprocess"

func init() {
	runtime.Register(providerName, FromConfig)
}

// Config localprocess 适配器配置。
type Config struct {
	// BaseDir 实例工作区根目录（每实例一个子目录）。空为 ./aiarsenal-instances。
	BaseDir string
}

// FromConfig 从通用 Config 构造 localprocess Adapter。
func FromConfig(cfg runtime.Config) (runtime.Adapter, error) {
	base, _ := cfg.Params["baseDir"].(string)
	if base == "" {
		base = filepath.Join(os.TempDir(), "aiarsenal-instances")
	}
	return &Adapter{baseDir: base}, nil
}

// Adapter 本机进程适配器。
type Adapter struct {
	baseDir string
	mu      sync.Mutex
	procs   map[string]*managedProc // instanceID → 进程
}

type managedProc struct {
	cmd    *exec.Cmd
	cancel context.CancelFunc
}

func (a *Adapter) Provider() string { return providerName }

func (a *Adapter) Create(ctx context.Context, spec runtime.InstanceSpec) (string, error) {
	if spec.Name == "" {
		return "", errors.New("localprocess: spec.Name required")
	}
	if len(spec.Cmd) == 0 {
		return "", errors.New("localprocess: spec.Cmd required (the agent binary + args)")
	}
	id := spec.Name
	dir := a.instanceDir(id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	// 写环境变量到 .env 便于排查；实际通过 cmd.Env 注入。
	return id, a.startProc(id, spec, dir)
}

func (a *Adapter) Start(ctx context.Context, instanceID string) error {
	// 进程模型下 Start 需要重新拉起：读取上次规格不可得，这里若已在跑则幂等，否则返回需重建。
	a.mu.Lock()
	_, running := a.procs[instanceID]
	a.mu.Unlock()
	if running {
		return nil
	}
	return fmt.Errorf("localprocess: instance %s not running, recreate with Create (process model has no persisted spec)", instanceID)
}

func (a *Adapter) Stop(ctx context.Context, instanceID string) error {
	a.mu.Lock()
	p, ok := a.procs[instanceID]
	a.mu.Unlock()
	if !ok {
		return &runtime.ErrNotFound{InstanceID: instanceID}
	}
	p.cancel()
	_ = p.cmd.Process.Kill()
	a.mu.Lock()
	delete(a.procs, instanceID)
	a.mu.Unlock()
	return nil
}

func (a *Adapter) Restart(ctx context.Context, instanceID string) error {
	if err := a.Stop(ctx, instanceID); err != nil && !runtime.IsNotFound(err) {
		return err
	}
	return fmt.Errorf("localprocess: restart requires spec (use Stop + Create); process model has no persisted spec")
}

func (a *Adapter) Remove(ctx context.Context, instanceID string) error {
	_ = a.Stop(ctx, instanceID)
	return os.RemoveAll(a.instanceDir(instanceID))
}

func (a *Adapter) Status(ctx context.Context, instanceID string) (*runtime.Status, error) {
	a.mu.Lock()
	p, ok := a.procs[instanceID]
	a.mu.Unlock()
	if !ok {
		return &runtime.Status{InstanceID: instanceID, State: runtime.StateStopped}, nil
	}
	st := &runtime.Status{InstanceID: instanceID, State: runtime.StateRunning, ContainerID: strconv.Itoa(p.cmd.Process.Pid)}
	return st, nil
}

// Exec 在实例工作目录执行命令（非进程内，基于本机 shell）。
func (a *Adapter) Exec(ctx context.Context, instanceID string, opts runtime.ExecOpts) ([]byte, error) {
	if len(opts.Cmd) == 0 {
		return nil, errors.New("localprocess: exec requires Cmd")
	}
	dir := a.instanceDir(instanceID)
	if _, err := os.Stat(dir); err != nil {
		return nil, &runtime.ErrNotFound{InstanceID: instanceID}
	}
	c := exec.CommandContext(ctx, opts.Cmd[0], opts.Cmd[1:]...)
	c.Dir = orDefault(opts.WorkDir, dir)
	c.Env = append(os.Environ(), envToSlice(opts.Env)...)
	return c.CombinedOutput()
}

func (a *Adapter) Logs(ctx context.Context, instanceID string, opts runtime.LogOpts) (io.ReadCloser, error) {
	// 进程日志写在工作目录 agent.log。
	f, err := os.Open(filepath.Join(a.instanceDir(instanceID), "agent.log"))
	if err != nil {
		return nil, &runtime.ErrNotFound{InstanceID: instanceID}
	}
	return f, nil
}

func (a *Adapter) Shell(ctx context.Context, instanceID string, opts runtime.ShellOpts) (runtime.ReadWriteCloser, error) {
	return nil, errors.New("localprocess: interactive shell not supported (use Exec)")
}

// 文件接口直接操作实例工作目录。
func (a *Adapter) ListFiles(ctx context.Context, instanceID, p string) ([]runtime.FileEntry, error) {
	full := a.workPath(instanceID, p)
	ents, err := os.ReadDir(full)
	if err != nil {
		return nil, err
	}
	out := make([]runtime.FileEntry, 0, len(ents))
	for _, e := range ents {
		info, _ := e.Info()
		out = append(out, runtime.FileEntry{Name: e.Name(), Path: filepath.Join(p, e.Name()), IsDir: e.IsDir(), Size: fileSize(info), ModTime: fileMod(info)})
	}
	return out, nil
}

func (a *Adapter) UploadFile(ctx context.Context, instanceID, p string, content io.Reader, mode int64) error {
	full := a.workPath(instanceID, p)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(full, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, os.FileMode(mode))
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(f, content)
	return err
}

func (a *Adapter) DownloadFile(ctx context.Context, instanceID, p string) (io.ReadCloser, error) {
	return os.Open(a.workPath(instanceID, p))
}

func (a *Adapter) Mkdir(ctx context.Context, instanceID, p string) error {
	return os.MkdirAll(a.workPath(instanceID, p), 0o755)
}

func (a *Adapter) RenameFile(ctx context.Context, instanceID, oldPath, newPath string) error {
	return os.Rename(a.workPath(instanceID, oldPath), a.workPath(instanceID, newPath))
}

func (a *Adapter) DeleteFile(ctx context.Context, instanceID, p string) error {
	return os.RemoveAll(a.workPath(instanceID, p))
}

func (a *Adapter) CreateVolume(ctx context.Context, instanceID, mountPath string, sizeGB int) (string, error) {
	// 进程模型：卷即实例工作目录下的子目录。
	volID := instanceID
	return volID, os.MkdirAll(a.workPath(instanceID, mountPath), 0o755)
}

func (a *Adapter) RemoveVolume(ctx context.Context, volumeID string) error {
	// 卷与实例工作区同生命周期，Remove 已清理；这里幂等。
	return nil
}

func (a *Adapter) Backup(ctx context.Context, instanceID, name string) (*runtime.Backup, error) {
	tarPath := filepath.Join(a.baseDir, "backups", name+".tar.gz")
	if err := os.MkdirAll(filepath.Dir(tarPath), 0o755); err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, "tar", "czf", tarPath, "-C", a.baseDir, instanceID)
	if err := cmd.Run(); err != nil {
		return nil, err
	}
	info, _ := os.Stat(tarPath)
	return &runtime.Backup{BackupID: name, Location: tarPath, Status: "completed", CreatedAt: time.Now(), SizeGB: int(info.Size() / (1 << 30))}, nil
}

func (a *Adapter) Inspect(ctx context.Context, instanceID string) (*runtime.InstanceDetail, error) {
	st, err := a.Status(ctx, instanceID)
	if err != nil {
		return nil, err
	}
	return &runtime.InstanceDetail{Status: *st, ProviderInfo: map[string]string{"driver": "localprocess", "workdir": a.instanceDir(instanceID)}}, nil
}

func (a *Adapter) Metrics(ctx context.Context, instanceID string) (*runtime.Metrics, error) {
	return &runtime.Metrics{RecordedAt: time.Now()}, nil
}

func (a *Adapter) Close() error { return nil }

// ---- 辅助 ----

func (a *Adapter) instanceDir(id string) string  { return filepath.Join(a.baseDir, id) }
func (a *Adapter) workPath(id, p string) string  { return filepath.Join(a.instanceDir(id), filepath.Clean("/"+p)) }

func (a *Adapter) startProc(id string, spec runtime.InstanceSpec, dir string) error {
	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(ctx, spec.Cmd[0], spec.Cmd[1:]...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), envToSlice(spec.Env)...)
	logFile, err := os.Create(filepath.Join(dir, "agent.log"))
	if err != nil {
		cancel()
		return err
	}
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	if err := cmd.Start(); err != nil {
		cancel()
		logFile.Close()
		return err
	}
	a.mu.Lock()
	a.procs[id] = &managedProc{cmd: cmd, cancel: cancel}
	a.mu.Unlock()
	go func() {
		_ = cmd.Wait()
		logFile.Close()
		a.mu.Lock()
		delete(a.procs, id)
		a.mu.Unlock()
	}()
	return nil
}

func envToSlice(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k, v := range m {
		out = append(out, k+"="+v)
	}
	return out
}

func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

func fileSize(i os.FileInfo) int64 {
	if i == nil {
		return 0
	}
	return i.Size()
}

func fileMod(i os.FileInfo) time.Time {
	if i == nil {
		return time.Time{}
	}
	return i.ModTime()
}

// 避免 strings 未用（保留以备扩展）。
var _ = strings.TrimSpace
