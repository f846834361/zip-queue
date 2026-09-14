<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useQuasar } from 'quasar'
import api, { type AppConfig, type Password, type UpdateConfigBody } from '../api'
import { useBrowseStore } from '../stores/browse'
import { COMPRESSION_LABELS, compressionLabel } from '../utils/task'

const $q = useQuasar()
const browseStore = useBrowseStore()

interface TablePagination {
  page: number
  rowsPerPage: number
  rowsNumber: number
  sortBy: string | undefined
  descending: boolean
}

const passwords = ref<Password[]>([])
const loading = ref(false)
const pagination = ref<TablePagination>({
  page: 1,
  rowsPerPage: 20,
  rowsNumber: 0,
  sortBy: undefined,
  descending: false
})
const showDialog = ref(false)
const editing = ref<Password | null>(null)
const form = ref({ value: '', note: '' })
const duplicateError = ref('')

// 批量操作"穿透文件夹"开关（持久化在后端 settings）
const penetrateSubfolders = ref(false)
const savingPenetrate = ref(false)

// 压缩"去掉顶层文件夹"开关（持久化在后端 settings）
const stripFolder = ref(false)
const savingStrip = ref(false)

// 解压"智能添加文件夹"模式（持久化在后端 settings）
const addFolderMode = ref<'one' | 'multiple' | 'none'>('none')
const savingAddFolder = ref(false)

// 任务与压缩设置（持久化在后端 settings）
const maxConcurrentTasks = ref(1)
const compressionLevel = ref<AppConfig['compression_level']>('normal')
const savingTaskSettings = ref(false)

// 下拉框限定可选值：并发数 1-4，压缩效率四档
const concurrencyOptions = [
  { label: '1', value: 1 },
  { label: '2', value: 2 },
  { label: '3', value: 3 },
  { label: '4', value: 4 }
]
// 与顶栏展示共用同一份文案映射
const compressionOptions = (Object.keys(COMPRESSION_LABELS) as AppConfig['compression_level'][]).map(
  (value) => ({ label: COMPRESSION_LABELS[value], value })
)

// 解压"智能添加文件夹"可选值，与后端 setting.AddFolder* 对应
const addFolderOptions: { label: string; value: 'one' | 'multiple' | 'none' }[] = [
  { label: '1个', value: 'one' },
  { label: '多个', value: 'multiple' },
  { label: '无', value: 'none' }
]

// 变更即保存；失败回滚到改动前的值，并以服务端返回的生效值为准
async function saveTaskSettings(body: UpdateConfigBody, okMessage: string) {
  if (savingTaskSettings.value) return
  const prevConcurrency = maxConcurrentTasks.value
  const prevLevel = compressionLevel.value
  savingTaskSettings.value = true
  try {
    const cfg = await api.updateConfig(body)
    browseStore.setConfig(cfg)
    maxConcurrentTasks.value = cfg.max_concurrent_tasks
    compressionLevel.value = cfg.compression_level
    $q.notify({ type: 'positive', message: okMessage })
  } catch (e) {
    maxConcurrentTasks.value = prevConcurrency
    compressionLevel.value = prevLevel
    $q.notify({ type: 'negative', message: (e as Error).message })
  } finally {
    savingTaskSettings.value = false
  }
}

function onConcurrencyChange(val: number | null) {
  if (val === null) return
  void saveTaskSettings({ max_concurrent_tasks: val }, `已设置为同时执行 ${val} 个任务`)
}

function onCompressionChange(val: AppConfig['compression_level'] | null) {
  if (val === null) return
  void saveTaskSettings({ compression_level: val }, `压缩效率已设为「${compressionLabel(val)}」`)
}

// 手动唤醒调度器（用户兜底）：让调度器重新扫描待处理任务并补派空闲并发槽
const waking = ref(false)

