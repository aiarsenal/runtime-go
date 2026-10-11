package storage

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"sync"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

const s3ProviderName = "s3"

func init() {
	Register(s3ProviderName, FromConfigS3)
}

type s3Config struct {
	Endpoint       string // S3 兼容端点：OSS oss-cn-beijing.aliyuncs.com / COS cos.ap-beijing.myqcloud.com / MinIO host:9000
	Region         string
	AccessKey      string
	SecretKey      string
	Bucket         string
	BasePath       string // key 前缀（可空）
	UseSSL         bool
	ForcePathStyle bool   // OSS/COS/MinIO 建议 true；AWS S3 一般 false
	LocalFallback  string // endpoint 空时降级到本机磁盘（容灾，可选）
}

type s3Storage struct {
	cfg        s3Config
	client     *minio.Client
	bucket     string
	fallback   Storage // endpoint 空时委托本地
	ensureOnce sync.Once
	ensureErr  error
}

// FromConfigS3 从通用 Config 构造 S3 兼容存储。
func FromConfigS3(cfg Config) (Storage, error) {
	str := func(k string) string { s, _ := cfg.Params[k].(string); return s }
	boolP := func(k string, def bool) bool {
		v, ok := cfg.Params[k].(bool)
		if !ok {
			return def
		}
		return v
	}
	return NewS3(s3Config{
		Endpoint:       str("endpoint"),
		Region:         str("region"),
		AccessKey:      str("accessKey"),
		SecretKey:      str("secretKey"),
		Bucket:         str("bucket"),
		BasePath:       strings.Trim(str("basePath"), "/"),
		UseSSL:         boolP("useSSL", true),
		ForcePathStyle: boolP("forcePathStyle", true),
		LocalFallback:  str("localFallback"),
	})
}

// NewS3 按 s3Config 构造存储。endpoint 空且配了 LocalFallback 时降级委托本地磁盘。
func NewS3(rc s3Config) (Storage, error) {
	if strings.TrimSpace(rc.Endpoint) == "" {
		if rc.LocalFallback == "" {
			return nil, errors.New("storage: s3 endpoint empty and no localFallback configured")
		}
		fb, err := FromConfigLocal(Config{Type: localProviderName, Params: map[string]any{"localRoot": rc.LocalFallback}})
		if err != nil {
			return nil, err
		}
		return &s3Storage{cfg: rc, fallback: fb}, nil
	}
	if rc.Bucket == "" {
		return nil, errors.New("storage: s3 bucket required")
	}
	lookup := minio.BucketLookupPath
	if !rc.ForcePathStyle {
		lookup = minio.BucketLookupDNS
	}
	client, err := minio.New(rc.Endpoint, &minio.Options{
		Creds:        credentials.NewStaticV4(rc.AccessKey, rc.SecretKey, ""),
		Secure:       rc.UseSSL,
		Region:       rc.Region,
		BucketLookup: lookup,
	})
	if err != nil {
		return nil, err
	}
	return &s3Storage{cfg: rc, client: client, bucket: rc.Bucket}, nil
}

func (s *s3Storage) Provider() string {
	if s.fallback != nil {
		return s3ProviderName + "(fallback:local)"
	}
	return s3ProviderName
}

func (s *s3Storage) resolve(key string) string {
	if s.cfg.BasePath == "" {
		return key
	}
	return s.cfg.BasePath + "/" + key
}

// ensureBucket 首次写入前确保 bucket 存在（懒创建，sync.Once 只执行一次）。
func (s *s3Storage) ensureBucket(ctx context.Context) error {
	s.ensureOnce.Do(func() {
		exists, err := s.client.BucketExists(ctx, s.bucket)
		if err != nil {
			s.ensureErr = err
			return
		}
		if !exists {
			s.ensureErr = s.client.MakeBucket(ctx, s.bucket, minio.MakeBucketOptions{Region: s.cfg.Region})
		}
	})
	return s.ensureErr
}

func (s *s3Storage) Save(ctx context.Context, key string, r io.Reader) (int64, error) {
	if s.fallback != nil {
		return s.fallback.Save(ctx, key, r)
	}
	if err := s.ensureBucket(ctx); err != nil {
		return 0, err
	}
	// 包文件较小（≤10MB），读入 buffer 以拿到准确 size；流式 -1 分片需要可 Seek 的 reader，buffer 更稳。
	data, err := io.ReadAll(r)
	if err != nil {
		return 0, err
	}
	if _, err := s.client.PutObject(ctx, s.bucket, s.resolve(key), bytes.NewReader(data), int64(len(data)),
		minio.PutObjectOptions{ContentType: "application/octet-stream"}); err != nil {
		return 0, err
	}
	return int64(len(data)), nil
}

func (s *s3Storage) Open(ctx context.Context, key string) (io.ReadCloser, error) {
	if s.fallback != nil {
		return s.fallback.Open(ctx, key)
	}
	obj, err := s.client.GetObject(ctx, s.bucket, s.resolve(key), minio.GetObjectOptions{})
	if err != nil {
		return nil, err
	}
	// 触发 stat 以早暴露 NoSuchKey（minio GetObject 延迟错误）。
	if _, err := obj.Stat(); err != nil {
		obj.Close()
		if minio.ToErrorResponse(err).Code == "NoSuchKey" {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return obj, nil
}

func (s *s3Storage) Delete(ctx context.Context, key string) error {
	if s.fallback != nil {
		return s.fallback.Delete(ctx, key)
	}
	return s.client.RemoveObject(ctx, s.bucket, s.resolve(key), minio.RemoveObjectOptions{})
}

func (s *s3Storage) Stat(ctx context.Context, key string) (*ObjectInfo, error) {
	if s.fallback != nil {
		return s.fallback.Stat(ctx, key)
	}
	fi, err := s.client.StatObject(ctx, s.bucket, s.resolve(key), minio.StatObjectOptions{})
	if err != nil {
		if minio.ToErrorResponse(err).Code == "NoSuchKey" {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &ObjectInfo{Key: key, Size: fi.Size, ContentType: fi.ContentType, LastModified: fi.LastModified}, nil
}

func (s *s3Storage) Close() error { return nil }
