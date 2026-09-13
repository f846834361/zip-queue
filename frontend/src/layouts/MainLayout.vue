<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { useQuasar } from 'quasar'
import { useUiStore } from '../stores/ui'
import { useBrowseStore } from '../stores/browse'
import api from '../api'

const $q = useQuasar()

const ui = useUiStore()
const browse = useBrowseStore()
// 首次进入且无持久化记录时，按平台默认（桌面打开 / 移动端收起）
ui.initDrawer($q.platform.is.desktop)
const leftDrawerOpen = computed({
  get: () => ui.leftDrawerOpen ?? $q.platform.is.desktop,
  set: (open: boolean) => ui.setLeftDrawerOpen(open)
})

const links = computed(() => [
  { to: '/', label: '文件浏览', icon: 'folder_open' },
  { to: '/tasks', label: '任务列表', icon: 'list_alt' },
  { to: '/config', label: '配置', icon: 'settings' }
])

onMounted(async () => {
  try {
    await browse.ensureConfig()
  } catch (e) {
    $q.notify({ type: 'negative', message: (e as Error).message })
  }
  // 顶栏后台任务状态：每 5 秒轮询轻量接口，仅判断是否有活跃任务
  await refreshTaskStatus()
  statusTimer = setInterval(refreshTaskStatus, 5000)
})

onUnmounted(() => {
  if (statusTimer !== null) clearInterval(statusTimer)
})

// 后台是否活跃（执行中或排队中），仅用于顶栏展示，不展示具体任务明细
const taskActive = ref(false)
let statusTimer: ReturnType<typeof setInterval> | null = null
async function refreshTaskStatus() {
  try {
    const r = await api.activeTasks()
    taskActive.value = r.active
  } catch {
    // 状态轮询失败静默忽略，不影响浏览与任务操作
  }
}
</script>

<template>
  <q-layout view="hHh lpR fff">
    <q-header elevated class="bg-primary text-white">
      <q-toolbar>
        <q-btn dense flat round icon="menu" @click="leftDrawerOpen = !leftDrawerOpen" />
        <q-toolbar-title class="text-weight-medium">
          <!-- 点击 logo 回到文件浏览页；用 router-link 保留可访问性与右键/中键打开能力 -->
          <router-link :to="{ name: 'files' }" class="logo-link text-white">
            <q-icon name="archive" size="24px" class="q-mr-sm" />
            Zip-Queue
          </router-link>
        </q-toolbar-title>
        <q-chip
          dense
          square
          :color="taskActive ? 'green' : 'white'"
          :text-color="taskActive ? 'white' : 'primary'"
          class="q-mr-none"
        >
          <q-icon :name="taskActive ? 'sync' : 'check_circle'" size="xs" class="q-mr-xs" />
          后台任务 · {{ taskActive ? '活跃' : '空闲' }}
          <q-tooltip>{{ taskActive ? '有任务正在执行或排队中' : '当前无进行中的任务' }}</q-tooltip>
        </q-chip>
      </q-toolbar>
    </q-header>

    <q-drawer v-model="leftDrawerOpen" show-if-above bordered :width="220">
      <q-list padding>
        <q-item-label header>导航</q-item-label>
        <q-item
          v-for="link in links"
          :key="link.to"
          :to="link.to"
          clickable
          v-ripple
          active-class="text-primary"
          exact
        >
          <q-item-section avatar>
            <q-icon :name="link.icon" />
          </q-item-section>
          <q-item-section>{{ link.label }}</q-item-section>
        </q-item>
      </q-list>
    </q-drawer>

    <q-page-container>
      <router-view />
    </q-page-container>
  </q-layout>
</template>

<style scoped>
.logo-link {
  text-decoration: none;
  cursor: pointer;
}
.logo-link:hover {
  opacity: 0.85;
}
</style>