async function onWake() {
  if (waking.value) return
  waking.value = true
  try {
    const resp = await api.wakeTasks()
    $q.notify({
      type: 'positive',
      message:
        resp.pending > 0
          ? `已重启，${resp.pending} 个待处理任务开始排队`
          : '已重启，当前没有待处理任务'
    })
  } catch (e) {
    $q.notify({ type: 'negative', message: (e as Error).message })
  } finally {
    waking.value = false
  }
}

async function loadConfig() {
  try {
    const cfg = await browseStore.ensureConfig()
    // 仅当后端明确返回 true 才视为开启；undefined/null 一律按关闭（默认否），
    // 避免赋值为 undefined 导致 q-toggle 显示成"中间"态
    penetrateSubfolders.value = cfg.penetrate_subfolders === true
    stripFolder.value = cfg.strip_folder === true
    addFolderMode.value = (cfg.add_folder_mode as 'one' | 'multiple' | 'none') || 'none'
    maxConcurrentTasks.value = cfg.max_concurrent_tasks || 1
    compressionLevel.value = cfg.compression_level || 'normal'
  } catch (e) {
    $q.notify({ type: 'negative', message: (e as Error).message })
  }
}

async function savePenetrate(val: boolean) {
  if (savingPenetrate.value) return
  const prev = penetrateSubfolders.value
  savingPenetrate.value = true
  try {
    const cfg = await api.updateConfig({ penetrate_subfolders: val })
    browseStore.setConfig(cfg)
    $q.notify({
      type: 'positive',
      message: val ? '已开启穿透文件夹' : '已关闭穿透文件夹'
    })
  } catch (e) {
    penetrateSubfolders.value = prev
    $q.notify({ type: 'negative', message: (e as Error).message })
  } finally {
    savingPenetrate.value = false
  }
}

async function saveStripFolder(val: boolean) {
  if (savingStrip.value) return
  const prev = stripFolder.value
  savingStrip.value = true
  try {
    const cfg = await api.updateConfig({ strip_folder: val })
    browseStore.setConfig(cfg)
    stripFolder.value = cfg.strip_folder === true
    $q.notify({
      type: 'positive',
      message: val ? '已开启去掉文件夹' : '已关闭去掉文件夹'
    })
  } catch (e) {
    stripFolder.value = prev
    $q.notify({ type: 'negative', message: (e as Error).message })
  } finally {
    savingStrip.value = false
  }
}

async function saveAddFolderMode(val: 'one' | 'multiple' | 'none') {
  if (savingAddFolder.value) return
  const prev = addFolderMode.value
  savingAddFolder.value = true
  try {
    const cfg = await api.updateConfig({ add_folder_mode: val })
    browseStore.setConfig(cfg)
    addFolderMode.value = (cfg.add_folder_mode as 'one' | 'multiple' | 'none') || 'none'
    $q.notify({ type: 'positive', message: '已保存智能添加文件夹设置' })
  } catch (e) {
    addFolderMode.value = prev
    $q.notify({ type: 'negative', message: (e as Error).message })
  } finally {
    savingAddFolder.value = false
  }
}

const columns = [
  { name: 'enabled', label: '状态', field: 'enabled', align: 'center' as const },
  { name: 'sort', label: '序号', field: 'sort_order', align: 'left' as const, sortable: true },
  { name: 'value', label: '密码', field: 'value', align: 'left' as const },
  { name: 'note', label: '备注', field: 'note', align: 'left' as const },
  { name: 'actions', label: '操作', field: 'actions', align: 'right' as const }
]

