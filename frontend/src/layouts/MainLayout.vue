<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { useQuasar } from 'quasar'
import { useRouter } from 'vue-router'
import { useUiStore } from '../stores/ui'
import { useBrowseStore } from '../stores/browse'
import api from '../api'

const $q = useQuasar()
const router = useRouter()

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

// 顶栏源路径搜索：输入时模糊查询候选（自动补全），点击候选或回车/搜索图标跳转任务页筛选。
const searchText = ref('')
const suggestions = ref<string[]>([])
const searchLoading = ref(false)
const showSuggest = ref(false)
let suggestTimer: ReturnType<typeof setTimeout> | undefined

function onSearchInput(val: string | number | null) {
  showSuggest.value = true
  if (suggestTimer) clearTimeout(suggestTimer)
  const q = (val ?? '').toString().trim()
  if (!q) {
    suggestions.value = []
    return
  }
  // 输入防抖 250ms，避免每次按键都请求候选接口
  suggestTimer = setTimeout(() => void loadSuggest(q), 250)
}

async function loadSuggest(q: string) {
  searchLoading.value = true
  try {
    const r = await api.suggestSourcePaths(q, 10)
    suggestions.value = r.items
  } catch {
    suggestions.value = []
  } finally {
    searchLoading.value = false
  }
}

// 执行搜索：跳转任务页，按其源路径模糊筛选展示结果（不展示具体任务明细，只过滤列表）
function doSearch(value: string) {
  const v = value.trim()
  if (!v) return
  showSuggest.value = false
  suggestions.value = []
  void router.push({ path: '/tasks', query: { source: v } })
}

function closeSuggest() {
  // 延迟关闭，留出点击候选的时间差，避免面板先消失导致点击不到
  setTimeout(() => {
    showSuggest.value = false
  }, 150)
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

        <q-space />

        <div class="search-wrap q-mx-md">
          <q-input
            v-model="searchText"
            type="search"
            dense
            outlined
            rounded
            clearable
            placeholder="搜索源路径"
            class="search-box"
            :loading="searchLoading"
            @update:model-value="onSearchInput"
            @keyup.enter="doSearch(searchText)"
            @blur="closeSuggest"
          >
            <template #append>
              <q-icon name="search" class="cursor-pointer search-icon" @click="doSearch(searchText)" />
            </template>
          </q-input>
          <q-list
            v-if="showSuggest && suggestions.length"
            bordered
            separator
            class="suggest-panel"
          >
            <q-item
              v-for="s in suggestions"
              :key="s"
              clickable
              v-ripple
              @click="doSearch(s)"
              @mousedown.prevent
            >
              <q-item-section>
                <span class="mono text-caption text-break-all">{{ s }}</span>
              </q-item-section>
            </q-item>
          </q-list>
        </div>

        <q-space />

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
.search-wrap {
  position: relative;
  width: 440px;
  max-width: 50vw;
}
.search-box :deep(.q-field__control) {
  background: #ffffff;
}
.search-box :deep(.q-field__native),
.search-box :deep(.q-field__prefix),
.search-box :deep(.q-field__suffix) {
  color: #1d1d1d;
}
.search-box :deep(.q-field__label) {
  color: rgba(0, 0, 0, 0.6);
}
.search-icon {
  color: #757575;
}
.suggest-panel {
  position: absolute;
  top: calc(100% + 4px);
  left: 0;
  right: 0;
  z-index: 5000;
  max-height: 320px;
  overflow: auto;
  background: #fff;
  color: #1d1d1d;
  border-radius: 4px;
}
</style>
