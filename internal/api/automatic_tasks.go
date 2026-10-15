package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/iniwex5/vowifi-go/runtimehost/voicehost"
	"github.com/swordstudiox/vohive-plus/internal/backend"
	"github.com/swordstudiox/vohive-plus/internal/config"
	"github.com/swordstudiox/vohive-plus/internal/db"
	"github.com/swordstudiox/vohive-plus/internal/device"
	"github.com/swordstudiox/vohive-plus/pkg/logger"
	"gorm.io/gorm"
)

const (
	automaticTaskPollInterval = 5 * time.Second
	automaticTaskMaxRuntime   = 8 * time.Minute
	automaticTaskQueueSize    = 100
)

var automaticTaskPhonePattern = regexp.MustCompile(`^\+?[0-9*#]{1,32}$`)
var errAutomaticTaskQueueFull = errors.New("automatic task device queue is full")

type automaticTaskScheduler struct {
	server     *Server
	ctx        context.Context
	mu         sync.Mutex
	queues     map[string]chan db.AutomaticTaskRun
	enqueued   map[uint]bool
	executeRun func(db.AutomaticTaskRun)
}

type automaticTaskExecutionError struct {
	err       error
	retryable bool
}

func (e automaticTaskExecutionError) Error() string { return e.err.Error() }
func (e automaticTaskExecutionError) Unwrap() error { return e.err }

type automaticTaskEnvironmentSnapshot struct {
	originalPolicy  db.CardPolicy
	appliedPolicy   db.CardPolicy
	temporaryActive bool
}

func (s *Server) startAutomaticTasks() {
	if s == nil || s.automaticTasks != nil || db.DB == nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	scheduler := &automaticTaskScheduler{
		server: s, ctx: ctx, queues: make(map[string]chan db.AutomaticTaskRun), enqueued: make(map[uint]bool),
	}
	s.automaticTasks = scheduler
	s.automaticTasksCancel = cancel
	queued, err := db.RecoverAutomaticTaskRuns(time.Now().UTC())
	if err != nil {
		logger.Warn("恢复自动任务队列失败", "err", err)
	} else {
		for _, run := range queued {
			if err := scheduler.enqueue(run); err != nil && !errors.Is(err, errAutomaticTaskQueueFull) {
				logger.Warn("恢复自动任务入队失败", "run_id", run.ID, "err", err)
			}
		}
	}
	go scheduler.run()
}

func (s *automaticTaskScheduler) run() {
	ticker := time.NewTicker(automaticTaskPollInterval)
	defer ticker.Stop()
	s.claim()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-ticker.C:
			s.claim()
		}
	}
}

func (s *automaticTaskScheduler) claim() {
	runs, err := db.ClaimDueAutomaticTasks(time.Now().UTC(), 50)
	if err != nil {
		logger.Warn("领取到期自动任务失败", "err", err)
		return
	}
	for _, run := range runs {
		if err := s.enqueue(run); err != nil && !errors.Is(err, errAutomaticTaskQueueFull) {
			logger.Warn("自动任务入队失败", "run_id", run.ID, "err", err)
		}
	}
	s.dispatchQueued()
}

func (s *automaticTaskScheduler) dispatchQueued() {
	runs, err := db.ListQueuedAutomaticTaskRuns(500)
	if err != nil {
		logger.Warn("读取待执行自动任务失败", "err", err)
		return
	}
	for _, run := range runs {
		if err := s.enqueue(run); err != nil && !errors.Is(err, errAutomaticTaskQueueFull) {
			logger.Warn("待执行自动任务入队失败", "run_id", run.ID, "err", err)
		}
	}
}

