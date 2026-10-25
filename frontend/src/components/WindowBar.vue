<script setup lang="ts">
import { onMounted, onUnmounted, ref } from 'vue'
import { WindowIsMaximised, WindowMinimise, WindowToggleMaximise, Quit } from '../../wailsjs/runtime/runtime'
import AppIcon from './AppIcon.vue'

const maximised = ref(false)
let observer: ResizeObserver | undefined
async function syncState() { maximised.value = await WindowIsMaximised() }
function hide() { Quit() } // Goes through Go's close-to-tray OnBeforeClose hook.
onMounted(() => {
  void syncState()
  observer = new ResizeObserver(() => { void syncState() })
  observer.observe(document.documentElement)
})
onUnmounted(() => observer?.disconnect())
</script>

<template>
  <header class="window-bar" aria-label="应用标题栏" @dblclick="WindowToggleMaximise">
    <div class="window-brand"><img src="/favicon.svg" width="24" height="24" alt="" draggable="false" /><span>OpenList<span class="brand-fs">FS</span></span></div>
    <span class="window-caption">存储连接工作台</span>
    <div class="window-controls" @dblclick.stop>
      <button aria-label="最小化" title="最小化" @click="WindowMinimise"><svg viewBox="0 0 16 16"><path d="M3 8h10" /></svg></button>
      <button :aria-label="maximised ? '还原窗口' : '最大化'" :title="maximised ? '还原窗口' : '最大化'" @click="WindowToggleMaximise"><svg viewBox="0 0 16 16"><path v-if="maximised" d="M5 5V3h8v8h-2 M3 5h8v8H3z" /><path v-else d="M3 3h10v10H3z" /></svg></button>
      <button class="window-close" aria-label="关闭到托盘" title="关闭到托盘" @click="hide"><AppIcon name="close" :size="16" /></button>
    </div>
  </header>
</template>
