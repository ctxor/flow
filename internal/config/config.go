package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/joho/godotenv"
)

type Config struct {
	Addr, DatabaseURL, RedisURL, AgentAddr, AgentToken                     string
	SecureCookie                                                           bool
	FeishuAppID, FeishuSecret, FeishuToken, FeishuEncryptKey               string
	WechatAppID, WechatSecret, WechatToken, WechatAESKey, WechatTemplateID string
	WechatTemplateField                                                    string
}

func Load() (Config, error) {
	if err := loadEnv(); err != nil {
		return Config{}, err
	}
	c := Config{
		Addr:        env("HTTP_ADDR", "127.0.0.1:8080"),
		DatabaseURL: env("DATABASE_URL", "postgres://assistant:assistant@127.0.0.1:55432/assistant?sslmode=disable"),
		RedisURL:    env("REDIS_URL", "redis://127.0.0.1:56379/0"),
		AgentAddr:   env("AGENT_ADDR", "127.0.0.1:50051"), AgentToken: os.Getenv("AGENT_TOKEN"),
		SecureCookie: env("COOKIE_SECURE", "false") == "true",
		FeishuAppID:  os.Getenv("FEISHU_APP_ID"), FeishuSecret: os.Getenv("FEISHU_APP_SECRET"),
		FeishuToken: os.Getenv("FEISHU_VERIFICATION_TOKEN"), FeishuEncryptKey: os.Getenv("FEISHU_ENCRYPT_KEY"),
		WechatAppID: os.Getenv("WECHAT_APP_ID"), WechatSecret: os.Getenv("WECHAT_APP_SECRET"),
		WechatToken: os.Getenv("WECHAT_TOKEN"), WechatAESKey: os.Getenv("WECHAT_AES_KEY"),
		WechatTemplateID: os.Getenv("WECHAT_TEMPLATE_ID"), WechatTemplateField: env("WECHAT_TEMPLATE_FIELD", "thing1"),
	}
	if len(c.AgentToken) < 16 {
		return c, fmt.Errorf("AGENT_TOKEN must have at least 16 characters; run make init")
	}
	for _, pair := range [][2]string{{c.FeishuAppID, c.FeishuSecret}, {c.WechatAppID, c.WechatSecret}} {
		if (pair[0] == "") != (pair[1] == "") {
			return c, fmt.Errorf("platform app ID and secret must be configured together")
		}
	}
	if c.FeishuAppID != "" && (c.FeishuToken == "" || c.FeishuEncryptKey == "") {
		return c, fmt.Errorf("Feishu callbacks require verification token and encryption key")
	}
	if c.WechatAppID != "" && c.WechatToken == "" {
		return c, fmt.Errorf("WeChat callbacks require WECHAT_TOKEN")
	}
	return c, nil
}

// loadEnv supports both project-root execution and IDE launches from cmd/api or
// cmd/worker. Existing process environment always takes precedence over the file.
func loadEnv() error {
	if path := strings.TrimSpace(os.Getenv("ENV_FILE")); path != "" {
		if err := godotenv.Load(path); err != nil {
			return fmt.Errorf("load ENV_FILE %q: %w", path, err)
		}
		return nil
	}
	dir, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("locate project configuration: %w", err)
	}
	for {
		path := filepath.Join(dir, ".env")
		if _, err := os.Stat(path); err == nil {
			if err := godotenv.Load(path); err != nil {
				return fmt.Errorf("load configuration %q: %w", path, err)
			}
			return nil
		} else if !os.IsNotExist(err) {
			return fmt.Errorf("read configuration %q: %w", path, err)
		}
		// Never pick up an unrelated parent project's configuration.
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return nil // Containers can supply configuration entirely via environment.
		}
		dir = parent
	}
}

func env(key, fallback string) string {
	if s := strings.TrimSpace(os.Getenv(key)); s != "" {
		return s
	}
	return fallback
}
func IntEnv(key string, fallback int) int {
	if n, e := strconv.Atoi(os.Getenv(key)); e == nil && n > 0 {
		return n
	}
	return fallback
}