func (s *automaticTaskScheduler) enqueue(run db.AutomaticTaskRun) error {
	deviceID := strings.TrimSpace(run.DeviceID)
	s.mu.Lock()
	if s.enqueued == nil {
		s.enqueued = make(map[uint]bool)
	}
	if s.enqueued[run.ID] {
		s.mu.Unlock()
		return nil
	}
	queue := s.queues[deviceID]
	if queue == nil {
		queue = make(chan db.AutomaticTaskRun, automaticTaskQueueSize)
		s.queues[deviceID] = queue
		go s.worker(queue)
	}
	select {
	case queue <- run:
		s.enqueued[run.ID] = true
		s.mu.Unlock()
		return nil
	case <-s.ctx.Done():
		s.mu.Unlock()
		return s.ctx.Err()
	default:
		s.mu.Unlock()
		return errAutomaticTaskQueueFull
	}
}

func (s *automaticTaskScheduler) worker(queue <-chan db.AutomaticTaskRun) {
	for {
		select {
		case <-s.ctx.Done():
			return
		case run := <-queue:
			if s.executeRun != nil {
				s.executeRun(run)
			} else {
				s.execute(run)
			}
			s.mu.Lock()
			delete(s.enqueued, run.ID)
			s.mu.Unlock()
		}
	}
}

func (s *automaticTaskScheduler) execute(run db.AutomaticTaskRun) {
	task, startedRun, err := db.StartAutomaticTaskRun(run.ID, time.Now().UTC())
	if err != nil {
		if !errors.Is(err, db.ErrAutomaticTaskRunNotFound) {
			logger.Warn("领取自动任务执行记录失败", "run_id", run.ID, "err", err)
		}
		return
	}
	run = startedRun
	var output string
attemptLoop:
	for attempt := 1; attempt <= task.RetryCount+1; attempt++ {
		run.Attempts = attempt
		run.Output = fmt.Sprintf("第 %d 次尝试：正在检查设备和 SIM/eSIM Profile", attempt)
		_ = db.UpdateAutomaticTaskRun(run)
		progress := func(message string) {
			run.Output = fmt.Sprintf("第 %d 次尝试：%s", attempt, message)
			_ = db.UpdateAutomaticTaskRun(run)
		}
		opCtx, cancel := context.WithTimeout(s.ctx, automaticTaskMaxRuntime)
		output, err = s.server.executeAutomaticTask(opCtx, task, progress)
		cancel()
		if err == nil {
			break
		}
		var executionErr automaticTaskExecutionError
		if errors.As(err, &executionErr) && !executionErr.retryable {
			break
		}
		if attempt <= task.RetryCount {
			logger.Warn("自动任务尝试失败", "task_id", task.ID, "device_id", task.DeviceID, "attempt", attempt)
			select {
			case <-s.ctx.Done():
				err = s.ctx.Err()
				break attemptLoop
			case <-time.After(time.Duration(attempt*5) * time.Second):
			}
		}
	}
	s.finishRun(&run, output, err)
	if task.Notify && s.server.notifyMgr != nil {
		status := "成功"
		detail := output
		if err != nil {
			status, detail = "失败", err.Error()
		}
		s.server.notifyMgr.NotifyRaw(fmt.Sprintf("自动任务执行%s\n任务  %s\n设备  %s\n结果  %s", status, task.Name, task.DeviceID, detail))
	}
}

func (s *automaticTaskScheduler) finishRun(run *db.AutomaticTaskRun, output string, err error) {
	finished := time.Now().UTC()
	run.FinishedAt = &finished
	if err == nil {
		run.Status, run.Output, run.Error = db.AutomaticTaskStatusSuccess, output, ""
	} else {
		run.Status, run.Error = db.AutomaticTaskStatusFailed, err.Error()
	}
	if updateErr := db.UpdateAutomaticTaskRun(*run); updateErr != nil {
		logger.Warn("更新自动任务执行记录失败", "run_id", run.ID, "err", updateErr)
	}
}

