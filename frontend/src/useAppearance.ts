import { computed, onUnmounted, ref, watch } from 'vue'
import { WindowSetDarkTheme, WindowSetLightTheme, WindowSetSystemDefaultTheme } from '../wailsjs/runtime/runtime'

export type Appearance = 'light' | 'dark' | 'system'
const key = 'openlistfs.appearance'
function readPreference(): Appearance {
  try { const value = localStorage.getItem(key); if (value === 'light' || value === 'dark') return value } catch { /* Storage can be disabled. */ }
  return 'system'
}

export function useAppearance() {
  const appearance = ref<Appearance>(readPreference())
  const media = window.matchMedia('(prefers-color-scheme: dark)')
  const systemDark = ref(media.matches)
  const resolvedAppearance = computed(() => appearance.value === 'system' ? systemDark.value ? 'dark' : 'light' : appearance.value)
  const onChange = (event: MediaQueryListEvent) => { systemDark.value = event.matches }
  media.addEventListener('change', onChange)
  watch(resolvedAppearance, value => { document.documentElement.dataset.theme = value }, { immediate: true })
  watch(appearance, value => {
    try { localStorage.setItem(key, value) } catch { /* The current session still changes. */ }
    try {
      if (value === 'system') WindowSetSystemDefaultTheme()
      else if (value === 'dark') WindowSetDarkTheme()
      else WindowSetLightTheme()
    } catch { /* The browser preview has no native window. */ }
  }, { immediate: true })
  onUnmounted(() => media.removeEventListener('change', onChange))
  return { appearance, resolvedAppearance }
}
