<script setup lang="ts">
import { ref, watch } from 'vue'

const props = withDefaults(
  defineProps<{
    modelValue?: string
    label?: string
    defaultTime?: string
  }>(),
  { modelValue: '', label: '', defaultTime: '00:00' }
)

const emit = defineEmits<{
  (e: 'update:modelValue', value: string): void
}>()

// 弹出层内的临时选择状态，格式分别为 YYYY-MM-DD 与 HH:mm
const date = ref('')
const time = ref(props.defaultTime)
// 输入框显示文本，支持手写输入
const text = ref(props.modelValue)
// 手写内容格式非法时置为 true（不向上提交非法值）
const invalid = ref(false)

// 外部改动（重置等）时同步内部状态
watch(
  () => props.modelValue,
  (val) => {
    invalid.value = false
    if (text.value !== val) text.value = val
    if (!val) {
      date.value = ''
      time.value = props.defaultTime
      return
    }
    const parts = val.split(' ')
    const d = parts[0] ?? ''
    const t = parts[1] ?? props.defaultTime
    if (d !== date.value) date.value = d
    if (t !== time.value) time.value = t
  },
  { immediate: true }
)

// 支持 "YYYY-MM-DD" 或 "YYYY-MM-DD HH:mm"（空格或 T 分隔）
const MANUAL_RE = /^(\d{4})-(\d{2})-(\d{2})(?:[ T](\d{2}):(\d{2}))?$/

// 解析手写文本；非法返回 null；合法时返回 { date, time }，time 为 null 表示只填了日期
function parseManual(s: string): { date: string; time: string | null } | null {
  const m = s.trim().match(MANUAL_RE)
  if (!m) return null
  const year = Number(m[1])
  const month = Number(m[2])
  const day = Number(m[3])
  if (year < 1000 || year > 9999 || month < 1 || month > 12 || day < 1) return null
  if (day > new Date(year, month, 0).getDate()) return null
  let time: string | null = null
  if (m[4] !== undefined) {
    const hh = Number(m[4])
    const mi = Number(m[5])
    if (hh > 23 || mi > 59) return null
    time = `${m[4]}:${m[5]}`
  }
  return { date: `${m[1]}-${m[2]}-${m[3]}`, time }
}

// 手写输入：合法完整值立即提交；仅日期则延后到失焦补默认时间再提交，便于继续输入时间
function onInput(val: string | number | null) {
  const s = String(val ?? '')
  text.value = s
  invalid.value = false
  if (!s) {
    // 手动清空：同时清掉内部选择状态，避免被 commit 回填
    date.value = ''
    time.value = props.defaultTime
    commit()
    return
  }
  const parsed = parseManual(s)
  if (!parsed) return // 不完整或非法，留待失焦时提示
  date.value = parsed.date
  if (parsed.time === null) return // 只填了日期，等失焦再提交
  time.value = parsed.time
  commit()
}

// 失焦：只填日期的输入补默认时间后提交；非法格式则标记错误并保留原文供修正
function onBlur() {
  const val = text.value
  if (!val) {
    invalid.value = false
    return
  }
  const parsed = parseManual(val)
  if (!parsed) {
    invalid.value = true
    return
  }
  date.value = parsed.date
  time.value = parsed.time ?? props.defaultTime
  invalid.value = false
  commit()
}

function onEnter(evt: Event) {
  ;(evt.target as HTMLInputElement | null)?.blur()
}

// 日期/时间变化后即时组合并回写
function commit() {
  const next = date.value ? `${date.value} ${time.value}` : ''
  invalid.value = false
  if (text.value !== next) text.value = next
  if (next !== props.modelValue) emit('update:modelValue', next)
}

function clearAll() {
  invalid.value = false
  date.value = ''
  time.value = props.defaultTime
  text.value = ''
  emit('update:modelValue', '')
}
</script>

<template>
  <q-input
    :model-value="text"
    :label="label"
    outlined
    dense
    clearable
    :error="invalid"
    :error-message="invalid ? '格式应为 YYYY-MM-DD 或 YYYY-MM-DD HH:mm' : ''"
    @update:model-value="onInput"
    @blur="onBlur"
    @keydown.enter.prevent="onEnter"
    @clear="clearAll"
  >
    <template #prepend>
      <q-icon name="event" class="cursor-pointer">
        <q-popup-proxy cover transition-show="scale" transition-hide="scale">
          <div class="row no-wrap items-stretch">
            <q-date
              v-model="date"
              mask="YYYY-MM-DD"
              today-btn
              nav-min-year-mode="list"
              @update:model-value="commit"
            />
            <q-time
              v-model="time"
              mask="HH:mm"
              format24h
              @update:model-value="commit"
            />
          </div>
          <div class="row justify-between items-center q-px-md q-py-sm" style="min-width: 440px">
            <div class="text-caption text-grey-7">
              <template v-if="date">{{ date }} {{ time }}</template>
              <template v-else>请选择日期与时间</template>
            </div>
            <q-btn flat dense no-caps color="negative" label="清除" @click="clearAll" />
          </div>
        </q-popup-proxy>
      </q-icon>
    </template>
  </q-input>
</template>
