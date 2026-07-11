// 示例：用 localdocker 适配器起一个实例并验证生命周期/exec/文件。
//
// 运行：go run ./cmd/example
// 前提：本机 docker daemon 可用（DOCKER_HOST 或默认 unix socket）。
package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"strings"
	"time"

	"github.com/aiarsenal/runtime-go"
	_ "github.com/aiarsenal/runtime-go/localdocker"
)

func main() {
	// 构造 adapter：Type=localdocker，参数留空走默认 docker socket。
	adapter, err := runtime.New(runtime.Config{
		Type:   "localdocker",
		Params: map[string]any{},
	})
	if err != nil {
		log.Fatalf("new adapter: %v", err)
	}
	defer adapter.Close()

	ctx := context.Background()
	instanceID := "demo-" + time.Now().Format("150405")

	// 1. Create：起一个长期运行的 alpine（sleep 保活）。
	fmt.Println("==> Create", instanceID)
	if _, err := adapter.Create(ctx, runtime.InstanceSpec{
		Name:     instanceID,
		Type:     "ubuntu",
		Image:    "alpine:latest",
		Cmd:      []string{"sleep", "3600"},
		CPU:      1,
		MemoryMB: 256,
	}); err != nil {
		log.Fatalf("create: %v", err)
	}

	// 等容器就绪。
	time.Sleep(time.Second)

	// 2. Status。
	st, err := adapter.Status(ctx, instanceID)
	must(err)
	fmt.Printf("==> Status: state=%d containerID=%s ip=%s\n", st.State, truncate(st.ContainerID, 12), st.IP)

	// 3. Exec：跑 echo。
	out, err := adapter.Exec(ctx, instanceID, runtime.ExecOpts{Cmd: []string{"echo", "hello-from-agent"}})
	must(err)
	fmt.Printf("==> Exec: %s", string(out))

	// 4. 文件：写 + 读 + 列。
	must(adapter.Mkdir(ctx, instanceID, "/work"))
	must(adapter.UploadFile(ctx, instanceID, "/work/note.txt", strings.NewReader("agent workspace file\n"), 0644))
	entries, err := adapter.ListFiles(ctx, instanceID, "/work")
	must(err)
	fmt.Printf("==> ListFiles /work: %v\n", entries)
	rc, err := adapter.DownloadFile(ctx, instanceID, "/work/note.txt")
	must(err)
	data, _ := io.ReadAll(rc)
	rc.Close()
	fmt.Printf("==> DownloadFile: %s", string(data))

	// 5. Metrics。
	m, err := adapter.Metrics(ctx, instanceID)
	must(err)
	fmt.Printf("==> Metrics: cpu=%.1f%% mem=%.1fMB\n", m.CPUPercent, m.MemoryMB)

	// 6. Inspect。
	d, err := adapter.Inspect(ctx, instanceID)
	must(err)
	fmt.Printf("==> Inspect: image=%s cpu=%d mem=%dMB\n", d.Image, d.CPU, d.MemoryMB)

	// 7. Restart。
	must(adapter.Restart(ctx, instanceID))
	fmt.Println("==> Restart ok")

	// 8. Stop / Start。
	must(adapter.Stop(ctx, instanceID))
	fmt.Println("==> Stop ok")
	must(adapter.Start(ctx, instanceID))
	fmt.Println("==> Start ok")

	// 9. Remove。
	must(adapter.Remove(ctx, instanceID))
	fmt.Println("==> Remove ok")

	fmt.Println("\n所有生命周期 / exec / 文件 / 健康检查均通过 ✓")
}

func must(err error) {
	if err != nil {
		log.Fatalf("FAIL: %v", err)
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// 避免未用 os 报错（保留以备示例扩展写日志文件）。
var _ = os.Stdout