func (s *Server) executeAutomaticTask(ctx context.Context, task db.AutomaticTask, progress func(string)) (output string, err error) {
	worker := s.pool.GetWorker(task.DeviceID)
	if worker == nil {
		return "", errors.New("设备离线或尚未启动")
	}
	progress("正在检查设备和 SIM/eSIM Profile")
	if err := s.ensureAutomaticTaskProfile(ctx, task, progress); err != nil {
		return "", err
	}
	worker = s.pool.GetWorker(task.DeviceID)
	if worker == nil {
		return "", errors.New("切卡后设备尚未恢复")
	}
	policy, err := db.ResolveCardPolicy(task.ProfileICCID)
	if err != nil {
		return "", fmt.Errorf("读取卡策略失败: %w", err)
	}
	snapshot := automaticTaskEnvironmentSnapshot{originalPolicy: policy}
	actionStarted := false
	defer func() {
		progress("正在恢复该 Profile 原先保存的卡策略")
		if restoreErr := s.restoreAutomaticTaskEnvironment(task.DeviceID, snapshot); restoreErr != nil {
			if err == nil && actionStarted {
				output = ""
				err = automaticTaskExecutionError{err: fmt.Errorf("任务完成但卡策略恢复失败: %w", restoreErr), retryable: false}
			} else if err == nil {
				err = fmt.Errorf("恢复卡策略失败: %w", restoreErr)
			} else {
				err = errors.Join(err, fmt.Errorf("恢复卡策略也失败: %w", restoreErr))
			}
		}
	}()
	if err := s.prepareAutomaticTaskEnvironment(ctx, worker, task, &snapshot, progress); err != nil {
		return "", err
	}
	actionStarted = true
	switch task.TaskType {
	case "sms":
		progress("正在发送短信")
		output, err = s.executeAutomaticSMS(ctx, worker, task)
	case "call":
		progress("正在发起通话")
		output, err = s.executeAutomaticCall(ctx, worker, task)
	default:
		err = automaticTaskExecutionError{err: fmt.Errorf("不支持的任务类型 %q", task.TaskType), retryable: false}
	}
	if err != nil {
		var executionErr automaticTaskExecutionError
		if !errors.As(err, &executionErr) {
			err = automaticTaskExecutionError{err: err, retryable: false}
		}
	}
	return output, err
}

func (s *Server) ensureAutomaticTaskProfile(ctx context.Context, task db.AutomaticTask, progress func(string)) error {
	worker := s.pool.GetWorker(task.DeviceID)
	if worker == nil {
		return errors.New("设备离线或尚未启动")
	}
	target := db.CanonicalICCID(task.ProfileICCID)
	if db.CanonicalICCID(worker.ConfirmedICCID()) == target {
		return nil
	}
	if worker.EsimMgr == nil {
		return errors.New("当前 SIM 与任务不一致，且设备没有可用的 eSIM 管理器")
	}
	progress("正在切换到任务指定的 eSIM Profile")
	if _, err := worker.EsimMgr.SwitchProfileWithResult(ctx, target, task.ProfileAID); err != nil {
		return fmt.Errorf("切换 eSIM Profile 失败: %w", err)
	}
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		current := s.pool.GetWorker(task.DeviceID)
		if current != nil && db.CanonicalICCID(current.ConfirmedICCID()) == target && !current.SIMIdentityUnconfirmed() {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("等待目标 Profile 生效失败: %w", ctx.Err())
		case <-ticker.C:
		}
	}
}

