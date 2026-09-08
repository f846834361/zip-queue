import { defineStore } from 'pinia'
import { ref } from 'vue'
import api, { type AppConfig } from '../api'

export const useBrowseStore = defineStore('browse', () => {
  const currentPath = ref('')
  const config = ref<AppConfig | null>(null)
  let configPromise: Promise<AppConfig> | null = null

  function setPath(path: string) {
    currentPath.value = path
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
  }

  return { currentPath, config, setPath, ensureConfig, setConfig, reset }
})
