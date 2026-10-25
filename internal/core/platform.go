package core

import (
	"context"
	"errors"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"protobuf/internal/model"
	"strings"
	"time"
)

func (s *Service) NewBindingCode(ctx context.Context, uid, provider string) (string, error) {
	if provider != "wechat" && provider != "feishu" {
		return "", invalid("无效平台")
	}
	code := Secret()[:16]
	err := s.DB.WithContext(ctx).Create(&model.BindingCode{Hash: Hash(code), Provider: provider, UserID: uid, ExpiresAt: time.Now().Add(10 * time.Minute)}).Error
	return code, err
}
func (s *Service) Bind(ctx context.Context, provider, appID, externalID, code string) (string, error) {
	var uid string
	err := s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var b model.BindingCode
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("hash=? AND provider=? AND consumed_at IS NULL AND expires_at>?", Hash(code), provider, time.Now()).First(&b).Error; err != nil {
			return invalid("绑定码无效、已使用或已过期")
		}
		uid = b.UserID
		p := model.PlatformIdentity{ID: ID(), Provider: provider, AppID: appID, ExternalID: externalID, UserID: uid, CreatedAt: time.Now().UTC()}
		var exists model.PlatformIdentity
		err := tx.Where("provider=? AND app_id=? AND (external_id=? OR user_id=?)", provider, appID, externalID, uid).First(&exists).Error
		if err == nil {
			if exists.UserID != uid || exists.ExternalID != externalID {
				return invalid("平台账号已绑定，请先在网页解除旧绑定")
			}
		} else if errors.Is(err, gorm.ErrRecordNotFound) {
			if err = tx.Create(&p).Error; err != nil {
				return err
			}
		} else {
			return err
		}
		return tx.Model(&b).Update("consumed_at", time.Now().UTC()).Error
	})
	return uid, err
}
func (s *Service) Receive(ctx context.Context, event model.InboxEvent) error {
	if event.ExternalID == "" || event.SenderID == "" || event.AppID == "" {
		return invalid("缺少平台事件身份")
	}
	if len([]rune(event.Text)) > 4000 {
		event.Text = "消息过长，请限制在4000字以内"
	}
	event.ID = ID()
	event.CreatedAt = time.Now().UTC()
	return s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		res := tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "provider"}, {Name: "app_id"}, {Name: "external_id"}}, DoNothing: true}).Create(&event)
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return nil
		}
		return Outbox(tx, "inbox", event.ID)
	})
}
func (s *Service) ProcessInbox(ctx context.Context, id string) (model.InboxEvent, error) {
	var event model.InboxEvent
	err := s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&event, "id=?", id).Error; err != nil {
			return err
		}
		if event.ProcessedAt != nil {
			return nil
		}
		c := &Service{DB: tx}
		fields := strings.Fields(event.Text)
		var uid string
		if len(fields) == 2 && (fields[0] == "绑定" || strings.EqualFold(fields[0], "bind")) {
			var err error
			uid, err = c.Bind(ctx, event.Provider, event.AppID, event.SenderID, fields[1])
			if err != nil {
				event.ReplyText = "绑定失败：请在网页生成新的绑定码，或先解除旧绑定。"
			} else {
				event.ReplyText = "绑定成功。可以试试：明天下午三点交周报，提前十分钟提醒我。"
			}
		} else {
			var p model.PlatformIdentity
			if err := tx.Where("provider=? AND app_id=? AND external_id=?", event.Provider, event.AppID, event.SenderID).First(&p).Error; err == nil {
				uid = p.UserID
			} else if !errors.Is(err, gorm.ErrRecordNotFound) {
				return err
			}
			if uid == "" {
				event.ReplyText = "请先登录网页，在平台接入中生成绑定码，然后向我发送：绑定 你的绑定码。"
			} else {
				var w model.Workspace
				if err := tx.Where("owner_id=?", uid).Order("created_at").First(&w).Error; err != nil {
					return err
				}
				if _, err := c.NewJob(ctx, uid, w.ID, event.Text, "inbox:"+event.ID, event.Provider); err != nil {
					return err
				}
			}
		}
		now := time.Now().UTC()
		event.ProcessedAt = &now
		return tx.Model(&event).Updates(map[string]any{"processed_at": now, "reply_text": event.ReplyText}).Error
	})
	return event, err
}
