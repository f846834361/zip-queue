/** 任务状态/类型展示映射（TasksPage 与 TaskDetailPage 共用）。 */

/** 运行中任务的轮询刷新间隔（毫秒）。 */
export const POLL_INTERVAL = 2000

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
