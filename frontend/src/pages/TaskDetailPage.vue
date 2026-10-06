<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useQuasar } from 'quasar'
import api, { type DedupGroup, type DedupResult, type Task } from '../api'
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

const taskId = computed(() => Number(props.id ?? route.params.id))
const isRunning = computed(() => task.value?.status === 'running')
const isPending = computed(() => task.value?.status === 'pending')
const isFinished = computed(
  () =>
    task.value?.status === 'succeeded' ||
    task.value?.status === 'failed' ||
    task.value?.status === 'cancelled'
)
const isFailed = computed(() => task.value?.status === 'failed')

// 查重结果：后端以 JSON 写入 task.result，解析失败（或任务未完成）时不展示结果区块
const dedupResult = computed<DedupResult | null>(() => {
  if (!task.value || task.value.type !== 'dedup' || !task.value.result) return null
  try {
    return JSON.parse(task.value.result) as DedupResult
  } catch {
    return null
  }
})

// 查重任务勾选的输入路径集合，供详情页回显查重范围
const dedupSources = computed<string[]>(() => {
  if (!task.value || task.value.type !== 'dedup' || !task.value.sources) return []
  try {
    const arr = JSON.parse(task.value.sources) as string[]
    return Array.isArray(arr) ? arr : []
  } catch {
    return []
  }
})

const hasDuplicates = computed(
  () =>
    !!dedupResult.value &&
    (dedupResult.value.duplicate_files.length > 0 ||
      dedupResult.value.duplicate_folders.length > 0)
)

// 一个重复分组内的条目总数（跨全部子目录桶）
function groupCount(g: DedupGroup): number {
  return g.buckets.reduce((n, b) => n + b.names.length, 0)
}

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