func (s *Server) prepareAutomaticTaskEnvironment(ctx context.Context, worker *device.Worker, task db.AutomaticTask, snapshot *automaticTaskEnvironmentSnapshot, progress func(string)) error {
	temporary := snapshot.originalPolicy
	temporary.Source = "automatic_task"
	if task.Environment == "vowifi" {
		progress("正在准备 VoWiFi 执行环境")
		temporary.NetworkEnabled = false
		temporary.VoWiFiEnabled = true
		applied, current, err := db.ReplaceCardPolicyIfUnchanged(snapshot.originalPolicy, temporary)
		if err != nil {
			return err
		}
		if !applied {
			return automaticTaskExecutionError{err: errors.New("卡策略已被其他操作更新，请稍后重试"), retryable: true}
		}
		snapshot.appliedPolicy, snapshot.temporaryActive = current, true
		s.pool.SetWorkerNetworkPolicy(task.DeviceID, false, temporary.IPVersion, temporary.APN)
		s.pool.SetWorkerVoWiFiPolicy(task.DeviceID, true)
		state, stateOK := s.pool.GetVoWiFiRuntimeState(task.DeviceID)
		ready := stateOK && state.IMSReady && (task.TaskType != "sms" || state.SMSReady)
		if !ready {
			if err := s.pool.EnableVoWiFi(task.DeviceID); err != nil {
				return fmt.Errorf("启动 VoWiFi 失败: %w", err)
			}
		}
		return s.waitAutomaticVoWiFi(ctx, task.DeviceID, task.TaskType == "sms")
	}
	progress("正在准备基站直连执行环境")
	temporary.NetworkEnabled = false
	temporary.VoWiFiEnabled = false
	temporary.AirplaneEnabled = false
	applied, current, err := db.ReplaceCardPolicyIfUnchanged(snapshot.originalPolicy, temporary)
	if err != nil {
		return err
	}
	if !applied {
		return automaticTaskExecutionError{err: errors.New("卡策略已被其他操作更新，请稍后重试"), retryable: true}
	}
	snapshot.appliedPolicy, snapshot.temporaryActive = current, true
	s.pool.SetWorkerNetworkPolicy(task.DeviceID, false, temporary.IPVersion, temporary.APN)
	s.pool.SetWorkerVoWiFiPolicy(task.DeviceID, false)
	s.pool.SetWorkerAirplanePolicy(task.DeviceID, false)
	if s.pool.IsVoWiFiActive(task.DeviceID) {
		if err := s.pool.DisableVoWiFi(task.DeviceID); err != nil {
			return fmt.Errorf("停止 VoWiFi 失败: %w", err)
		}
	}
	if worker.NetworkController() != nil && worker.NetworkConnected() {
		if err := worker.StopNetwork(); err != nil {
			return fmt.Errorf("停止蜂窝数据失败: %w", err)
		}
	}
	if worker.Backend == nil {
		return errors.New("设备控制后端不可用")
	}
	if err := worker.Backend.SetOperatingMode(ctx, backend.ModeOnline); err != nil {
		return fmt.Errorf("开启蜂窝无线失败: %w", err)
	}
	return s.waitAutomaticCellular(ctx, task.DeviceID)
}

func (s *Server) waitAutomaticVoWiFi(ctx context.Context, deviceID string, requireSMS bool) error {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		state, ok := s.pool.GetVoWiFiRuntimeState(deviceID)
		if ok && state.IMSReady && (!requireSMS || state.SMSReady) {
			return nil
		}
		if ok && state.LastError != "" && state.Phase == "failed" {
			return errors.New(state.LastError)
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("等待 VoWiFi 就绪失败: %w", ctx.Err())
		case <-ticker.C:
		}
	}
}

func (s *Server) waitAutomaticCellular(ctx context.Context, deviceID string) error {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	stable := 0
	for {
		worker := s.pool.GetWorker(deviceID)
		if worker != nil {
			status := worker.GetCachedDeviceStatus()
			if status.RegStatus == 1 || status.RegStatus == 5 {
				stable++
				if stable >= 2 {
					return nil
				}
			} else {
				stable = 0
			}
			_ = worker.RefreshRuntime(ctx, "automatic_task_cellular_wait")
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("等待蜂窝注册失败: %w", ctx.Err())
		case <-ticker.C:
		}
	}
}

