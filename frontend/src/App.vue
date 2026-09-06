<script setup lang="ts">
import { onMounted, onUnmounted, reactive, ref } from 'vue'

// Wails binds the Go App struct to window.go.main.App.
const wails = () => (window as any).go.main.App

interface Cfg {
  url: string
  token: string
  username: string
  password: string
}

interface Status {
  mounted: boolean
  mountpoint: string
  server: string
  error: string
  uptimeSec: number
  readAtCalls: number
  httpReqs: number
  retries: number
  bytesFetched: number
  avgTtfbMs: number
  hitBytes: number
  missBytes: number
  hitRatio: number
  ramInUse: number
  prefetchBytes: number
  prefetchWaste: number
  seekCancels: number
}

const cfg = reactive<Cfg>({ url: '', token: '', username: '', password: '' })
function string0() { return '' }

const opts = reactive({
  drive: 'O:',
  network: true,
  readAheadMiB: 64,
  chunkKiB: 512,
  cacheMiB: 256,
  workers: 3,
  ttlMinutes: 2,
})

const status = reactive<Status>({
  mounted: false, mountpoint: '', server: '', error: '', uptimeSec: 0,
  readAtCalls: 0, httpReqs: 0, retries: 0, bytesFetched: 0, avgTtfbMs: 0,
  hitBytes: 0, missBytes: 0, hitRatio: 0, ramInUse: 0,
  prefetchBytes: 0, prefetchWaste: 0, seekCancels: 0,
})

const busy = ref(false)
const saveMsg = ref('')
const mountedNow = ref(false)

async function loadConfig() {
  const c = await wails().GetConfig()
  cfg.url = c.url || ''
  cfg.token = c.token || ''
  cfg.username = c.username || ''
  cfg.password = c.password || ''
}

async function saveConfig() {
  saveMsg.value = await wails().SaveConfig({ ...cfg })
  if (!saveMsg.value) {
    saveMsg.value = 'saved ✓'
    setTimeout(() => (saveMsg.value = ''), 2500)
  }
}

async function doMount() {
  busy.value = true
  try {
    const err = await wails().Mount({ ...opts })
    if (err) status.error = err
  } finally {
    busy.value = false
  }
}

async function doUnmount() {
  busy.value = true
  try {
    await wails().Unmount()
  } finally {
    busy.value = false
  }
}

function fmtBytes(n: number): string {
  if (!n) return '0 B'
  const u = ['B', 'KiB', 'MiB', 'GiB', 'TiB']
  let i = 0
  let v = n
  while (v >= 1024 && i < u.length - 1) { v /= 1024; i++ }
  return `${v.toFixed(1)} ${u[i]}`
}

function fmtUptime(s: number): string {
  const h = Math.floor(s / 3600)
  const m = Math.floor((s % 3600) / 60)
  const sec = Math.floor(s % 60)
  return h > 0 ? `${h}h ${m}m` : m > 0 ? `${m}m ${sec}s` : `${sec}s`
}

let timer: number | undefined
onMounted(() => {
  loadConfig()
  timer = window.setInterval(async () => {
    const s = await wails().GetStatus()
    Object.assign(status, s)
    mountedNow.value = s.mounted
  }, 1000)
})
onUnmounted(() => window.clearInterval(timer))
</script>

<template>
  <h1>OpenListFS</h1>
  <p class="sub">read-only OpenList filesystem client · performance PoC · WinFsp</p>

  <div v-if="status.error" class="error">{{ status.error }}</div>

  <div class="card">
    <h2>Server</h2>
    <div class="row">
      <div class="field" style="flex: 2">
        <label>OPENLISTFS_URL</label>
        <input type="text" v-model="cfg.url" placeholder="https://example.com" />
      </div>
      <div class="field">
        <label>Username</label>
        <input type="text" v-model="cfg.username" autocomplete="off" />
      </div>
      <div class="field">
        <label>Password</label>
        <input type="password" v-model="cfg.password" autocomplete="off" />
      </div>
    </div>
    <div class="row" style="align-items: end">
      <div class="field">
        <label>Token (alternative to user/password)</label>
        <input type="password" v-model="cfg.token" autocomplete="off" />
      </div>
      <button class="secondary" @click="saveConfig" :disabled="busy">Save .env</button>
      <span class="msg">{{ saveMsg }}</span>
    </div>
  </div>

  <div class="card">
    <h2>Mount</h2>
    <div class="row">
      <div class="field" style="max-width: 120px">
        <label>Drive letter</label>
        <input type="text" v-model="opts.drive" :disabled="mountedNow" />
      </div>
      <div class="field" style="max-width: 140px">
        <label>Read-ahead (MiB, 0 = off)</label>
        <input type="number" v-model.number="opts.readAheadMiB" min="0" :disabled="mountedNow" />
      </div>
      <div class="field" style="max-width: 140px">
        <label>Chunk (KiB)</label>
        <input type="number" v-model.number="opts.chunkKiB" min="64" :disabled="mountedNow" />
      </div>
      <div class="field" style="max-width: 140px">
        <label>Cache (MiB)</label>
        <input type="number" v-model.number="opts.cacheMiB" min="16" :disabled="mountedNow" />
      </div>
      <div class="field" style="max-width: 120px">
        <label>Workers</label>
        <input type="number" v-model.number="opts.workers" min="1" max="8" :disabled="mountedNow" />
      </div>
    </div>
    <div class="check">
      <input type="checkbox" id="nw" v-model="opts.network" :disabled="mountedNow" />
      <label for="nw">Network drive mode (recommended — Explorer remote-share pipeline)</label>
    </div>
    <div class="row" style="margin-top: 14px; align-items: center">
      <button v-if="!mountedNow" @click="doMount" :disabled="busy">Mount</button>
      <button v-else class="danger" @click="doUnmount" :disabled="busy">Unmount</button>
      <span class="badge" :class="mountedNow ? 'on' : 'off'">
        {{ mountedNow ? `MOUNTED ${status.mountpoint}` : 'NOT MOUNTED' }}
      </span>
      <span v-if="mountedNow" class="msg">up {{ fmtUptime(status.uptimeSec) }} · {{ status.server }}</span>
    </div>
  </div>

  <div class="card" v-if="mountedNow">
    <h2>Session statistics</h2>
    <div class="stats">
      <div class="stat"><div class="k">ReadAt calls</div><div class="v">{{ status.readAtCalls }}</div></div>
      <div class="stat"><div class="k">HTTP requests</div><div class="v">{{ status.httpReqs }} <small style="color:#7c8698">(+{{ status.retries }} retry)</small></div></div>
      <div class="stat"><div class="k">Downloaded</div><div class="v">{{ fmtBytes(status.bytesFetched) }}</div></div>
      <div class="stat"><div class="k">Avg TTFB</div><div class="v">{{ status.avgTtfbMs.toFixed(0) }} ms</div></div>
      <div class="stat"><div class="k">Cache hit</div><div class="v">{{ status.hitRatio.toFixed(1) }}%</div></div>
      <div class="stat"><div class="k">Cache RAM</div><div class="v">{{ fmtBytes(status.ramInUse) }}</div></div>
      <div class="stat"><div class="k">Prefetch</div><div class="v">{{ fmtBytes(status.prefetchBytes) }}</div></div>
      <div class="stat"><div class="k">Prefetch waste</div><div class="v">{{ fmtBytes(status.prefetchWaste) }}</div></div>
      <div class="stat"><div class="k">Seek cancels</div><div class="v">{{ status.seekCancels }}</div></div>
    </div>
  </div>
</template>
