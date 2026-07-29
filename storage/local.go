package storage

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const localProviderName = "local"

func init() {
	Register(localProviderName, FromConfigLocal)
}

type localStorage struct{ root string }

// FromConfigLocal 从 Config.Params["localRoot"] 构造本地存储；为空则默认 ./data/skill。
func FromConfigLocal(cfg Config) (Storage, error) {
	root, _ := cfg.Params["localRoot"].(string)
	if root == "" {
		root = "./data/skill"
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, err
	}
	return &localStorage{root: root}, nil
}

func (s *localStorage) Provider() string { return localProviderName }

// full 把相对 key 解析为磁盘绝对路径，含路径穿越保护（拒绝含 .. 的 key）。
func (s *localStorage) full(key string) (string, error) {
	if strings.Contains(key, "..") {
		return "", errUnsafeKey
	}
	return filepath.Join(s.root, filepath.Clean("/"+key)), nil
}

func (s *localStorage) Save(_ context.Context, key string, r io.Reader) (int64, error) {
	full, err := s.full(key)
	if err != nil {
		return 0, err
	}
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return 0, err
	}
	f, err := os.Create(full)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	return io.Copy(f, r)
}

func (s *localStorage) Open(_ context.Context, key string) (io.ReadCloser, error) {
	full, err := s.full(key)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(full)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return f, nil
}

func (s *localStorage) Delete(_ context.Context, key string) error {
	full, err := s.full(key)
	if err != nil {
		return err
	}
	if err := os.Remove(full); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil // 不存在视为成功（幂等）
}

func (s *localStorage) Stat(_ context.Context, key string) (*ObjectInfo, error) {
	full, err := s.full(key)
	if err != nil {
		return nil, err
	}
	fi, err := os.Stat(full)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &ObjectInfo{Key: key, Size: fi.Size(), LastModified: fi.ModTime()}, nil
}

func (s *localStorage) Close() error { return nil }
