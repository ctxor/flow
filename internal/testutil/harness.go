package testutil

import (
	"context"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"
	"protobuf/internal/core"
	"protobuf/internal/model"
	"protobuf/internal/store"
)

type Harness struct {
	DB    *gorm.DB
	Core  *core.Service
	Redis *redis.Client
}

func New(t *testing.T) *Harness {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set TEST_DATABASE_URL to run isolated PostgreSQL integration tests")
	}
	admin, err := store.Open(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := "test_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if err = admin.Exec("CREATE SCHEMA " + schema).Error; err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	db, err := store.Open(context.Background(), u.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		raw, _ := db.DB()
		raw.Close()
		admin.Exec("DROP SCHEMA " + schema + " CASCADE")
		raw, _ = admin.DB()
		raw.Close()
	})
	if err = store.Migrate(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	mini := miniredis.RunT(t)
	r := redis.NewClient(&redis.Options{Addr: mini.Addr()})
	t.Cleanup(func() { r.Close() })
	return &Harness{DB: db, Core: &core.Service{DB: db}, Redis: r}
}
func (h *Harness) User(t *testing.T, name string) (model.User, string, model.Workspace) {
	t.Helper()
	u, token, err := h.Core.Register(context.Background(), name+"@example.test", "test-password-123", "Asia/Shanghai")
	if err != nil {
		t.Fatal(err)
	}
	w, err := h.Core.Workspaces(context.Background(), u.ID)
	if err != nil || len(w) != 1 {
		t.Fatalf("workspaces: %v %v", w, err)
	}
	return u, token, w[0]
}
