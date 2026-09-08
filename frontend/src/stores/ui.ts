import { defineStore } from 'pinia'
import { ref } from 'vue'

const STORAGE_KEY = 'zip-queue.drawer-open'

export const useUiStore = defineStore('ui', () => {
  // null 表示此前没有持久化记录，首次渲染由调用方通过 initDrawer 按平台给出默认值
  const stored = localStorage.getItem(STORAGE_KEY)
  const leftDrawerOpen = ref<boolean | null>(stored === null ? null : stored === '1')

  function initDrawer(defaultOpen: boolean) {
    if (leftDrawerOpen.value === null) {
      leftDrawerOpen.value = defaultOpen
    }
  }

  function setLeftDrawerOpen(open: boolean) {
    leftDrawerOpen.value = open
    localStorage.setItem(STORAGE_KEY, open ? '1' : '0')
  }

  function toggleLeftDrawer() {
    setLeftDrawerOpen(!leftDrawerOpen.value)
  }

  return { leftDrawerOpen, initDrawer, setLeftDrawerOpen, toggleLeftDrawer }
})
