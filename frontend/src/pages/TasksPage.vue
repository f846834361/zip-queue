<script setup lang="ts">
import { onBeforeUnmount, onMounted, reactive, ref, watch } from 'vue'
import { useRouter } from 'vue-router'
import { useQuasar } from 'quasar'
import api, { type Task } from '../api'
import DateTimePicker from '../components/DateTimePicker.vue'
import { formatDateTime } from '../utils/format'
import { POLL_INTERVAL, statusColor, statusLabel, typeLabel } from '../utils/task'
import { usePolling } from '../composables/usePolling'

const $q = useQuasar()
const router = useRouter()

const filters = reactive({
  status: '',
  type: '',
  source_path: '',
  completed_after: '',
  completed_before: ''
})

const statusOptions = [
  { label: '全部', value: '' },
  { label: '待处理', value: 'pending' },
  { label: '执行中', value: 'running' },
  { label: '成功', value: 'succeeded' },
  { label: '失败', value: 'failed' }
]
const typeOptions = [
  { label: '全部', value: '' },
  { label: '解压', value: 'decompress' },
  { label: '压缩', value: 'compress' }
]

const items = ref<Task[]>([])
const total = ref(0)
const page = ref(1)
const pageSize = ref(20)
const loading = ref(false)

const pagination = ref({
  page: 1,
  rowsPerPage: 20,
  rowsNumber: 0,
  sortBy: 'created_at',
  descending: true
})

const columns = [
  { name: 'type', label: '类型', field: 'type', align: 'left' as const },
  { name: 'source_path', label: '源路径', field: 'source_path', align: 'left' as const },
  { name: 'status', label: '状态', field: 'status', align: 'left' as const },
  { name: 'progress', label: '进度', field: 'progress_percent', align: 'left' as const },
  { name: 'created_at', label: '创建时间', field: 'created_at', align: 'left' as const, sortable: true },
  { name: 'completed_at', label: '完成时间', field: 'completed_at', align: 'left' as const },
  { name: 'actions', label: '操作', field: 'actions', align: 'right' as const }
]

function buildParams() {
  const params: Record<string, string | number> = {
    page: page.value,
    page_size: pageSize.value
  }
  if (filters.status) params.status = filters.status
  if (filters.type) params.type = filters.type
  if (filters.source_path) params.source_path = filters.source_path
  const after = toRFC3339(filters.completed_after)
  const before = toRFC3339(filters.completed_before)
  if (after) params.completed_after = after
  if (before) params.completed_before = before
  return params
}

// "YYYY-MM-DD HH:mm"（浏览器本地时间）→ RFC3339（含时区偏移），保证时间语义正确。
function toRFC3339(v: string): string | null {
  const d = new Date(v.replace(' ', 'T'))
  if (Number.isNaN(d.getTime())) return null
  return d.toISOString()
}

// 时间范围校验：开始必须早于结束，非法返回错误提示文案
function checkRange(): string | null {
  const after = toRFC3339(filters.completed_after)
  const before = toRFC3339(filters.completed_before)
  if (!after || !before) return null
  if (after >= before) return '「完成时间从」必须早于「完成时间到」'
  return null
}

async function fetchList(silent = false) {
  const rangeError = checkRange()
  if (rangeError) {
    if (!silent) $q.notify({ type: 'warning', message: rangeError })
    return
  }
  if (!silent) loading.value = true
  try {
    const resp = await api.listTasks(buildParams())
    items.value = resp.items
    total.value = resp.total
    pagination.value = {
      page: page.value,
      rowsPerPage: pageSize.value,
      rowsNumber: total.value,
      sortBy: pagination.value.sortBy,
      descending: pagination.value.descending
    }
    syncPolling()
  } catch (e) {
    // 轮询失败保持静默，不打扰用户；由手动操作时的请求提示错误
    if (!silent) $q.notify({ type: 'negative', message: (e as Error).message })
  } finally {
    if (!silent) loading.value = false
  }
}

// 列表存在 pending/running 任务时静默轮询刷新，全部结束后自动停止
const polling = usePolling(() => fetchList(true), POLL_INTERVAL)

function syncPolling() {
  const active = items.value.some((t) => t.status === 'pending' || t.status === 'running')
  if (active) polling.start()
  else polling.stop()
}

function onRequest(payload: { pagination: { page: number; rowsPerPage: number; sortBy?: string; descending?: boolean } }) {
  page.value = payload.pagination.page
  pageSize.value = payload.pagination.rowsPerPage
  void fetchList()
}

function onRowClick(_evt: unknown, row: Task) {
  openDetail(row)
}

// 实时筛选：条件变化后防抖自动刷新，无需点击"筛选"按钮，
// 输入过程中也不会每敲一个字就发请求。
const FILTER_DEBOUNCE_MS = 400
let filterTimer: ReturnType<typeof setTimeout> | undefined

watch(
  filters,
  () => {
    if (filterTimer) clearTimeout(filterTimer)
    filterTimer = setTimeout(() => {
      page.value = 1
      void fetchList()
    }, FILTER_DEBOUNCE_MS)
  },
  { deep: true }
)

