package jobs

import (
	"context"
	"google.golang.org/grpc"
	agentv1 "protobuf/gen/agent/v1"
	"protobuf/internal/core"
	"protobuf/internal/model"
	"protobuf/internal/platform"
	"protobuf/internal/testutil"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type fakeSender struct {
	calls atomic.Int32
	err   error
}

func (s *fakeSender) Send(context.Context, string, string, string, string, string) (string, error) {
	s.calls.Add(1)
	return "provider-id", s.err
}

type fakeAgent struct{ calls atomic.Int32 }

func (a *fakeAgent) Plan(context.Context, *agentv1.PlanRequest, ...grpc.CallOption) (*agentv1.PlanResponse, error) {
	a.calls.Add(1)
	time.Sleep(20 * time.Millisecond)
	return &agentv1.PlanResponse{Result: &agentv1.PlanResponse_CreateTask{CreateTask: &agentv1.CreateTask{Title: "from AI"}}}, nil
}
func runner(t *testing.T) (*Runner, *testutil.Harness, *fakeSender, *fakeAgent) {
	h := testutil.New(t)
	sender := &fakeSender{}
	ai := &fakeAgent{}
	return &Runner{DB: h.DB, Redis: h.Redis, Core: h.Core, Sender: sender, Agent: ai, Token: "test"}, h, sender, ai
}
func link(t *testing.T, h *testutil.Harness, uid, provider string) {
	t.Helper()
	if err := h.DB.Create(&model.PlatformIdentity{ID: core.ID(), Provider: provider, AppID: "app", ExternalID: "external_" + uid, UserID: uid, CreatedAt: time.Now().UTC()}).Error; err != nil {
		t.Fatal(err)
	}
}
func TestReminderSchedulingAndDeliveryAreIdempotent(t *testing.T) {
	r, h, sender, _ := runner(t)
	ctx := context.Background()
	u, _, w := h.User(t, "alice")
	link(t, h, u.ID, "feishu")
	due := time.Now().Add(time.Hour)
	zero := 0
	task, err := h.Core.CreateTask(ctx, u.ID, core.CreateTaskInput{WorkspaceID: w.ID, Title: "report", DueAt: &due, RemindBeforeMinutes: &zero})
	if err != nil {
		t.Fatal(err)
	}
	h.DB.Model(&model.Reminder{}).Where("object_id=?", task.ID).Update("fire_at", time.Now().Add(-time.Second))
	for i := 0; i < 2; i++ {
		if err = r.Schedule(ctx); err != nil {
			t.Fatal(err)
		}
	}
	var ns, ds int64
	h.DB.Model(&model.Notification{}).Where("kind='reminder'").Count(&ns)
	h.DB.Model(&model.Delivery{}).Count(&ds)
	if ns != 1 || ds != 1 {
		t.Fatalf("duplicate reminder: %d %d", ns, ds)
	}
	var d model.Delivery
	h.DB.First(&d)
	for i := 0; i < 2; i++ {
		if err = r.Deliver(ctx, d.ID); err != nil {
			t.Fatal(err)
		}
	}
	if sender.calls.Load() != 1 {
		t.Fatalf("sent %d times", sender.calls.Load())
	}
	h.DB.First(&d, "id=?", d.ID)
	if d.Status != "accepted" {
		t.Fatal(d.Status)
	}
	if err = r.Publish(ctx); err != nil {
		t.Fatal(err)
	}
	if n := h.Redis.XLen(ctx, stream).Val(); n == 0 {
		t.Fatal("outbox not published")
	}
}
func TestRescheduleCancelsStaleDelivery(t *testing.T) {
	r, h, sender, _ := runner(t)
	ctx := context.Background()
	u, _, w := h.User(t, "alice")
	link(t, h, u.ID, "feishu")
	due := time.Now().Add(time.Hour)
	zero := 0
	task, err := h.Core.CreateTask(ctx, u.ID, core.CreateTaskInput{WorkspaceID: w.ID, Title: "old", DueAt: &due, RemindBeforeMinutes: &zero})
	if err != nil {
		t.Fatal(err)
	}
	h.DB.Model(&model.Reminder{}).Where("object_id=?", task.ID).Update("fire_at", time.Now().Add(-time.Second))
	if err = r.Schedule(ctx); err != nil {
		t.Fatal(err)
	}
	var d model.Delivery
	h.DB.First(&d)
	later := due.Add(time.Hour)
	if _, err = h.Core.UpdateTask(ctx, u.ID, task.ID, core.UpdateTaskInput{Version: 1, DueAt: &later}); err != nil {
		t.Fatal(err)
	}
	if err = r.Deliver(ctx, d.ID); err != nil {
		t.Fatal(err)
	}
	h.DB.First(&d, "id=?", d.ID)
	if d.Status != "cancelled" || sender.calls.Load() != 0 {
		t.Fatalf("stale delivery sent: %+v", d)
	}
}
func TestConcurrentAgentReplayExecutesOneAction(t *testing.T) {
	r, h, _, ai := runner(t)
	ctx := context.Background()
	u, _, w := h.User(t, "alice")
	job, err := h.Core.NewJob(ctx, u.ID, w.ID, "创建任务：test", "same-request", "web")
	if err != nil {
		t.Fatal(err)
	}
	duplicate, err := h.Core.NewJob(ctx, u.ID, w.ID, "创建任务：test", "same-request", "web")
	if err != nil || duplicate.ID != job.ID {
		t.Fatalf("idempotency failure: %v", err)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); errs <- r.AgentJob(ctx, job.ID) }()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	var count int64
	h.DB.Model(&model.Task{}).Count(&count)
	if count != 1 || ai.calls.Load() != 1 {
		t.Fatalf("duplicate operation: tasks=%d calls=%d", count, ai.calls.Load())
	}
	h.DB.First(&job, "id=?", job.ID)
	if job.Status != "succeeded" {
		t.Fatalf("job: %+v", job)
	}
}
func TestRetryAndUnknownDeliveryStates(t *testing.T) {
	r, h, sender, _ := runner(t)
	ctx := context.Background()
	u, _, w := h.User(t, "alice")
	link(t, h, u.ID, "feishu")
	if err := h.Core.Notify(ctx, u.ID, w.ID, "reply", "test", "", nil, 0); err != nil {
		t.Fatal(err)
	}
	var d model.Delivery
	h.DB.First(&d)
	sender.err = &platform.SendError{Message: "limited", Retryable: true}
	if err := r.Deliver(ctx, d.ID); err != nil {
		t.Fatal(err)
	}
	h.DB.First(&d, "id=?", d.ID)
	if d.Status != "retry" || d.Attempts != 1 || !d.NextAttemptAt.After(time.Now()) {
		t.Fatalf("retry state: %+v", d)
	}
	sender.err = nil
	h.DB.Model(&d).Update("next_attempt_at", time.Now().Add(-time.Second))
	if err := r.Deliver(ctx, d.ID); err != nil {
		t.Fatal(err)
	}
	h.DB.First(&d, "id=?", d.ID)
	if d.Status != "accepted" {
		t.Fatal(d.Status)
	}
	link(t, h, u.ID, "wechat")
	h.Core.Notify(ctx, u.ID, w.ID, "reply", "uncertain", "wechat", nil, 0)
	var uncertain model.Delivery
	h.DB.Where("provider='wechat'").First(&uncertain)
	sender.err = &platform.SendError{Message: "response lost", Retryable: true, Unknown: true}
	if err := r.Deliver(ctx, uncertain.ID); err != nil {
		t.Fatal(err)
	}
	h.DB.First(&uncertain, "id=?", uncertain.ID)
	if uncertain.Status != "unknown" {
		t.Fatalf("uncertain send retried: %+v", uncertain)
	}
}
func TestInboxDedupeAndPostgresRecovery(t *testing.T) {
	r, h, _, _ := runner(t)
	ctx := context.Background()
	u, _, _ := h.User(t, "alice")
	link(t, h, u.ID, "feishu")
	event := model.InboxEvent{Provider: "feishu", AppID: "app", ExternalID: "message-1", SenderID: "external_" + u.ID, Text: "创建任务：test"}
	for i := 0; i < 3; i++ {
		if err := h.Core.Receive(ctx, event); err != nil {
			t.Fatal(err)
		}
	}
	var inbox model.InboxEvent
	h.DB.First(&inbox)
	for i := 0; i < 2; i++ {
		if err := r.Inbox(ctx, inbox.ID); err != nil {
			t.Fatal(err)
		}
	}
	var count int64
	h.DB.Model(&model.AgentJob{}).Count(&count)
	if count != 1 {
		t.Fatalf("jobs created from replay: %d", count)
	}
	h.DB.Model(&model.AgentJob{}).Where("1=1").Updates(map[string]any{"status": "running", "lease_until": time.Now().Add(-time.Minute)})
	h.DB.Model(&model.OutboxEvent{}).Where("1=1").Update("published_at", time.Now())
	if err := r.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	if err := r.Publish(ctx); err != nil {
		t.Fatal(err)
	}
	if h.Redis.XLen(ctx, stream).Val() == 0 {
		t.Fatal("lost queue not rebuilt")
	}
}
func TestWorkerProcessesQueueEndToEnd(t *testing.T) {
	r, h, _, _ := runner(t)
	u, _, w := h.User(t, "alice")
	job, err := h.Core.NewJob(context.Background(), u.ID, w.ID, "创建任务：e2e", "queue-e2e", "web")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- r.Run(ctx) }()
	defer func() {
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
	}()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		h.DB.First(&job, "id=?", job.ID)
		if job.Status == "succeeded" {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("queue job unfinished: %+v", job)
}

