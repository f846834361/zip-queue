<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useQuasar } from 'quasar'
import api, { type Task } from '../api'
import { formatDateTime, formatSize } from '../utils/format'
import { POLL_INTERVAL, statusColor, statusLabel, typeLabel, compressionLabel } from '../utils/task'
import { usePolling } from '../composables/usePolling'

const props = defineProps<{ id?: string }>()

const $q = useQuasar()
const route = useRoute()
const router = useRouter()

const task = ref<Task | null>(null)
const loading = ref(false)
const error = ref('')
const retrying = ref(false)
// 本页只允许重试一次（刷新或重新进入页面后重置），避免重复点击创建多条重复任务
const retried = ref(false)

const pollIntervalText = `每 ${POLL_INTERVAL / 1000} 秒更新`

const taskId = computed(() => Number(props.id ?? route.params.id))
const isRunning = computed(() => task.value?.status === 'running')
const isPending = computed(() => task.value?.status === 'pending')
const isFinished = computed(
  () => task.value?.status === 'succeeded' || task.value?.status === 'failed'
)
const isFailed = computed(() => task.value?.status === 'failed')

// 轮询直到任务结束或请求出错
const polling = usePolling(refresh, POLL_INTERVAL)

async function refresh() {
  if (!taskId.value) return
  try {
    task.value = await api.getTask(taskId.value)
    if (isFinished.value) polling.stop()
  } catch (e) {
    error.value = (e as Error).message
    polling.stop()
  }
}

function startPolling() {
  polling.stop()
  if (isRunning.value || isPending.value) polling.start()
}

async function loadInitial() {
  loading.value = true
  error.value = ''
  try {
    task.value = await api.getTask(taskId.value)
    startPolling()
  } catch (e) {
    error.value = (e as Error).message
    $q.notify({ type: 'negative', message: error.value })
  } finally {
    loading.value = false
  }
}

async function deleteTask() {
  if (!task.value) return
  $q.dialog({
    title: '删除任务',
    message: `确定删除任务 #${task.value.id} 的记录吗？此操作不会影响原文件。`,
    cancel: true,
    ok: { label: '删除', color: 'negative', unelevated: true }
  }).onOk(async () => {
    try {
      await api.deleteTask(task.value!.id)
      $q.notify({ type: 'positive', message: '已删除任务记录' })
      void router.push({ name: 'tasks' })
    } catch (e) {
      $q.notify({ type: 'negative', message: (e as Error).message })
    }
  })
}

// 重试：按原类型与原路径重新创建一条待执行任务，原记录保留为历史
async function retryTask() {
  if (!task.value) return
  retrying.value = true
  try {
    const res = await api.retryTask(task.value.id)
    $q.notify({ type: 'positive', message: `已创建重试任务 #${res.id}` })
    void router.push({ name: 'task-detail', params: { id: res.id } })
  } catch (e) {
    $q.notify({ type: 'negative', message: (e as Error).message })
  } finally {
    // 无论成功还是失败，本页都不再允许重复发起重试
    retried.value = true
    retrying.value = false
  }
}

watch(taskId, () => {
  polling.stop()
  void loadInitial()
})

onMounted(loadInitial)
</script>

