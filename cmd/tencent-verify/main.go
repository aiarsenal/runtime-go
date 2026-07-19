// 示例：用 tencent 适配器在腾讯云真起一台 CVM，验证完整生命周期。
//
// 配置全部走环境变量（不含任何真实账号信息，便于安全提交）。运行：
//
//	TC_SECRET_ID=... TC_SECRET_KEY=... \
//	TC_REGION=ap-guangzhou TC_ZONE=ap-guangzhou-3 \
//	TC_VPC=vpc-xxx TC_SUBNET=subnet-xxx TC_SG=sg-xxx \
//	TC_IMAGE=img-xxxxxxxx TC_INSTANCE_TYPE=SA2.MEDIUM4 \
//	TC_LOGIN_KEY=skey-xxx TC_LOGIN_USER=ubuntu \
//	go run ./cmd/tencent-verify
//
// 流程：Create → 轮询 Status 到 Running（验证拿到公网 IP）→ Stop → Start → Remove（Terminate）。
// CVM 启动/关机需时间，全程约 6-9 分钟，按量计费，跑完即销毁。
// 任意阶段失败都会兜底销毁实例（避免孤儿计费）。
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/aiarsenal/runtime-go"
	_ "github.com/aiarsenal/runtime-go/tencent"
)

func main() {
	adapter, err := runtime.New(runtime.Config{
		Type: "tencent",
		Params: map[string]any{
			"mode":            "cvm",
			"region":          env("TC_REGION", "ap-guangzhou"),
			"zone":            env("TC_ZONE", "ap-guangzhou-3"),
			"secretId":        mustEnv("TC_SECRET_ID"),
			"secretKey":       mustEnv("TC_SECRET_KEY"),
			"vpcId":           env("TC_VPC", ""),
			"subnetId":        env("TC_SUBNET", ""),
			"securityGroupId": env("TC_SG", ""),
			"imageId":         env("TC_IMAGE", ""),
			"instanceType":    env("TC_INSTANCE_TYPE", "SA2.MEDIUM4"),
			"loginKey":        env("TC_LOGIN_KEY", ""),
			"loginUser":       env("TC_LOGIN_USER", "ubuntu"),
		},
	})
	if err != nil {
		log.Fatalf("new adapter: %v", err)
	}
	defer adapter.Close()
	fmt.Printf("==> provider=%s\n", adapter.Provider())

	ctx := context.Background()
	instanceID := "verify-" + time.Now().Format("0102-150405")

	// 兜底：无论如何都尝试销毁实例，避免孤儿计费。
	cleanupDone := false
	defer func() {
		if cleanupDone {
			return
		}
		fmt.Println("==> cleanup: 兜底 Remove")
		_ = adapter.Remove(ctx, instanceID)
	}()

	if err := run(ctx, adapter, instanceID); err != nil {
		log.Printf("FAIL: %v", err)
		if e := adapter.Remove(ctx, instanceID); e != nil {
			log.Printf("cleanup Remove: %v（请到控制台手动销毁实例）", e)
		} else {
			fmt.Println("==> cleanup Remove ok")
		}
		os.Exit(1)
	}
	cleanupDone = true // run 已自行 Remove
	fmt.Println("\n腾讯云 CVM 生命周期（Create/Status/Stop/Start/Remove）全部通过 ✓")
}

func run(ctx context.Context, a runtime.Adapter, instanceID string) error {
	// 1. Create：真起 CVM。注入无害 env 验证 UserData 落盘。
	fmt.Println("==> Create", instanceID)
	if _, err := a.Create(ctx, runtime.InstanceSpec{
		Name:   instanceID,
		DiskGB: 50,
		Env:    map[string]string{"AIARSENAL_INSTANCE_ID": instanceID, "AIARSENAL_BASE_URL": "https://api.moyu.cash"},
		Labels: map[string]string{"instanceType": os.Getenv("TC_INSTANCE_TYPE")},
	}); err != nil {
		return fmt.Errorf("create: %w", err)
	}

	// 2. 轮询 Status 到 Running（CVM 创建+开机约 1-3 分钟）。
	st, err := waitState(ctx, a, instanceID, runtime.StateRunning, 6*time.Minute)
	if err != nil {
		return err
	}
	fmt.Printf("==> Status Running: containerID=%s publicIP=%s\n", st.ContainerID, st.IP)
	if st.IP == "" {
		return fmt.Errorf("running 但没拿到公网 IP（InternetAccessible 未生效？检查带宽配置）")
	}

	// 3. Stop → 等 Stopped → Start → 等 Running。
	fmt.Println("==> Stop")
	if err := a.Stop(ctx, instanceID); err != nil {
		return fmt.Errorf("stop: %w", err)
	}
	if _, err := waitState(ctx, a, instanceID, runtime.StateStopped, 4*time.Minute); err != nil {
		return err
	}

	fmt.Println("==> Start")
	if err := a.Start(ctx, instanceID); err != nil {
		return fmt.Errorf("start: %w", err)
	}
	st2, err := waitState(ctx, a, instanceID, runtime.StateRunning, 4*time.Minute)
	if err != nil {
		return err
	}
	fmt.Printf("==> Status Running again: ip=%s\n", st2.IP)

	// 4. Remove（TerminateInstances，销毁）。
	fmt.Println("==> Remove")
	if err := a.Remove(ctx, instanceID); err != nil {
		return fmt.Errorf("remove: %w", err)
	}
	return nil
}

// waitState 轮询直到拿到目标状态或超时。
func waitState(ctx context.Context, a runtime.Adapter, id string, want runtime.State, timeout time.Duration) (*runtime.Status, error) {
	stop := time.Now().Add(timeout)
	for time.Now().Before(stop) {
		st, err := a.Status(ctx, id)
		if err != nil {
			fmt.Printf("   (status err: %v) 重试...\n", err)
			time.Sleep(10 * time.Second)
			continue
		}
		fmt.Printf("   state=%s(%d) cvmId=%s ip=%s\n", stateName(st.State), st.State, st.ContainerID, st.IP)
		if st.State == want {
			return st, nil
		}
		time.Sleep(15 * time.Second)
	}
	return nil, fmt.Errorf("等待 %s 超时(%v)", stateName(want), timeout)
}

func stateName(s runtime.State) string {
	switch s {
	case runtime.StateCreating:
		return "Creating"
	case runtime.StateRunning:
		return "Running"
	case runtime.StateStopped:
		return "Stopped"
	case runtime.StateError:
		return "Error"
	case runtime.StateDeleting:
		return "Deleting"
	}
	return "?"
}

func env(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}

func mustEnv(k string) string {
	v := os.Getenv(k)
	if v == "" {
		log.Fatalf("env %s required", k)
	}
	return v
}
