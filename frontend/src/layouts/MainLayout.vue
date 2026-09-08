<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useQuasar } from 'quasar'
import api, { type AppConfig } from '../api'
import { useUiStore } from '../stores/ui'

const $q = useQuasar()

const ui = useUiStore()
// 首次进入且无持久化记录时，按平台默认（桌面打开 / 移动端收起）
ui.initDrawer($q.platform.is.desktop)
const leftDrawerOpen = computed({
  get: () => ui.leftDrawerOpen ?? $q.platform.is.desktop,
  set: (open: boolean) => ui.setLeftDrawerOpen(open)
})

const config = ref<AppConfig | null>(null)

const links = computed(() => [
  { to: '/', label: '文件浏览', icon: 'folder_open' },
  { to: '/tasks', label: '任务列表', icon: 'list_alt' },
  { to: '/config', label: '配置', icon: 'settings' }
])

onMounted(async () => {
  try {
    config.value = await api.getConfig()
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
          <q-icon name="archive" size="24px" class="q-mr-sm" />
          Zip-Queue
        </q-toolbar-title>
        <template v-if="config">
          <q-chip dense square color="white" text-color="primary" class="q-mr-none">
            并发 {{ config.max_concurrent_tasks }}
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
