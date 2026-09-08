<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useQuasar } from 'quasar'
import api, { type AppConfig, type FsEntry } from '../api'
import { useBrowseStore } from '../stores/browse'
import { formatDateTime, formatSize } from '../utils/format'

const $q = useQuasar()
const browseStore = useBrowseStore()

const config = ref<AppConfig | null>(null)
const currentPath = ref('')
const manualPath = ref('')
const entries = ref<FsEntry[]>([])
const loading = ref(false)
// 用 Set 保存选中路径：勾选态查找 O(1)，避免大目录下 O(n·m) 的 includes 扫描
const selected = ref<Set<string>>(new Set())
const submitting = ref(false)

// 是否穿透子文件夹，来自配置页的全局开关（默认关闭）
const penetrateSubfolders = computed(() => !!config.value?.penetrate_subfolders)

const columns = [
  { name: 'name', label: '名称', field: 'name', align: 'left' as const, sortable: true },
  { name: 'size', label: '大小', field: 'size', align: 'right' as const, sortable: true },
  { name: 'type', label: '类型', field: 'type', align: 'left' as const },
  { name: 'mod_time', label: '修改时间', field: 'mod_time', align: 'left' as const, sortable: true }
]

async function load(path?: string) {
  loading.value = true
  selected.value = new Set()
  try {
    const resp = await api.listDir(path)
    currentPath.value = resp.path
    manualPath.value = resp.path
    entries.value = resp.entries
    browseStore.setPath(resp.path)
  } catch (e) {
    $q.notify({ type: 'negative', message: (e as Error).message })
  } finally {
    loading.value = false
  }
}

function parentPath(p: string): string | null {
  if (!p || p === '/' || p === '' ) return null
  // Windows root like C:\ → no parent
  const norm = p.replace(/[/\\]+$/, '')
  if (/^[A-Za-z]:\\?$/.test(norm)) return null
  if (norm.length <= 3 && /^[A-Za-z]:\\/.test(norm)) return null
  const idx = Math.max(norm.lastIndexOf('/'), norm.lastIndexOf('\\'))
  if (idx <= 0) return '/'
  return norm.slice(0, idx)
}

function goUp() {
  const p = parentPath(currentPath.value)
  if (p === null) {
    $q.notify({ type: 'info', message: '已是根目录' })
    return
  }
  void load(p)
}

function openDir(row: FsEntry) {
  if (!row.is_dir) return
  void load(row.path)
}

function submitManualPath() {
  const p = manualPath.value.trim()
  if (!p) return
  void load(p)
}

const selectedEntries = computed(() =>
  entries.value.filter((e) => selected.value.has(e.path))
)
const selectedCount = computed(() => selected.value.size)

// 面包屑分段：Windows/Unix 分隔符都兼容，逐级累加完整路径供点击跳转
const breadcrumbSegments = computed(() => {
  const p = currentPath.value
  if (!p) return [] as { label: string; path: string }[]
  const sep = p.includes('/') ? '/' : '\\'
  const segs: { label: string; path: string }[] = []
  for (const part of p.split(/[\\/]/).filter(Boolean)) {
    const prev = segs.length ? segs[segs.length - 1].path : ''
    segs.push({
      label: part,
      path: prev ? prev + sep + part : p.startsWith('/') ? '/' + part : part
    })
  }
  return segs
})

const canDecompress = computed(() =>
  selectedEntries.value.some((e) => e.is_archive)
)
const canCompress = computed(() =>
  selectedEntries.value.some((e) => !e.is_archive)
)

// 通用二次确认弹窗，返回用户是否确认
function confirmAction(title: string, message: string): Promise<boolean> {
  return new Promise((resolve) => {
    $q.dialog({
      title,
      message,
      ok: { label: '确认', color: 'primary', unelevated: true },
      cancel: { label: '取消', flat: true }
    })
      .onOk(() => resolve(true))
      .onCancel(() => resolve(false))
      .onDismiss(() => resolve(false))
  })
}

// 当前目录可被批量解压/压缩的数量（供未勾选时提示）
const bulkArchiveCount = computed(() => entries.value.filter((e) => e.is_archive).length)
const bulkCompressibleCount = computed(
  () => entries.value.filter((e) => !e.is_archive && (e.is_dir || e.size > 0)).length
)

