/** 任务状态/类型展示映射（TasksPage 与 TaskDetailPage 共用）。 */

/** 运行中任务的轮询刷新间隔（毫秒）。 */
export const POLL_INTERVAL = 2000

/** 压缩效率档位 → 中文名（配置页下拉框与顶栏展示共用，保证文案一致）。 */
export const COMPRESSION_LABELS = {
  fastest: '特快',
  fast: '快',
  normal: '中',
  slow: '慢'
} as const

export type CompressionLevel = keyof typeof COMPRESSION_LABELS

/** 压缩效率中文名；未知取值回落"中"（与后端默认档位一致）。 */
export function compressionLabel(level: string | null | undefined): string {
  if (!level) return COMPRESSION_LABELS.normal
  return COMPRESSION_LABELS[level as CompressionLevel] ?? COMPRESSION_LABELS.normal
}

export function statusColor(status: string): string {
  switch (status) {
    case 'pending': return 'grey-6'
    case 'running': return 'blue-7'
    case 'succeeded': return 'green-7'
    case 'failed': return 'red-7'
    default: return 'grey-6'
  }
}

export function statusLabel(status: string): string {
  switch (status) {
    case 'pending': return '待处理'
    case 'running': return '执行中'
    case 'succeeded': return '成功'
    case 'failed': return '失败'
    default: return status
  }
}

export function typeLabel(type: string): string {
  return type === 'decompress' ? '解压' : '压缩'
}
