import { defineStore } from 'pinia'
import { ref } from 'vue'
import api, { type Task, type TaskListResponse } from '../api'

export const useTasksStore = defineStore('tasks', () => {
  const items = ref<Task[]>([])
  const total = ref(0)
  const page = ref(1)
  const pageSize = ref(20)
  const loading = ref(false)

  async function fetchList(): Promise<void> {
    loading.value = true
    try {
      const resp: TaskListResponse = await api.listTasks({
        page: page.value,
        page_size: pageSize.value
      })
      items.value = resp.items
      total.value = resp.total
    } finally {
      loading.value = false
    }
  }

  function reset() {
    items.value = []
    total.value = 0
    page.value = 1
  }

  return { items, total, page, pageSize, loading, fetchList, reset }
})