func TestExpiredWechatSendLeaseBecomesUnknown(t *testing.T) {
	r, h, sender, _ := runner(t)
	ctx := context.Background()
	u, _, w := h.User(t, "alice")
	link(t, h, u.ID, "wechat")
	if err := h.Core.Notify(ctx, u.ID, w.ID, "reply", "test", "wechat", nil, 0); err != nil {
		t.Fatal(err)
	}
	var d model.Delivery
	h.DB.First(&d)
	h.DB.Model(&d).Updates(map[string]any{"status": "sending", "lease_until": time.Now().Add(-time.Minute)})
	if err := r.Deliver(ctx, d.ID); err != nil {
		t.Fatal(err)
	}
	h.DB.First(&d, "id=?", d.ID)
	if d.Status != "unknown" || sender.calls.Load() != 0 {
		t.Fatalf("uncertain crashed send was repeated: %+v", d)
	}
}

func TestWorkerRecreatesLostConsumerGroup(t *testing.T) {
	r, h, _, _ := runner(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	if err := h.Redis.XGroupCreateMkStream(ctx, stream, group, "0").Err(); err != nil {
		t.Fatal(err)
	}
	go func() { done <- r.Run(ctx) }()
	defer func() {
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
	}()
	if err := h.Redis.XGroupDestroy(ctx, stream, group).Err(); err != nil {
		t.Fatal(err)
	}
	u, _, w := h.User(t, "alice")
	job, err := h.Core.NewJob(ctx, u.ID, w.ID, "创建任务：recovered", "group-recovery", "web")
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		h.DB.First(&job, "id=?", job.ID)
		if job.Status == "succeeded" {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("job stalled after consumer group loss: %+v", job)
}
