package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func isolatedEnv(t *testing.T) {
	t.Helper()
	for _, key := range []string{"ENV_FILE", "AGENT_TOKEN", "DATABASE_URL", "REDIS_URL", "AGENT_ADDR", "HTTP_ADDR", "FEISHU_APP_ID", "FEISHU_APP_SECRET", "FEISHU_VERIFICATION_TOKEN", "FEISHU_ENCRYPT_KEY", "WECHAT_APP_ID", "WECHAT_APP_SECRET", "WECHAT_TOKEN"} {
		old, exists := os.LookupEnv(key)
		if err := os.Unsetenv(key); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if exists {
				_ = os.Setenv(key, old)
			} else {
				_ = os.Unsetenv(key)
			}
		})
	}
}

func writeFixture(t *testing.T, path, text string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestLoadFindsRootEnvFromCommandDirectories(t *testing.T) {
	for _, command := range []string{"api", "worker"} {
		t.Run(command, func(t *testing.T) {
			isolatedEnv(t)
			root := t.TempDir()
			writeFixture(t, filepath.Join(root, "go.mod"), "module fixture\n")
			writeFixture(t, filepath.Join(root, ".env"), "AGENT_TOKEN=fixture-secret-at-least-16\nHTTP_ADDR=127.0.0.1:18080\n")
			cwd := filepath.Join(root, "cmd", command)
			if err := os.MkdirAll(cwd, 0700); err != nil {
				t.Fatal(err)
			}
			t.Chdir(cwd)
			got, err := Load()
			if err != nil {
				t.Fatal(err)
			}
			if got.AgentToken != "fixture-secret-at-least-16" || got.Addr != "127.0.0.1:18080" {
				t.Fatal("project-root configuration was not loaded")
			}
		})
	}
}

func TestProcessEnvironmentOverridesFile(t *testing.T) {
	isolatedEnv(t)
	root := t.TempDir()
	writeFixture(t, filepath.Join(root, ".env"), "AGENT_TOKEN=fixture-secret-at-least-16\nHTTP_ADDR=127.0.0.1:18080\n")
	t.Chdir(root)
	t.Setenv("AGENT_TOKEN", "process-secret-at-least-16")
	t.Setenv("HTTP_ADDR", "127.0.0.1:28080")
	got, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if got.AgentToken != "process-secret-at-least-16" || got.Addr != "127.0.0.1:28080" {
		t.Fatal("file replaced process configuration")
	}
}

func TestExplicitEnvFile(t *testing.T) {
	isolatedEnv(t)
	root := t.TempDir()
	path := filepath.Join(root, "debug.env")
	writeFixture(t, path, "AGENT_TOKEN=explicit-secret-at-least-16\n")
	t.Chdir(t.TempDir())
	t.Setenv("ENV_FILE", path)
	got, err := Load()
	if err != nil || got.AgentToken != "explicit-secret-at-least-16" {
		t.Fatalf("explicit file: %v", err)
	}
}

func TestMissingExplicitEnvFileIsReported(t *testing.T) {
	isolatedEnv(t)
	t.Setenv("ENV_FILE", filepath.Join(t.TempDir(), "missing.env"))
	_, err := Load()
	if err == nil || !strings.Contains(err.Error(), "ENV_FILE") {
		t.Fatalf("missing explicit file was ignored: %v", err)
	}
}

func TestDoesNotLoadBeyondModuleRoot(t *testing.T) {
	isolatedEnv(t)
	parent := t.TempDir()
	writeFixture(t, filepath.Join(parent, ".env"), "AGENT_TOKEN=unrelated-parent-secret\n")
	root := filepath.Join(parent, "project")
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	writeFixture(t, filepath.Join(root, "go.mod"), "module fixture\n")
	t.Chdir(root)
	_, err := Load()
	if err == nil || !strings.Contains(err.Error(), "AGENT_TOKEN") {
		t.Fatalf("unrelated configuration was accepted: %v", err)
	}
}

func TestEnvironmentOnlyDeployment(t *testing.T) {
	isolatedEnv(t)
	root := t.TempDir()
	writeFixture(t, filepath.Join(root, "go.mod"), "module fixture\n")
	t.Chdir(root)
	t.Setenv("AGENT_TOKEN", "container-secret-at-least-16")
	if _, err := Load(); err != nil {
		t.Fatal(err)
	}
}