async function fetchList() {
  loading.value = true
  try {
    const resp = await api.listPasswords({
      page: pagination.value.page,
      page_size: pagination.value.rowsPerPage,
      sort_by: pagination.value.sortBy,
      desc: pagination.value.descending
    })
    passwords.value = resp.items
    pagination.value.rowsNumber = resp.total
    // 删空最后一页等场景：当前页超出范围时回退到末页再拉取一次
    const totalPages = Math.max(1, Math.ceil(resp.total / pagination.value.rowsPerPage))
    if (pagination.value.page > totalPages) {
      pagination.value.page = totalPages
      const resp2 = await api.listPasswords({
        page: pagination.value.page,
        page_size: pagination.value.rowsPerPage,
        sort_by: pagination.value.sortBy,
        desc: pagination.value.descending
      })
      passwords.value = resp2.items
      pagination.value.rowsNumber = resp2.total
    }
  } catch (e) {
    $q.notify({ type: 'negative', message: (e as Error).message })
  } finally {
    loading.value = false
  }
}

// 服务端分页模式（pagination 含 rowsNumber）下，翻页 / 改每页条数 / 排序都通过 @request 通知，
// 我们据此更新分页状态并向服务端重新拉取当前页
function onRequest(props: {
  pagination: {
    page: number
    rowsPerPage: number
    rowsNumber?: number
    sortBy: string | null
    descending: boolean
  }
}) {
  const p = props.pagination
  pagination.value = {
    ...pagination.value,
    page: p.page,
    rowsPerPage: p.rowsPerPage,
    rowsNumber: p.rowsNumber ?? pagination.value.rowsNumber,
    sortBy: p.sortBy ?? undefined,
    descending: p.descending
  }
  void fetchList()
}

// 总页数，供 q-pagination 的 :max 使用
const pageCount = computed(() =>
  Math.max(1, Math.ceil(pagination.value.rowsNumber / pagination.value.rowsPerPage))
)

// 切换每页条数时回到第 1 页重新拉取
function onRowsPerPageChange() {
  pagination.value.page = 1
  void fetchList()
}

// 正在切换状态的行 id，避免重复请求
const togglingIds = new Set<number>()

async function toggleEnabled(row: Password) {
  if (togglingIds.has(row.id)) return
  togglingIds.add(row.id)
  const next = !row.enabled
  try {
    await api.setPasswordEnabled(row.id, next)
    row.enabled = next
    $q.notify({
      type: 'positive',
      message: next ? `密码 ${row.value} 已生效` : `密码 ${row.value} 已失效`
    })
  } catch (e) {
    $q.notify({ type: 'negative', message: (e as Error).message })
  } finally {
    togglingIds.delete(row.id)
  }
}

function onRowClick(_evt: unknown, row: Password) {
  void toggleEnabled(row)
}

// 失效密码整行淡化，直观显示未生效
function rowClass(row: Password): string {
  return row.enabled ? '' : 'row-disabled'
}

function openCreate() {
  editing.value = null
  form.value = { value: '', note: '' }
  duplicateError.value = ''
  showDialog.value = true
}

function openEdit(row: Password) {
  editing.value = row
  form.value = { value: row.value, note: row.note || '' }
  duplicateError.value = ''
  showDialog.value = true
}

// 重复校验（与后端一致：区分大小写；编辑时排除自身）
function isDuplicate(value: string): boolean {
  return passwords.value.some((p) => p.value === value && p.id !== editing.value?.id)
}

async function save() {
  if (!form.value.value.trim()) {
    $q.notify({ type: 'warning', message: '密码不能为空' })
    return
  }
  if (isDuplicate(form.value.value)) {
    duplicateError.value = '该密码已存在'
    return
  }
  try {
    if (editing.value) {
      await api.updatePassword(editing.value.id, form.value.value, form.value.note)
      $q.notify({ type: 'positive', message: '已更新' })
    } else {
      await api.createPassword(form.value.value, form.value.note)
      $q.notify({ type: 'positive', message: '已添加' })
    }
    showDialog.value = false
    await fetchList()
  } catch (e) {
    $q.notify({ type: 'negative', message: (e as Error).message })
  }
}

