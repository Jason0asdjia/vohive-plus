import { api } from '../stores/auth'
import { callService } from './http'

export type AutomaticTaskType = 'sms' | 'call'
export type AutomaticTaskEnvironment = 'vowifi' | 'cellular'

export type AutomaticTask = {
  id: number
  name: string
  enabled: boolean
  device_id: string
  profile_iccid: string
  profile_aid: string
  task_type: AutomaticTaskType
  environment: AutomaticTaskEnvironment
  interval_days: number
  start_date: string
  run_time: string
  timezone: string
  phone: string
  message: string
  duration_seconds: number
  retry_count: number
  notify: boolean
  next_run_at?: string
  last_run_at?: string
  last_status: string
  last_error: string
}

export type AutomaticTaskRun = {
  id: number
  task_id: number
  task_name: string
  device_id: string
  scheduled_at: string
  started_at?: string
  finished_at?: string
  status: 'queued' | 'running' | 'success' | 'failed'
  attempts: number
  output: string
  error: string
}

export type AutomaticTaskInput = Omit<AutomaticTask, 'id' | 'next_run_at' | 'last_run_at' | 'last_status' | 'last_error'>

export const automaticTasksService = {
  list() {
    return callService(async () => {
      const res = await api.get<{ tasks?: AutomaticTask[] }>('/automatic-tasks')
      return res.data?.tasks || []
    })
  },
  create(payload: AutomaticTaskInput) {
    return callService(async () => {
      const res = await api.post<AutomaticTask>('/automatic-tasks', payload)
      return res.data
    })
  },
  update(id: number, payload: AutomaticTaskInput) {
    return callService(async () => {
      const res = await api.put<AutomaticTask>(`/automatic-tasks/${id}`, payload)
      return res.data
    })
  },
  remove(id: number) {
    return callService(async () => {
      await api.delete(`/automatic-tasks/${id}`)
      return true
    })
  },
  runNow(id: number) {
    return callService(async () => {
      const res = await api.post<AutomaticTaskRun>(`/automatic-tasks/${id}/actions/run`)
      return res.data
    })
  },
  listRuns(limit: number, offset: number) {
    return callService(async () => {
      const res = await api.get<{ runs?: AutomaticTaskRun[]; total?: number }>('/automatic-task-runs', { params: { limit, offset } })
      return { runs: res.data?.runs || [], total: Number(res.data?.total || 0) }
    })
  }
}
