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
  type: 'decompress' | 'compress' | 'dedup'
  source_path: string
  target_path: string
  temp_path: string
  status: 'pending' | 'running' | 'succeeded' | 'failed' | 'cancelled'
  progress_percent: number
  processed_bytes: number
  total_bytes: number
  error: string
  requeue_count: number
  /** 查重任务的输入路径集合（JSON 字符串数组）；其他类型为空。 */
  sources?: string
  /** 查重任务的结论（JSON，见 DedupResult）；其他类型为空。 */
  result?: string
  /** 压缩任务执行时实际使用的效率档位；解压任务及历史记录为 null/缺省。 */
  compression_level?: 'fastest' | 'fast' | 'normal' | 'slow' | null
  created_at: string
  started_at: string | null
  completed_at: string | null
}

/**
 * 一组内容相同的条目（文件或文件夹）。
 * 同组条目几乎总在同一个深层目录下，因此只保存一次公共前缀，
 * 其余为前缀之后的文件名，避免超长路径被重复 N 遍。
 */
export interface DedupGroup {
  /** 单个条目的大小（文件夹组为其包含文件的总大小）。 */
  size: number
  /** 文件组的内容哈希；文件夹组缺省。 */
  hash?: string
  /** 该组全部条目的公共目录前缀（以分隔符结尾）；无公共目录时为空串。 */
  prefix: string
  /** 在 prefix 之下按子目录拆分的条目桶，长目录只出现一次。 */
  buckets: DedupBucket[]
}

/** 同一子目录下的一组互为副本的条目。 */
export interface DedupBucket {
  /** 相对所属分组 prefix 之后的子目录（以分隔符结尾）；就在分组根目录下时为空串。 */
  prefix: string
  /** 该子目录下的条目名（路径末段），按名称排序。 */
  names: string[]
}

/** 查重任务（dedup）的结论，后端以 JSON 存入 Task.result。 */
export interface DedupResult {
  duplicate_files: DedupGroup[]
  duplicate_folders: DedupGroup[]
  /** 删除重复副本后可节省的空间（字节）。 */
  wasted_bytes: number
  total_files: number
  total_folders: number
  /** 本次实际从磁盘读取的字节数（采样 + 全量哈希）。 */
  checked_bytes: number
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
  /** 压缩时是否去掉顶层文件夹：true 则 zip 内不保留选中文件夹这一层（修复前逻辑）；false 保留（当前默认逻辑）。 */
  strip_folder?: boolean
  /** 解压"智能添加文件夹"模式：one=1个 / multiple=多个 / none=无 */
  add_folder_mode?: 'one' | 'multiple' | 'none'
  /** 压缩效率：fastest 特快（仅打包）/ fast 快 / normal 中 / slow 慢 */
  compression_level: 'fastest' | 'fast' | 'normal' | 'slow'
  /** 压缩时跳过已压缩文件（zip/7z/jpg/mp4/pdf 等），直接排除不写入压缩包，避免二次压缩 */
  skip_compressed?: boolean
}

/** 可在配置页修改、提交到后端保存的字段（均为可选，只传需要变更的项）。 */
export interface UpdateConfigBody {
  penetrate_subfolders?: boolean
  max_concurrent_tasks?: number
  compression_level?: AppConfig['compression_level']
  strip_folder?: boolean
  add_folder_mode?: AppConfig['add_folder_mode']
  skip_compressed?: boolean
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

export interface ActiveResponse {
  /** 当前执行中任务数 */
  running: number
  /** 当前排队中任务数 */
  pending: number
  /** 是否存在活跃（执行中或排队中）任务，供顶栏展示后台繁忙状态 */
  active: boolean
}

export interface SuggestResponse {
  /** 模糊匹配的源路径候选（去重） */
  items: string[]
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

export interface PasswordListResponse {
  items: Password[]
  total: number
  page: number
  page_size: number
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
  /** 勾选多条路径后创建一条查重任务（只读扫描，不改动任何文件）。 */
  async createDedupTask(paths: string[]): Promise<BulkResponse> {
    return (await http.post<BulkResponse>('/tasks/dedup', { paths })).data
  },
  async wakeTasks(): Promise<WakeResponse> {
    return (await http.post<WakeResponse>('/tasks/wake')).data
  },
  async listTasks(params: Record<string, string | number>): Promise<TaskListResponse> {
    return (await http.get<TaskListResponse>('/tasks', { params })).data
  },
  async activeTasks(): Promise<ActiveResponse> {
    return (await http.get<ActiveResponse>('/tasks/active')).data
  },
  async suggestSourcePaths(q: string, limit = 10): Promise<SuggestResponse> {
    return (await http.get<SuggestResponse>('/tasks/source-suggest', { params: { q, limit } })).data
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
  async cancelTask(id: number | string): Promise<void> {
    await http.post(`/tasks/${id}/cancel`)
  },
  async listPasswords(
    params: { page?: number; page_size?: number; sort_by?: string; desc?: boolean } = {}
  ): Promise<PasswordListResponse> {
    return (await http.get<PasswordListResponse>('/passwords', { params })).data
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
  async setAllPasswordsEnabled(enabled: boolean): Promise<void> {
    await http.post('/passwords/enabled', { enabled })
  },
  async deletePassword(id: number): Promise<void> {
    await http.delete(`/passwords/${id}`)
  },
  async reorderPassword(id: number, targetId: number): Promise<void> {
    await http.post('/passwords/reorder', { id, target_id: targetId })
  }
}

export default api
