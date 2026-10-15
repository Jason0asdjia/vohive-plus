package db

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func automaticTaskFixture() AutomaticTask {
	return AutomaticTask{
		Name: "保号短信", Enabled: true, DeviceID: "dev-1",
		ProfileICCID: "8986001234567890123F", TaskType: "sms", Environment: "cellular",
		IntervalDays: 2, StartDate: "2026-10-10", RunTime: "10:00", Timezone: "Asia/Shanghai",
		Phone: "+8613800138000", Message: "keepalive", RetryCount: 1, Notify: true,
	}
}

func TestNextAutomaticTaskRunUsesWallClockSchedule(t *testing.T) {
	task := automaticTaskFixture()
	after := time.Date(2026, 10, 10, 2, 0, 0, 0, time.UTC) // 10:00 Asia/Shanghai
	got, err := NextAutomaticTaskRun(task, after)
	if err != nil {
		t.Fatalf("NextAutomaticTaskRun() error=%v", err)
	}
	want := time.Date(2026, 10, 12, 2, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Fatalf("next=%s want=%s", got, want)
	}
}

func TestAutomaticTaskClaimAndCompletion(t *testing.T) {
	openTestDB(t)
	task, err := SaveAutomaticTask(automaticTaskFixture())
	if err != nil {
		t.Fatalf("SaveAutomaticTask() error=%v", err)
	}
	if task.ProfileICCID != "8986001234567890123" {
		t.Fatalf("canonical ICCID=%q", task.ProfileICCID)
	}
	due := time.Now().UTC().Add(-time.Minute)
	if err := DB.Model(&AutomaticTask{}).Where("id = ?", task.ID).Update("next_run_at", due).Error; err != nil {
		t.Fatal(err)
	}
	runs, err := ClaimDueAutomaticTasks(time.Now().UTC(), 10)
	if err != nil {
		t.Fatalf("ClaimDueAutomaticTasks() error=%v", err)
	}
	if len(runs) != 1 || runs[0].Status != AutomaticTaskStatusQueued || runs[0].TaskID != task.ID {
		t.Fatalf("runs=%+v", runs)
	}
	second, err := ClaimDueAutomaticTasks(time.Now().UTC(), 10)
	if err != nil || len(second) != 0 {
		t.Fatalf("second claim=%+v err=%v", second, err)
	}
	started, finished := time.Now().UTC(), time.Now().UTC().Add(time.Second)
	run := runs[0]
	run.Status, run.StartedAt, run.FinishedAt = AutomaticTaskStatusSuccess, &started, &finished
	run.Attempts, run.Output = 1, "done"
	if err := UpdateAutomaticTaskRun(run); err != nil {
		t.Fatalf("UpdateAutomaticTaskRun() error=%v", err)
	}
	updated, err := GetAutomaticTask(task.ID)
	if err != nil || updated.LastStatus != AutomaticTaskStatusSuccess || updated.LastRunAt == nil {
		t.Fatalf("updated=%+v err=%v", updated, err)
	}
}

