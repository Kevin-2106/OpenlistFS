<script setup lang="ts">
import { computed, nextTick, onUnmounted, ref } from 'vue'
import AppIcon from './AppIcon.vue'
const props = defineProps<{ id: string; modelValue: string; options: { value: string; label: string }[]; disabled?: boolean; invalid?: boolean; describedby?: string }>()
const emit = defineEmits<{ 'update:modelValue': [value: string] }>()
const open = ref(false), candidate = ref(0), trigger = ref<HTMLButtonElement>(), list = ref<HTMLElement>()
const position = ref({ top: '0px', left: '0px', width: '0px', maxHeight: '260px' })
const selected = computed(() => props.options.findIndex(o => o.value === props.modelValue))
async function expand() {
  if (props.disabled || trigger.value?.matches(':disabled')) return
  candidate.value = Math.max(0, selected.value); open.value = true
  const r = trigger.value!.getBoundingClientRect(), below = innerHeight - r.bottom - 12
  const height = Math.min(260, props.options.length * 44 + 8)
  position.value = { left: `${r.left}px`, width: `${r.width}px`, top: `${below < height && r.top > height ? r.top - height - 4 : r.bottom + 4}px`, maxHeight: `${Math.min(height, Math.max(below, r.top - 12))}px` }
  await nextTick(); reveal()
}
function reveal() { list.value?.querySelector(`[data-index="${candidate.value}"]`)?.scrollIntoView({ block: 'nearest' }) }
function close() { open.value = false }
function commit(index: number) { emit('update:modelValue', props.options[index].value); close(); trigger.value?.focus() }
function onKey(e: KeyboardEvent) {
  if (e.key === 'Escape' && open.value) { e.preventDefault(); e.stopPropagation(); close(); return }
  if (e.key === 'Tab') { close(); return }
  if (['Enter', ' '].includes(e.key)) { e.preventDefault(); open.value ? commit(candidate.value) : void expand(); return }
  if (['ArrowDown', 'ArrowUp', 'Home', 'End'].includes(e.key)) {
    e.preventDefault()
    if (!open.value) { void expand(); return }
    candidate.value = e.key === 'Home' ? 0 : e.key === 'End' ? props.options.length - 1 : (candidate.value + (e.key === 'ArrowDown' ? 1 : -1) + props.options.length) % props.options.length
    void nextTick(reveal)
    return
  }
  if (e.key.length === 1 && !e.ctrlKey && !e.altKey && !e.metaKey) {
    e.preventDefault()
    if (!open.value) void expand()
    const search = e.key.toLocaleLowerCase()
    const start = candidate.value
    for (let step = 1; step <= props.options.length; step++) {
      const index = (start + step) % props.options.length, option = props.options[index]
      if (option.label.toLocaleLowerCase().startsWith(search) || option.value.toLocaleLowerCase().startsWith(search)) {
        candidate.value = index; void nextTick(reveal); break
      }
    }
  }
}
function outside(e: PointerEvent) { if (!trigger.value?.contains(e.target as Node) && !list.value?.contains(e.target as Node)) close() }
function scroll(e: Event) { if (!list.value?.contains(e.target as Node)) close() }
document.addEventListener('pointerdown', outside)
document.addEventListener('scroll', scroll, true)
window.addEventListener('resize', close)
onUnmounted(() => { document.removeEventListener('pointerdown', outside); document.removeEventListener('scroll', scroll, true); window.removeEventListener('resize', close) })
</script>

<template>
  <div class="ef-select-wrapper">
    <select class="native-select" :data-for="id" tabindex="-1" aria-hidden="true" :value="modelValue" :disabled="disabled" @change="emit('update:modelValue', ($event.target as HTMLSelectElement).value)">
      <option v-for="o in options" :key="o.value" :value="o.value">{{ o.label }}</option>
    </select>
    <button :id="id" ref="trigger" type="button" class="ef-select" role="combobox" aria-haspopup="listbox" :aria-expanded="open" :aria-controls="`${id}-list`" :aria-activedescendant="open ? `${id}-option-${candidate}` : undefined" :aria-invalid="invalid" :aria-describedby="describedby" :disabled="disabled" @click="open ? close() : expand()" @keydown="onKey">
      <span>{{ options[selected]?.label || modelValue }}</span><AppIcon name="down" :size="16" />
    </button>
    <Teleport to="body">
      <div v-if="open" :id="`${id}-list`" ref="list" class="ef-option-list" role="listbox" :aria-labelledby="id" :style="position">
        <div v-for="(o, index) in options" :id="`${id}-option-${index}`" :key="o.value" role="option" :data-index="index" :aria-selected="o.value === modelValue" :class="{ preview: index === candidate }" @pointerdown.prevent @click="commit(index)">
          <span>{{ o.label }}</span><AppIcon v-if="o.value === modelValue" name="check" :size="16" />
        </div>
      </div>
    </Teleport>
  </div>
</template>
