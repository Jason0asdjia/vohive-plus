<script setup lang="ts">
import { computed, onMounted, onUnmounted, reactive, ref, watch } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { Add24Regular, Delete24Regular, Edit24Regular, Play24Regular } from '@vicons/fluent'
import { api } from '../stores/auth'
import { devicesService } from '../services/devices'
import { errorMessage } from '../services/http'
import {
  automaticTasksService,
  type AutomaticTask,
  type AutomaticTaskEnvironment,
  type AutomaticTaskInput,
  type AutomaticTaskRun,
  type AutomaticTaskType
} from '../services/automatic-tasks'
import type { DeviceMgmtListItem } from '../types/api'

type ProfileOption = { iccid: string; aid: string; label: string }
type RawProfile = { iccid?: string; nickname?: string; name?: string; state?: string }
type RawProfileGroup = { aid_hex?: string; profiles?: RawProfile[] }

const tasks = ref<AutomaticTask[]>([])
const runs = ref<AutomaticTaskRun[]>([])
const runsTotal = ref(0)
const runPage = ref(1)
const runPageSize = ref(20)
const devices = ref<DeviceMgmtListItem[]>([])
const profiles = ref<ProfileOption[]>([])
const loading = ref(true)
const profilesLoading = ref(false)
const editorOpen = ref(false)
const saving = ref(false)
const busyID = ref(0)
let pollTimer: number | undefined
let profileRequestSequence = 0

const today = () => {
  const d = new Date()
  return `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, '0')}-${String(d.getDate()).padStart(2, '0')}`
}

const defaultTime = () => {
  const d = new Date(Date.now() + 5 * 60_000)
  return `${String(d.getHours()).padStart(2, '0')}:${String(d.getMinutes()).padStart(2, '0')}`
}

const form = reactive({
  id: 0,
  name: '',
  enabled: true,
  device_id: '',
  profile_iccid: '',
  profile_aid: '',
  task_type: 'sms' as AutomaticTaskType,
  environment: 'cellular' as AutomaticTaskEnvironment,
  interval_days: 1,
  start_date: today(),
  run_time: defaultTime(),
  timezone: 'Asia/Shanghai',
  phone: '',
  message: '',
  duration_seconds: 30,
  retry_count: 1,
  notify: true
})

const deviceByID = computed(() => new Map(devices.value.map((item) => [item.id, item])))
const taskByID = computed(() => new Map(tasks.value.map((item) => [item.id, item])))

function resetForm(task?: AutomaticTask) {
  const firstDevice = task?.device_id || devices.value[0]?.id || ''
  Object.assign(form, {
    id: task?.id || 0,
    name: task?.name || '',
    enabled: task?.enabled ?? true,
    device_id: firstDevice,
    profile_iccid: task?.profile_iccid || '',
    profile_aid: task?.profile_aid || '',
    task_type: task?.task_type || 'sms',
    environment: task?.environment || 'cellular',
    interval_days: task?.interval_days || 1,
    start_date: task?.start_date || today(),
    run_time: task?.run_time || defaultTime(),
    timezone: task?.timezone || 'Asia/Shanghai',
    phone: task?.phone || '',
    message: task?.message || '',
    duration_seconds: task?.duration_seconds || 30,
    retry_count: task?.retry_count ?? 1,
    notify: task?.notify ?? true
  })
}

async function loadTasksAndDevices(initial = false) {
  if (initial) loading.value = true
  try {
    const [taskResult, deviceResponse] = await Promise.all([
      automaticTasksService.list(),
      api.get<{ devices?: DeviceMgmtListItem[] }>('/devices')
    ])
    if (!taskResult.ok) throw taskResult.error
    tasks.value = taskResult.data
    devices.value = deviceResponse.data?.devices || []
  } catch (e) {
    ElMessage.error(errorMessage(e, '加载自动任务失败'))
  } finally {
    loading.value = false
  }
}

async function loadRuns() {
  const result = await automaticTasksService.listRuns(runPageSize.value, (runPage.value - 1) * runPageSize.value)
  if (!result.ok) return
  runs.value = result.data.runs
  runsTotal.value = result.data.total
}