async function remove(row: Password) {
  $q.dialog({
    title: '删除密码',
    message: `确定删除序号 ${row.sort_order} 的密码吗？`,
    ok: { label: '删除', color: 'negative', unelevated: true },
    cancel: { label: '取消', flat: true }
  }).onOk(async () => {
    try {
      await api.deletePassword(row.id)
      $q.notify({ type: 'positive', message: '已删除' })
      await fetchList()
    } catch (e) {
      $q.notify({ type: 'negative', message: (e as Error).message })
    }
  })
}

// 拖拽排序：拖动行释放到目标行即与目标行交换位置（一次调用交换两条记录）。
const dragId = ref<number | null>(null)
const dropId = ref<number | null>(null)
const reordering = ref(false)

function onDragStart(row: Password) {
  dragId.value = row.id
}

function onDragOver(row: Password) {
  if (dragId.value !== null && dragId.value !== row.id) dropId.value = row.id
}

function onDragEnd() {
  dragId.value = null
  dropId.value = null
}

async function onDrop(row: Password) {
  const from = dragId.value
  onDragEnd()
  if (from === null || from === row.id || reordering.value) return
  reordering.value = true
  try {
    // 与被拖放到的目标行直接交换位置（一次调用交换两条记录）
    await api.reorderPassword(from, row.id)
    await fetchList()
    $q.notify({ type: 'positive', message: `已与「${row.value}」交换位置` })
  } catch (e) {
    $q.notify({ type: 'negative', message: (e as Error).message })
  } finally {
    reordering.value = false
  }
}

onMounted(() => {
  void loadConfig()
  void fetchList()
})
</script>

