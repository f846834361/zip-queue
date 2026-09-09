import { defineStore } from 'pinia'
import { ref } from 'vue'
import api, { type AppConfig } from '../api'

// 浏览路径持久化键：刷新/关闭页面后仍可恢复上次浏览的目录（按浏览器隔离，不落服务端）。
const PATH_STORAGE_KEY = 'zip-queue.browse-path'

export const useBrowseStore = defineStore('browse', () => {
  const currentPath = ref(localStorage.getItem(PATH_STORAGE_KEY) ?? '')
  const config = ref<AppConfig | null>(null)
  let configPromise: Promise<AppConfig> | null = null

  function setPath(path: string) {
    currentPath.value = path
    localStorage.setItem(PATH_STORAGE_KEY, path)
  }

  /** 获取全局配置：结果缓存，并发调用共享同一次请求。 */
  function ensureConfig(): Promise<AppConfig> {
    if (config.value) return Promise.resolve(config.value)
    if (!configPromise) {
      configPromise = api
        .getConfig()
        .then((c) => {
          config.value = c
          return c
        })
        .finally(() => {
          configPromise = null
        })
    }
    return configPromise
  }

  /** 用新配置更新本地缓存（保存配置后调用）。 */
  function setConfig(c: AppConfig) {
    config.value = c
  }

  function reset() {
    currentPath.value = ''
    localStorage.removeItem(PATH_STORAGE_KEY)
  }

  return { currentPath, config, setPath, ensureConfig, setConfig, reset }
})
