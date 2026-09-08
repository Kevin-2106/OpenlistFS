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

const profilesUi = reactive({
  names: [] as string[],
  active: '',
  autoMount: false,
  autostart: false,
  newProfileName: '',
})

const opts = reactive({
  drive: 'O:',
  network: true,
  readAheadMiB: 64,
  chunkKiB: 512,
  cacheMiB: 256,
  workers: 3,
  ttlMinutes: 2,
  writable: false,
  backend: 'api', // 'api' (OpenList native) | 'webdav'
  proxyMode: 'none', // 'none' | 'system' | 'custom'
  proxyUrl: '',
})

// The backend takes one combined string; the UI splits it for the dropdown.
function proxySetting(): string {
  return opts.proxyMode === 'custom' ? opts.proxyUrl.trim() : opts.proxyMode
}

function applyProxySetting(v: string) {
  if (v === 'system') { opts.proxyMode = 'system'; opts.proxyUrl = '' }
  else if (v && v !== 'none') { opts.proxyMode = 'custom'; opts.proxyUrl = v }
  else { opts.proxyMode = 'none'; opts.proxyUrl = '' }
}

// Payload for Mount/SaveOptions: backend fields only.
function mountPayload() {
  const p: any = { ...opts, proxy: proxySetting(), backend: opts.backend || 'api' }
  delete p.proxyMode
  delete p.proxyUrl
  return p
}

const status = reactive<Status>({
  mounted: false, mountpoint: '', server: '', error: '', uptimeSec: 0,
  readAtCalls: 0, httpReqs: 0, retries: 0, bytesFetched: 0, avgTtfbMs: 0,
  hitBytes: 0, missBytes: 0, hitRatio: 0, ramInUse: 0,
  prefetchBytes: 0, prefetchWaste: 0, seekCancels: 0,
})

const busy = ref(false)
const saveMsg = ref('')
const mountedNow = ref(false)

async function loadProfiles() {
  const p = await wails().GetProfiles()
  profilesUi.names = p.names || []
  profilesUi.active = p.active || ''
  profilesUi.autoMount = !!p.autoMount
  profilesUi.autostart = await wails().GetAutostart()
  if (profilesUi.active) await loadProfile(profilesUi.active)
  else await loadLegacy()
}

async function loadLegacy() {
  const c = await wails().GetActiveConfig()
  cfg.url = c.url || ''
  cfg.token = c.token || ''
  cfg.username = c.username || ''
  cfg.password = c.password || ''
}

async function loadProfile(name: string) {
  const p = await wails().GetProfile(name)
  cfg.url = p.url || ''
  cfg.token = p.token || ''
  cfg.username = p.username || ''
  cfg.password = p.password || ''
}

async function switchProfile() {
  if (!profilesUi.active) return
  const err = await wails().SetActiveProfile(profilesUi.active)
  if (err) { status.error = err; return }
  await loadProfile(profilesUi.active)
}

async function saveProfile() {
  const name = profilesUi.newProfileName || profilesUi.active
  if (!name) { saveMsg.value = 'profile name required'; return }
  const err = await wails().SaveProfile(name, { ...cfg })
  saveMsg.value = err || 'saved ✓'
  if (!err) {
    profilesUi.active = name
    if (!profilesUi.names.includes(name)) profilesUi.names.push(name)
    setTimeout(() => (saveMsg.value = ''), 2500)
  }
}

async function deleteProfile() {
  if (!profilesUi.active) return
  const err = await wails().DeleteProfile(profilesUi.active)
  if (err) { status.error = err; return }
  profilesUi.newProfileName = ''
  await loadProfiles()
}

async function toggleAutostart() {
  profilesUi.autostart = !profilesUi.autostart
  const err = await wails().SetAutostart(profilesUi.autostart)
  if (err) { profilesUi.autostart = !profilesUi.autostart; status.error = err }
}

async function toggleAutoMount() {
  profilesUi.autoMount = !profilesUi.autoMount
  const err = await wails().SetAutoMount(profilesUi.autoMount)
  if (err) { profilesUi.autoMount = !profilesUi.autoMount; status.error = err }
}

async function saveOptions() {
  const err = await wails().SaveOptions(mountPayload())
  saveMsg.value = err || 'options saved ✓'
  if (!err) setTimeout(() => (saveMsg.value = ''), 2500)
}

async function doMount() {
  busy.value = true
  try {
    const err = await wails().Mount(mountPayload())
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
  loadProfiles()
  wails().GetOptions().then((o: any) => {
    if (o && o.drive) {
      Object.assign(opts, o)
      applyProxySetting(o.proxy || 'none')
      opts.backend = o.backend || 'api'
    }
  })
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
      <div class="field" style="max-width: 220px">
        <label>Profile</label>
        <select v-model="profilesUi.active" @change="switchProfile">
          <option v-for="n in profilesUi.names" :key="n" :value="n">{{ n }}</option>
          <option v-if="!profilesUi.names.length" value="">(default from .env)</option>
        </select>
      </div>
      <div class="field" style="max-width: 220px">
        <label>Save as / rename to</label>
        <input type="text" v-model="profilesUi.newProfileName" placeholder="profile name" />
      </div>
      <button class="secondary" @click="saveProfile" :disabled="busy">Save profile</button>
      <button class="danger" @click="deleteProfile" :disabled="busy || !profilesUi.active">Delete</button>
      <span class="msg">{{ saveMsg }}</span>
    </div>
    <div class="row" style="align-items: end">
      <div class="field" style="flex: 2">
        <label>Token (alternative to user/password)</label>
        <input type="password" v-model="cfg.token" autocomplete="off" />
      </div>
      <button class="secondary" @click="toggleAutostart">
        {{ profilesUi.autostart ? '开机自启: ON' : '开机自启: OFF' }}
      </button>
      <button class="secondary" @click="toggleAutoMount">
        {{ profilesUi.autoMount ? '启动自动挂载: ON' : '启动自动挂载: OFF' }}
      </button>
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
    <div class="check">
      <input type="checkbox" id="wr" v-model="opts.writable" :disabled="mountedNow" />
      <label for="wr">Writable (needs server write permission; uploads on file close)</label>
    </div>
    <div class="row" style="align-items: end">
      <div class="field" style="max-width: 220px">
        <label>Backend</label>
        <select v-model="opts.backend" :disabled="mountedNow">
          <option value="api">OpenList API</option>
          <option value="webdav">WebDAV</option>
        </select>
      </div>
      <div class="field" style="max-width: 180px">
        <label>Proxy (API + downloads)</label>
        <select v-model="opts.proxyMode" :disabled="mountedNow">
          <option value="none">无代理 (direct)</option>
          <option value="system">系统代理</option>
          <option value="custom">自定义…</option>
        </select>
      </div>
      <div class="field" v-if="opts.proxyMode === 'custom'" style="flex: 2">
        <label>Proxy URL</label>
        <input type="text" v-model="opts.proxyUrl" placeholder="http://127.0.0.1:7897 或 socks5://host:port" :disabled="mountedNow" />
      </div>
    </div>
    <p v-if="opts.backend === 'webdav'" class="sub" style="margin: 6px 0 0">
      WebDAV 模式：使用 Username/Password（忽略 token）；服务器 URL 无路径时自动追加 /dav
    </p>
    <div class="row" style="margin-top: 14px; align-items: center">
      <button v-if="!mountedNow" @click="doMount" :disabled="busy">Mount</button>
      <button v-else class="danger" @click="doUnmount" :disabled="busy">Unmount</button>
      <button class="secondary" @click="saveOptions" :disabled="mountedNow">Save options</button>
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
