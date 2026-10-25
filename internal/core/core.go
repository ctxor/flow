package core

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/mail"
	"strings"
	"time"

	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"protobuf/internal/model"
)

var (
	ErrForbidden    = errors.New("没有此空间的操作权限")
	ErrInvalid      = errors.New("参数不正确")
	ErrConflict     = errors.New("数据已发生变化，请刷新后重试")
	ErrUnauthorized = errors.New("请先登录")
)

type Service struct{ DB *gorm.DB }

func ID() string { return uuid.NewString() }
func Secret() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}
func Hash(s string) string             { h := sha256.Sum256([]byte(s)); return hex.EncodeToString(h[:]) }
func invalid(msg string) error         { return fmt.Errorf("%w：%s", ErrInvalid, msg) }
func validText(s string, max int) bool { return strings.TrimSpace(s) != "" && len([]rune(s)) <= max }

func (s *Service) Register(ctx context.Context, email, password, tz string) (model.User, string, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	addr, err := mail.ParseAddress(email)
	if err != nil || addr.Address != email || len(email) > 254 {
		return model.User{}, "", invalid("邮箱格式不正确")
	}
	if len(password) < 10 || len(password) > 72 {
		return model.User{}, "", invalid("密码需要 10–72 字节")
	}
	if tz == "" {
		tz = "Asia/Shanghai"
	}
	if _, err = time.LoadLocation(tz); err != nil {
		return model.User{}, "", invalid("无效时区")
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return model.User{}, "", err
	}
	user := model.User{ID: ID(), Email: email, PasswordHash: string(hash), Timezone: tz, CreatedAt: time.Now().UTC()}
	err = s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if e := tx.Create(&user).Error; e != nil {
			return e
		}
		w := model.Workspace{ID: ID(), Name: "我的空间", OwnerID: user.ID, CreatedAt: time.Now().UTC()}
		if e := tx.Create(&w).Error; e != nil {
			return e
		}
		return tx.Create(&model.Membership{WorkspaceID: w.ID, UserID: user.ID, Role: "owner", CreatedAt: time.Now().UTC()}).Error
	})
	if err != nil {
		return user, "", err
	}
	token, err := s.NewSession(ctx, user.ID)
	return user, token, err
}
func (s *Service) Login(ctx context.Context, email, password string) (model.User, string, error) {
	var u model.User
	err := s.DB.WithContext(ctx).Where("email = ?", strings.ToLower(strings.TrimSpace(email))).First(&u).Error
	if err != nil {
		bcrypt.CompareHashAndPassword([]byte("$2a$10$7EqJtq98hPqEX7fNZaFWoO5y8BvQ2E6nUV6rFdK4xxYtcA3MZnJjK"), []byte(password))
		return u, "", ErrUnauthorized
	}
	if bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(password)) != nil {
		return u, "", ErrUnauthorized
	}
	token, err := s.NewSession(ctx, u.ID)
	return u, token, err
}
func (s *Service) NewSession(ctx context.Context, userID string) (string, error) {
	token := Secret()
	return token, s.DB.WithContext(ctx).Create(&model.Session{Hash: Hash(token), UserID: userID, ExpiresAt: time.Now().Add(7 * 24 * time.Hour)}).Error
}
func (s *Service) Authenticate(ctx context.Context, token string) (model.User, error) {
	var u model.User
	if token == "" {
		return u, ErrUnauthorized
	}
	err := s.DB.WithContext(ctx).Table("users").Select("users.*").Joins("JOIN sessions ON sessions.user_id=users.id").Where("sessions.hash=? AND sessions.expires_at>?", Hash(token), time.Now()).Take(&u).Error
	if err != nil {
		return u, ErrUnauthorized
	}
	return u, nil
}
func (s *Service) Check(ctx context.Context, uid, wid string, write bool) error {
	if _, err := uuid.Parse(wid); err != nil {
		return invalid("workspace_id 必须是 UUID")
	}
	var m model.Membership
	if s.DB.WithContext(ctx).Where("workspace_id=? AND user_id=?", wid, uid).First(&m).Error != nil {
		return ErrForbidden
	}
	if write && m.Role == "viewer" {
		return ErrForbidden
	}
	return nil
}
func (s *Service) Workspaces(ctx context.Context, uid string) ([]model.Workspace, error) {
	out := []model.Workspace{}
	err := s.DB.WithContext(ctx).Model(&model.Workspace{}).Joins("JOIN memberships ON memberships.workspace_id=workspaces.id").Where("memberships.user_id=?", uid).Order("workspaces.created_at").Find(&out).Error
	return out, err
}
func (s *Service) CreateWorkspace(ctx context.Context, uid, name string) (model.Workspace, error) {
	w := model.Workspace{ID: ID(), Name: strings.TrimSpace(name), OwnerID: uid, CreatedAt: time.Now().UTC()}
	if !validText(name, 100) {
		return w, invalid("空间名称不能为空且不能超过100字")
	}
	err := s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&w).Error; err != nil {
			return err
		}
		return tx.Create(&model.Membership{WorkspaceID: w.ID, UserID: uid, Role: "owner", CreatedAt: time.Now().UTC()}).Error
	})
	return w, err
}
func (s *Service) AddMember(ctx context.Context, uid, wid, email, role string) error {
	var owner model.Membership
	if s.DB.WithContext(ctx).Where("workspace_id=? AND user_id=? AND role='owner'", wid, uid).First(&owner).Error != nil {
		return ErrForbidden
	}
	if role != "editor" && role != "viewer" {
		return invalid("role 必须是 editor 或 viewer")
	}
	var u model.User
	if s.DB.WithContext(ctx).Where("email=?", strings.ToLower(strings.TrimSpace(email))).First(&u).Error != nil {
		return invalid("该用户尚未注册")
	}
	if u.ID == uid {
		return invalid("不能修改自己的所有者角色")
	}
	return s.DB.WithContext(ctx).Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "workspace_id"}, {Name: "user_id"}}, DoUpdates: clause.AssignmentColumns([]string{"role"})}).Create(&model.Membership{WorkspaceID: wid, UserID: u.ID, Role: role, CreatedAt: time.Now().UTC()}).Error
}

