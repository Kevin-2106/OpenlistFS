<script setup lang="ts">
import { onMounted, onUnmounted, ref } from 'vue'
import AppIcon from './AppIcon.vue'

const props = withDefaults(defineProps<{ title: string; description?: string; locked?: boolean; wide?: boolean }>(), { locked: false, wide: false })
const emit = defineEmits<{ close: [] }>()
const panel = ref<HTMLElement>()
let previous: HTMLElement | null = null
function close() { if (!props.locked) emit('close') }
function onKey(event: KeyboardEvent) {
  if (event.key === 'Escape') { event.stopPropagation(); close() }
  if (event.key !== 'Tab' || !panel.value) return
  const focusable = [...panel.value.querySelectorAll<HTMLElement>('button:not(:disabled), input:not(:disabled), textarea:not(:disabled), select:not(:disabled), [tabindex="0"]')].filter(el => el.offsetParent !== null)
  const first = focusable[0], last = focusable[focusable.length - 1]
  if (!first) { event.preventDefault(); panel.value.focus(); return }
  if (event.shiftKey && document.activeElement === first) { event.preventDefault(); last.focus() }
  else if (!event.shiftKey && document.activeElement === last) { event.preventDefault(); first.focus() }
}
onMounted(() => {
  previous = document.activeElement as HTMLElement
  const target = panel.value?.querySelector<HTMLElement>('input, textarea, [data-autofocus]') || panel.value
  target?.focus()
})
onUnmounted(() => previous?.isConnected && previous.focus())
</script>

<template>
  <Teleport to="body">
    <div class="dialog-backdrop" @click.self="close">
      <section ref="panel" class="dialog" :class="{ wide }" role="dialog" aria-modal="true" aria-labelledby="dialog-title" :aria-describedby="description ? 'dialog-description' : undefined" tabindex="-1" @keydown="onKey">
        <header class="dialog-header">
          <div><h2 id="dialog-title">{{ title }}</h2><p v-if="description" id="dialog-description">{{ description }}</p></div>
          <button class="icon-button" :disabled="locked" aria-label="关闭弹窗" @click="close"><AppIcon name="close" /></button>
        </header>
        <slot />
      </section>
    </div>
  </Teleport>
</template>
