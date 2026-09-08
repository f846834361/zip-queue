<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { useQuasar } from 'quasar'
import api, { type Password } from '../api'

const $q = useQuasar()

const passwords = ref<Password[]>([])
const loading = ref(false)
const showDialog = ref(false)
const editing = ref<Password | null>(null)
const form = ref({ value: '', note: '' })

// 批量操作"穿透文件夹"开关（持久化在后端 settings）
const penetrateSubfolders = ref(false)
const savingPenetrate = ref(false)

async function loadConfig() {
  try {
    const cfg = await api.getConfig()
    // 仅当后端明确返回 true 才视为开启；undefined/null 一律按关闭（默认否），
    // 避免赋值为 undefined 导致 q-toggle 显示成"中间"态
    penetrateSubfolders.value = cfg.penetrate_subfolders === true
  } catch (e) {
    $q.notify({ type: 'negative', message: (e as Error).message })
  }
}

async function savePenetrate(val: boolean) {
  if (savingPenetrate.value) return
  const prev = penetrateSubfolders.value
  savingPenetrate.value = true
  try {
    await api.updateConfig({ penetrate_subfolders: val })
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
    const resp = await api.listPasswords()
    passwords.value = resp.items
  } catch (e) {
    $q.notify({ type: 'negative', message: (e as Error).message })
  } finally {
    loading.value = false
  }
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
      message: next ? `序号 ${row.sort_order} 密码已生效` : `序号 ${row.sort_order} 密码已失效`
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
  showDialog.value = true
}

function openEdit(row: Password) {
  editing.value = row
  form.value = { value: row.value, note: row.note || '' }
  showDialog.value = true
}

async function save() {
  if (!form.value.value.trim()) {
    $q.notify({ type: 'warning', message: '密码不能为空' })
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

async function moveUp(row: Password) {
  const idx = passwords.value.findIndex((p) => p.id === row.id)
  if (idx <= 0) return
  try {
    await api.reorderPassword(row.id, -1)
    await fetchList()
  } catch (e) {
    $q.notify({ type: 'negative', message: (e as Error).message })
  }
}

async function moveDown(row: Password) {
  const idx = passwords.value.findIndex((p) => p.id === row.id)
  if (idx < 0 || idx >= passwords.value.length - 1) return
  try {
    await api.reorderPassword(row.id, 1)
    await fetchList()
  } catch (e) {
    $q.notify({ type: 'negative', message: (e as Error).message })
  }
}

onMounted(() => {
  void loadConfig()
  void fetchList()
})
</script>

<template>
  <q-page padding>
    <!-- 批量操作设置 -->
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
          按"序号"顺序轮询尝试，排在前的生效密码优先使用。点击列表行可快速切换密码生效/失效，失效密码不会参与解压尝试。留空表示无密码压缩包正常解压。
        </div>
        <q-table
          :rows="passwords"
          :columns="columns"
          :loading="loading"
          row-key="id"
          flat
          dense
          :pagination="{ rowsPerPage: 0 }"
          hide-pagination
          @row-click="onRowClick"
          :row-class="rowClass"
        >
          <template #body-cell-enabled="props">
            <q-td :props="props">
              <q-badge :color="props.row.enabled ? 'positive' : 'grey-6'" :label="props.row.enabled ? '生效' : '失效'" />
            </q-td>
          </template>
          <template #body-cell-value="props">
            <q-td :props="props">
              <span class="mono">{{ props.row.value }}</span>
            </q-td>
          </template>
          <template #body-cell-actions="props">
            <q-td :props="props">
              <q-btn
                flat
                dense
                round
                icon="arrow_upward"
                size="sm"
                :color="props.rowIndex === 0 ? 'grey-5' : 'primary'"
                :disable="props.rowIndex === 0"
                @click.stop="moveUp(props.row)"
              >
                <q-tooltip>上移</q-tooltip>
              </q-btn>
              <q-btn
                flat
                dense
                round
                icon="arrow_downward"
                size="sm"
                :color="props.rowIndex === passwords.length - 1 ? 'grey-5' : 'primary'"
                :disable="props.rowIndex === passwords.length - 1"
                @click.stop="moveDown(props.row)"
              >
                <q-tooltip>下移</q-tooltip>
              </q-btn>
              <q-btn flat dense round icon="edit" size="sm" @click.stop="openEdit(props.row)">
                <q-tooltip>编辑</q-tooltip>
              </q-btn>
              <q-btn flat dense round icon="delete" size="sm" color="negative" @click.stop="remove(props.row)">
                <q-tooltip>删除</q-tooltip>
              </q-btn>
            </q-td>
          </template>
          <template #no-data>
            <div class="full-width text-center text-grey q-pa-md">暂无密码</div>
          </template>
        </q-table>
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
          <q-input v-model="form.value" label="密码" outlined dense autofocus />
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
:deep(.q-table tbody tr.row-disabled td) {
  color: rgba(0, 0, 0, 0.42);
}
</style>