async function loadProfiles(deviceID: string, keepICCID = '') {
	const requestSequence = ++profileRequestSequence
  profilesLoading.value = true
  const options: ProfileOption[] = []
  const current = deviceByID.value.get(deviceID)?.modem?.iccid?.trim() || ''
  if (current) options.push({ iccid: current, aid: '', label: `当前 SIM · …${current.slice(-8)}` })
  const result = await devicesService.getEsimProfiles(deviceID, { refresh: true })
	if (requestSequence !== profileRequestSequence || form.device_id !== deviceID || !editorOpen.value) return
  if (result.ok) {
    for (const group of result.data as RawProfileGroup[]) {
      for (const profile of group.profiles || []) {
        const iccid = String(profile.iccid || '').trim()
        if (!iccid || options.some((item) => item.iccid === iccid)) continue
        const name = profile.nickname || profile.name || profile.state || 'eSIM Profile'
        options.push({ iccid, aid: String(group.aid_hex || ''), label: `${name} · …${iccid.slice(-8)}` })
      }
    }
  }
  profiles.value = options
  const selected = options.find((item) => item.iccid === keepICCID) || options[0]
  if (selected) {
    form.profile_iccid = selected.iccid
    form.profile_aid = selected.aid
  }
	if (requestSequence === profileRequestSequence) profilesLoading.value = false
}

async function openEditor(task?: AutomaticTask) {
  resetForm(task)
  editorOpen.value = true
  if (form.device_id) await loadProfiles(form.device_id, form.profile_iccid)
}

async function chooseDevice(deviceID: string) {
  form.device_id = deviceID
  form.profile_iccid = ''
  form.profile_aid = ''
  await loadProfiles(deviceID)
}

function chooseProfile(iccid: string) {
  const selected = profiles.value.find((item) => item.iccid === iccid)
  form.profile_iccid = iccid
  form.profile_aid = selected?.aid || ''
}

function payload(): AutomaticTaskInput {
  return {
    name: form.name.trim(), enabled: form.enabled, device_id: form.device_id,
    profile_iccid: form.profile_iccid, profile_aid: form.profile_aid,
    task_type: form.task_type, environment: form.environment,
    interval_days: Number(form.interval_days), start_date: form.start_date,
    run_time: form.run_time, timezone: form.timezone,
    phone: form.phone.trim(), message: form.message,
    duration_seconds: Number(form.duration_seconds), retry_count: Number(form.retry_count),
    notify: form.notify
  }
}

async function saveTask() {
  if (!form.name.trim() || !form.device_id || !form.profile_iccid || !form.phone.trim()) {
    ElMessage.warning('请填写任务名称、设备、SIM/eSIM Profile 和号码')
    return
  }
  if (form.task_type === 'sms' && !form.message.trim()) {
    ElMessage.warning('短信内容不能为空')
    return
  }
  saving.value = true
  const result = form.id
    ? await automaticTasksService.update(form.id, payload())
    : await automaticTasksService.create(payload())
  saving.value = false
  if (!result.ok) {
    ElMessage.error(result.error.message || '保存失败')
    return
  }
  editorOpen.value = false
  ElMessage.success('自动任务已保存')
  await loadTasksAndDevices()
}

async function toggleTask(task: AutomaticTask) {
  busyID.value = task.id
  const input: AutomaticTaskInput = {
    name: task.name, enabled: !task.enabled, device_id: task.device_id,
    profile_iccid: task.profile_iccid, profile_aid: task.profile_aid,
    task_type: task.task_type, environment: task.environment,
    interval_days: task.interval_days, start_date: task.start_date, run_time: task.run_time,
    timezone: task.timezone, phone: task.phone, message: task.message,
    duration_seconds: task.duration_seconds, retry_count: task.retry_count, notify: task.notify
  }
  const result = await automaticTasksService.update(task.id, input)
  busyID.value = 0
  if (!result.ok) ElMessage.error(result.error.message || '更新失败')
  await loadTasksAndDevices()
}

async function runNow(task: AutomaticTask) {
  busyID.value = task.id
  const result = await automaticTasksService.runNow(task.id)
  busyID.value = 0
  if (!result.ok) ElMessage.error(result.error.message || '加入执行队列失败')
  else ElMessage.success('任务已加入设备串行队列')
  await loadRuns()
}

async function removeTask(task: AutomaticTask) {
  const confirmed = await ElMessageBox.confirm(`确认删除任务“${task.name}”？`, '删除自动任务', { type: 'warning' })
    .then(() => true).catch(() => false)
  if (!confirmed) return
  const result = await automaticTasksService.remove(task.id)
  if (!result.ok) ElMessage.error(result.error.message || '删除失败')
  else ElMessage.success('任务已删除')
  await Promise.all([loadTasksAndDevices(), loadRuns()])
}

function formatDateTime(value?: string) {
  if (!value) return '--'
  const date = new Date(value)
  return Number.isNaN(date.getTime()) ? '--' : date.toLocaleString()
}

