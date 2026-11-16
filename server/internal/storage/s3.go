// Package storage 生成对象存储的预签名下载链接。
package storage

import (
	"context"
	"net/url"
	"strings"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

// Presigner 基于 S3 兼容存储（MinIO / 阿里云 OSS）签发临时下载地址。
type Presigner struct {
	client *minio.Client
	bucket string
}

// NewPresigner 当 endpoint / bucket 未配置时返回 nil，表示使用本地路径直出。
func NewPresigner(endpoint, bucket, accessKey, secretKey string, useSSL bool) (*Presigner, error) {
	if endpoint == "" || bucket == "" {
		return nil, nil
	}
	host := strings.TrimPrefix(strings.TrimPrefix(endpoint, "https://"), "http://")
	client, err := minio.New(host, &minio.Options{
		Creds:  credentials.NewStaticV4(accessKey, secretKey, ""),
		Secure: useSSL,
	})
	if err != nil {
		return nil, err
	}
	return &Presigner{client: client, bucket: bucket}, nil
}

// URL 把对象 key 转为 7 天有效的下载链接；已是完整 URL / 本地路径时原样返回。
func (p *Presigner) URL(ctx context.Context, key string) (string, error) {
	if p == nil || key == "" || strings.HasPrefix(key, "http://") ||
		strings.HasPrefix(key, "https://") || strings.HasPrefix(key, "file:") ||
		strings.HasPrefix(key, "/") {
		return key, nil
	}
	u, err := p.client.PresignedGetObject(ctx, p.bucket, key, 7*24*time.Hour, url.Values{})
	if err != nil {
		return "", err
	}
	return u.String(), nil
}