func TestRecoverAutomaticTaskRunsDoesNotReplayRunningSideEffects(t *testing.T) {
	openTestDB(t)
	task, err := SaveAutomaticTask(automaticTaskFixture())
	if err != nil {
		t.Fatal(err)
	}
	run, err := QueueAutomaticTaskNow(task)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := StartAutomaticTaskRun(run.ID, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	recovered, err := RecoverAutomaticTaskRuns(time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if len(recovered) != 0 {
		t.Fatalf("recovered=%+v", recovered)
	}
	var interrupted AutomaticTaskRun
	if err := DB.First(&interrupted, run.ID).Error; err != nil {
		t.Fatal(err)
	}
	if interrupted.Status != AutomaticTaskStatusFailed || interrupted.FinishedAt == nil || !strings.Contains(interrupted.Error, "避免重复") {
		t.Fatalf("interrupted=%+v", interrupted)
	}
	updated, err := GetAutomaticTask(task.ID)
	if err != nil || updated.LastStatus != AutomaticTaskStatusFailed || !strings.Contains(updated.LastError, "避免重复") {
		t.Fatalf("updated=%+v err=%v", updated, err)
	}
}

func TestSaveAutomaticTaskPreservesConcurrentRunResult(t *testing.T) {
	openTestDB(t)
	task, err := SaveAutomaticTask(automaticTaskFixture())
	if err != nil {
		t.Fatal(err)
	}
	finished := time.Now().UTC()
	if err := DB.Model(&AutomaticTask{}).Where("id = ?", task.ID).Updates(map[string]any{
		"last_run_at": finished, "last_status": AutomaticTaskStatusSuccess, "last_error": "",
	}).Error; err != nil {
		t.Fatal(err)
	}
	task.Name = "编辑后的名称"
	saved, err := SaveAutomaticTask(task)
	if err != nil {
		t.Fatal(err)
	}
	if saved.Name != task.Name || saved.LastStatus != AutomaticTaskStatusSuccess || saved.LastRunAt == nil {
		t.Fatalf("saved=%+v", saved)
	}
}

func TestDeleteAutomaticTaskCancelsQueuedRunWithoutReinsertion(t *testing.T) {
	openTestDB(t)
	task, err := SaveAutomaticTask(automaticTaskFixture())
	if err != nil {
		t.Fatal(err)
	}
	run, err := QueueAutomaticTaskNow(task)
	if err != nil {
		t.Fatal(err)
	}
	if err := DeleteAutomaticTask(task.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := StartAutomaticTaskRun(run.ID, time.Now().UTC()); !errors.Is(err, ErrAutomaticTaskRunNotFound) {
		t.Fatalf("StartAutomaticTaskRun() err=%v", err)
	}
	run.Status = AutomaticTaskStatusFailed
	if err := UpdateAutomaticTaskRun(run); !errors.Is(err, ErrAutomaticTaskRunNotFound) {
		t.Fatalf("UpdateAutomaticTaskRun() err=%v", err)
	}
	var count int64
	if err := DB.Model(&AutomaticTaskRun{}).Where("id = ?", run.ID).Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("count=%d err=%v", count, err)
	}
}

func TestDeleteAutomaticTaskRejectsRunningRun(t *testing.T) {
	openTestDB(t)
	task, err := SaveAutomaticTask(automaticTaskFixture())
	if err != nil {
		t.Fatal(err)
	}
	run, err := QueueAutomaticTaskNow(task)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := StartAutomaticTaskRun(run.ID, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if err := DeleteAutomaticTask(task.ID); !errors.Is(err, ErrAutomaticTaskRunning) {
		t.Fatalf("DeleteAutomaticTask() err=%v", err)
	}
}

func TestReplaceCardPolicyIfUnchangedPreservesNewerUserPolicy(t *testing.T) {
	openTestDB(t)
	original := DefaultCardPolicy("8986001234567890123")
	original.NetworkEnabled = true
	original.Source = "user"
	if err := UpsertCardPolicy(original); err != nil {
		t.Fatal(err)
	}
	original, _ = GetCardPolicy(original.ICCID)
	temporary := original
	temporary.NetworkEnabled = false
	temporary.Source = "automatic_task"
	applied, temporary, err := ReplaceCardPolicyIfUnchanged(original, temporary)
	if err != nil || !applied {
		t.Fatalf("temporary applied=%v policy=%+v err=%v", applied, temporary, err)
	}
	user := temporary
	user.AirplaneEnabled = true
	user.Source = "user"
	if err := UpsertCardPolicy(user); err != nil {
		t.Fatal(err)
	}
	restored, effective, err := ReplaceCardPolicyIfUnchanged(temporary, original)
	if err != nil {
		t.Fatal(err)
	}
	if restored || effective.Source != "user" || !effective.AirplaneEnabled {
		t.Fatalf("restored=%v effective=%+v", restored, effective)
	}
}

func TestReplaceCardPolicyIfUnchangedRestoresUnmodifiedTemporaryPolicy(t *testing.T) {
	openTestDB(t)
	original := DefaultCardPolicy("8986009876543210000")
	original.NetworkEnabled = true
	original.Source = "user"
	if err := UpsertCardPolicy(original); err != nil {
		t.Fatal(err)
	}
	original, _ = GetCardPolicy(original.ICCID)
	temporary := original
	temporary.NetworkEnabled = false
	temporary.Source = "automatic_task"
	applied, temporary, err := ReplaceCardPolicyIfUnchanged(original, temporary)
	if err != nil || !applied {
		t.Fatalf("temporary applied=%v err=%v", applied, err)
	}
	restored, effective, err := ReplaceCardPolicyIfUnchanged(temporary, original)
	if err != nil || !restored {
		t.Fatalf("restored=%v err=%v", restored, err)
	}
	if effective.Source != "user" || !effective.NetworkEnabled {
		t.Fatalf("effective=%+v", effective)
	}
}
