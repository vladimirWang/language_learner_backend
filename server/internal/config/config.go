// Package config 从环境变量加载版本服务运行配置。
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// Config 是版本服务的全部运行期配置。
type Config struct {
	Addr string

	PGDSN      string
	PGMaxConns int32

	S3Endpoint  string
	S3Bucket    string
	S3AccessKey string
	S3SecretKey string
	S3UseSSL    bool

	MinAvailMB int64

	// Builder 子进程配置：默认在宿主机上执行 python3 -m builder。
	BuilderCmd  string
	BuilderArgs []string
	BuilderDir  string

	// RepoURLs 各语种素材仓库地址（用于监听变更），如 de -> git@github.com:user/de.git
	RepoURLs map[string]string

	// RabbitMQURL / PDFQueue 用于 PDF 试卷导入的异步解析。
	RabbitMQURL string
	PDFQueue    string
	WorkDir     string

	// 每日新词接口：无 key 时接口返回 503，避免匿名暴露。
	DailyWordsAPIKey         string
	DailyWordsRateLimit      int
	AppTZ                    string
	DailyChallengeGenerateAt string

	// 管理台 SSO 登录：BaseURL 供服务端换 token / 校验会话，PublicBaseURL 供浏览器跳转授权页。
	SSOBaseURL       string
	SSOPublicBaseURL string
	SSOClientID      string
	SSOClientSecret  string
	SSORedirectURI   string
	// 仅本地开发：跳过真实 SSO，固定返回开发账号。
	SSODevMock      bool
	SSODevMockEmail string

	// 微信小程序登录：AppID/AppSecret 用于 code2session，TokenSecret 用于签发登录态。
	WXAppID       string
	WXAppSecret   string
	WXTokenSecret string
	WXBaseURL     string
}

// Load 读取环境变量并做缺省处理；不因缺少可选项而失败。
func Load() Config {
	cfg := Config{
		Addr:        envOr("LISTEN_ADDR", ":8080"),
		PGMaxConns:  int32(envInt("PG_MAX_CONNS", 4)),
		S3Endpoint:  os.Getenv("S3_ENDPOINT"),
		S3Bucket:    os.Getenv("S3_BUCKET"),
		S3UseSSL:    strings.HasPrefix(strings.ToLower(os.Getenv("S3_ENDPOINT")), "https://"),
		MinAvailMB:  int64(envInt("MIN_AVAILABLE_MEM_MB", 120)),
		BuilderCmd:  envOr("BUILDER_CMD", "uv"),
		BuilderDir:  envOr("BUILDER_DIR", "../builder"),
		RabbitMQURL: envOr("RABBITMQ_URL", "amqp://root:1234@rabbitmq:5672/"),
		PDFQueue:    envOr("PDF_IMPORT_QUEUE", "pdf.import"),
		WorkDir:     envOr("WORK_DIR", "work"),

		DailyWordsAPIKey:         os.Getenv("DAILY_WORDS_API_KEY"),
		DailyWordsRateLimit:      envInt("DAILY_WORDS_RATE_LIMIT", 60),
		AppTZ:                    envOr("APP_TZ", "Asia/Shanghai"),
		DailyChallengeGenerateAt: envOr("DAILY_CHALLENGE_GENERATE_AT", "00:05"),

		SSOBaseURL:      os.Getenv("SSO_BASE_URL"),
		SSOClientID:     os.Getenv("SSO_CLIENT_ID"),
		SSOClientSecret: os.Getenv("SSO_CLIENT_SECRET"),
		SSORedirectURI:  os.Getenv("SSO_REDIRECT_URI"),
		SSODevMock:      envBool("SSO_DEV_MOCK", false),
		SSODevMockEmail: envOr("SSO_DEV_MOCK_EMAIL", "413114463@qq.com"),

		WXAppID:       os.Getenv("WX_APPID"),
		WXAppSecret:   os.Getenv("WX_APPSECRET"),
		WXTokenSecret: os.Getenv("WX_TOKEN_SECRET"),
		WXBaseURL:     os.Getenv("WX_BASE_URL"),
	}
	cfg.SSOPublicBaseURL = envOr("SSO_PUBLIC_BASE_URL", cfg.SSOBaseURL)
	cfg.S3AccessKey = os.Getenv("S3_ACCESS_KEY")
	cfg.S3SecretKey = os.Getenv("S3_SECRET_KEY")
	cfg.PGDSN = buildDSN()
	if raw := os.Getenv("BUILDER_ARGS"); raw != "" {
		cfg.BuilderArgs = strings.Fields(raw)
	} else {
		// 本地开发：在 builder/ 目录用 uv 虚拟环境执行一次性构建任务。
		cfg.BuilderArgs = []string{"run", "python", "-m", "builder"}
	}
	cfg.RepoURLs = parseRepoURLs()
	return cfg
}

// parseRepoURLs 从 REPO_<LANG>_URL 环境变量解析各语种仓库地址。
func parseRepoURLs() map[string]string {
	urls := map[string]string{}
	for _, lang := range []string{"de", "en", "ja"} {
		key := "REPO_" + strings.ToUpper(lang) + "_URL"
		if v := os.Getenv(key); v != "" {
			urls[lang] = v
		}
	}
	return urls
}

func buildDSN() string {
	if dsn := os.Getenv("PG_DSN"); dsn != "" {
		return dsn
	}
	host := envOr("PG_HOST", "127.0.0.1")
	port := envOr("PG_PORT", "5432")
	user := envOr("PG_USER", "postgres")
	password := os.Getenv("PG_PASSWORD")
	database := envOr("PG_DATABASE", "postgres")
	sslMode := envOr("PG_SSLMODE", "disable")
	return fmt.Sprintf("postgres://%s:%s@%s:%s/%s?sslmode=%s",
		user, password, host, port, database, sslMode)
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func envBool(key string, fallback bool) bool {
	v := strings.TrimSpace(strings.ToLower(os.Getenv(key)))
	if v == "" {
		return fallback
	}
	switch v {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	default:
		return fallback
	}
}

func envInt(key string, fallback int) int {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return fallback
	}
	return n
}
