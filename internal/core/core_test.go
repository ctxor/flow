package core_test

import (
	"context"
	"errors"
	"protobuf/internal/core"
	"protobuf/internal/model"
	"protobuf/internal/testutil"
	"testing"
	"time"
)

func TestWorkspacePermissionsAndPrivateNotes(t *testing.T) {
	h := testutil.New(t)
	ctx := context.Background()
	a, _, w := h.User(t, "alice")
	b, _, _ := h.User(t, "bob")
	c, _, _ := h.User(t, "carol")
	if err := h.Core.AddMember(ctx, a.ID, w.ID, b.Email, "editor"); err != nil {
		t.Fatal(err)
	}
	for _, visibility := range []string{"private", "workspace"} {
		if _, err := h.Core.CreateNote(ctx, a.ID, core.CreateNoteInput{WorkspaceID: w.ID, Title: visibility, Content: "content", Visibility: visibility}); err != nil {
			t.Fatal(err)
		}
	}
	notes, err := h.Core.Notes(ctx, b.ID, w.ID, "")
	if err != nil || len(notes) != 1 || notes[0].Visibility != "workspace" {
		t.Fatalf("private note leaked: %v %v", notes, err)
	}
	if _, err = h.Core.Tasks(ctx, c.ID, w.ID); !errors.Is(err, core.ErrForbidden) {
		t.Fatalf("outsider access: %v", err)
	}
	searched, err := h.Core.Notes(ctx, b.ID, w.ID, "content")
	if err != nil || len(searched) != 1 {
		t.Fatalf("search bypassed visibility: %+v %v", searched, err)
	}
	if err = h.Core.AddMember(ctx, b.ID, w.ID, c.Email, "editor"); !errors.Is(err, core.ErrForbidden) {
		t.Fatalf("editor elevated another user: %v", err)
	}
	if err = h.Core.AddMember(ctx, a.ID, w.ID, b.Email, "viewer"); err != nil {
		t.Fatal(err)
	}
	var privateEvents int64
	h.DB.Model(&model.Notification{}).Where("user_id=? AND kind='workspace_changed'", b.ID).Count(&privateEvents)
	if privateEvents != 1 {
		t.Fatalf("private note change was broadcast: %d", privateEvents)
	}
	if _, err = h.Core.CreateTask(ctx, b.ID, core.CreateTaskInput{WorkspaceID: w.ID, Title: "forbidden"}); !errors.Is(err, core.ErrForbidden) {
		t.Fatalf("viewer created task: %v", err)
	}
}
func TestTaskVersionRemindersAndRollback(t *testing.T) {
	h := testutil.New(t)
	ctx := context.Background()
	u, _, w := h.User(t, "alice")
	due := time.Now().Add(time.Hour).UTC()
	offset := 10
	task, err := h.Core.CreateTask(ctx, u.ID, core.CreateTaskInput{WorkspaceID: w.ID, Title: "report", DueAt: &due, RemindBeforeMinutes: &offset})
	if err != nil {
		t.Fatal(err)
	}
	later := due.Add(time.Hour)
	updated, err := h.Core.UpdateTask(ctx, u.ID, task.ID, core.UpdateTaskInput{Version: 1, DueAt: &later})
	if err != nil || updated.Version != 2 {
		t.Fatalf("update: %+v %v", updated, err)
	}
	var reminder model.Reminder
	if err = h.DB.First(&reminder, "object_id=?", task.ID).Error; err != nil {
		t.Fatal(err)
	}
	if reminder.Version != 2 || reminder.FireAt.Sub(later.Add(-10*time.Minute)).Abs() > time.Millisecond {
		t.Fatalf("reminder not rescheduled: %+v", reminder)
	}
	done := "done"
	if _, err = h.Core.UpdateTask(ctx, u.ID, task.ID, core.UpdateTaskInput{Version: 1, Status: &done}); !errors.Is(err, core.ErrConflict) {
		t.Fatalf("stale update: %v", err)
	}
	if _, err = h.Core.UpdateTask(ctx, u.ID, task.ID, core.UpdateTaskInput{Version: 2, Status: &done}); err != nil {
		t.Fatal(err)
	}
	h.DB.First(&reminder, "id=?", reminder.ID)
	if reminder.Status != "cancelled" {
		t.Fatalf("completed task reminder active: %+v", reminder)
	}
	invalidOffset := -1
	if _, err = h.Core.CreateTask(ctx, u.ID, core.CreateTaskInput{WorkspaceID: w.ID, Title: "rollback", DueAt: &due, RemindBeforeMinutes: &invalidOffset}); !errors.Is(err, core.ErrInvalid) {
		t.Fatalf("invalid reminder accepted: %v", err)
	}
	var count int64
	h.DB.Model(&model.Task{}).Count(&count)
	if count != 1 {
		t.Fatalf("partial task committed: %d", count)
	}
}
func TestBindingIsSingleUseAndCannotHijack(t *testing.T) {
	h := testutil.New(t)
	ctx := context.Background()
	a, _, _ := h.User(t, "alice")
	b, _, _ := h.User(t, "bob")
	code, err := h.Core.NewBindingCode(ctx, a.ID, "feishu")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = h.Core.Bind(ctx, "feishu", "app", "open_alice", code); err != nil {
		t.Fatal(err)
	}
	if _, err = h.Core.Bind(ctx, "feishu", "app", "open_other", code); err == nil {
		t.Fatal("binding code reused")
	}
	other, _ := h.Core.NewBindingCode(ctx, b.ID, "feishu")
	if _, err = h.Core.Bind(ctx, "feishu", "app", "open_alice", other); err == nil {
		t.Fatal("platform identity hijacked")
	}
	var identity model.PlatformIdentity
	h.DB.First(&identity, "external_id=?", "open_alice")
	if identity.UserID != a.ID {
		t.Fatal("binding changed owner")
	}
}

func TestFollowupConsumedOnceAndIdempotencyMatchesInput(t *testing.T) {
	h := testutil.New(t)
	ctx := context.Background()
	u, _, w := h.User(t, "alice")
	original, err := h.Core.NewJob(ctx, u.ID, w.ID, "提醒我交周报", "first", "web")
	if err != nil {
		t.Fatal(err)
	}
	h.DB.Model(&original).Update("status", "needs_input")
	follow, err := h.Core.NewJob(ctx, u.ID, w.ID, "明天下午三点", "second", "web")
	if err != nil {
		t.Fatal(err)
	}
	if follow.Input != "提醒我交周报\n补充：明天下午三点" {
		t.Fatal(follow.Input)
	}
	next, err := h.Core.NewJob(ctx, u.ID, w.ID, "记一下：新的内容", "third", "web")
	if err != nil {
		t.Fatal(err)
	}
	if next.Input != "记一下：新的内容" {
		t.Fatalf("old question reused: %s", next.Input)
	}
	if _, err = h.Core.NewJob(ctx, u.ID, w.ID, "different input", "third", "web"); !errors.Is(err, core.ErrConflict) {
		t.Fatalf("idempotency key reused for a different action: %v", err)
	}
}
