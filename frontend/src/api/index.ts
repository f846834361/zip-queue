import axios from 'axios'

const http = axios.create({
  baseURL: '/api',
  timeout: 30000
})

http.interceptors.response.use(
  (resp) => resp,
  (err) => {
    const msg = err?.response?.data?.error || err?.message || '请求失败'
    return Promise.reject(new Error(msg))
  }
)

export interface FsEntry {
  name: string
  path: string
  is_dir: boolean
  is_archive: boolean
  size: number
  mod_time: string
}

export interface ListDirResponse {
  path: string
  entries: FsEntry[]
  /** 所列出目录的最后修改时间（unix 秒），供轮询对比是否需要刷新 */
  modified: number
}

export interface FileStatusResponse {
  /** 目录最后修改时间（unix 秒）；为 0 表示路径不存在/无效 */
  modified: number
}

export interface Task {
  id: number
  type: 'decompress' | 'compress'
  source_path: string
  target_path: string
  temp_path: string
  status: 'pending' | 'running' | 'succeeded' | 'failed'
  progress_percent: number
  processed_bytes: number
  total_bytes: number
  error: string
  requeue_count: number
  /** 压缩任务执行时实际使用的效率档位；解压任务及历史记录为 null/缺省。 */
  compression_level?: 'fastest' | 'fast' | 'normal' | 'slow' | null
  created_at: string
  started_at: string | null
  completed_at: string | null
}

export interface RetryTaskResponse {
  id: number
  requeue_count: number
}

export interface TaskListResponse {
  items: Task[]
  total: number
  page: number
  page_size: number
}

export interface AppConfig {
  max_concurrent_tasks: number
  default_browse_path: string
  penetrate_subfolders: boolean
  /** 压缩效率：fastest 特快（仅打包）/ fast 快 / normal 中 / slow 慢 */
  compression_level: 'fastest' | 'fast' | 'normal' | 'slow'
}

/** 可在配置页修改、提交到后端保存的字段（均为可选，只传需要变更的项）。 */
export interface UpdateConfigBody {
  penetrate_subfolders?: boolean
  max_concurrent_tasks?: number
  compression_level?: AppConfig['compression_level']
}

export interface BulkResponse {
  created: number
  ids: number[]
  // 与已有进行中任务同源被跳过的数量
  skipped?: number
}

export interface BatchCreateResponse extends BulkResponse {
  skipped: number
}

export interface WakeResponse {
  ok: boolean
  /** 当前待处理任务数 */
  pending: number
  /** 当前执行中任务数 */
  running: number
}

export interface Password {
  id: number
  value: string
  sort_order: number
  note: string
  enabled: boolean
  created_at: string
  updated_at: string
}

export const api = {
  async getConfig(): Promise<AppConfig> {
    return (await http.get<AppConfig>('/config')).data
  },
  async updateConfig(body: UpdateConfigBody): Promise<AppConfig> {
    return (await http.put<AppConfig>('/config', body)).data
  },
  async listDir(path?: string): Promise<ListDirResponse> {
    const params = path ? { path } : {}
    return (await http.get<ListDirResponse>('/fs/list', { params })).data
  },
  async getFileStatus(path: string): Promise<FileStatusResponse> {
    return (await http.get<FileStatusResponse>('/file/status', { params: { path } })).data
  },
  async createTask(type: 'decompress' | 'compress', path: string): Promise<Task> {
    return (await http.post<Task>('/tasks', { type, path })).data
  },
  async bulkDecompress(path: string, recursive = false): Promise<BulkResponse> {
    return (await http.post<BulkResponse>('/tasks/bulk-decompress-folder', {
      path,
      recursive
    })).data
  },
  async bulkCompress(path: string, recursive = false): Promise<BulkResponse> {
    return (await http.post<BulkResponse>('/tasks/bulk-compress-folder', {
      path,
      recursive
    })).data
  },
  async createTasksBatch(type: 'decompress' | 'compress', paths: string[]): Promise<BatchCreateResponse> {
    return (await http.post<BatchCreateResponse>('/tasks/batch', { type, paths })).data
  },
  async wakeTasks(): Promise<WakeResponse> {
    return (await http.post<WakeResponse>('/tasks/wake')).data
  },
  async listTasks(params: Record<string, string | number>): Promise<TaskListResponse> {
    return (await http.get<TaskListResponse>('/tasks', { params })).data
  },
  async getTask(id: number | string): Promise<Task> {
    return (await http.get<Task>(`/tasks/${id}`)).data
  },
  async deleteTask(id: number | string): Promise<void> {
    await http.delete(`/tasks/${id}`)
  },
  async retryTask(id: number | string): Promise<RetryTaskResponse> {
    return (await http.post<RetryTaskResponse>(`/tasks/${id}/retry`)).data
  },
  async listPasswords(): Promise<{ items: Password[]; total: number }> {
    return (await http.get('/passwords')).data
  },
  async createPassword(value: string, note?: string): Promise<Password> {
    return (await http.post<Password>('/passwords', { value, note })).data
  },
  async updatePassword(id: number, value: string, note?: string): Promise<Password> {
    return (await http.patch<Password>(`/passwords/${id}`, { value, note })).data
  },
  async setPasswordEnabled(id: number, enabled: boolean): Promise<Password> {
    return (await http.patch<Password>(`/passwords/${id}/enabled`, { enabled })).data
  },
  async deletePassword(id: number): Promise<void> {
    await http.delete(`/passwords/${id}`)
  },
  async reorderPassword(id: number, direction: -1 | 1): Promise<void> {
    await http.post('/passwords/reorder', { id, direction })
  }
}

export default api
