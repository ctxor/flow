package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gin-gonic/gin/binding"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"
	"protobuf/internal/config"
	"protobuf/internal/core"
	"protobuf/internal/model"
	"protobuf/internal/platform"
	"protobuf/web"
)

type Server struct {
	Core      *core.Service
	Redis     *redis.Client
	Config    config.Config
	Platforms *platform.Manager
}

func (s *Server) Router() *gin.Engine {
	gin.SetMode(gin.ReleaseMode)
	binding.EnableDecoderDisallowUnknownFields = true
	r := gin.New()
	_ = r.SetTrustedProxies(nil)
	r.Use(gin.CustomRecoveryWithWriter(io.Discard, func(c *gin.Context, recovered any) {
		slog.Error("HTTP panic", "request_id", c.GetString("request_id"))
		c.AbortWithStatusJSON(500, gin.H{"error": "服务暂时不可用"})
	}))
	r.Use(func(c *gin.Context) {
		id := core.ID()
		c.Set("request_id", id)
		c.Header("X-Request-ID", id)
		c.Header("X-Content-Type-Options", "nosniff")
		c.Header("Referrer-Policy", "same-origin")
		c.Header("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; connect-src 'self'; frame-ancestors 'none'; base-uri 'self'; form-action 'self'")
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 1<<20)
		if c.Request.Method != "GET" && c.Request.Method != "HEAD" {
			if origin := c.GetHeader("Origin"); origin != "" {
				u, err := url.Parse(origin)
				if err != nil || u.Host != c.Request.Host {
					c.AbortWithStatusJSON(403, gin.H{"error": "请求来源不匹配"})
					return
				}
			}
		}
		c.Next()
	})
	r.GET("/healthz", func(c *gin.Context) { c.JSON(200, gin.H{"status": "ok"}) })
	r.GET("/readyz", func(c *gin.Context) {
		ctx, cancel := context.WithTimeout(c.Request.Context(), 2*time.Second)
		defer cancel()
		db, err := s.Core.DB.DB()
		if err != nil || db.PingContext(ctx) != nil || s.Redis.Ping(ctx).Err() != nil {
			c.JSON(503, gin.H{"status": "unavailable"})
			return
		}
		c.JSON(200, gin.H{"status": "ready"})
	})
	r.POST("/callbacks/feishu", gin.WrapF(s.Platforms.FeishuWebhook))
	r.GET("/callbacks/wechat", gin.WrapF(s.Platforms.WechatWebhook))
	r.POST("/callbacks/wechat", gin.WrapF(s.Platforms.WechatWebhook))
	r.POST("/v1/auth/register", s.rateLimit(), s.register)
	r.POST("/v1/auth/login", s.rateLimit(), s.login)
	v := r.Group("/v1", s.auth())
	v.GET("/me", func(c *gin.Context) { c.JSON(200, user(c)) })
	v.POST("/auth/logout", s.logout)
	v.GET("/workspaces", func(c *gin.Context) {
		out, err := s.Core.Workspaces(c.Request.Context(), user(c).ID)
		respond(c, 200, out, err)
	})
	v.POST("/workspaces", func(c *gin.Context) {
		var in struct {
			Name string `json:"name"`
		}
		if !decode(c, &in) {
			return
		}
		out, err := s.Core.CreateWorkspace(c.Request.Context(), user(c).ID, in.Name)
		respond(c, 201, out, err)
	})
	v.GET("/workspaces/:id/members", s.uuidParam(), func(c *gin.Context) {
		out, err := s.Core.Members(c.Request.Context(), user(c).ID, c.Param("id"))
		respond(c, 200, out, err)
	})
	v.POST("/workspaces/:id/members", s.uuidParam(), func(c *gin.Context) {
		var in struct {
			Email string `json:"email"`
			Role  string `json:"role"`
		}
		if !decode(c, &in) {
			return
		}
		err := s.Core.AddMember(c.Request.Context(), user(c).ID, c.Param("id"), in.Email, in.Role)
		respond(c, 200, gin.H{"ok": true}, err)
	})
	v.GET("/tasks", func(c *gin.Context) {
		out, err := s.Core.Tasks(c.Request.Context(), user(c).ID, c.Query("workspace_id"))
		respond(c, 200, out, err)
	})
	v.POST("/tasks", func(c *gin.Context) {
		var in core.CreateTaskInput
		if !decode(c, &in) {
			return
		}
		out, err := s.Core.CreateTask(c.Request.Context(), user(c).ID, in)
		respond(c, 201, out, err)
	})
	v.PATCH("/tasks/:id", s.uuidParam(), func(c *gin.Context) {
		var in core.UpdateTaskInput
		if !decode(c, &in) {
			return
		}
		out, err := s.Core.UpdateTask(c.Request.Context(), user(c).ID, c.Param("id"), in)
		respond(c, 200, out, err)
	})
	v.GET("/tasks/:id/comments", s.uuidParam(), func(c *gin.Context) {
		out, err := s.Core.Comments(c.Request.Context(), user(c).ID, c.Param("id"))
		respond(c, 200, out, err)
	})
	v.POST("/tasks/:id/comments", s.uuidParam(), func(c *gin.Context) {
		var in struct {
			Body string `json:"body"`
		}
		if !decode(c, &in) {
			return
		}
		out, err := s.Core.AddComment(c.Request.Context(), user(c).ID, c.Param("id"), in.Body)
		respond(c, 201, out, err)
	})
	v.GET("/notes", func(c *gin.Context) {
		out, err := s.Core.Notes(c.Request.Context(), user(c).ID, c.Query("workspace_id"), c.Query("q"))
		respond(c, 200, out, err)
	})
	v.POST("/notes", func(c *gin.Context) {
		var in core.CreateNoteInput
		if !decode(c, &in) {
			return
		}
		out, err := s.Core.CreateNote(c.Request.Context(), user(c).ID, in)
		respond(c, 201, out, err)
	})
	v.DELETE("/notes/:id", s.uuidParam(), s.deleteNote)
	v.GET("/events", func(c *gin.Context) {
		out, err := s.Core.Events(c.Request.Context(), user(c).ID, c.Query("workspace_id"))
		respond(c, 200, out, err)
	})
	v.POST("/events", func(c *gin.Context) {
		var in core.CreateEventInput
		if !decode(c, &in) {
			return
		}
		out, err := s.Core.CreateEvent(c.Request.Context(), user(c).ID, in)
		respond(c, 201, out, err)
	})
	v.DELETE("/events/:id", s.uuidParam(), s.deleteEvent)
	v.GET("/reminders", s.reminders)
	v.DELETE("/reminders/:id", s.uuidParam(), s.cancelReminder)
	v.POST("/assistant/messages", func(c *gin.Context) {
		var in struct {
			WorkspaceID string `json:"workspace_id"`
			Text        string `json:"text"`
		}
		if !decode(c, &in) {
			return
		}
		out, err := s.Core.NewJob(c.Request.Context(), user(c).ID, in.WorkspaceID, in.Text, c.GetHeader("Idempotency-Key"), "web")
		respond(c, 202, out, err)
	})
	v.GET("/agent-jobs/:id", s.uuidParam(), func(c *gin.Context) {
		var out model.AgentJob
		err := s.Core.DB.WithContext(c.Request.Context()).Where("id=? AND user_id=?", c.Param("id"), user(c).ID).First(&out).Error
		respond(c, 200, out, err)
	})
	v.GET("/notifications", func(c *gin.Context) {
		after, _ := strconv.ParseInt(c.DefaultQuery("after", "0"), 10, 64)
		out, err := s.Core.Notifications(c.Request.Context(), user(c).ID, after)
		respond(c, 200, out, err)
	})
	v.GET("/stream", s.events)
	v.GET("/bindings", func(c *gin.Context) {
		out := []model.PlatformIdentity{}
		err := s.Core.DB.WithContext(c.Request.Context()).Where("user_id=?", user(c).ID).Find(&out).Error
		respond(c, 200, out, err)
	})
	v.POST("/bindings/codes", func(c *gin.Context) {
		var in struct {
			Provider string `json:"provider"`
		}
		if !decode(c, &in) {
			return
		}
		if in.Provider == "feishu" && s.Config.FeishuAppID == "" || in.Provider == "wechat" && s.Config.WechatAppID == "" {
			respond(c, 503, nil, fmt.Errorf("%w：该平台尚未配置应用凭证", core.ErrInvalid))
			return
		}
		code, err := s.Core.NewBindingCode(c.Request.Context(), user(c).ID, in.Provider)
		respond(c, 201, gin.H{"code": code, "expires_in": 600, "command": "绑定 " + code}, err)
	})
	v.DELETE("/bindings/:provider", s.unbind)
	v.GET("/deliveries", func(c *gin.Context) {
		out := []model.Delivery{}
		err := s.Core.DB.WithContext(c.Request.Context()).Model(&model.Delivery{}).Joins("JOIN notifications ON notifications.id=deliveries.notification_id").Where("notifications.user_id=?", user(c).ID).Order("deliveries.created_at DESC").Limit(100).Find(&out).Error
		respond(c, 200, out, err)
	})
	v.GET("/capabilities", func(c *gin.Context) {
		c.JSON(200, gin.H{"feishu": s.Config.FeishuAppID != "", "wechat": s.Config.WechatAppID != "", "wechat_reminders": s.Config.WechatTemplateID != ""})
	})
	asset := func(name, contentType string) gin.HandlerFunc {
		data, _ := web.Assets.ReadFile(name)
		return func(c *gin.Context) { c.Header("Cache-Control", "no-cache"); c.Data(200, contentType, data) }
	}
	r.GET("/", asset("index.html", "text/html; charset=utf-8"))
	r.GET("/app.js", asset("app.js", "application/javascript; charset=utf-8"))
	r.GET("/style.css", asset("style.css", "text/css; charset=utf-8"))
	return r
}
func user(c *gin.Context) model.User { return c.MustGet("user").(model.User) }
func decode(c *gin.Context, v any) bool {
	if err := c.ShouldBindJSON(v); err != nil {
		c.JSON(400, gin.H{"error": "请求格式不正确，请检查字段和时间格式"})
		return false
	}
	return true
}
func respond(c *gin.Context, status int, v any, err error) {
	if err == nil {
		c.JSON(status, v)
		return
	}
	message := "服务暂时不可用"
	code := 500
	switch {
	case errors.Is(err, core.ErrInvalid):
		code = 400
		message = err.Error()
	case errors.Is(err, core.ErrForbidden):
		code = 403
		message = err.Error()
	case errors.Is(err, core.ErrUnauthorized):
		code = 401
		message = err.Error()
	case errors.Is(err, core.ErrConflict):
		code = 409
		message = err.Error()
	case errors.Is(err, gorm.ErrDuplicatedKey):
		code = 409
		message = "记录已存在"
	case errors.Is(err, gorm.ErrRecordNotFound):
		code = 404
		message = "记录不存在"
	default:
		slog.Error("HTTP operation failed", "request_id", c.GetString("request_id"), "error", err)
	}
	c.JSON(code, gin.H{"error": message, "request_id": c.GetString("request_id")})
}
func (s *Server) uuidParam() gin.HandlerFunc {
	return func(c *gin.Context) {
		if _, err := uuid.Parse(c.Param("id")); err != nil {
			c.AbortWithStatusJSON(400, gin.H{"error": "无效的记录ID"})
			return
		}
		c.Next()
	}
}
func (s *Server) auth() gin.HandlerFunc {
	return func(c *gin.Context) {
		token := ""
		if h := c.GetHeader("Authorization"); strings.HasPrefix(h, "Bearer ") {
			token = strings.TrimPrefix(h, "Bearer ")
		} else {
			token, _ = c.Cookie("session")
		}
		u, err := s.Core.Authenticate(c.Request.Context(), token)
		if err != nil {
			c.AbortWithStatusJSON(401, gin.H{"error": "请先登录"})
			return
		}
		c.Set("user", u)
		c.Set("session_token", token)
		c.Header("Cache-Control", "no-store")
		c.Next()
	}
}
func (s *Server) rateLimit() gin.HandlerFunc {
	return func(c *gin.Context) {
		ctx, cancel := context.WithTimeout(c.Request.Context(), time.Second)
		defer cancel()
		key := "assistant:auth-limit:" + core.Hash(c.ClientIP()) + ":" + time.Now().UTC().Format("200601021504")
		result, err := s.Redis.Eval(ctx, "local n=redis.call('INCR',KEYS[1]); if n==1 then redis.call('EXPIRE',KEYS[1],65) end; return n", []string{key}).Int()
		if err != nil {
			c.AbortWithStatusJSON(503, gin.H{"error": "登录服务暂时不可用"})
			return
		}
		if result > 20 {
			c.Header("Retry-After", "60")
			c.AbortWithStatusJSON(429, gin.H{"error": "尝试过于频繁，请稍后再试"})
			return
		}
		c.Next()
	}
}

