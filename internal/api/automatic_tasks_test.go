package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/swordstudiox/vohive-plus/internal/config"
	"github.com/swordstudiox/vohive-plus/internal/db"
	"github.com/swordstudiox/vohive-plus/internal/device"
)

func newAutomaticTasksTestServer(t *testing.T) *Server {
	t.Helper()
	gin.SetMode(gin.TestMode)
	if err := db.Init(filepath.Join(t.TempDir(), "automatic-tasks.db")); err != nil {
		t.Fatalf("db.Init() error=%v", err)
	}
	configPath := writeDeviceMgmtLimitConfig(t, `
server:
  port: ":0"
devices:
  - id: dev-1
    name: Test Modem
    at_port: /dev/ttyUSB2
`)
	if err := config.InitGlobalManager(configPath); err != nil {
		t.Fatalf("config.InitGlobalManager() error=%v", err)
	}
	t.Cleanup(func() { db.DB = nil })
	return &Server{pool: device.NewPool(config.GetConfig())}
}

func automaticTaskRequestJSON(phone string) string {
	payload := map[string]any{
		"name": "保号短信", "enabled": true, "device_id": "dev-1",
		"profile_iccid": "8986001234567890123", "profile_aid": "",
		"task_type": "sms", "environment": "cellular", "interval_days": 30,
		"start_date": "2026-10-10", "run_time": "17:13", "timezone": "Asia/Shanghai",
		"phone": phone, "message": "keepalive", "duration_seconds": 30,
		"retry_count": 2, "notify": true,
	}
	encoded, _ := json.Marshal(payload)
	return string(encoded)
}

func TestAutomaticTaskCreateAndListAPI(t *testing.T) {
	server := newAutomaticTasksTestServer(t)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/automatic-tasks", strings.NewReader(automaticTaskRequestJSON("+8613800138000")))
	c.Request.Header.Set("Content-Type", "application/json")
	server.handleCreateAutomaticTask(c)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", recorder.Code, recorder.Body.String())
	}

	recorder = httptest.NewRecorder()
	c, _ = gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/automatic-tasks", nil)
	server.handleListAutomaticTasks(c)
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), "保号短信") {
		t.Fatalf("list status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestAutomaticTaskRejectsUnsafePhone(t *testing.T) {
	server := newAutomaticTasksTestServer(t)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/automatic-tasks", strings.NewReader(automaticTaskRequestJSON("10086;AT+CFUN=0")))
	c.Request.Header.Set("Content-Type", "application/json")
	server.handleCreateAutomaticTask(c)
	if recorder.Code != http.StatusBadRequest || !strings.Contains(recorder.Body.String(), "号码格式无效") {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestAutomaticTaskSchedulerSerializesPerDevice(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	started := make(chan uint, 3)
	releaseFirst := make(chan struct{})
	scheduler := &automaticTaskScheduler{
		ctx: ctx, queues: make(map[string]chan db.AutomaticTaskRun), enqueued: make(map[uint]bool),
		executeRun: func(run db.AutomaticTaskRun) {
			started <- run.ID
			if run.ID == 1 {
				<-releaseFirst
			}
		},
	}
	if err := scheduler.enqueue(db.AutomaticTaskRun{ID: 1, DeviceID: "dev-a"}); err != nil {
		t.Fatal(err)
	}
	if err := scheduler.enqueue(db.AutomaticTaskRun{ID: 2, DeviceID: "dev-a"}); err != nil {
		t.Fatal(err)
	}
	if err := scheduler.enqueue(db.AutomaticTaskRun{ID: 3, DeviceID: "dev-b"}); err != nil {
		t.Fatal(err)
	}

	seen := map[uint]bool{}
	deadline := time.After(time.Second)
	for len(seen) < 2 {
		select {
		case id := <-started:
			seen[id] = true
		case <-deadline:
			t.Fatalf("started before release=%v, want task 1 and cross-device task 3", seen)
		}
	}
	if !seen[1] || !seen[3] || seen[2] {
		t.Fatalf("started before release=%v, same-device task ran concurrently", seen)
	}
	close(releaseFirst)
	select {
	case id := <-started:
		if id != 2 {
			t.Fatalf("next task=%d want=2", id)
		}
	case <-time.After(time.Second):
		t.Fatal("same-device queued task did not start after predecessor finished")
	}
}

func TestAutomaticTaskSchedulerFullDeviceQueueDoesNotBlock(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	queue := make(chan db.AutomaticTaskRun, 1)
	queue <- db.AutomaticTaskRun{ID: 1, DeviceID: "dev-a"}
	scheduler := &automaticTaskScheduler{
		ctx:      ctx,
		queues:   map[string]chan db.AutomaticTaskRun{"dev-a": queue},
		enqueued: map[uint]bool{1: true},
	}
	started := time.Now()
	err := scheduler.enqueue(db.AutomaticTaskRun{ID: 2, DeviceID: "dev-a"})
	if !errors.Is(err, errAutomaticTaskQueueFull) {
		t.Fatalf("enqueue() err=%v", err)
	}
	if elapsed := time.Since(started); elapsed > 100*time.Millisecond {
		t.Fatalf("enqueue blocked for %s", elapsed)
	}
}

func TestDeleteAutomaticTaskAPIRejectsRunningTask(t *testing.T) {
	server := newAutomaticTasksTestServer(t)
	task, err := db.SaveAutomaticTask(db.AutomaticTask{
		Name: "运行中任务", Enabled: true, DeviceID: "dev-1", ProfileICCID: "8986001234567890123",
		TaskType: "sms", Environment: "cellular", IntervalDays: 30,
		StartDate: "2026-10-10", RunTime: "17:13", Timezone: "Asia/Shanghai",
		Phone: "+8613800138000", Message: "keepalive", RetryCount: 0,
	})
	if err != nil {
		t.Fatal(err)
	}
	run, err := db.QueueAutomaticTaskNow(task)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := db.StartAutomaticTaskRun(run.ID, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Params = gin.Params{{Key: "task_id", Value: strconv.FormatUint(uint64(task.ID), 10)}}
	c.Request = httptest.NewRequest(http.MethodDelete, "/api/automatic-tasks/1", nil)
	server.handleDeleteAutomaticTask(c)
	if recorder.Code != http.StatusConflict {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}