<template>
  <q-page padding>
    <div class="row items-center q-mb-md">
      <q-btn flat dense round icon="arrow_back" @click="router.back()">
        <q-tooltip>返回</q-tooltip>
      </q-btn>
      <div class="text-h6 q-ml-sm">
        任务详情 <template v-if="task">#{{ task.id }}</template>
      </div>
      <q-space />
      <q-btn
        v-if="task && isFailed"
        :color="retried ? 'grey-6' : 'primary'"
        icon="restart_alt"
        label="重试"
        outline
        no-caps
        class="q-mr-sm"
        :loading="retrying"
        :disable="retried"
        @click="retryTask"
      >
        <q-tooltip>
          {{ retried ? '本页已发起重试，刷新页面后可再次重试' : '按原类型与原路径重新创建一条待执行任务' }}
        </q-tooltip>
      </q-btn>
      <q-btn
        v-if="task && isFinished"
        color="negative"
        icon="delete"
        label="删除记录"
        outline
        no-caps
        @click="deleteTask"
      />
    </div>

    <q-banner v-if="error" class="bg-red-1 text-red-8 q-mb-md">
      <template #avatar>
        <q-icon name="error" color="red-8" />
      </template>
      {{ error }}
    </q-banner>

    <q-card flat bordered class="q-mb-md">
      <q-card-section>
        <div class="row items-center q-col-gutter-md">
          <div class="col-12 col-md-6">
            <div class="text-caption text-grey-7">类型</div>
            <q-badge
              v-if="task"
              :color="task.type === 'decompress' ? 'deep-orange' : 'teal'"
              :label="typeLabel(task.type)"
              class="q-mt-xs"
            />
          </div>
          <div class="col-12 col-md-6">
            <div class="text-caption text-grey-7">状态</div>
            <q-badge
              v-if="task"
              :color="statusColor(task.status)"
              :label="statusLabel(task.status)"
              class="q-mt-xs"
            />
          </div>
          <div v-if="task && task.type === 'compress'" class="col-12 col-md-6">
            <div class="text-caption text-grey-7">压缩效率</div>
            <q-badge
              color="indigo"
              :label="compressionLabel(task.compression_level)"
              class="q-mt-xs"
            />
          </div>
          <div class="col-12">
            <div class="text-caption text-grey-7">源路径</div>
            <div class="mono text-break-all q-mt-xs">{{ task?.source_path || '—' }}</div>
          </div>
          <div class="col-12">
            <div class="text-caption text-grey-7">目标路径</div>
            <div class="mono text-break-all q-mt-xs">{{ task?.target_path || '—' }}</div>
          </div>
        </div>
      </q-card-section>
    </q-card>

    <q-card flat bordered class="q-mb-md">
      <q-card-section>
        <div class="text-subtitle2 text-weight-medium q-mb-md">进度</div>

        <q-banner
          v-if="task && task.status === 'failed' && task.error"
          class="bg-red-1 text-red-8 q-mb-md"
          rounded
        >
          <template #avatar>
            <q-icon name="error" color="red-8" />
          </template>
          <div class="text-weight-medium">任务失败</div>
          <div class="text-body2 q-mt-xs">{{ task.error }}</div>
        </q-banner>

        <div v-if="task" class="q-mb-sm">
          <q-linear-progress
            v-if="isRunning || task.progress_percent > 0"
            :value="task.progress_percent / 100"
            color="primary"
            size="20px"
            stripe
            :animation-speed="200"
          >
            <div class="absolute-full flex flex-center">
              <q-badge color="white" text-color="primary" :label="`${task.progress_percent}%`" />
            </div>
          </q-linear-progress>
          <q-linear-progress
            v-else-if="task.status === 'pending'"
            indeterminate
            color="grey-7"
            size="20px"
          />
          <q-linear-progress
            v-else-if="task.status === 'failed'"
            :value="0"
            color="red-7"
            size="20px"
          />
        </div>

        <q-list dense v-if="task">
          <q-item>
            <q-item-section side>字节</q-item-section>
            <q-item-section>
              <template v-if="task.total_bytes">
                {{ formatSize(task.processed_bytes) }} / {{ formatSize(task.total_bytes) }}
              </template>
              <template v-else>
                {{ formatSize(task.processed_bytes) }}{{ task.status === 'running' ? '（估算中）' : '' }}
              </template>
            </q-item-section>
          </q-item>
          <q-item v-if="task.status === 'running'">
            <q-item-section side>自动刷新</q-item-section>
            <q-item-section>
              <q-spinner-dots color="primary" size="sm" class="q-mr-sm" />
              <span class="text-caption text-grey-7">{{ pollIntervalText }}</span>
            </q-item-section>
          </q-item>
        </q-list>
      </q-card-section>
    </q-card>

    <q-card flat bordered>
      <q-card-section>
        <div class="text-subtitle2 text-weight-medium q-mb-md">时间戳</div>
        <q-list dense v-if="task">
          <q-item>
            <q-item-section side>创建时间</q-item-section>
            <q-item-section>{{ formatDateTime(task.created_at) }}</q-item-section>
          </q-item>
          <q-item>
            <q-item-section side>开始时间</q-item-section>
            <q-item-section>{{ formatDateTime(task.started_at) }}</q-item-section>
          </q-item>
          <q-item>
            <q-item-section side>完成时间</q-item-section>
            <q-item-section>{{ formatDateTime(task.completed_at) }}</q-item-section>
          </q-item>
          <q-item>
            <q-item-section side>临时路径</q-item-section>
            <q-item-section class="mono text-break-all">{{ task.temp_path || '—' }}</q-item-section>
          </q-item>
        </q-list>
        <q-banner v-else-if="loading" class="bg-grey-1 text-grey-7">
          <q-spinner-dots color="primary" size="sm" class="q-mr-sm" />
          加载中...
        </q-banner>
      </q-card-section>
    </q-card>
  </q-page>
</template>