type credentials struct {
	Email    string `json:"email"`
	Password string `json:"password"`
	Timezone string `json:"timezone"`
}

func (s *Server) cookie(c *gin.Context, token string, age int) {
	c.SetSameSite(http.SameSiteStrictMode)
	c.SetCookie("session", token, age, "/", "", s.Config.SecureCookie, true)
}
func (s *Server) register(c *gin.Context) {
	var in credentials
	if !decode(c, &in) {
		return
	}
	u, token, err := s.Core.Register(c.Request.Context(), in.Email, in.Password, in.Timezone)
	if err != nil {
		respond(c, 0, nil, err)
		return
	}
	s.cookie(c, token, 7*24*3600)
	c.Header("Cache-Control", "no-store")
	c.JSON(201, gin.H{"user": u, "access_token": token})
}
func (s *Server) login(c *gin.Context) {
	var in credentials
	if !decode(c, &in) {
		return
	}
	u, token, err := s.Core.Login(c.Request.Context(), in.Email, in.Password)
	if err != nil {
		respond(c, 0, nil, err)
		return
	}
	s.cookie(c, token, 7*24*3600)
	c.Header("Cache-Control", "no-store")
	c.JSON(200, gin.H{"user": u, "access_token": token})
}
func (s *Server) logout(c *gin.Context) {
	err := s.Core.DB.WithContext(c.Request.Context()).Delete(&model.Session{}, "hash=?", core.Hash(c.GetString("session_token"))).Error
	s.cookie(c, "", -1)
	respond(c, 200, gin.H{"ok": true}, err)
}
func (s *Server) reminders(c *gin.Context) {
	out := []model.Reminder{}
	err := s.Core.DB.WithContext(c.Request.Context()).Where("user_id=?", user(c).ID).Order("fire_at DESC").Limit(200).Find(&out).Error
	respond(c, 200, out, err)
}
func (s *Server) cancelReminder(c *gin.Context) {
	res := s.Core.DB.WithContext(c.Request.Context()).Model(&model.Reminder{}).Where("id=? AND user_id=?", c.Param("id"), user(c).ID).Updates(map[string]any{"status": "cancelled", "version": gorm.Expr("version+1")})
	if res.Error == nil && res.RowsAffected == 0 {
		res.Error = gorm.ErrRecordNotFound
	}
	respond(c, 200, gin.H{"ok": true}, res.Error)
}
func (s *Server) deleteNote(c *gin.Context) {
	var n model.Note
	err := s.Core.DB.WithContext(c.Request.Context()).First(&n, "id=?", c.Param("id")).Error
	if err == nil {
		err = s.Core.Check(c.Request.Context(), user(c).ID, n.WorkspaceID, true)
		if n.CreatedBy != user(c).ID {
			err = core.ErrForbidden
		}
	}
	if err == nil {
		err = s.Core.DB.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
			if err := tx.Delete(&n).Error; err != nil {
				return err
			}
			target := ""
			if n.Visibility == "private" {
				target = n.CreatedBy
			}
			return (&core.Service{DB: tx}).Change(c.Request.Context(), n.WorkspaceID, target)
		})
	}
	respond(c, 200, gin.H{"ok": true}, err)
}
func (s *Server) deleteEvent(c *gin.Context) {
	err := s.Core.DB.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		var e model.CalendarEvent
		if err := tx.Clauses().First(&e, "id=?", c.Param("id")).Error; err != nil {
			return err
		}
		if err := (&core.Service{DB: tx}).Check(c.Request.Context(), user(c).ID, e.WorkspaceID, true); err != nil {
			return err
		}
		if err := tx.Model(&model.Reminder{}).Where("object_type='event' AND object_id=?", e.ID).Updates(map[string]any{"status": "cancelled", "version": gorm.Expr("version+1")}).Error; err != nil {
			return err
		}
		if err := tx.Delete(&e).Error; err != nil {
			return err
		}
		return (&core.Service{DB: tx}).Change(c.Request.Context(), e.WorkspaceID, "")
	})
	respond(c, 200, gin.H{"ok": true}, err)
}
func (s *Server) unbind(c *gin.Context) {
	p := c.Param("provider")
	if p != "feishu" && p != "wechat" {
		respond(c, 0, nil, core.ErrInvalid)
		return
	}
	err := s.Core.DB.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		if err := tx.Delete(&model.PlatformIdentity{}, "user_id=? AND provider=?", user(c).ID, p).Error; err != nil {
			return err
		}
		return tx.Model(&model.Delivery{}).Where("provider=? AND status IN ('queued','retry') AND notification_id IN (SELECT id FROM notifications WHERE user_id=?)", p, user(c).ID).Updates(map[string]any{"status": "cancelled"}).Error
	})
	respond(c, 200, gin.H{"ok": true}, err)
}
func (s *Server) events(c *gin.Context) {
	cursor, _ := strconv.ParseInt(c.DefaultQuery("after", "0"), 10, 64)
	if h := c.GetHeader("Last-Event-ID"); h != "" {
		if n, err := strconv.ParseInt(h, 10, 64); err == nil && n > cursor {
			cursor = n
		}
	}
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("X-Accel-Buffering", "no")
	c.Status(200)
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()
	expires := time.NewTimer(30 * time.Minute)
	defer expires.Stop()
	flusher, ok := c.Writer.(http.Flusher)
	if !ok {
		return
	}
	_, _ = fmt.Fprint(c.Writer, ": connected\n\n")
	flusher.Flush()
	for {
		items, err := s.Core.Notifications(c.Request.Context(), user(c).ID, cursor)
		if err != nil {
			return
		}
		for _, n := range items {
			data, _ := json.Marshal(n)
			if _, err = fmt.Fprintf(c.Writer, "id: %d\nevent: notification\ndata: %s\n\n", n.ID, data); err != nil {
				return
			}
			cursor = n.ID
		}
		if len(items) > 0 {
			flusher.Flush()
		}
		select {
		case <-c.Request.Context().Done():
			return
		case <-expires.C:
			return
		case <-heartbeat.C:
			if _, err := s.Core.Authenticate(c.Request.Context(), c.GetString("session_token")); err != nil {
				return
			}
			if _, err := fmt.Fprint(c.Writer, ": heartbeat\n\n"); err != nil {
				return
			}
			flusher.Flush()
		case <-ticker.C:
		}
	}
}