<template>
  <q-page padding>
    <!-- 任务配置 -->
    <q-card flat bordered class="q-mb-md">
      <q-card-section>
        <div class="text-h6 text-weight-medium">任务配置</div>
        <q-select
          v-model="maxConcurrentTasks"
          :options="concurrencyOptions"
          :disable="savingTaskSettings"
          label="同时执行任务数"
          outlined
          dense
          emit-value
          map-options
          class="q-mt-sm"
          style="width: 160px"
          @update:model-value="onConcurrencyChange"
        />
        <div class="text-caption text-grey-7 q-mt-xs">
          同时运行任务数,最多4
        </div>

        <q-separator class="q-my-md" />

        <div class="row items-center justify-between">
          <div class="q-pr-lg">
            <div class="text-subtitle1 text-weight-medium">重启任务队列</div>
            <div class="text-caption text-grey-7 q-mt-xs">
              如果有任务不运行,点此按钮手动重启调度器，唤醒任务。
            </div>
          </div>
          <q-btn
            label="重启任务队列"
            icon="play_arrow"
            color="primary"
            outline
            no-caps
            dense
            :loading="waking"
            @click="onWake"
          />
        </div>
      </q-card-section>
    </q-card>

    <!-- 批量操作与压缩设置 -->
    <q-card flat bordered class="q-mb-md">
      <q-card-section>
        <div class="row items-center justify-between">
          <div class="q-pr-lg">
            <div class="text-h6 text-weight-medium">批量操作设置</div>
            <div class="text-caption text-grey-7 q-mt-xs">
              开启「穿透文件夹」后，文件页面的「解压全部 / 压缩全部」会递归处理当前目录下的所有子文件夹，而不仅限于当前目录本身。
            </div>
          </div>
          <q-toggle
            v-model="penetrateSubfolders"
            :disable="savingPenetrate"
            color="primary"
            @update:model-value="savePenetrate"
          />
        </div>

        <q-separator class="q-my-md" />

        <div class="row items-center justify-between">
          <div class="q-pr-lg">
            <div class="text-subtitle1 text-weight-medium">压缩效率</div>
            <div class="text-caption text-grey-7 q-mt-xs">
              「特快」仅打包不压缩，速度最快、体积最大；越慢压缩率越高、耗时越长。仅影响此后开始的任务。
            </div>
          </div>
          <q-tabs
            v-model="compressionLevel"
            dense
            no-caps
            inline-label
            active-color="primary"
            indicator-color="primary"
            align="left"
            class="compression-tabs rounded-borders q-pa-xs"
            @update:model-value="onCompressionChange"
          >
            <q-tab
              v-for="opt in compressionOptions"
              :key="opt.value"
              :name="opt.value"
              :label="opt.label"
              :disable="savingTaskSettings"
            />
          </q-tabs>
        </div>

        <q-separator class="q-my-md" />

        <div class="row items-center justify-between">
          <div class="q-pr-lg">
            <div class="text-subtitle1 text-weight-medium">去掉文件夹</div>
            <div class="text-caption text-grey-7 q-mt-xs">
              压缩文件夹时，开启后 zip 内不再保留选中的顶层文件夹这一层（条目为 a.txt / sub/b.txt）；关闭则保留（MyFolder/a.txt，与 PC 右键压缩一致）。仅影响此后开始的任务。
            </div>
          </div>
          <q-toggle
            v-model="stripFolder"
            :disable="savingStrip"
            color="primary"
            @update:model-value="saveStripFolder"
          />
        </div>

        <q-separator class="q-my-md" />

        <div class="row items-center justify-between">
          <div class="q-pr-lg">
            <div class="text-subtitle1 text-weight-medium">智能添加文件夹</div>
            <div class="text-caption text-grey-7 q-mt-xs">
              解压时是否自动包一层以压缩包命名的父文件夹。「1个」：多个文件或单个文件都包（自动避免文件夹嵌套）；「多个」：仅多个文件包，单文件/单文件夹不包；「无」：始终不包。仅影响此后开始的任务。
            </div>
          </div>
          <q-tabs
            v-model="addFolderMode"
            dense
            no-caps
            inline-label
            active-color="primary"
            indicator-color="primary"
            align="left"
            class="compression-tabs rounded-borders q-pa-xs"
            @update:model-value="saveAddFolderMode"
          >
            <q-tab
              v-for="opt in addFolderOptions"
              :key="opt.value"
              :name="opt.value"
              :label="opt.label"
              :disable="savingAddFolder"
            />
          </q-tabs>
        </div>
      </q-card-section>
    </q-card>

    <!-- 解压密码卡片 -->
    <q-card flat bordered class="q-mb-md">
      <q-card-section>
        <div class="row items-center justify-between q-mb-sm">
          <div class="text-h6 text-weight-medium">解压密码</div>
          <q-btn color="primary" icon="add" label="添加" unelevated no-caps dense @click="openCreate" />
        </div>
        <div class="text-caption text-grey-7 q-mb-md">
          按"序号"顺序轮询尝试，排在前的生效密码优先使用。拖动行到目标位置释放即可调整顺序；点击列表行可快速切换密码生效/失效，失效密码不会参与解压尝试。留空表示无密码压缩包正常解压。
        </div>
        <q-table
          :rows="passwords"
          :columns="columns"
          :loading="loading"
          row-key="id"
          flat
          dense
          v-model:pagination="pagination"
          :rows-per-page-options="[20, 50, 100]"
          :hide-pagination="true"
          @request="onRequest"
          @row-click="onRowClick"
          :row-class="rowClass"
        >
          <!-- 自定义整行：给 <tr> 加 draggable 与拖拽事件（QTr 会透传属性到 <tr>） -->
          <template #body="props">
            <q-tr
              :key="props.row.id"
              :props="props"
              draggable="true"
              class="pw-row"
              :class="{
                'pw-row--dragging': dragId === props.row.id,
                'pw-row--target': dropId === props.row.id
              }"
              @click="onRowClick($event, props.row)"
              @dragstart="onDragStart(props.row)"
              @dragover.prevent="onDragOver(props.row)"
              @dragenter.prevent
              @drop.prevent="onDrop(props.row)"
              @dragend="onDragEnd"
            >
              <q-td :props="props" key="enabled">
                <q-badge
                  :color="props.row.enabled ? 'positive' : 'grey-6'"
                  :label="props.row.enabled ? '生效' : '失效'"
                />
              </q-td>
              <q-td :props="props" key="sort">
                <q-icon name="drag_indicator" size="18px" class="pw-drag-handle q-mr-xs" />
                {{ props.row.sort_order }}
              </q-td>
              <q-td :props="props" key="value">
                <span class="mono">{{ props.row.value }}</span>
              </q-td>
              <q-td :props="props" key="note">{{ props.row.note }}</q-td>
              <q-td :props="props" key="actions" class="text-right">
                <q-btn flat dense round icon="edit" size="sm" @click.stop="openEdit(props.row)">
                  <q-tooltip>编辑</q-tooltip>
                </q-btn>
                <q-btn flat dense round icon="delete" size="sm" color="negative" @click.stop="remove(props.row)">
                  <q-tooltip>删除</q-tooltip>
                </q-btn>
              </q-td>
            </q-tr>
          </template>
          <template #no-data>
            <div class="full-width text-center text-grey q-pa-md">暂无密码</div>
          </template>
        </q-table>
        <div
          v-if="pagination.rowsNumber > 20"
          class="row items-center justify-end q-mt-sm q-gutter-x-md"
        >
         <q-pagination
            v-model="pagination.page"
            :max="pageCount"
            :max-pages="5"
          
            direction-links
            @update:model-value="fetchList"
          />
          <q-select
            v-model="pagination.rowsPerPage"
            :options="[20, 50, 100]"
            label="每页条数"
            dense
            outlined
            hide-bottom-space
            style="min-width: 110px"
            @update:model-value="onRowsPerPageChange"
          />
         
          <div class="text-body2 text-grey-7">共 {{ pagination.rowsNumber }} 条</div>
        </div>
      </q-card-section>
    </q-card>

    <!-- 预留：其他配置卡片 -->
    <!-- 后续可在此添加更多配置卡片 -->

    <!-- 添加/编辑对话框 -->
    <q-dialog v-model="showDialog">
      <q-card style="min-width: 360px">
        <q-card-section>
          <div class="text-h6">{{ editing ? '编辑密码' : '添加密码' }}</div>
        </q-card-section>
        <q-card-section class="q-pt-none">
          <q-input
            v-model="form.value"
            label="密码"
            outlined
            dense
            autofocus
            :error="duplicateError !== ''"
            :error-message="duplicateError"
            @update:model-value="duplicateError = ''"
          />
          <q-input v-model="form.note" label="备注（可选）" outlined dense class="q-mt-sm" />
        </q-card-section>
        <q-card-actions align="right">
          <q-btn flat label="取消" color="grey-7" v-close-popup />
          <q-btn unelevated label="保存" color="primary" @click="save" />
        </q-card-actions>
      </q-card>
    </q-dialog>
  </q-page>
</template>

<style lang="scss" scoped>
// 停用密码整行淡化（q-badge / 带独立颜色的按钮不受影响）
:deep(tr.row-disabled td) {
  color: rgba(0, 0, 0, 0.42);
}

// 拖拽排序反馈：拖起的行淡化，悬停的目标行高亮
.pw-drag-handle {
  cursor: grab;
  color: rgba(0, 0, 0, 0.38);
  vertical-align: middle;
}
:deep(tr.pw-row--dragging) {
  opacity: 0.4;
}
:deep(tr.pw-row--target td) {
  background: rgba(25, 118, 210, 0.08);
  box-shadow:
    inset 0 1px 0 rgba(0, 0, 0, 0.12),
    inset 0 -1px 0 rgba(0, 0, 0, 0.12);
}

// 压缩效率：让 q-tabs 看起来像一组分段按钮
.compression-tabs {
  border: 1px solid rgba(0, 0, 0, 0.12);
  background: rgba(0, 0, 0, 0.02);
  min-width: 220px;
}
</style>
