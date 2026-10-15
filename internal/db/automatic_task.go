package db

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"
)

const (
	AutomaticTaskStatusQueued  = "queued"
	AutomaticTaskStatusRunning = "running"
	AutomaticTaskStatusSuccess = "success"
	AutomaticTaskStatusFailed  = "failed"
)

var (
	ErrAutomaticTaskRunning     = errors.New("automatic task is running")
	ErrAutomaticTaskRunNotFound = errors.New("automatic task run not found")
)

// AutomaticTask defines a recurring device action. Times are persisted in UTC;
// StartDate/RunTime/Timezone preserve the user's wall-clock schedule.
type AutomaticTask struct {
	ID              uint       `gorm:"primaryKey" json:"id"`
	Name            string     `gorm:"size:128;not null" json:"name"`
	Enabled         bool       `gorm:"not null;default:true;index" json:"enabled"`
	DeviceID        string     `gorm:"size:128;not null;index" json:"device_id"`
	ProfileICCID    string     `gorm:"size:64;not null;index" json:"profile_iccid"`
	ProfileAID      string     `gorm:"size:128" json:"profile_aid"`
	TaskType        string     `gorm:"size:24;not null" json:"task_type"`
	Environment     string     `gorm:"size:24;not null" json:"environment"`
	IntervalDays    int        `gorm:"not null" json:"interval_days"`
	StartDate       string     `gorm:"size:10;not null" json:"start_date"`
	RunTime         string     `gorm:"size:5;not null" json:"run_time"`
	Timezone        string     `gorm:"size:64;not null" json:"timezone"`
	Phone           string     `gorm:"size:64;not null" json:"phone"`
	Message         string     `gorm:"type:text" json:"message"`
	DurationSeconds int        `gorm:"not null;default:30" json:"duration_seconds"`
	RetryCount      int        `gorm:"not null;default:1" json:"retry_count"`
	Notify          bool       `gorm:"not null;default:true" json:"notify"`
	NextRunAt       *time.Time `gorm:"index" json:"next_run_at,omitempty"`
	LastRunAt       *time.Time `json:"last_run_at,omitempty"`
	LastStatus      string     `gorm:"size:24" json:"last_status"`
	LastError       string     `gorm:"type:text" json:"last_error"`
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
}

func (AutomaticTask) TableName() string { return "automatic_tasks" }