func (s *Server) executeAutomaticSMS(ctx context.Context, worker *device.Worker, task db.AutomaticTask) (string, error) {
	if task.Environment == "vowifi" {
		outcome, err := s.pool.SendVoWiFiSMSWithResult(ctx, task.DeviceID, task.Phone, task.Message)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("短信已提交到 %s（%d/%d 分片）", task.Phone, outcome.Parts, outcome.PartsTotal), nil
	}
	err := worker.SendSMS(task.Phone, task.Message)
	imsi := worker.GetIMSI()
	if err != nil {
		if imsi != "" {
			_ = db.SaveSMS(imsi, worker.ID, task.Phone, task.Message, 2, 3, time.Now())
		}
		return "", err
	}
	if imsi != "" {
		_ = db.SaveSMS(imsi, worker.ID, task.Phone, task.Message, 2, 2, time.Now())
	}
	return "短信已提交到 " + task.Phone, nil
}

func waitAutomaticTaskDuration(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (s *Server) executeAutomaticCall(ctx context.Context, worker *device.Worker, task db.AutomaticTask) (string, error) {
	duration := time.Duration(task.DurationSeconds) * time.Second
	if task.Environment == "vowifi" {
		if s.voiceGW == nil {
			return "", errors.New("VoWiFi 语音网关不可用")
		}
		result, err := s.voiceGW.SimulateCall(ctx, task.DeviceID, voicehost.SimulateCallRequest{Callee: task.Phone, HoldSeconds: task.DurationSeconds})
		if err != nil {
			return "", err
		}
		if !result.Success {
			return "", fmt.Errorf("通话未接通: %s", result.Reason)
		}
		return fmt.Sprintf("已拨打 %s，通话保持 %d 秒", task.Phone, task.DurationSeconds), nil
	}
	if worker.QMICore != nil {
		callID, err := worker.QMICore.VOICEDialCall(ctx, task.Phone)
		if err != nil {
			return "", err
		}
		if err := waitAutomaticTaskDuration(ctx, duration); err != nil {
			_, _ = worker.QMICore.VOICEEndCall(context.Background(), callID)
			return "", err
		}
		if _, err := worker.QMICore.VOICEEndCall(ctx, callID); err != nil {
			return "", err
		}
		return fmt.Sprintf("已拨打 %s，并在 %d 秒后挂断", task.Phone, task.DurationSeconds), nil
	}
	if worker.Modem == nil {
		return "", errors.New("设备没有可用的蜂窝语音控制通道")
	}
	if err := worker.Modem.DialCall(task.Phone); err != nil {
		return "", err
	}
	if err := waitAutomaticTaskDuration(ctx, duration); err != nil {
		_ = worker.Modem.HangupCall()
		return "", err
	}
	if err := worker.Modem.HangupCall(); err != nil {
		return "", err
	}
	return fmt.Sprintf("已拨打 %s，并在 %d 秒后挂断", task.Phone, task.DurationSeconds), nil
}

func (s *Server) restoreAutomaticTaskEnvironment(deviceID string, snapshot automaticTaskEnvironmentSnapshot) error {
	if !snapshot.temporaryActive {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	var restoreErrors []error
	restored, effective, err := db.ReplaceCardPolicyIfUnchanged(snapshot.appliedPolicy, snapshot.originalPolicy)
	if err != nil {
		return fmt.Errorf("恢复卡策略: %w", err)
	}
	if !restored {
		logger.Info("自动任务结束时卡策略已被用户更新，保留最新策略", "device", deviceID, "iccid", effective.ICCID)
	}
	s.pool.SetWorkerAirplanePolicy(deviceID, effective.AirplaneEnabled)
	s.pool.SetWorkerNetworkPolicy(deviceID, effective.NetworkEnabled, effective.IPVersion, effective.APN)
	worker := s.pool.SetWorkerVoWiFiPolicy(deviceID, effective.VoWiFiEnabled)
	s.pool.SetWorkerRoamingPolicy(deviceID, effective.RoamingEnabled)
	if worker == nil {
		return errors.Join(append(restoreErrors, errors.New("恢复时设备离线"))...)
	}
	if effective.VoWiFiEnabled {
		if err := s.pool.EnableVoWiFi(deviceID); err != nil {
			restoreErrors = append(restoreErrors, fmt.Errorf("恢复 VoWiFi: %w", err))
		}
		return errors.Join(restoreErrors...)
	}
	if s.pool.IsVoWiFiActive(deviceID) {
		if err := s.pool.DisableVoWiFi(deviceID); err != nil {
			restoreErrors = append(restoreErrors, fmt.Errorf("停止临时 VoWiFi: %w", err))
		}
	}
	if worker.Backend != nil {
		mode := backend.ModeOnline
		if effective.AirplaneEnabled {
			mode = backend.ModeRFOff
		}
		if err := worker.Backend.SetOperatingMode(ctx, mode); err != nil {
			restoreErrors = append(restoreErrors, fmt.Errorf("恢复射频模式: %w", err))
		}
	}
	if effective.NetworkEnabled && !effective.AirplaneEnabled {
		if err := worker.StartNetwork(); err != nil {
			restoreErrors = append(restoreErrors, fmt.Errorf("恢复数据网络: %w", err))
		}
	} else if worker.NetworkController() != nil && worker.NetworkConnected() {
		if err := worker.StopNetwork(); err != nil {
			restoreErrors = append(restoreErrors, fmt.Errorf("恢复数据网络关闭状态: %w", err))
		}
	}
	return errors.Join(restoreErrors...)
}

type automaticTaskRequest struct {
	Name            string `json:"name"`
	Enabled         bool   `json:"enabled"`
	DeviceID        string `json:"device_id"`
	ProfileICCID    string `json:"profile_iccid"`
	ProfileAID      string `json:"profile_aid"`
	TaskType        string `json:"task_type"`
	Environment     string `json:"environment"`
	IntervalDays    int    `json:"interval_days"`
	StartDate       string `json:"start_date"`
	RunTime         string `json:"run_time"`
	Timezone        string `json:"timezone"`
	Phone           string `json:"phone"`
	Message         string `json:"message"`
	DurationSeconds int    `json:"duration_seconds"`
	RetryCount      int    `json:"retry_count"`
	Notify          bool   `json:"notify"`
}

func automaticTaskID(c *gin.Context) (uint, bool) {
	value, err := strconv.ParseUint(strings.TrimSpace(c.Param("task_id")), 10, 64)
	if err != nil || value == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"status": "error", "message": "无效的任务 ID"})
		return 0, false
	}
	return uint(value), true
}

