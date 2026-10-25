<script setup lang="ts">
withDefaults(defineProps<{ name: string; size?: number }>(), { size: 20 })
const paths: Record<string, string> = {
  drive: 'M4 4h16v12H4z M4 16v4h16v-4 M7 18h.01 M10 18h.01 M7 8h10 M7 11h6',
  grid: 'M3 3h7v7H3z M14 3h7v7h-7z M3 14h7v7H3z M14 14h7v7h-7z',
  activity: 'M3 12h4l3-8 4 16 3-8h4',
  settings: 'M12 8a4 4 0 1 0 0 8 4 4 0 0 0 0-8 M9 3h6l1 3 3 1 2 5-2 5-3 1-1 3H9l-1-3-3-1-2-5 2-5 3-1z',
  plus: 'M12 5v14 M5 12h14',
  arrow: 'M5 12h14 M13 6l6 6-6 6',
  chevron: 'M9 5l7 7-7 7',
  down: 'M6 9l6 6 6-6',
  check: 'M5 12l4 4L19 6',
  close: 'M6 6l12 12 M6 18L18 6',
  link: 'M10 13a5 5 0 0 0 7 0l3-3a5 5 0 0 0-7-7l-2 2 M14 11a5 5 0 0 0-7 0l-3 3a5 5 0 0 0 7 7l2-2',
  globe: 'M21 12a9 9 0 1 0-18 0 9 9 0 0 0 18 0 M3 12h18 M12 3c5 5 5 13 0 18-5-5-5-13 0-18',
  shield: 'M12 3l8 3v6c0 5-8 9-8 9s-8-4-8-9V6z M8 12l3 3 5-6',
  copy: 'M9 9h12v12H9z M15 9V3H3v12h6',
  import: 'M12 3v12 M7 10l5 5 5-5 M4 15v6h16v-6',
  export: 'M12 15V3 M7 8l5-5 5 5 M4 15v6h16v-6',
  trash: 'M3 6h18 M9 6V3h6v3 M5 6l1 15h12l1-15 M10 10v7 M14 10v7',
  refresh: 'M20 7a9 9 0 1 0 1 8 M20 3v5h-5',
  eye: 'M2 12s4-7 10-7 10 7 10 7-4 7-10 7S2 12 2 12 M15 12a3 3 0 1 0-6 0 3 3 0 0 0 6 0',
  eyeOff: 'M3 3l18 18 M9 5c1-.3 2-.5 3-.5 6 0 10 7.5 10 7.5a20 20 0 0 1-3 4 M6 6a19 19 0 0 0-4 6s4 7 10 7c2 0 4-.7 5-1.5 M10 10a3 3 0 0 0 4 4',
  lock: 'M5 10h14v11H5z M8 10V7a4 4 0 0 1 8 0v3',
  clock: 'M21 12a9 9 0 1 0-18 0 9 9 0 0 0 18 0 M12 7v5l3 2',
  folder: 'M3 5h6l2 3h10v12H3z',
  warning: 'M12 3L2 21h20z M12 9v5 M12 17h.01',
  power: 'M12 2v10 M6 5a9 9 0 1 0 12 0',
  save: 'M4 3h13l4 4v14H3V3z M7 3v6h10V3 M7 21v-7h10v7',
  code: 'M8 7l-5 5 5 5 M16 7l5 5-5 5 M14 4l-4 16',
  help: 'M21 12a9 9 0 1 0-18 0 9 9 0 0 0 18 0 M9 9a3 3 0 1 1 4 3c-1 0-1 1-1 2 M12 17h.01',
  stop: 'M6 6h12v12H6z',
  sun: 'M12 8a4 4 0 1 0 0 8 4 4 0 0 0 0-8 M12 2v2 M12 20v2 M2 12h2 M20 12h2 M5 5l1.5 1.5 M17.5 17.5L19 19 M5 19l1.5-1.5 M17.5 6.5L19 5',
  moon: 'M20 15a9 9 0 0 1-11-11A9 9 0 1 0 20 15z',
  monitor: 'M3 4h18v13H3z M8 21h8 M12 17v4',
}
</script>

<template>
  <svg :width="size" :height="size" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.65" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true" class="app-icon">
    <path :d="paths[name] || paths.drive" />
  </svg>
</template>