function statusText(status: string) {
  return ({ queued: '排队中', running: '执行中', success: '成功', failed: '失败' } as Record<string, string>)[status] || status || '--'
}

function statusType(status: string) {
  if (status === 'success') return 'success'
  if (status === 'failed') return 'danger'
  if (status === 'running') return 'warning'
  return 'info'
}

watch([runPage, runPageSize], () => void loadRuns())
watch(editorOpen, (open) => {
  if (!open) {
    profileRequestSequence++
    profilesLoading.value = false
  }
})

onMounted(async () => {
  await Promise.all([loadTasksAndDevices(true), loadRuns()])
  pollTimer = window.setInterval(() => {
    void loadTasksAndDevices()
    void loadRuns()
  }, 5000)
})

onUnmounted(() => {
  if (pollTimer) window.clearInterval(pollTimer)
})
</script>

<template>
  <div class="max-w-[1480px] mx-auto space-y-5">
    <div class="flex flex-col sm:flex-row sm:items-center justify-between gap-3">
      <div>
        <h1 class="text-2xl sm:text-3xl font-bold text-gray-900 dark:text-white">自动任务</h1>
        <p class="mt-1 text-sm text-gray-500">按周期使用指定 SIM 卡或切换到指定 eSIM Profile，并在设备串行队列中执行短信或通话任务</p>
      </div>
      <el-button type="primary" :disabled="!devices.length" @click="openEditor()">
        <el-icon><Add24Regular /></el-icon><span class="ml-1">添加任务</span>
      </el-button>
    </div>

    <div class="ui-panel-muted overflow-x-auto" v-loading="loading">
      <table class="w-full min-w-[1080px] text-sm">
        <thead class="text-left text-xs text-gray-500 border-b border-gray-200 dark:border-white/10">
          <tr>
            <th class="px-4 py-3">任务</th><th class="px-4 py-3">设备 / SIM / PROFILE</th>
            <th class="px-4 py-3">类型</th><th class="px-4 py-3">执行环境</th>
            <th class="px-4 py-3">周期</th><th class="px-4 py-3">下次执行</th>
            <th class="px-4 py-3">上次结果</th><th class="px-4 py-3 text-right">操作</th>
          </tr>
        </thead>
        <tbody class="divide-y divide-gray-100 dark:divide-white/5">
          <tr v-for="task in tasks" :key="task.id" class="hover:bg-sky-50/40 dark:hover:bg-white/[0.03]">
            <td class="px-4 py-3">
              <div class="flex items-center gap-2"><el-switch :model-value="task.enabled" :loading="busyID === task.id" @change="toggleTask(task)" /><b>{{ task.name }}</b></div>
              <div class="text-xs text-gray-400 mt-1">{{ task.notify ? '完成后推送通知' : '不推送通知' }} · 失败重试 {{ task.retry_count }} 次</div>
            </td>
            <td class="px-4 py-3"><div>{{ deviceByID.get(task.device_id)?.name || task.device_id }}</div><div class="text-xs font-mono text-gray-400 mt-1">…{{ task.profile_iccid.slice(-8) }}</div></td>
            <td class="px-4 py-3">{{ task.task_type === 'sms' ? '发送短信' : '拨打电话' }}</td>
            <td class="px-4 py-3"><el-tag effect="plain" :type="task.environment === 'vowifi' ? 'primary' : 'warning'">{{ task.environment === 'vowifi' ? 'VoWiFi' : '基站直连' }}</el-tag></td>
            <td class="px-4 py-3">每 {{ task.interval_days }} 天 · {{ task.run_time }}</td>
            <td class="px-4 py-3">{{ formatDateTime(task.next_run_at) }}</td>
            <td class="px-4 py-3"><el-tag v-if="task.last_status" :type="statusType(task.last_status)">{{ statusText(task.last_status) }}</el-tag><span v-else>--</span></td>
            <td class="px-4 py-3"><div class="flex justify-end gap-2">
              <el-button size="small" :loading="busyID === task.id" @click="runNow(task)"><el-icon><Play24Regular /></el-icon><span class="ml-1">立即执行</span></el-button>
              <el-button size="small" @click="openEditor(task)"><el-icon><Edit24Regular /></el-icon><span class="ml-1">编辑</span></el-button>
              <el-button size="small" type="danger" plain @click="removeTask(task)"><el-icon><Delete24Regular /></el-icon><span class="ml-1">删除</span></el-button>
            </div></td>
          </tr>
          <tr v-if="!tasks.length && !loading"><td colspan="8" class="px-4 py-12 text-center text-gray-400">暂无自动任务</td></tr>
        </tbody>
      </table>
    </div>

    <div class="ui-panel-muted overflow-hidden">
      <div class="px-4 py-4 font-bold border-b border-gray-200 dark:border-white/10">最近执行记录</div>
      <div class="overflow-x-auto"><table class="w-full min-w-[850px] text-sm">
        <thead class="text-left text-xs text-gray-500 border-b border-gray-200 dark:border-white/10"><tr><th class="px-4 py-3">任务</th><th class="px-4 py-3">设备</th><th class="px-4 py-3">状态</th><th class="px-4 py-3">排队时间</th><th class="px-4 py-3">尝试次数</th><th class="px-4 py-3">结果</th></tr></thead>
        <tbody class="divide-y divide-gray-100 dark:divide-white/5"><tr v-for="run in runs" :key="run.id"><td class="px-4 py-3">{{ taskByID.get(run.task_id)?.name || run.task_name }}</td><td class="px-4 py-3">{{ deviceByID.get(run.device_id)?.name || run.device_id }}</td><td class="px-4 py-3"><el-tag :type="statusType(run.status)">{{ statusText(run.status) }}</el-tag></td><td class="px-4 py-3">{{ formatDateTime(run.scheduled_at) }}</td><td class="px-4 py-3">{{ run.attempts }}</td><td class="px-4 py-3 max-w-md truncate" :title="run.error || run.output">{{ run.error || run.output || '--' }}</td></tr><tr v-if="!runs.length"><td colspan="6" class="px-4 py-10 text-center text-gray-400">暂无执行记录</td></tr></tbody>
      </table></div>
      <div class="p-4 flex justify-end"><el-pagination v-model:current-page="runPage" v-model:page-size="runPageSize" :total="runsTotal" :page-sizes="[10,20,50]" layout="total, sizes, prev, pager, next" /></div>
    </div>

    <el-dialog v-model="editorOpen" :title="form.id ? '编辑自动任务' : '添加自动任务'" width="min(720px, 94vw)" destroy-on-close>
      <el-form label-position="top" class="grid grid-cols-1 sm:grid-cols-2 gap-x-4">
        <el-form-item label="任务名称"><el-input v-model="form.name" maxlength="128" /></el-form-item>
        <el-form-item label="启用任务"><el-switch v-model="form.enabled" /></el-form-item>
        <el-form-item label="设备"><el-select :model-value="form.device_id" class="w-full" @change="chooseDevice"><el-option v-for="d in devices" :key="d.id" :label="d.name || d.id" :value="d.id" /></el-select></el-form-item>
        <el-form-item label="SIM / eSIM Profile"><el-select :model-value="form.profile_iccid" class="w-full" :loading="profilesLoading" @change="chooseProfile"><el-option v-for="p in profiles" :key="p.iccid" :label="p.label" :value="p.iccid" /></el-select></el-form-item>
        <el-form-item label="任务类型"><el-select v-model="form.task_type" class="w-full"><el-option label="发送短信" value="sms" /><el-option label="拨打电话并自动挂断" value="call" /></el-select></el-form-item>
        <el-form-item label="执行环境"><el-select v-model="form.environment" class="w-full"><el-option label="VoWiFi" value="vowifi" /><el-option label="基站直连" value="cellular" /></el-select></el-form-item>
        <el-form-item label="接收号码"><el-input v-model="form.phone" placeholder="例如 +8613800138000" /></el-form-item>
        <el-form-item v-if="form.task_type === 'call'" label="通话保持秒数"><el-input-number v-model="form.duration_seconds" :min="1" :max="300" class="!w-full" /></el-form-item>
        <el-form-item v-if="form.task_type === 'sms'" label="短信内容" class="sm:col-span-2"><el-input v-model="form.message" type="textarea" :rows="3" maxlength="1000" show-word-limit /></el-form-item>
        <el-form-item label="开始日期"><el-input v-model="form.start_date" type="date" /></el-form-item>
        <el-form-item label="执行时间"><el-input v-model="form.run_time" type="time" /></el-form-item>
        <el-form-item label="周期（天）"><el-input-number v-model="form.interval_days" :min="1" :max="3650" class="!w-full" /></el-form-item>
        <el-form-item label="失败重试次数"><el-input-number v-model="form.retry_count" :min="0" :max="5" class="!w-full" /></el-form-item>
        <el-form-item label="完成后通知"><el-switch v-model="form.notify" /></el-form-item>
      </el-form>
      <template #footer><el-button @click="editorOpen = false">取消</el-button><el-button type="primary" :loading="saving" @click="saveTask">保存</el-button></template>
    </el-dialog>
  </div>
</template>
