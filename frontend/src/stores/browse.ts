import { defineStore } from 'pinia'
import { ref } from 'vue'

export const useBrowseStore = defineStore('browse', () => {
  const currentPath = ref('')

  function setPath(path: string) {
    currentPath.value = path
  }

  function reset() {
    currentPath.value = ''
  }

  return { currentPath, setPath, reset }
})