type AutomaticTaskRun struct {
	ID          uint       `gorm:"primaryKey" json:"id"`
	TaskID      uint       `gorm:"not null;index" json:"task_id"`
	TaskName    string     `gorm:"size:128;not null" json:"task_name"`
	DeviceID    string     `gorm:"size:128;not null;index" json:"device_id"`
	ScheduledAt time.Time  `gorm:"not null;index" json:"scheduled_at"`
	StartedAt   *time.Time `json:"started_at,omitempty"`
	FinishedAt  *time.Time `json:"finished_at,omitempty"`
	Status      string     `gorm:"size:24;not null;index" json:"status"`
	Attempts    int        `gorm:"not null;default:0" json:"attempts"`
	Output      string     `gorm:"type:text" json:"output"`
	Error       string     `gorm:"type:text" json:"error"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
}

func (AutomaticTaskRun) TableName() string { return "automatic_task_runs" }

func NormalizeAutomaticTask(task *AutomaticTask) {
	if task == nil {
		return
	}
	task.Name = strings.TrimSpace(task.Name)
	task.DeviceID = strings.TrimSpace(task.DeviceID)
	task.ProfileICCID = CanonicalICCID(task.ProfileICCID)
	task.ProfileAID = strings.ToUpper(strings.TrimSpace(task.ProfileAID))
	task.TaskType = strings.ToLower(strings.TrimSpace(task.TaskType))
	task.Environment = strings.ToLower(strings.TrimSpace(task.Environment))
	task.StartDate = strings.TrimSpace(task.StartDate)
	task.RunTime = strings.TrimSpace(task.RunTime)
	task.Timezone = strings.TrimSpace(task.Timezone)
	task.Phone = strings.TrimSpace(task.Phone)
	if task.Timezone == "" {
		task.Timezone = "Asia/Shanghai"
	}
}

func NextAutomaticTaskRun(task AutomaticTask, after time.Time) (time.Time, error) {
	NormalizeAutomaticTask(&task)
	if task.IntervalDays <= 0 {
		return time.Time{}, errors.New("interval_days must be positive")
	}
	location, err := time.LoadLocation(task.Timezone)
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid timezone: %w", err)
	}
	start, err := time.ParseInLocation("2006-01-02 15:04", task.StartDate+" "+task.RunTime, location)
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid start date/time: %w", err)
	}
	afterLocal := after.In(location)
	candidate := start
	if !candidate.After(afterLocal) {
		days := int(afterLocal.Sub(candidate).Hours() / 24)
		steps := days / task.IntervalDays
		candidate = candidate.AddDate(0, 0, steps*task.IntervalDays)
		for !candidate.After(afterLocal) {
			candidate = candidate.AddDate(0, 0, task.IntervalDays)
		}
	}
	return candidate.UTC(), nil
}

func SaveAutomaticTask(task AutomaticTask) (AutomaticTask, error) {
	if DB == nil {
		return AutomaticTask{}, errors.New("db 未初始化")
	}
	NormalizeAutomaticTask(&task)
	next, err := NextAutomaticTaskRun(task, time.Now().UTC())
	if err != nil {
		return AutomaticTask{}, err
	}
	task.NextRunAt = &next
	if task.ID == 0 {
		if err := DB.Create(&task).Error; err != nil {
			return AutomaticTask{}, err
		}
		return task, nil
	}
	updates := map[string]any{
		"name": task.Name, "enabled": task.Enabled, "device_id": task.DeviceID,
		"profile_icc_id": task.ProfileICCID, "profile_a_id": task.ProfileAID,
		"task_type": task.TaskType, "environment": task.Environment,
		"interval_days": task.IntervalDays, "start_date": task.StartDate,
		"run_time": task.RunTime, "timezone": task.Timezone,
		"phone": task.Phone, "message": task.Message,
		"duration_seconds": task.DurationSeconds, "retry_count": task.RetryCount,
		"notify": task.Notify, "next_run_at": task.NextRunAt, "updated_at": time.Now().UTC(),
	}
	result := DB.Model(&AutomaticTask{}).Where("id = ?", task.ID).Updates(updates)
	if result.Error != nil {
		return AutomaticTask{}, result.Error
	}
	if result.RowsAffected == 0 {
		return AutomaticTask{}, gorm.ErrRecordNotFound
	}
	return GetAutomaticTask(task.ID)
}

func GetAutomaticTask(id uint) (AutomaticTask, error) {
	var task AutomaticTask
	if DB == nil {
		return task, errors.New("db 未初始化")
	}
	err := DB.First(&task, id).Error
	return task, err
}

func ListAutomaticTasks() ([]AutomaticTask, error) {
	var tasks []AutomaticTask
	if DB == nil {
		return tasks, errors.New("db 未初始化")
	}
	return tasks, DB.Order("created_at desc, id desc").Find(&tasks).Error
}

func DeleteAutomaticTask(id uint) error {
	if DB == nil {
		return errors.New("db 未初始化")
	}
	return DB.Transaction(func(tx *gorm.DB) error {
		var running int64
		if err := tx.Model(&AutomaticTaskRun{}).Where("task_id = ? AND status = ?", id, AutomaticTaskStatusRunning).Count(&running).Error; err != nil {
			return err
		}
		if running > 0 {
			return ErrAutomaticTaskRunning
		}
		if err := tx.Where("task_id = ?", id).Delete(&AutomaticTaskRun{}).Error; err != nil {
			return err
		}
		result := tx.Delete(&AutomaticTask{}, id)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return gorm.ErrRecordNotFound
		}
		return nil
	})
}

func queueAutomaticTaskRun(tx *gorm.DB, task AutomaticTask, scheduledAt time.Time) (AutomaticTaskRun, error) {
	run := AutomaticTaskRun{
		TaskID: task.ID, TaskName: task.Name, DeviceID: task.DeviceID,
		ScheduledAt: scheduledAt.UTC(), Status: AutomaticTaskStatusQueued,
	}
	if err := tx.Create(&run).Error; err != nil {
		return AutomaticTaskRun{}, err
	}
	return run, nil
}

// StartAutomaticTaskRun atomically claims a queued run while verifying its
// parent task still exists. It closes the delete-vs-start race.
func StartAutomaticTaskRun(runID uint, now time.Time) (AutomaticTask, AutomaticTaskRun, error) {
	if DB == nil {
		return AutomaticTask{}, AutomaticTaskRun{}, errors.New("db 未初始化")
	}
	var task AutomaticTask
	var run AutomaticTaskRun
	err := DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("id = ? AND status = ?", runID, AutomaticTaskStatusQueued).First(&run).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrAutomaticTaskRunNotFound
			}
			return err
		}
		if err := tx.First(&task, run.TaskID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrAutomaticTaskRunNotFound
			}
			return err
		}
		started := now.UTC()
		result := tx.Model(&AutomaticTaskRun{}).
			Where("id = ? AND status = ?", run.ID, AutomaticTaskStatusQueued).
			Updates(map[string]any{"status": AutomaticTaskStatusRunning, "started_at": started, "updated_at": started})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrAutomaticTaskRunNotFound
		}
		run.Status = AutomaticTaskStatusRunning
		run.StartedAt = &started
		return nil
	})
	return task, run, err
}

func QueueAutomaticTaskNow(task AutomaticTask) (AutomaticTaskRun, error) {
	if DB == nil {
		return AutomaticTaskRun{}, errors.New("db 未初始化")
	}
	return queueAutomaticTaskRun(DB, task, time.Now().UTC())
}

func ClaimDueAutomaticTasks(now time.Time, limit int) ([]AutomaticTaskRun, error) {
	if DB == nil {
		return nil, errors.New("db 未初始化")
	}
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	var runs []AutomaticTaskRun
	err := DB.Transaction(func(tx *gorm.DB) error {
		var tasks []AutomaticTask
		if err := tx.Where("enabled = ? AND next_run_at IS NOT NULL AND next_run_at <= ?", true, now.UTC()).
			Order("next_run_at asc").Limit(limit).Find(&tasks).Error; err != nil {
			return err
		}
		for _, task := range tasks {
			scheduledAt := now.UTC()
			if task.NextRunAt != nil {
				scheduledAt = task.NextRunAt.UTC()
			}
			run, err := queueAutomaticTaskRun(tx, task, scheduledAt)
			if err != nil {
				return err
			}
			next, err := NextAutomaticTaskRun(task, now.UTC())
			if err != nil {
				return err
			}
			if err := tx.Model(&AutomaticTask{}).Where("id = ?", task.ID).Update("next_run_at", next).Error; err != nil {
				return err
			}
			runs = append(runs, run)
		}
		return nil
	})
	return runs, err
}

func RecoverAutomaticTaskRuns(now time.Time) ([]AutomaticTaskRun, error) {
	if DB == nil {
		return nil, errors.New("db 未初始化")
	}
	var runs []AutomaticTaskRun
	err := DB.Transaction(func(tx *gorm.DB) error {
		var interrupted []AutomaticTaskRun
		if err := tx.Where("status = ?", AutomaticTaskStatusRunning).Find(&interrupted).Error; err != nil {
			return err
		}
		finished := now.UTC()
		const message = "服务重启时任务执行结果不确定；为避免重复短信或通话，未自动重试"
		for _, run := range interrupted {
			result := tx.Model(&AutomaticTaskRun{}).Where("id = ? AND status = ?", run.ID, AutomaticTaskStatusRunning).
				Updates(map[string]any{
					"status": AutomaticTaskStatusFailed, "finished_at": finished,
					"error": message, "updated_at": finished,
				})
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected == 0 {
				continue
			}
			if err := tx.Model(&AutomaticTask{}).Where("id = ?", run.TaskID).Updates(map[string]any{
				"last_run_at": finished, "last_status": AutomaticTaskStatusFailed,
				"last_error": message, "updated_at": finished,
			}).Error; err != nil {
				return err
			}
		}
		return tx.Where("status = ?", AutomaticTaskStatusQueued).Order("scheduled_at asc").Find(&runs).Error
	})
	return runs, err
}

func UpdateAutomaticTaskRun(run AutomaticTaskRun) error {
	if DB == nil {
		return errors.New("db 未初始化")
	}
	return DB.Transaction(func(tx *gorm.DB) error {
		result := tx.Model(&AutomaticTaskRun{}).Where("id = ?", run.ID).Updates(map[string]any{
			"task_id": run.TaskID, "task_name": run.TaskName, "device_id": run.DeviceID,
			"scheduled_at": run.ScheduledAt, "started_at": run.StartedAt,
			"finished_at": run.FinishedAt, "status": run.Status, "attempts": run.Attempts,
			"output": run.Output, "error": run.Error, "updated_at": time.Now().UTC(),
		})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return ErrAutomaticTaskRunNotFound
		}
		if run.Status != AutomaticTaskStatusSuccess && run.Status != AutomaticTaskStatusFailed {
			return nil
		}
		updates := map[string]any{
			"last_run_at": run.FinishedAt, "last_status": run.Status,
			"last_error": run.Error, "updated_at": time.Now().UTC(),
		}
		return tx.Model(&AutomaticTask{}).Where("id = ?", run.TaskID).Updates(updates).Error
	})
}

func ListQueuedAutomaticTaskRuns(limit int) ([]AutomaticTaskRun, error) {
	if DB == nil {
		return nil, errors.New("db 未初始化")
	}
	if limit <= 0 || limit > 1000 {
		limit = 500
	}
	var runs []AutomaticTaskRun
	err := DB.Where("status = ?", AutomaticTaskStatusQueued).
		Order("scheduled_at asc, id asc").Limit(limit).Find(&runs).Error
	return runs, err
}

func ListAutomaticTaskRuns(limit, offset int) ([]AutomaticTaskRun, int64, error) {
	if DB == nil {
		return nil, 0, errors.New("db 未初始化")
	}
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	if offset < 0 {
		offset = 0
	}
	var total int64
	if err := DB.Model(&AutomaticTaskRun{}).Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var runs []AutomaticTaskRun
	err := DB.Order("scheduled_at desc, id desc").Limit(limit).Offset(offset).Find(&runs).Error
	return runs, total, err
}
