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

// 顶栏源路径搜索：QSelect + use-input + filter 实现自动补全，
// 选中候选或回车/搜索图标跳转任务页按源路径筛选。
const searchText = ref('')
const searchSelection = ref<string | null>(null)
const searchOptions = ref<string[]>([])
const searchLoading = ref(false)

// use-input 输入时触发：防抖由 QSelect 的 input-debounce 负责，这里拉取候选并写入 options
function onFilter(val: string, update: (cb: () => void) => void, abort: () => void) {
  const q = val.trim()
  if (!q) {
    update(() => {
      searchOptions.value = []
    })
    return
  }
  searchLoading.value = true
  api.suggestSourcePaths(q, 10)
    .then((r) => {
      update(() => {
        searchOptions.value = r.items
      })
    })
    .catch(() => abort())
    .finally(() => {
      searchLoading.value = false
    })
}

// 同步输入框文本，供回车/搜索图标使用当前输入值
function onInputValue(val: string | number | null) {
  searchText.value = (val ?? '').toString()
}

// 执行搜索：跳转任务页，按源路径模糊筛选展示结果
function doSearch(value: string) {
  const v = value.trim()
  if (!v) return
  void router.push({ path: '/tasks', query: { source: v } })
}

// 从下拉候选中选择某项时触发
function onSelect(val: string | null) {
  if (val) doSearch(val)
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
          <q-select
            v-model="searchSelection"
            :options="searchOptions"
            use-input
            input-debounce="250"
            dense
            outlined
            rounded
            clearable
            placeholder="搜索源路径"
            class="search-box"
            :loading="searchLoading"
            @filter="onFilter"
            @input-value="onInputValue"
            @update:model-value="onSelect"
            @keyup.enter="doSearch(searchText)"
          >
            <template #no-option>
              <q-item>
                <q-item-section class="text-grey">无匹配源路径</q-item-section>
              </q-item>
            </template>
            <template #append>
              <q-icon name="search" class="cursor-pointer search-icon" @click="doSearch(searchText)" />
            </template>
          </q-select>
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
</style>