async function handleDecompress() {
  if (submitting.value) return
  submitting.value = true
  try {
    if (selected.value.size > 0) {
      const archives = selectedEntries.value.filter((e) => e.is_archive)
      if (archives.length === 0) return
      const go = await confirmAction(
        '确认解压',
        `将为选中的 ${archives.length} 个压缩包创建解压任务，任务完成后会删除对应的原压缩包。是否继续？`
      )
      if (go) await addToQueue('decompress')
      return
    }
    if (bulkArchiveCount.value === 0) {
      $q.notify({ type: 'info', message: '当前目录没有可解压的压缩包' })
      return
    }
    const scopeText = penetrateSubfolders.value
      ? '开启穿透：将连同当前目录所有子文件夹中的压缩包一起创建解压任务'
      : '仅处理当前目录中的压缩包（不进入子目录）'
    const go = await confirmAction(
      '确认批量解压',
      `当前目录下共有 ${bulkArchiveCount.value} 个压缩包。${scopeText}，任务完成后会删除对应的原压缩包。是否继续？`
    )
    if (go) await bulkDecompress()
  } finally {
    submitting.value = false
  }
}

async function handleCompress() {
  if (submitting.value) return
  submitting.value = true
  try {
    if (selected.value.size > 0) {
      const items = selectedEntries.value.filter((e) => !e.is_archive)
      if (items.length === 0) return
      const go = await confirmAction(
        '确认压缩',
        `将为选中的 ${items.length} 项（文件/文件夹）创建压缩任务并生成同名 .zip，任务完成后会删除原文件/文件夹。是否继续？`
      )
      if (go) await addToQueue('compress')
      return
    }
    if (bulkCompressibleCount.value === 0) {
      $q.notify({ type: 'info', message: '当前目录没有可压缩的内容' })
      return
    }
    const go = penetrateSubfolders.value
      ? await confirmAction(
          '确认批量压缩',
          '开启穿透：将穿透当前目录所有子文件夹，把其中每个文件（非压缩包）单独创建压缩任务（文件夹本身不压缩），任务完成后会删除原文件。是否继续？'
        )
      : await confirmAction(
          '确认批量压缩',
          `将把当前目录下除压缩包外的 ${bulkCompressibleCount.value} 项内容全部压缩为同名 .zip（不进入子目录），任务完成后会删除原文件/文件夹。是否继续？`
        )
    if (go) await bulkCompress()
  } finally {
    submitting.value = false
  }
}

// addToQueue 把勾选项一次性批量建任务（每个勾选项独立一条任务），不再逐个循环请求。
async function addToQueue(type: 'decompress' | 'compress') {
  const candidates =
    type === 'decompress'
      ? selectedEntries.value.filter((e) => e.is_archive)
      : selectedEntries.value.filter((e) => !e.is_archive)

  if (candidates.length === 0) {
    $q.notify({
      type: 'warning',
      message: type === 'decompress' ? '请选择至少一个压缩包' : '请选择至少一个文件夹'
    })
    return
  }

  try {
    const resp = await api.createTasksBatch(
      type,
      candidates.map((e) => e.path)
    )
    const ignored = resp.skipped > 0 ? `，忽略 ${resp.skipped} 个不符合条件的项` : ''
    $q.notify({
      type: 'positive',
      message: `已创建 ${resp.created} 个${type === 'decompress' ? '解压' : '压缩'}任务${ignored}`
    })
    selected.value = new Set()
  } catch (e) {
    $q.notify({ type: 'negative', message: (e as Error).message })
  }
}

async function bulkDecompress() {
  if (!currentPath.value) return
  try {
    const resp = await api.bulkDecompress(currentPath.value, penetrateSubfolders.value)
    const skipped = resp.skipped ? `，跳过 ${resp.skipped} 个已在进行中的压缩包` : ''
    $q.notify({
      type: 'positive',
      message: `已创建 ${resp.created} 个解压任务${resp.created > 0 ? '（每个压缩包独立一条任务）' : ''}${skipped}`
    })
  } catch (e) {
    $q.notify({ type: 'negative', message: (e as Error).message })
  }
}

async function bulkCompress() {
  if (!currentPath.value) return
  try {
    const resp = await api.bulkCompress(currentPath.value, penetrateSubfolders.value)
    const skipped = resp.skipped ? `，跳过 ${resp.skipped} 个已在进行中的项` : ''
    $q.notify({
      type: 'positive',
      message: `已创建 ${resp.created} 个压缩任务${resp.created > 0 ? '（每个文件/文件夹独立一条任务）' : ''}${skipped}`
    })
  } catch (e) {
    $q.notify({ type: 'negative', message: (e as Error).message })
  }
}