type MemberView struct {
	UserID string `json:"user_id"`
	Email  string `json:"email"`
	Role   string `json:"role"`
}

func (s *Service) Members(ctx context.Context, uid, wid string) ([]MemberView, error) {
	out := []MemberView{}
	if err := s.Check(ctx, uid, wid, false); err != nil {
		return out, err
	}
	err := s.DB.WithContext(ctx).Table("memberships").Select("memberships.user_id, users.email, memberships.role").Joins("JOIN users ON users.id=memberships.user_id").Where("workspace_id=?", wid).Order("memberships.created_at").Scan(&out).Error
	return out, err
}

type CreateTaskInput struct {
	WorkspaceID         string     `json:"workspace_id"`
	Title               string     `json:"title"`
	Description         string     `json:"description"`
	DueAt               *time.Time `json:"due_at"`
	RemindBeforeMinutes *int       `json:"remind_before_minutes"`
	AssigneeID          *string    `json:"assignee_id"`
}

func (s *Service) CreateTask(ctx context.Context, uid string, in CreateTaskInput) (model.Task, error) {
	t := model.Task{ID: ID(), WorkspaceID: in.WorkspaceID, CreatedBy: uid, AssigneeID: in.AssigneeID, Title: strings.TrimSpace(in.Title), Description: in.Description, Status: "open", DueAt: in.DueAt, Version: 1}
	if err := s.Check(ctx, uid, in.WorkspaceID, true); err != nil {
		return t, err
	}
	if !validText(in.Title, 300) || len(in.Description) > 10000 {
		return t, invalid("任务标题或描述过长或为空")
	}
	if in.AssigneeID != nil {
		if err := s.Check(ctx, *in.AssigneeID, in.WorkspaceID, false); err != nil {
			return t, invalid("被指派人不在当前空间")
		}
	}
	err := s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&t).Error; err != nil {
			return err
		}
		c := &Service{DB: tx}
		if err := c.Change(ctx, in.WorkspaceID, ""); err != nil {
			return err
		}
		if in.RemindBeforeMinutes != nil {
			if err := c.addReminder(ctx, uid, in.WorkspaceID, "task", t.ID, in.DueAt, *in.RemindBeforeMinutes); err != nil {
				return err
			}
		}
		if in.AssigneeID != nil && *in.AssigneeID != uid {
			return c.Notify(ctx, *in.AssigneeID, in.WorkspaceID, "assignment", "你收到任务："+t.Title, "", nil, 0)
		}
		return nil
	})
	return t, err
}
func (s *Service) addReminder(ctx context.Context, uid, wid, kind, objectID string, at *time.Time, offset int) error {
	if at == nil || offset < 0 || offset > 525600 {
		return invalid("提醒需要截止时间，提前分钟数为0–525600")
	}
	fire := at.Add(-time.Duration(offset) * time.Minute)
	if !fire.After(time.Now().Add(-5 * time.Second)) {
		return invalid("提醒时间必须在未来")
	}
	return s.DB.WithContext(ctx).Create(&model.Reminder{ID: ID(), WorkspaceID: wid, UserID: uid, ObjectType: kind, ObjectID: objectID, FireAt: fire, OffsetMinutes: offset, Version: 1, Status: "pending"}).Error
}
func (s *Service) Tasks(ctx context.Context, uid, wid string) ([]model.Task, error) {
	out := []model.Task{}
	if err := s.Check(ctx, uid, wid, false); err != nil {
		return out, err
	}
	err := s.DB.WithContext(ctx).Where("workspace_id=?", wid).Order("created_at DESC").Limit(200).Find(&out).Error
	return out, err
}