func (s *Server) decodeAutomaticTask(c *gin.Context, id uint) (db.AutomaticTask, error) {
	var req automaticTaskRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		return db.AutomaticTask{}, err
	}
	task := db.AutomaticTask{
		ID: id, Name: req.Name, Enabled: req.Enabled, DeviceID: req.DeviceID,
		ProfileICCID: req.ProfileICCID, ProfileAID: req.ProfileAID,
		TaskType: req.TaskType, Environment: req.Environment,
		IntervalDays: req.IntervalDays, StartDate: req.StartDate, RunTime: req.RunTime,
		Timezone: req.Timezone, Phone: req.Phone, Message: req.Message,
		DurationSeconds: req.DurationSeconds, RetryCount: req.RetryCount, Notify: req.Notify,
	}
	db.NormalizeAutomaticTask(&task)
	if task.Name == "" || task.DeviceID == "" || task.ProfileICCID == "" {
		return task, errors.New("任务名称、设备和 SIM/eSIM Profile 为必填项")
	}
	if len([]rune(task.Name)) > 128 {
		return task, errors.New("任务名称不能超过 128 个字符")
	}
	if task.TaskType != "sms" && task.TaskType != "call" {
		return task, errors.New("任务类型必须是 sms 或 call")
	}
	if task.Environment != "vowifi" && task.Environment != "cellular" {
		return task, errors.New("执行环境必须是 vowifi 或 cellular")
	}
	if !automaticTaskPhonePattern.MatchString(task.Phone) {
		return task, errors.New("号码格式无效")
	}
	if task.TaskType == "sms" && strings.TrimSpace(task.Message) == "" {
		return task, errors.New("短信内容不能为空")
	}
	if len([]rune(task.Message)) > 1000 {
		return task, errors.New("短信内容不能超过 1000 个字符")
	}
	if task.TaskType == "call" && (task.DurationSeconds < 1 || task.DurationSeconds > voicehost.MaxSimulateCallHoldSeconds) {
		return task, fmt.Errorf("通话保持时间必须为 1-%d 秒", voicehost.MaxSimulateCallHoldSeconds)
	}
	if task.IntervalDays < 1 || task.IntervalDays > 3650 {
		return task, errors.New("周期天数必须为 1-3650")
	}
	if task.RetryCount < 0 || task.RetryCount > 5 {
		return task, errors.New("失败重试次数必须为 0-5")
	}
	if _, err := db.NextAutomaticTaskRun(task, time.Now().UTC()); err != nil {
		return task, err
	}
	if configured, _ := config.GetDeviceByID(task.DeviceID); configured == nil && s.pool.GetWorker(task.DeviceID) == nil {
		return task, errors.New("所选设备不存在")
	}
	return task, nil
}