onMounted(async () => {
  try {
    // 配置由 browse store 缓存，与 MainLayout 共享同一次请求
    config.value = await browseStore.ensureConfig()
  } catch (e) {
    $q.notify({ type: 'negative', message: (e as Error).message })
  }
  // 优先使用 store 中保存的路径，其次配置的默认路径
  const initialPath = browseStore.currentPath || config.value?.default_browse_path
  await load(initialPath || undefined)
})

function onRowClick(_evt: unknown, row: FsEntry) {
  openDir(row)
}

function onUpdateSelected(val: readonly FsEntry[]) {
  selected.value = new Set(val.map((e) => e.path))
}
</script>

<template>
  <q-page padding>
    <q-card flat bordered class="q-mb-md">
      <q-card-section class="q-pb-none">
        <div class="row items-center q-gutter-sm">
          <q-input
            v-model="manualPath"
            label="手动输入绝对路径"
            outlined
            dense
            class="col"
            clearable
            @keyup.enter="submitManualPath"
          />
          <q-btn color="primary" icon="arrow_forward" label="前往" unelevated @click="submitManualPath" />
          <q-btn color="secondary" icon="arrow_upward" label="上级" outline @click="goUp" />
          <q-btn color="grey-7" icon="refresh" label="刷新" flat @click="load(currentPath)" />
        </div>
      </q-card-section>

      <q-card-section class="q-pt-md">
        <q-breadcrumbs gutter="sm" class="text-body2">
          <q-breadcrumbs-el
            v-for="(seg, idx) in breadcrumbSegments"
            :key="idx"
            :label="seg.label"
            icon="folder"
            @click="load(seg.path)"
          />
        </q-breadcrumbs>
        <div v-if="currentPath" class="text-caption text-grey-7 q-mt-xs mono">
          {{ currentPath }}
        </div>
      </q-card-section>
    </q-card>

    <q-card flat bordered>
      <q-card-section class="q-pb-none">
        <div class="row items-center justify-between q-mb-sm">
          <div class="row items-center q-gutter-sm">
            <q-btn
              color="primary"
              icon="unarchive"
              :label="selectedCount ? '解压' : '解压全部'"
              unelevated
              no-caps
              dense
              :disable="submitting || (selectedCount > 0 && !canDecompress)"
              :loading="submitting"
              @click="handleDecompress"
            />
            <q-btn
              color="secondary"
              icon="archive"
              :label="selectedCount ? '压缩' : '压缩全部'"
              unelevated
              no-caps
              dense
              :disable="submitting || (selectedCount > 0 && !canCompress)"
              :loading="submitting"
              @click="handleCompress"
            />
          </div>
          <div v-if="selectedCount" class="text-caption text-grey-7">
            已选 {{ selectedCount }} 项
          </div>
        </div>
      </q-card-section>

      <q-card-section>
        <q-table
          :rows="entries"
          :columns="columns"
          :loading="loading"
          row-key="path"
          :pagination="{ rowsPerPage: 0 }"
          selection="multiple"
          :selected="selectedEntries"
          @update:selected="onUpdateSelected"
          @row-click="onRowClick"
          virtual-scroll
          :virtual-scroll-slice-size="50"
          style="max-height: 65vh"
          hide-pagination
          flat
        >
          <template #body-cell-name="props">
            <q-td :props="props">
              <q-icon
                :name="props.row.is_dir ? (props.row.is_archive ? 'folder_zip' : 'folder') : (props.row.is_archive ? 'folder_zip' : 'description')"
                :color="props.row.is_dir ? 'amber-8' : (props.row.is_archive ? 'deep-orange' : 'grey-7')"
                size="sm"
                class="q-mr-xs"
              />
              <span :class="{ 'text-primary cursor-pointer': props.row.is_dir, 'text-weight-medium': props.row.is_archive }">
                {{ props.row.name }}
              </span>
            </q-td>
          </template>
          <template #body-cell-size="props">
            <q-td :props="props">
              {{ formatSize(props.row.size, props.row.is_dir) }}
            </q-td>
          </template>
          <template #body-cell-type="props">
            <q-td :props="props">
              <q-badge
                v-if="props.row.is_archive"
                color="deep-orange"
                label="压缩包"
              />
              <q-badge
                v-else-if="props.row.is_dir"
                color="amber-8"
                text-color="black"
                label="文件夹"
              />
              <q-badge v-else color="grey-6" label="文件" />
            </q-td>
          </template>
          <template #body-cell-mod_time="props">
            <q-td :props="props">{{ formatDateTime(props.row.mod_time) }}</q-td>
          </template>
          <template #no-data>
            <div class="full-width text-center text-grey q-pa-md">目录为空</div>
          </template>
        </q-table>
      </q-card-section>
    </q-card>
  </q-page>
</template>