type UpdateTaskInput struct {
	Version int        `json:"version"`
	Title   *string    `json:"title"`
	Status  *string    `json:"status"`
	DueAt   *time.Time `json:"due_at"`
}

func (s *Service) UpdateTask(ctx context.Context, uid, id string, in UpdateTaskInput) (model.Task, error) {
	var t model.Task
	err := s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&t, "id=?", id).Error; err != nil {
			return err
		}
		c := &Service{DB: tx}
		if err := c.Check(ctx, uid, t.WorkspaceID, true); err != nil {
			return err
		}
		if t.Version != in.Version {
			return ErrConflict
		}
		updates := map[string]any{"version": t.Version + 1, "updated_at": time.Now().UTC()}
		if in.Title != nil {
			if !validText(*in.Title, 300) {
				return invalid("无效标题")
			}
			updates["title"] = strings.TrimSpace(*in.Title)
		}
		if in.Status != nil {
			if *in.Status != "open" && *in.Status != "done" {
				return invalid("无效状态")
			}
			updates["status"] = *in.Status
		}
		if in.DueAt != nil {
			updates["due_at"] = in.DueAt
		}
		if err := tx.Model(&t).Updates(updates).Error; err != nil {
			return err
		}
		if err := c.Change(ctx, t.WorkspaceID, ""); err != nil {
			return err
		}
		if in.Status != nil && *in.Status == "done" {
			return tx.Model(&model.Reminder{}).Where("object_type='task' AND object_id=? AND status <> 'cancelled'", id).Updates(map[string]any{"status": "cancelled", "version": gorm.Expr("version+1")}).Error
		}
		if in.DueAt != nil {
			var reminders []model.Reminder
			if err := tx.Where("object_type='task' AND object_id=? AND status <> 'cancelled'", id).Find(&reminders).Error; err != nil {
				return err
			}
			for _, r := range reminders {
				fire := in.DueAt.Add(-time.Duration(r.OffsetMinutes) * time.Minute)
				if !fire.After(time.Now()) {
					return invalid("改期后的提醒时间必须在未来")
				}
				if err := tx.Model(&r).Updates(map[string]any{"fire_at": fire, "status": "pending", "version": r.Version + 1}).Error; err != nil {
					return err
				}
			}
		}
		return nil
	})
	if err == nil {
		err = s.DB.WithContext(ctx).First(&t, "id=?", id).Error
	}
	return t, err
}

type CreateNoteInput struct {
	WorkspaceID string `json:"workspace_id"`
	Title       string `json:"title"`
	Content     string `json:"content"`
	Visibility  string `json:"visibility"`
}

