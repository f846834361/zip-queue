import { onBeforeUnmount } from 'vue'

/**
 * 定时轮询 composable：start/stop 显式控制，组件卸载时自动清理，
 * 供任务详情页与任务列表页共用（运行中任务每 POLL_INTERVAL 刷新一次）。
 */
export function usePolling(fn: () => void | Promise<void>, intervalMs = 2000) {
  let timer: ReturnType<typeof setInterval> | null = null

  function stop() {
    if (timer) {
      clearInterval(timer)
      timer = null
    }
  }

  function start() {
    stop()
    timer = setInterval(() => {
      void fn()
    }, intervalMs)
  }

  onBeforeUnmount(stop)
  return { start, stop }
}
