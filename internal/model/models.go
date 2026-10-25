package model

import "time"

type User struct {
	ID           string    `json:"id" gorm:"primaryKey"`
	Email        string    `json:"email"`
	PasswordHash string    `json:"-"`
	Timezone     string    `json:"timezone"`
	CreatedAt    time.Time `json:"created_at"`
}
type Session struct {
	Hash      string `gorm:"primaryKey"`
	UserID    string
	ExpiresAt time.Time
}
type Workspace struct {
	ID        string    `json:"id" gorm:"primaryKey"`
	Name      string    `json:"name"`
	OwnerID   string    `json:"owner_id"`
	CreatedAt time.Time `json:"created_at"`
}
type Membership struct {
	WorkspaceID string    `json:"workspace_id" gorm:"primaryKey"`
	UserID      string    `json:"user_id" gorm:"primaryKey"`
	Role        string    `json:"role"`
	CreatedAt   time.Time `json:"created_at"`
}
type Task struct {
	ID          string     `json:"id" gorm:"primaryKey"`
	WorkspaceID string     `json:"workspace_id"`
	CreatedBy   string     `json:"created_by"`
	AssigneeID  *string    `json:"assignee_id"`
	Title       string     `json:"title"`
	Description string     `json:"description"`
	Status      string     `json:"status"`
	DueAt       *time.Time `json:"due_at"`
	Version     int        `json:"version"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
}
type Note struct {
	ID          string    `json:"id" gorm:"primaryKey"`
	WorkspaceID string    `json:"workspace_id"`
	CreatedBy   string    `json:"created_by"`
	Title       string    `json:"title"`
	Content     string    `json:"content"`
	Visibility  string    `json:"visibility"`
	CreatedAt   time.Time `json:"created_at"`
}
type CalendarEvent struct {
	ID          string    `json:"id" gorm:"primaryKey"`
	WorkspaceID string    `json:"workspace_id"`
	CreatedBy   string    `json:"created_by"`
	Title       string    `json:"title"`
	StartsAt    time.Time `json:"starts_at"`
	EndsAt      time.Time `json:"ends_at"`
	Attendees   []string  `json:"attendees" gorm:"serializer:json;type:jsonb"`
	CreatedAt   time.Time `json:"created_at"`
}
type Comment struct {
	ID        string    `json:"id" gorm:"primaryKey"`
	TaskID    string    `json:"task_id"`
	UserID    string    `json:"user_id"`
	Body      string    `json:"body"`
	CreatedAt time.Time `json:"created_at"`
}
type Reminder struct {
	ID            string    `json:"id" gorm:"primaryKey"`
	WorkspaceID   string    `json:"workspace_id"`
	UserID        string    `json:"user_id"`
	ObjectType    string    `json:"object_type"`
	ObjectID      string    `json:"object_id"`
	FireAt        time.Time `json:"fire_at"`
	OffsetMinutes int       `json:"offset_minutes"`
	Version       int       `json:"version"`
	Status        string    `json:"status"`
	CreatedAt     time.Time `json:"created_at"`
}
type PlatformIdentity struct {
	ID         string    `json:"id" gorm:"primaryKey"`
	Provider   string    `json:"provider"`
	AppID      string    `json:"app_id"`
	ExternalID string    `json:"external_id"`
	UserID     string    `json:"user_id"`
	CreatedAt  time.Time `json:"created_at"`
}
type BindingCode struct {
	Hash       string `gorm:"primaryKey"`
	Provider   string
	UserID     string
	ExpiresAt  time.Time
	ConsumedAt *time.Time
}
type InboxEvent struct {
	ID          string `gorm:"primaryKey"`
	Provider    string
	AppID       string
	ExternalID  string
	SenderID    string
	Text        string
	ProcessedAt *time.Time
	ReplyText   string
	RepliedAt   *time.Time
	CreatedAt   time.Time
}
type OutboxEvent struct {
	ID          string `gorm:"primaryKey"`
	Kind        string
	RefID       string
	PublishedAt *time.Time
	CreatedAt   time.Time
}
type Notification struct {
	ID          int64     `json:"id" gorm:"primaryKey;autoIncrement"`
	UserID      string    `json:"user_id"`
	WorkspaceID string    `json:"workspace_id"`
	Kind        string    `json:"kind"`
	Body        string    `json:"body"`
	CreatedAt   time.Time `json:"created_at"`
}
type Delivery struct {
	ID                string     `json:"id" gorm:"primaryKey"`
	NotificationID    int64      `json:"notification_id"`
	Provider          string     `json:"provider"`
	Target            string     `json:"-"`
	ReminderID        *string    `json:"reminder_id"`
	ReminderVersion   int        `json:"reminder_version"`
	Status            string     `json:"status"`
	Attempts          int        `json:"attempts"`
	NextAttemptAt     time.Time  `json:"next_attempt_at"`
	LeaseUntil        *time.Time `json:"-"`
	LeaseToken        string     `json:"-"`
	LastError         string     `json:"last_error"`
	ProviderMessageID string     `json:"provider_message_id"`
	CreatedAt         time.Time  `json:"created_at"`
	UpdatedAt         time.Time  `json:"updated_at"`
}
type AgentJob struct {
	ID                 string     `json:"id" gorm:"primaryKey"`
	UserID             string     `json:"user_id"`
	WorkspaceID        string     `json:"workspace_id"`
	RequestKey         string     `json:"-"`
	RequestHash        string     `json:"-"`
	FollowupConsumedAt *time.Time `json:"-"`
	Input              string     `json:"input"`
	Provider           string     `json:"provider"`
	Status             string     `json:"status"`
	Result             string     `json:"result"`
	LeaseUntil         *time.Time `json:"-"`
	LeaseToken         string     `json:"-"`
	Attempts           int        `json:"attempts"`
	CreatedAt          time.Time  `json:"created_at"`
	UpdatedAt          time.Time  `json:"updated_at"`
}