func (s *Service) CreateNote(ctx context.Context, uid string, in CreateNoteInput) (model.Note, error) {
	n := model.Note{ID: ID(), WorkspaceID: in.WorkspaceID, CreatedBy: uid, Title: strings.TrimSpace(in.Title), Content: in.Content, Visibility: in.Visibility}
	if err := s.Check(ctx, uid, in.WorkspaceID, true); err != nil {
		return n, err
	}
	if n.Visibility == "" {
		n.Visibility = "private"
	}
	if n.Visibility != "private" && n.Visibility != "workspace" {
		return n, invalid("无效可见范围")
	}
	if !validText(in.Title, 300) || !validText(in.Content, 20000) {
		return n, invalid("备忘标题或内容不正确")
	}
	err := s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&n).Error; err != nil {
			return err
		}
		target := ""
		if n.Visibility == "private" {
			target = uid
		}
		return (&Service{DB: tx}).Change(ctx, n.WorkspaceID, target)
	})
	return n, err
}
func (s *Service) Notes(ctx context.Context, uid, wid, q string) ([]model.Note, error) {
	out := []model.Note{}
	if err := s.Check(ctx, uid, wid, false); err != nil {
		return out, err
	}
	db := s.DB.WithContext(ctx).Where("workspace_id=? AND (visibility='workspace' OR created_by=?)", wid, uid)
	if q != "" {
		if len(q) > 200 {
			return out, invalid("搜索词过长")
		}
		db = db.Where("title ILIKE ? OR content ILIKE ?", "%"+q+"%", "%"+q+"%")
	}
	err := db.Order("created_at DESC").Limit(200).Find(&out).Error
	return out, err
}

type CreateEventInput struct {
	WorkspaceID         string    `json:"workspace_id"`
	Title               string    `json:"title"`
	StartsAt            time.Time `json:"starts_at"`
	EndsAt              time.Time `json:"ends_at"`
	Attendees           []string  `json:"attendees"`
	RemindBeforeMinutes *int      `json:"remind_before_minutes"`
}

func (s *Service) CreateEvent(ctx context.Context, uid string, in CreateEventInput) (model.CalendarEvent, error) {
	if in.Attendees == nil {
		in.Attendees = []string{}
	}
	ev := model.CalendarEvent{ID: ID(), WorkspaceID: in.WorkspaceID, CreatedBy: uid, Title: strings.TrimSpace(in.Title), StartsAt: in.StartsAt, EndsAt: in.EndsAt, Attendees: in.Attendees}
	if err := s.Check(ctx, uid, in.WorkspaceID, true); err != nil {
		return ev, err
	}
	if !validText(in.Title, 300) || in.StartsAt.IsZero() || !in.EndsAt.After(in.StartsAt) || len(in.Attendees) > 50 {
		return ev, invalid("日程标题、起止时间或参与人数不正确")
	}
	for _, id := range in.Attendees {
		if err := s.Check(ctx, id, in.WorkspaceID, false); err != nil {
			return ev, invalid("参与人不在当前空间")
		}
	}
	err := s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&ev).Error; err != nil {
			return err
		}
		if err := (&Service{DB: tx}).Change(ctx, in.WorkspaceID, ""); err != nil {
			return err
		}
		if in.RemindBeforeMinutes != nil {
			return (&Service{DB: tx}).addReminder(ctx, uid, in.WorkspaceID, "event", ev.ID, &in.StartsAt, *in.RemindBeforeMinutes)
		}
		return nil
	})
	return ev, err
}
func (s *Service) Events(ctx context.Context, uid, wid string) ([]model.CalendarEvent, error) {
	out := []model.CalendarEvent{}
	if err := s.Check(ctx, uid, wid, false); err != nil {
		return out, err
	}
	err := s.DB.WithContext(ctx).Where("workspace_id=?", wid).Order("starts_at").Limit(200).Find(&out).Error
	return out, err
}
func (s *Service) AddComment(ctx context.Context, uid, taskID, body string) (model.Comment, error) {
	c := model.Comment{ID: ID(), TaskID: taskID, UserID: uid, Body: body}
	if !validText(body, 2000) {
		return c, invalid("评论不能为空或超过2000字")
	}
	var task model.Task
	if err := s.DB.WithContext(ctx).First(&task, "id=?", taskID).Error; err != nil {
		return c, err
	}
	if err := s.Check(ctx, uid, task.WorkspaceID, true); err != nil {
		return c, err
	}
	err := s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&c).Error; err != nil {
			return err
		}
		return (&Service{DB: tx}).Change(ctx, task.WorkspaceID, "")
	})
	return c, err
}
func (s *Service) Comments(ctx context.Context, uid, taskID string) ([]model.Comment, error) {
	out := []model.Comment{}
	var task model.Task
	if err := s.DB.WithContext(ctx).First(&task, "id=?", taskID).Error; err != nil {
		return out, err
	}
	if err := s.Check(ctx, uid, task.WorkspaceID, false); err != nil {
		return out, err
	}
	err := s.DB.WithContext(ctx).Where("task_id=?", taskID).Order("created_at").Limit(200).Find(&out).Error
	return out, err
}

