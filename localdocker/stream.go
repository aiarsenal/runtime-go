package localdocker

import (
	"archive/tar"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"time"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/client"

	"github.com/aiarsenal/runtime-go"
)

// hijackStream 包装 docker 的 hijacked connection，实现 runtime.ReadWriteCloser。
type hijackStream struct {
	hijack types.HijackedResponse
	cli    *client.Client
	execID string
}

func (s *hijackStream) Read(p []byte) (int, error)  { return s.hijack.Reader.Read(p) }
func (s *hijackStream) Write(p []byte) (int, error) { return s.hijack.Conn.Write(p) }
func (s *hijackStream) Close() error {
	s.hijack.Close()
	// 关闭后查询 exec 结果（best-effort，用于 daemon 释放资源）。
	_, _ = s.cli.ContainerExecInspect(context.Background(), s.execID)
	return nil
}

// untarSingle 从 tar 流中取出第一个成员的内容，返回 ReadCloser。
func untarSingle(rc io.ReadCloser) (io.ReadCloser, error) {
	tr := tar.NewReader(rc)
	if _, err := tr.Next(); err != nil {
		return nil, fmt.Errorf("localdocker: untar: %w", err)
	}
	// 返回一个读完内容且关闭底层 rc 的 reader。
	return &tarReader{tr: tr, rc: rc}, nil
}

type tarReader struct {
	tr *tar.Reader
	rc io.Closer
}

func (t *tarReader) Read(p []byte) (int, error) { return t.tr.Read(p) }
func (t *tarReader) Close() error               { return t.rc.Close() }

// parseStats 解析 docker stats JSON，提取 cpu/内存用量。
func parseStats(body io.Reader, instanceID string) (*runtime.Metrics, error) {
	var raw struct {
		Read   time.Time `json:"read"`
		Memory struct {
			Usage uint64 `json:"usage"`
		} `json:"memory_stats"`
		CPUCPU struct {
			CPUUsage struct {
				TotalUsage uint64   `json:"total_usage"`
				Percpu     []uint64 `json:"percpu_usage"`
			} `json:"cpu_usage"`
			SystemUsage uint64 `json:"system_cpu_usage"`
			OnlineCPUs  uint64 `json:"online_cpus"`
		} `json:"cpu_stats"`
		PreCPU struct {
			CPUUsage struct {
				TotalUsage uint64 `json:"total_usage"`
			} `json:"cpu_usage"`
			SystemUsage uint64 `json:"system_cpu_usage"`
		} `json:"precpu_stats"`
	}
	if err := json.NewDecoder(body).Decode(&raw); err != nil {
		return nil, fmt.Errorf("localdocker: decode stats: %w", err)
	}
	m := &runtime.Metrics{
		MemoryMB:   float64(raw.Memory.Usage) / (1024 * 1024),
		RecordedAt: raw.Read,
	}
	// CPU 百分比 = Δusage / Δsystem * onlineCPUs * 100。
	deltaUsage := raw.CPUCPU.CPUUsage.TotalUsage - raw.PreCPU.CPUUsage.TotalUsage
	deltaSystem := raw.CPUCPU.SystemUsage - raw.PreCPU.SystemUsage
	online := raw.CPUCPU.OnlineCPUs
	if online == 0 {
		online = uint64(len(raw.CPUCPU.CPUUsage.Percpu))
	}
	if deltaSystem > 0 && online > 0 {
		m.CPUPercent = math.Round(float64(deltaUsage)/float64(deltaSystem)*float64(online)*100) / 100
	}
	return m, nil
}

// parseDockerPort 解析 "8080/tcp" → (8080, "tcp")。
func parseDockerPort(s string) (int, string) {
	var port int
	var proto string
	if i := lastIndexByte(s, '/'); i >= 0 {
		proto = s[i+1:]
		port = atoi(s[:i])
	} else {
		port = atoi(s)
		proto = "tcp"
	}
	if proto == "" {
		proto = "tcp"
	}
	return port, proto
}

func atoi(s string) int {
	n := 0
	for _, c := range []byte(s) {
		if c < '0' || c > '9' {
			break
		}
		n = n*10 + int(c-'0')
	}
	return n
}

func lastIndexByte(s string, b byte) int {
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] == b {
			return i
		}
	}
	return -1
}
