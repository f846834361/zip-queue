<script setup lang="ts">
import { computed, onMounted } from 'vue'
import { useQuasar } from 'quasar'
import { useUiStore } from '../stores/ui'
import { useBrowseStore } from '../stores/browse'
import { compressionLabel } from '../utils/task'

const $q = useQuasar()

const ui = useUiStore()
const browse = useBrowseStore()
// 首次进入且无持久化记录时，按平台默认（桌面打开 / 移动端收起）
ui.initDrawer($q.platform.is.desktop)
const leftDrawerOpen = computed({
  get: () => ui.leftDrawerOpen ?? $q.platform.is.desktop,
  set: (open: boolean) => ui.setLeftDrawerOpen(open)
})

// 配置由 browse store 缓存，与文件页共享同一次请求
const config = computed(() => browse.config)

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
})
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
        <template v-if="config">
          <q-chip dense square color="white" text-color="primary" class="q-mr-none">
            压缩效率 {{ compressionLabel(config.compression_level) }}
            <q-tooltip>当前压缩效率，可在配置页修改</q-tooltip>
          </q-chip>
        </template>
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