func Outbox(tx *gorm.DB, kind, ref string) error {
	return tx.Create(&model.OutboxEvent{ID: ID(), Kind: kind, RefID: ref, CreatedAt: time.Now().UTC()}).Error
}

// Change persists a content-free invalidation event; private notes only notify their owner.
func (s *Service) Change(ctx context.Context, wid, onlyUser string) error {
	var members []model.Membership
	q := s.DB.WithContext(ctx).Where("workspace_id=?", wid)
	if onlyUser != "" {
		q = q.Where("user_id=?", onlyUser)
	}
	if err := q.Find(&members).Error; err != nil {
		return err
	}
	notifications := make([]model.Notification, 0, len(members))
	for _, member := range members {
		notifications = append(notifications, model.Notification{UserID: member.UserID, WorkspaceID: wid, Kind: "workspace_changed", Body: "空间内容已更新", CreatedAt: time.Now().UTC()})
	}
	if len(notifications) == 0 {
		return nil
	}
	return s.DB.WithContext(ctx).Create(&notifications).Error
}
func (s *Service) Notify(ctx context.Context, uid, wid, kind, body, provider string, reminderID *string, version int) error {
	return s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		n := model.Notification{UserID: uid, WorkspaceID: wid, Kind: kind, Body: body, CreatedAt: time.Now().UTC()}
		if err := tx.Create(&n).Error; err != nil {
			return err
		}
		if provider == "web" {
			return nil
		}
		var identities []model.PlatformIdentity
		q := tx.Where("user_id=?", uid)
		if provider != "" {
			q = q.Where("provider=?", provider)
		}
		if err := q.Find(&identities).Error; err != nil {
			return err
		}
		for _, p := range identities {
			d := model.Delivery{ID: ID(), NotificationID: n.ID, Provider: p.Provider, Target: p.ExternalID, ReminderID: reminderID, ReminderVersion: version, Status: "queued", NextAttemptAt: time.Now().UTC()}
			if err := tx.Create(&d).Error; err != nil {
				return err
			}
			if err := Outbox(tx, "delivery", d.ID); err != nil {
				return err
			}
		}
		return nil
	})
}
func (s *Service) Notifications(ctx context.Context, uid string, after int64) ([]model.Notification, error) {
	out := []model.Notification{}
	err := s.DB.WithContext(ctx).Where("user_id=? AND id>?", uid, after).Order("id ASC").Limit(100).Find(&out).Error
	return out, err
}
func (s *Service) NewJob(ctx context.Context, uid, wid, text, key, provider string) (model.AgentJob, error) {
	j := model.AgentJob{ID: ID(), UserID: uid, WorkspaceID: wid, Input: strings.TrimSpace(text), RequestKey: key, Provider: provider, Status: "queued"}
	j.RequestHash = Hash(wid + "\x00" + provider + "\x00" + j.Input)
	if err := s.Check(ctx, uid, wid, false); err != nil {
		return j, err
	}
	if !validText(text, 4000) || len(key) > 150 {
		return j, invalid("消息或幂等键不正确")
	}
	if key == "" {
		j.RequestKey = ID()
	}
	err := s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var prior model.AgentJob
		if e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("user_id=? AND workspace_id=? AND provider=? AND status='needs_input' AND followup_consumed_at IS NULL AND created_at>?", uid, wid, provider, time.Now().Add(-15*time.Minute)).Order("created_at DESC").First(&prior).Error; e == nil {
			j.Input = prior.Input + "\n补充：" + j.Input
			if len([]rune(j.Input)) > 8000 {
				return invalid("会话过长，请重新描述完整需求")
			}
		} else if !errors.Is(e, gorm.ErrRecordNotFound) {
			return e
		}
		res := tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "user_id"}, {Name: "request_key"}}, DoNothing: true}).Create(&j)
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			var old model.AgentJob
			if err := tx.Where("user_id=? AND request_key=?", uid, j.RequestKey).First(&old).Error; err != nil {
				return err
			}
			if old.WorkspaceID != wid || old.RequestHash != j.RequestHash {
				return ErrConflict
			}
			j = old
			return nil
		}
		if prior.ID != "" {
			if err := tx.Model(&prior).Update("followup_consumed_at", time.Now().UTC()).Error; err != nil {
				return err
			}
		}
		return Outbox(tx, "agent", j.ID)
	})
	return j, err
}
