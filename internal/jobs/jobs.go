package jobs

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"strings"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	agentv1 "protobuf/gen/agent/v1"
	"protobuf/internal/config"
	"protobuf/internal/core"
	"protobuf/internal/model"
	"protobuf/internal/platform"
)

const stream = "assistant:jobs"
const group = "assistant-workers"

type Sender interface {
	Send(context.Context, string, string, string, string, string) (string, error)
}
type Runner struct {
	DB     *gorm.DB
	Redis  *redis.Client
	Core   *core.Service
	Agent  agentv1.AgentServiceClient
	Sender Sender
	Token  string
}

func ConnectAgent(c config.Config) (*grpc.ClientConn, error) {
	return grpc.NewClient(c.AgentAddr, grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithDefaultCallOptions(grpc.MaxCallRecvMsgSize(1<<20)))
}

func (r *Runner) Run(ctx context.Context) error {
	if err := r.Redis.XGroupCreateMkStream(ctx, stream, group, "0").Err(); err != nil && !strings.Contains(err.Error(), "BUSYGROUP") {
		return err
	}
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); r.consume(ctx, core.ID()) }()
	}
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	recovery := time.NewTicker(15 * time.Second)
	defer recovery.Stop()
	for {
		select {
		case <-ctx.Done():
			wg.Wait()
			return nil
		case <-ticker.C:
			if err := r.Schedule(ctx); err != nil {
				slog.Error("reminder scan failed", "error", err)
			}
			if err := r.Publish(ctx); err != nil {
				slog.Error("outbox publish failed", "error", err)
			}
		case <-recovery.C:
			if err := r.Recover(ctx); err != nil {
				slog.Error("recovery failed", "error", err)
			}
		}
	}
}
func (r *Runner) Schedule(ctx context.Context) error {
	return r.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var reminders []model.Reminder
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"}).Where("status='pending' AND fire_at<=?", time.Now()).Order("fire_at").Limit(100).Find(&reminders).Error; err != nil {
			return err
		}
		s := &core.Service{DB: tx}
		for _, rem := range reminders {
			body := ""
			if rem.ObjectType == "task" {
				var task model.Task
				if err := tx.First(&task, "id=?", rem.ObjectID).Error; err != nil {
					return err
				}
				if task.Status == "open" {
					body = "任务提醒：" + task.Title
				}
			} else {
				var ev model.CalendarEvent
				if err := tx.First(&ev, "id=?", rem.ObjectID).Error; err != nil {
					return err
				}
				body = "日程提醒：" + ev.Title
			}
			status := "queued"
			if body == "" || s.Check(ctx, rem.UserID, rem.WorkspaceID, false) != nil {
				status = "cancelled"
			} else {
				if err := s.Notify(ctx, rem.UserID, rem.WorkspaceID, "reminder", body, "", &rem.ID, rem.Version); err != nil {
					return err
				}
			}
			if err := tx.Model(&rem).Update("status", status).Error; err != nil {
				return err
			}
		}
		return nil
	})
}
func (r *Runner) Publish(ctx context.Context) error {
	var events []model.OutboxEvent
	if err := r.DB.WithContext(ctx).Where("published_at IS NULL").Order("created_at").Limit(100).Find(&events).Error; err != nil {
		return err
	}
	for _, e := range events {
		if err := r.Redis.XAdd(ctx, &redis.XAddArgs{Stream: stream, Values: map[string]any{"kind": e.Kind, "ref": e.RefID}}).Err(); err != nil {
			return err
		}
		if err := r.DB.WithContext(ctx).Model(&e).Where("published_at IS NULL").Update("published_at", time.Now().UTC()).Error; err != nil {
			return err
		}
	}
	return nil
}
func (r *Runner) consume(ctx context.Context, consumer string) {
	nextClaim := time.Now()
	for ctx.Err() == nil {
		if time.Now().After(nextClaim) {
			messages, _, err := r.Redis.XAutoClaim(ctx, &redis.XAutoClaimArgs{Stream: stream, Group: group, Consumer: consumer, MinIdle: 2 * time.Minute, Start: "0-0", Count: 10}).Result()
			if err == nil {
				for _, m := range messages {
					r.process(ctx, m)
				}
			}
			nextClaim = time.Now().Add(15 * time.Second)
		}
		rows, err := r.Redis.XReadGroup(ctx, &redis.XReadGroupArgs{Group: group, Consumer: consumer, Streams: []string{stream, ">"}, Count: 1, Block: time.Second}).Result()
		if err != nil {
			if strings.Contains(err.Error(), "NOGROUP") {
				_ = r.Redis.XGroupCreateMkStream(ctx, stream, group, "0").Err()
				continue
			}
			if err != redis.Nil && ctx.Err() == nil {
				slog.Warn("queue read failed", "error", err)
				select {
				case <-ctx.Done():
				case <-time.After(time.Second):
				}
			}
			continue
		}
		for _, row := range rows {
			for _, msg := range row.Messages {
				r.process(ctx, msg)
			}
		}
	}
}
func (r *Runner) process(ctx context.Context, m redis.XMessage) {
	var err error
	kind, _ := m.Values["kind"].(string)
	ref, _ := m.Values["ref"].(string)
	switch kind {
	case "agent":
		err = r.AgentJob(ctx, ref)
	case "delivery":
		err = r.Deliver(ctx, ref)
	case "inbox":
		err = r.Inbox(ctx, ref)
	default:
		slog.Error("unknown queue item", "kind", kind)
	}
	if err != nil {
		slog.Error("queue item failed", "kind", kind, "ref", ref, "error", err)
		return
	}
	if err = r.Redis.XAck(ctx, stream, group, m.ID).Err(); err != nil {
		slog.Warn("queue ack failed", "error", err)
	}
}
func (r *Runner) Recover(ctx context.Context) error {
	// PostgreSQL retains every unfinished operation, so recovery also works after Redis data loss.
	var agent []model.AgentJob
	if err := r.DB.WithContext(ctx).Where("(status='queued' AND created_at<?) OR (status='running' AND lease_until<?)", time.Now().Add(-15*time.Second), time.Now()).Limit(100).Find(&agent).Error; err != nil {
		return err
	}
	for _, j := range agent {
		if err := core.Outbox(r.DB.WithContext(ctx), "agent", j.ID); err != nil {
			return err
		}
	}
	var ds []model.Delivery
	if err := r.DB.WithContext(ctx).Where("((status IN ('queued','retry') AND next_attempt_at<=?) OR (status='sending' AND lease_until<?)) AND updated_at<?", time.Now(), time.Now(), time.Now().Add(-10*time.Second)).Limit(100).Find(&ds).Error; err != nil {
		return err
	}
	for _, d := range ds {
		if err := core.Outbox(r.DB.WithContext(ctx), "delivery", d.ID); err != nil {
			return err
		}
	}
	var in []model.InboxEvent
	if err := r.DB.WithContext(ctx).Where("processed_at IS NULL OR (reply_text<>'' AND replied_at IS NULL)").Limit(100).Find(&in).Error; err != nil {
		return err
	}
	for _, e := range in {
		if err := core.Outbox(r.DB.WithContext(ctx), "inbox", e.ID); err != nil {
			return err
		}
	}
	return nil
}
func (r *Runner) Inbox(ctx context.Context, id string) error {
	e, err := r.Core.ProcessInbox(ctx, id)
	if err != nil {
		return err
	}
	if e.ReplyText == "" || e.RepliedAt != nil {
		return nil
	}
	sendCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	_, err = r.Sender.Send(sendCtx, e.Provider, e.SenderID, e.ReplyText, "reply", e.ID)
	if err != nil {
		var failure *platform.SendError
		if errors.As(err, &failure) && (!failure.Retryable || failure.Unknown) {
			return r.DB.WithContext(ctx).Model(&e).Update("replied_at", time.Now().UTC()).Error
		}
		return err
	}
	return r.DB.WithContext(ctx).Model(&e).Update("replied_at", time.Now().UTC()).Error
}
func (r *Runner) AgentJob(ctx context.Context, id string) error {
	token := core.ID()
	res := r.DB.WithContext(ctx).Model(&model.AgentJob{}).Where("id=? AND (status='queued' OR (status='running' AND lease_until<?))", id, time.Now()).Updates(map[string]any{"status": "running", "lease_token": token, "lease_until": time.Now().Add(90 * time.Second), "attempts": gorm.Expr("attempts+1"), "updated_at": time.Now().UTC()})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return nil
	}
	var j model.AgentJob
	if err := r.DB.WithContext(ctx).First(&j, "id=?", id).Error; err != nil {
		return err
	}
	var u model.User
	if err := r.DB.WithContext(ctx).First(&u, "id=?", j.UserID).Error; err != nil {
		return err
	}
	var plan *agentv1.PlanResponse
	var planErr error
	if j.Attempts > 3 {
		planErr = errors.New("AI job exceeded recovery attempts")
	} else {
		callCtx, cancel := context.WithTimeout(ctx, 40*time.Second)
		callCtx = metadata.AppendToOutgoingContext(callCtx, "authorization", "Bearer "+r.Token)
		plan, planErr = r.Agent.Plan(callCtx, &agentv1.PlanRequest{JobId: j.ID, Text: j.Input, Timezone: u.Timezone, Now: j.CreatedAt.Format(time.RFC3339)})
		cancel()
	}
	return r.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var current model.AgentJob
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&current, "id=?", j.ID).Error; err != nil {
			return err
		}
		if current.Status != "running" || current.LeaseToken != token {
			return nil
		}
		status, body := "succeeded", ""
		if planErr != nil {
			status = "failed"
			body = "AI 服务暂时不可用，请稍后重试。你仍可以手动创建任务、备忘和日程。"
			slog.Warn("agent call failed", "job_id", j.ID, "error", planErr)
		} else {
			err := tx.Transaction(func(actionTx *gorm.DB) error {
				var err error
				body, status, err = r.apply(ctx, &core.Service{DB: actionTx}, j, u, plan)
				return err
			})
			if err != nil {
				status = "failed"
				body = "未执行：" + safeError(err)
			}
		}
		if err := tx.Model(&current).Updates(map[string]any{"status": status, "result": body, "lease_until": nil, "lease_token": "", "updated_at": time.Now().UTC()}).Error; err != nil {
			return err
		}
		return (&core.Service{DB: tx}).Notify(ctx, j.UserID, j.WorkspaceID, "agent_result", body, j.Provider, nil, 0)
	})
}
func safeError(err error) string {
	if errors.Is(err, core.ErrInvalid) || errors.Is(err, core.ErrForbidden) || errors.Is(err, core.ErrConflict) {
		return err.Error()
	}
	return "操作未保存，请稍后重试"
}
func parseTime(value string) (*time.Time, error) {
	if value == "" {
		return nil, nil
	}
	t, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return nil, fmt.Errorf("%w：AI 返回的时间格式不正确", core.ErrInvalid)
	}
	return &t, nil
}
func (r *Runner) apply(ctx context.Context, s *core.Service, j model.AgentJob, u model.User, plan *agentv1.PlanResponse) (string, string, error) {
	loc, _ := time.LoadLocation(u.Timezone)
	if loc == nil {
		loc = time.UTC
	}
	switch p := plan.GetResult().(type) {
	case *agentv1.PlanResponse_CreateTask:
		due, err := parseTime(p.CreateTask.DueAt)
		if err != nil {
			return "", "", err
		}
		in := core.CreateTaskInput{WorkspaceID: j.WorkspaceID, Title: p.CreateTask.Title, Description: p.CreateTask.Description, DueAt: due}
		if p.CreateTask.HasReminder {
			n := int(p.CreateTask.RemindBeforeMinutes)
			in.RemindBeforeMinutes = &n
		}
		task, err := s.CreateTask(ctx, j.UserID, in)
		body := "已创建任务：" + task.Title
		if due != nil {
			body += "\n截止：" + due.In(loc).Format("2006-01-02 15:04")
		}
		if p.CreateTask.HasReminder {
			body += fmt.Sprintf("\n提前 %d 分钟提醒", p.CreateTask.RemindBeforeMinutes)
		}
		return body, "succeeded", err
	case *agentv1.PlanResponse_SaveNote:
		note, err := s.CreateNote(ctx, j.UserID, core.CreateNoteInput{WorkspaceID: j.WorkspaceID, Title: p.SaveNote.Title, Content: p.SaveNote.Content, Visibility: "private"})
		return "已保存私有备忘：" + note.Title, "succeeded", err
	case *agentv1.PlanResponse_CreateEvent:
		start, err := parseTime(p.CreateEvent.StartsAt)
		if err != nil {
			return "", "", err
		}
		end, err := parseTime(p.CreateEvent.EndsAt)
		if err != nil {
			return "", "", err
		}
		if start == nil || end == nil {
			return "", "", fmt.Errorf("%w：日程缺少起止时间", core.ErrInvalid)
		}
		in := core.CreateEventInput{WorkspaceID: j.WorkspaceID, Title: p.CreateEvent.Title, StartsAt: *start, EndsAt: *end}
		if p.CreateEvent.HasReminder {
			n := int(p.CreateEvent.RemindBeforeMinutes)
			in.RemindBeforeMinutes = &n
		}
		ev, err := s.CreateEvent(ctx, j.UserID, in)
		return "已创建日程：" + ev.Title + "\n" + ev.StartsAt.In(loc).Format("2006-01-02 15:04") + " — " + ev.EndsAt.In(loc).Format("15:04"), "succeeded", err
	case *agentv1.PlanResponse_QueryTasks:
		tasks, err := s.Tasks(ctx, j.UserID, j.WorkspaceID)
		if err != nil {
			return "", "", err
		}
		var lines []string
		today := time.Now().In(loc).Format("2006-01-02")
		for _, t := range tasks {
			if t.Status != "open" {
				continue
			}
			if p.QueryTasks.TodayOnly && (t.DueAt == nil || t.DueAt.In(loc).Format("2006-01-02") != today) {
				continue
			}
			line := "• " + t.Title
			if t.DueAt != nil {
				line += "（" + t.DueAt.In(loc).Format("01-02 15:04") + "）"
			}
			lines = append(lines, line)
			if len(lines) == 20 {
				break
			}
		}
		if len(lines) == 0 {
			return "当前没有符合条件的未完成任务。", "succeeded", nil
		}
		return "未完成任务：\n" + strings.Join(lines, "\n"), "succeeded", nil
	case *agentv1.PlanResponse_SummarizeDay:
		tasks, err := s.Tasks(ctx, j.UserID, j.WorkspaceID)
		if err != nil {
			return "", "", err
		}
		open, done, overdue := 0, 0, 0
		for _, t := range tasks {
			if t.Status == "done" {
				done++
			} else {
				open++
				if t.DueAt != nil && t.DueAt.Before(time.Now()) {
					overdue++
				}
			}
		}
		return fmt.Sprintf("当前空间任务概览（最近200条）：\n待完成 %d 项，已完成 %d 项，逾期 %d 项。", open, done, overdue), "succeeded", nil
	case *agentv1.PlanResponse_Clarification:
		if strings.TrimSpace(p.Clarification.Question) == "" {
			return "", "", core.ErrInvalid
		}
		return p.Clarification.Question, "needs_input", nil
	default:
		return "", "", core.ErrInvalid
	}
}