async function cancelTask() {
  if (!task.value) return
  $q.dialog({
    title: '取消任务',
    message: `确定取消任务 #${task.value.id} 吗？`,
    ok: { label: '取消任务', color: 'warning', unelevated: true },
    cancel: { label: '返回', flat: true }
  }).onOk(async () => {
    try {
      await api.cancelTask(task.value!.id)
      $q.notify({ type: 'positive', message: '已取消任务' })
      await refresh()
    } catch (e) {
      $q.notify({ type: 'negative', message: (e as Error).message })
    }
  })
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
        v-if="task && (isPending || isRunning)"
        color="warning"
        icon="cancel"
        label="取消任务"
        outline
        no-caps
        class="q-mr-sm"
        @click="cancelTask"
      />
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
              :color="
                task.type === 'decompress'
                  ? 'deep-orange'
                  : task.type === 'dedup'
                    ? 'purple'
                    : 'teal'
              "
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
            <!-- 查重任务回显勾选的全部输入路径 -->
            <template v-if="dedupSources.length">
              <div v-for="p in dedupSources" :key="p" class="mono text-break-all q-mt-xs">
                {{ p }}
              </div>
            </template>
            <div v-else class="mono text-break-all q-mt-xs">{{ task?.source_path || '—' }}</div>
          </div>
          <!-- 查重为只读扫描，没有目标路径 -->
          <div v-if="!task || task.type !== 'dedup'" class="col-12">
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
        </q-list>
      </q-card-section>
    </q-card>

    <!-- 查重任务：任务结束后展示重复文件/文件夹分组 -->
    <q-card v-if="dedupResult && isFinished" flat bordered class="q-mb-md">
      <q-card-section>
        <div class="text-subtitle2 text-weight-medium q-mb-sm">重复项</div>

        <div class="row q-col-gutter-md q-mb-md">
          <div class="col-6 col-md-3">
            <div class="text-caption text-grey-7">重复文件组</div>
            <div class="text-subtitle1 text-weight-medium">
              {{ dedupResult.duplicate_files.length }}
            </div>
          </div>
          <div class="col-6 col-md-3">
            <div class="text-caption text-grey-7">重复文件夹组</div>
            <div class="text-subtitle1 text-weight-medium">
              {{ dedupResult.duplicate_folders.length }}
            </div>
          </div>
          <div class="col-6 col-md-3">
            <div class="text-caption text-grey-7">可节省空间</div>
            <div class="text-subtitle1 text-weight-medium text-negative">
              {{ formatSize(dedupResult.wasted_bytes) }}
            </div>
          </div>
          <div class="col-6 col-md-3">
            <div class="text-caption text-grey-7">已扫描 / 实际读取</div>
            <div class="text-subtitle1 text-weight-medium">
              {{ dedupResult.total_files }} 项 · {{ formatSize(dedupResult.checked_bytes) }}
            </div>
          </div>
        </div>

        <div v-if="!hasDuplicates" class="text-center text-grey-7 q-py-md">
          未发现重复文件或文件夹
        </div>

        <template v-else>
          <div v-if="dedupResult.duplicate_files.length" class="q-mb-md">
            <div class="text-caption text-grey-7 q-mb-xs">
              重复文件（内容完全相同，保留一份即可）
            </div>
            <q-list dense separator>
              <q-item
                v-for="(g, gi) in dedupResult.duplicate_files"
                :key="`file-${gi}`"
                class="q-py-sm"
              >
                <q-item-section>
                  <q-item-label class="text-weight-medium">
                    {{ formatSize(g.size) }}
                    <span class="text-caption text-grey-7 q-ml-sm">
                      {{ groupCount(g) }} 个副本
                    </span>
                  </q-item-label>
                  <!-- 一级：全组公共目录，灰色小字 + 目录图标 -->
                  <div v-if="g.prefix" class="row items-center no-wrap q-mt-xs">
                    <q-icon name="folder_open" size="14px" color="grey-5" class="q-mr-xs" />
                    <span class="mono text-caption text-grey-6 text-break-all">
                      {{ g.prefix }}
                    </span>
                  </div>
                  <!-- 二级：按子目录分桶，每个桶只写一次子目录 -->
                  <div v-for="(b, bi) in g.buckets" :key="bi">
                    <div v-if="b.prefix" class="row items-center no-wrap q-ml-md q-mt-xs">
                      <q-icon name="subdirectory_arrow_right" size="14px" color="grey-4" class="q-mr-xs" />
                      <span class="mono text-caption text-grey-6 text-break-all">
                        {{ b.prefix }}
                      </span>
                    </div>
                    <div
                      v-for="n in b.names"
                      :key="n"
                      class="row items-start no-wrap q-ml-xl q-mt-xs"
                    >
                      <q-icon name="description" size="14px" color="blue-grey-4" class="q-mr-xs" />
                      <span class="mono text-body2 text-weight-medium text-dark text-break-all">
                        {{ n }}
                      </span>
                    </div>
                  </div>
                </q-item-section>
              </q-item>
            </q-list>
          </div>

          <div v-if="dedupResult.duplicate_folders.length">
            <div class="text-caption text-grey-7 q-mb-xs">
              重复文件夹（所含文件内容相同，忽略目录层级）
            </div>
            <q-list dense separator>
              <q-item
                v-for="(g, gi) in dedupResult.duplicate_folders"
                :key="`dir-${gi}`"
                class="q-py-sm"
              >
                <q-item-section side>
                  <q-icon name="folder" color="amber-8" />
                </q-item-section>
                <q-item-section>
                  <q-item-label class="text-weight-medium">
                    {{ formatSize(g.size) }}
                    <span class="text-caption text-grey-7 q-ml-sm">
                      {{ groupCount(g) }} 个副本
                    </span>
                  </q-item-label>
                  <!-- 一级：公共父目录，灰色小字 + 目录图标 -->
                  <div v-if="g.prefix" class="row items-center no-wrap q-mt-xs">
                    <q-icon name="folder_open" size="14px" color="grey-5" class="q-mr-xs" />
                    <span class="mono text-caption text-grey-6 text-break-all">
                      {{ g.prefix }}
                    </span>
                  </div>
                  <!-- 二级：按子目录分桶 -->
                  <div v-for="(b, bi) in g.buckets" :key="bi">
                    <div v-if="b.prefix" class="row items-center no-wrap q-ml-md q-mt-xs">
                      <q-icon name="subdirectory_arrow_right" size="14px" color="grey-4" class="q-mr-xs" />
                      <span class="mono text-caption text-grey-6 text-break-all">
                        {{ b.prefix }}
                      </span>
                    </div>
                    <div
                      v-for="n in b.names"
                      :key="n"
                      class="row items-start no-wrap q-ml-xl q-mt-xs"
                    >
                      <q-icon name="folder" size="14px" color="amber-8" class="q-mr-xs" />
                      <span class="mono text-body2 text-weight-medium text-dark text-break-all">
                        {{ n }}
                      </span>
                    </div>
                  </div>
                </q-item-section>
              </q-item>
            </q-list>
          </div>
        </template>
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