func (s *Server) handleListAutomaticTasks(c *gin.Context) {
	tasks, err := db.ListAutomaticTasks()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"status": "error", "message": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"tasks": tasks})
}

func (s *Server) handleCreateAutomaticTask(c *gin.Context) {
	task, err := s.decodeAutomaticTask(c, 0)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"status": "error", "message": err.Error()})
		return
	}
	saved, err := db.SaveAutomaticTask(task)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"status": "error", "message": err.Error()})
		return
	}
	c.JSON(http.StatusCreated, saved)
}

func (s *Server) handleUpdateAutomaticTask(c *gin.Context) {
	id, ok := automaticTaskID(c)
	if !ok {
		return
	}
	task, err := s.decodeAutomaticTask(c, id)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"status": "error", "message": err.Error()})
		return
	}
	saved, err := db.SaveAutomaticTask(task)
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, gorm.ErrRecordNotFound) {
			status = http.StatusNotFound
		}
		c.JSON(status, gin.H{"status": "error", "message": err.Error()})
		return
	}
	c.JSON(http.StatusOK, saved)
}

func (s *Server) handleDeleteAutomaticTask(c *gin.Context) {
	id, ok := automaticTaskID(c)
	if !ok {
		return
	}
	if err := db.DeleteAutomaticTask(id); err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, gorm.ErrRecordNotFound) {
			status = http.StatusNotFound
		} else if errors.Is(err, db.ErrAutomaticTaskRunning) {
			status = http.StatusConflict
		}
		c.JSON(status, gin.H{"status": "error", "message": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"deleted": true})
}

func (s *Server) handleRunAutomaticTaskNow(c *gin.Context) {
	id, ok := automaticTaskID(c)
	if !ok {
		return
	}
	if s.automaticTasks == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"status": "error", "message": "自动任务调度器尚未启动"})
		return
	}
	task, err := db.GetAutomaticTask(id)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"status": "error", "message": "任务不存在"})
		return
	}
	run, err := db.QueueAutomaticTaskNow(task)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"status": "error", "message": err.Error()})
		return
	}
	if err := s.automaticTasks.enqueue(run); err != nil && !errors.Is(err, errAutomaticTaskQueueFull) {
		c.JSON(http.StatusServiceUnavailable, gin.H{"status": "error", "message": "任务已保存到队列，但调度器暂不可用"})
		return
	}
	c.JSON(http.StatusAccepted, run)
}

func (s *Server) handleListAutomaticTaskRuns(c *gin.Context) {
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "20"))
	offset, _ := strconv.Atoi(c.DefaultQuery("offset", "0"))
	runs, total, err := db.ListAutomaticTaskRuns(limit, offset)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"status": "error", "message": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"runs": runs, "total": total})
}