onBeforeUnmount(() => {
  if (filterTimer) clearTimeout(filterTimer)
})

function resetFilters() {
  filters.status = ''
  filters.type = ''
  filters.source_path = ''
  filters.completed_after = ''
  filters.completed_before = ''
}

function openDetail(row: Task) {
  void router.push({ name: 'task-detail', params: { id: row.id } })
}

async function deleteTask(row: Task) {
  $q.dialog({
    title: '删除任务',
    message: `确定删除任务 #${row.id} 的记录吗？此操作不会影响原文件。`,
    ok: { label: '删除', color: 'negative', unelevated: true },
    cancel: { label: '取消', flat: true }
  }).onOk(async () => {
    try {
      await api.deleteTask(row.id)
      $q.notify({ type: 'positive', message: '已删除任务记录' })
      await fetchList()
    } catch (e) {
      $q.notify({ type: 'negative', message: (e as Error).message })
    }
  })
}

onMounted(fetchList)
</script>

<template>
  <q-page padding>
    <q-card flat bordered class="q-mb-md">
      <q-card-section>
        <div class="row q-col-gutter-md">
          <div class="col-12 col-md-2">
            <q-select
              v-model="filters.status"
              :options="statusOptions"
              emit-value
              map-options
              label="状态"
              outlined
              dense
            />
          </div>
          <div class="col-12 col-md-2">
            <q-select
              v-model="filters.type"
              :options="typeOptions"
              emit-value
              map-options
              label="类型"
              outlined
              dense
            />
          </div>
          <div class="col-12 col-md-2">
            <q-input
              v-model="filters.source_path"
              label="源路径包含"
              outlined
              dense
              clearable
            />
          </div>
          <div class="col-12 fcol-md-18pct">
            <date-time-picker v-model="filters.completed_after" label="完成时间从" default-time="00:00" />
          </div>
          <div class="col-12 fcol-md-18pct">
            <date-time-picker v-model="filters.completed_before" label="完成时间到" default-time="23:59" />
          </div>
          <div class="col-12 col-md-1">
            <!-- <q-btn
              color="grey-7"
              icon="restart_alt"
              label="重置"
              flat
              dense
              @click="resetFilters"
              class="full-width"
            /> -->
          </div>
        </div>
      </q-card-section>
    </q-card>

    <q-card flat bordered>
      <q-table
        :rows="items"
        :columns="columns"
        :loading="loading"
        row-key="id"
        v-model:pagination="pagination"
        :rows-per-page-options="[10, 20, 50, 100]"
        flat
        @request="onRequest"
        @row-click="onRowClick"
      >
        <template #body-cell-type="props">
          <q-td :props="props">
            <q-badge
              :color="props.row.type === 'decompress' ? 'deep-orange' : 'teal'"
              :label="typeLabel(props.row.type)"
            />
          </q-td>
        </template>
        <template #body-cell-source_path="props">
          <q-td :props="props">
            <span class="mono text-break-all text-caption">{{ props.row.source_path }}</span>
          </q-td>
        </template>
        <template #body-cell-status="props">
          <q-td :props="props">
            <q-badge
              :color="statusColor(props.row.status)"
              :label="statusLabel(props.row.status)"
            />
            <q-tooltip v-if="props.row.error">{{ props.row.error }}</q-tooltip>
          </q-td>
        </template>
        <template #body-cell-progress="props">
          <q-td :props="props">
            <q-linear-progress
              :value="props.row.progress_percent / 100"
              color="primary"
              class="q-mt-xs"
              style="min-width: 100px"
            />
            <div class="text-caption text-grey-7">
              {{ props.row.progress_percent }}%
              <template v-if="props.row.status === 'running' && props.row.current_entry">
                — {{ props.row.current_entry }}
              </template>
            </div>
          </q-td>
        </template>
        <template #body-cell-created_at="props">
          <q-td :props="props">{{ formatDateTime(props.row.created_at) }}</q-td>
        </template>
        <template #body-cell-completed_at="props">
          <q-td :props="props">{{ formatDateTime(props.row.completed_at) }}</q-td>
        </template>
        <template #body-cell-actions="props">
          <q-td :props="props" class="text-right">
            <q-btn
              flat
              dense
              round
              color="primary"
              icon="visibility"
              @click.stop="openDetail(props.row)"
            >
              <q-tooltip>查看详情</q-tooltip>
            </q-btn>
            <q-btn
              v-if="props.row.status === 'succeeded' || props.row.status === 'failed'"
              flat
              dense
              round
              color="negative"
              icon="delete"
              @click.stop="deleteTask(props.row)"
            >
              <q-tooltip>删除任务记录</q-tooltip>
            </q-btn>
          </q-td>
        </template>
        <template #no-data>
          <div class="full-width text-center text-grey q-pa-md">暂无任务记录</div>
        </template>
      </q-table>
    </q-card>
  </q-page>
</template>

<style scoped>
/* Quasar 12 栅格只支持整数列，18% 宽度需自定义（md = ≥1024px） */
@media (min-width: 1024px) {
  .fcol-md-18pct {
    flex: 0 0 18%;
    max-width: 18%;
  }
}
</style>