func (r *Runner) Deliver(ctx context.Context, id string) error {
	token := core.ID()
	var d model.Delivery
	claimed := false
	err := r.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&d, "id=?", id).Error; err != nil {
			return err
		}
		now := time.Now().UTC()
		expired := d.Status == "sending" && d.LeaseUntil != nil && d.LeaseUntil.Before(now)
		if expired && d.Provider == "wechat" {
			return tx.Model(&d).Updates(map[string]any{"status": "unknown", "last_error": "worker stopped during send; verify platform result before retry", "lease_until": nil, "lease_token": "", "updated_at": now}).Error
		}
		if !expired && ((d.Status != "queued" && d.Status != "retry") || d.NextAttemptAt.After(now)) {
			return nil
		}
		claimed = true
		d.Attempts++
		return tx.Model(&d).Updates(map[string]any{"status": "sending", "lease_token": token, "lease_until": now.Add(time.Minute), "attempts": d.Attempts, "updated_at": now}).Error
	})
	if err != nil {
		return err
	}
	if !claimed {
		return nil
	}
	var n model.Notification
	if err := r.DB.WithContext(ctx).First(&n, "id=?", d.NotificationID).Error; err != nil {
		return err
	}
	cancelled := r.Core.Check(ctx, n.UserID, n.WorkspaceID, false) != nil
	var count int64
	if err := r.DB.WithContext(ctx).Model(&model.PlatformIdentity{}).Where("user_id=? AND provider=? AND external_id=?", n.UserID, d.Provider, d.Target).Count(&count).Error; err != nil {
		return err
	}
	cancelled = cancelled || count == 0
	if d.ReminderID != nil {
		var rem model.Reminder
		if err := r.DB.WithContext(ctx).First(&rem, "id=?", *d.ReminderID).Error; err != nil {
			return err
		}
		cancelled = cancelled || rem.Status != "queued" || rem.Version != d.ReminderVersion
	}
	updates := map[string]any{"lease_until": nil, "lease_token": "", "updated_at": time.Now().UTC()}
	if cancelled {
		updates["status"] = "cancelled"
	} else {
		callCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
		messageID, err := r.Sender.Send(callCtx, d.Provider, d.Target, n.Body, n.Kind, d.ID)
		cancel()
		if err == nil {
			updates["status"] = "accepted"
			updates["provider_message_id"] = messageID
			updates["last_error"] = ""
		} else {
			retry, unknown := true, false
			var failure *platform.SendError
			if errors.As(err, &failure) {
				retry, unknown = failure.Retryable, failure.Unknown
			}
			updates["last_error"] = err.Error()
			switch {
			case unknown && d.Provider == "wechat":
				updates["status"] = "unknown"
			case retry && d.Attempts < 5:
				updates["status"] = "retry"
				delay := time.Duration((1<<min(d.Attempts, 6))*5+rand.IntN(5)) * time.Second
				updates["next_attempt_at"] = time.Now().Add(delay)
			default:
				updates["status"] = "failed"
			}
		}
	}
	return r.DB.WithContext(ctx).Model(&d).Where("lease_token=?", token).Updates(updates).Error
}
