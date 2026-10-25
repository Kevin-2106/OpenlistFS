import { computed, nextTick, onMounted, onUnmounted, reactive, ref, watch } from 'vue'
import { ClipboardSetText } from '../wailsjs/runtime/runtime'

export interface MountOptions {
  drive: string; network: boolean; readAheadMiB: number; chunkKiB: number
  cacheMiB: number; workers: number; ttlMinutes: number; writable: boolean; proxy: string; backend: string; ipFamily: string
}
interface Profile { url: string; token: string; username: string; password: string; options?: MountOptions }
export interface Session {
  mounted: boolean; mountpoint: string; server: string; profile?: string; uptimeSec: number
  readAtCalls: number; httpReqs: number; retries: number; bytesFetched: number; avgTtfbMs: number
  hitBytes: number; missBytes: number; hitRatio: number; ramInUse: number
  prefetchBytes: number; prefetchWaste: number; seekCancels: number
}
interface DriverStatus { ready: boolean; installed: boolean; version: string; architecture: string; dllPath: string; code: string; message: string }
interface CodeResult { name: string; code: string; error: string }
interface Api {
  GetProfiles(): Promise<{ names: string[]; active: string; autoMount: boolean }>
  GetProfile(name: string): Promise<Profile>; GetActiveConfig(): Promise<Profile>; GetOptions(): Promise<MountOptions>
  SaveProfile(name: string, profile: Profile): Promise<string>; SetActiveProfile(name: string): Promise<string>; DeleteProfile(name: string): Promise<string>
  GetAutostart(): Promise<boolean>; SetAutostart(value: boolean): Promise<string>; SetAutoMount(value: boolean): Promise<string>
  GetStatuses(): Promise<{ error: string; sessions: Session[] }>; Mount(options: MountOptions): Promise<string>; Unmount(drive: string): Promise<string>
  GetWinFspStatus(): Promise<DriverStatus>; GetWinFspInstallerStatus(): Promise<{ available: boolean; message: string }>
  InstallWinFsp(): Promise<string>; OpenWinFspDownload(): Promise<void>; OpenWinFspProject(): Promise<void>
  ImportProfileCode(code: string): Promise<CodeResult>; ExportProfileCode(name: string): Promise<CodeResult>
}
const api = () => (window as unknown as { go: { main: { App: Api } } }).go.main.App
const errorMessage = (error: unknown) => error instanceof Error ? error.message : String(error)
const defaults = (): MountOptions => ({ drive: 'O:', network: true, readAheadMiB: 64, chunkKiB: 512, cacheMiB: 256, workers: 3, ttlMinutes: 2, writable: false, proxy: 'none', backend: 'api', ipFamily: 'auto' })
export const backendName = (value?: string) => ({ api: 'OpenList', webdav: 'WebDAV', s3: 'S3' }[value || 'api'] || 'OpenList')
export function hostName(value: string) { try { return new URL(value).host } catch { return '未设置服务器' } }
export function fmtBytes(value: number) {
  if (!Number.isFinite(value) || value <= 0) return '0 B'
  const units = ['B', 'KB', 'MB', 'GB', 'TB'], index = Math.min(Math.floor(Math.log(value) / Math.log(1024)), 4)
  return `${(value / 1024 ** index).toFixed(index === 0 ? 0 : 1)} ${units[index]}`
}
export function fmtUptime(value: number) {
  const hours = Math.floor(value / 3600), minutes = Math.floor(value % 3600 / 60)
  return hours ? `${hours} 小时 ${minutes} 分钟` : minutes ? `${minutes} 分钟` : `${Math.max(0, Math.floor(value))} 秒`
}

export function useWorkspace() {
  const page = ref<'connections' | 'activity' | 'settings'>('connections')
  const editorTab = ref<'connection' | 'mount'>('connection')
  const loading = ref(true), busy = ref(false), action = ref(''), startupBusy = ref(false)
  const profiles = ref<{ name: string; host: string; backend: string; drive: string }[]>([])
  const active = ref(''), profileName = ref(''), isNew = ref(false)
  const cfg = reactive<Profile>({ url: '', token: '', username: '', password: '' })
  const opts = reactive(defaults()), proxyMode = ref('none'), proxyUrl = ref('')
  const authMode = ref<'password' | 'token'>('password'), showSecret = ref(false)
  const autoMount = ref(false), autostart = ref(false)
  const sessions = ref<Session[]>([]), pollError = ref(''), mountError = ref('')
  const notice = ref<{ kind: 'success' | 'error'; text: string } | null>(null)
  const fields = reactive<Record<string, string>>({})
  const driver = reactive<DriverStatus>({ ready: false, installed: false, version: '', architecture: '', dllPath: '', code: 'checking', message: '' })
  const installer = reactive({ available: false, message: '' }), checkingDriver = ref(false), installingDriver = ref(false), dependencyMsg = ref('')
  const codeDialog = ref<'import' | 'export' | null>(null), importCode = ref(''), exportCode = ref(''), codeMessage = ref(''), transferring = ref(false)
  const confirmation = ref<{ kind: 'discard' | 'delete'; title: string; description: string; run: () => Promise<void> } | null>(null)
  const saved = ref(''), traffic = ref<number[]>([]), rate = ref(0)
  let timer: number | undefined, noticeTimer: number | undefined, disposed = false, lastBytes = 0, lastSample = 0, lastSessionKeys = ''
  const payload = (): MountOptions => ({ ...opts, drive: opts.drive.toUpperCase(), proxy: proxyMode.value === 'custom' ? proxyUrl.value.trim() : proxyMode.value })
  const fingerprint = () => JSON.stringify({ name: profileName.value, cfg: { url: cfg.url, username: cfg.username, password: cfg.password, token: cfg.token }, options: payload(), auth: authMode.value })
  const dirty = computed(() => !!saved.value && fingerprint() !== saved.value)
  const selectedSession = computed(() => sessions.value.find(s => s.mountpoint.toUpperCase() === opts.drive.toUpperCase()))
  const totalBytes = computed(() => sessions.value.reduce((sum, s) => sum + s.bytesFetched, 0))
  const cacheRatio = computed(() => {
    const hit = sessions.value.reduce((sum, s) => sum + s.hitBytes, 0), miss = sessions.value.reduce((sum, s) => sum + s.missBytes, 0)
    return hit + miss ? (100 * hit / (hit + miss)).toFixed(1) : '—'
  })
  const canMount = computed(() => !loading.value && !busy.value && !installingDriver.value && !checkingDriver.value && driver.ready && !isNew.value && !dirty.value && !!cfg.url)
  const mountHint = computed(() => isNew.value ? '保存连接后即可挂载' : dirty.value ? '先保存修改，再挂载磁盘' : !driver.ready ? '请先处理挂载驱动' : !cfg.url ? '请填写服务器地址' : '')
  const title = computed(() => ({ connections: '连接与挂载', activity: '活动监控', settings: '应用设置' }[page.value]))
  function notify(text: string, kind: 'success' | 'error' = 'success') {
    window.clearTimeout(noticeTimer); notice.value = { text, kind }
    if (kind === 'success') noticeTimer = window.setTimeout(() => { notice.value = null }, 5000)
  }
  function applyOptions(value?: MountOptions) {
    Object.assign(opts, defaults(), value?.drive ? value : {})
 opts.ipFamily ||= 'auto'
    opts.backend ||= 'api'
    proxyMode.value = opts.proxy === 'system' ? 'system' : !opts.proxy || opts.proxy === 'none' ? 'none' : 'custom'
    proxyUrl.value = proxyMode.value === 'custom' ? opts.proxy : ''
  }
  function applyProfile(value: Profile, name: string, options: MountOptions) {
    Object.assign(cfg, { url: value.url || '', username: value.username || '', password: value.password || '', token: value.token || '' })
    applyOptions(value.options || options)
    active.value = name; profileName.value = name; isNew.value = !name
    authMode.value = cfg.token ? 'token' : 'password'; showSecret.value = false
    clearFields(); saved.value = fingerprint()
  }
  function clearFields() { for (const key of Object.keys(fields)) delete fields[key] }
  async function loadProfiles() {
    const [store, options, launch] = await Promise.all([api().GetProfiles(), api().GetOptions(), api().GetAutostart()])
    const data = await Promise.all((store.names || []).map(async name => ({ name, profile: await api().GetProfile(name) })))
    profiles.value = data.map(({ name, profile }) => ({ name, host: hostName(profile.url), backend: profile.options?.backend || options.backend || 'api', drive: profile.options?.drive || options.drive }))
    autoMount.value = !!store.autoMount; autostart.value = launch
    const current = data.find(p => p.name === store.active)
    applyProfile(current?.profile || await api().GetActiveConfig(), current?.name || '', options)
  }
  async function perform(label: string, operation: () => Promise<void>) {
    if (busy.value) return
    busy.value = true; action.value = label
    try { await operation() } catch (error) { notify(errorMessage(error), 'error') }
    finally { busy.value = false; action.value = '' }
  }
  function guardDraft(run: () => Promise<void>) {
    if (dirty.value) confirmation.value = { kind: 'discard', title: '放弃未保存的修改？', description: '当前连接的修改还没有保存。继续后，这些修改会丢失。', run }
    else void run()
  }
  function selectProfile(name: string) {
    if (busy.value || loading.value) return
    if (!isNew.value && name === active.value) { page.value = 'connections'; return }
    guardDraft(async () => {
      await perform('切换连接', async () => {
        const error = await api().SetActiveProfile(name)
        if (error) throw new Error(error)
        const [value, options] = await Promise.all([api().GetProfile(name), api().GetOptions()])
        applyProfile(value, name, options); page.value = 'connections'
      })
    })
  }
  function newProfile(copy = false) {
    if (busy.value || loading.value) return
    guardDraft(async () => {
      if (!copy) {
        Object.assign(cfg, { url: '', username: '', password: '', token: '' }); applyOptions(defaults()); authMode.value = 'password'
        const free = 'OPQRSTUVWXYZDEFGHIJKLMN'.split('').find(letter => !sessions.value.some(s => s.mountpoint.toUpperCase() === `${letter}:`))
        if (free) opts.drive = `${free}:`
      }
      profileName.value = copy ? `${profileName.value} 副本` : ''
      isNew.value = true; editorTab.value = 'connection'; page.value = 'connections'; showSecret.value = false
      clearFields(); saved.value = fingerprint()
    })
  }
  function validate() {
    clearFields()
    if (!profileName.value.trim()) fields.name = '给连接起一个名称。'
    else if ([...profileName.value.trim()].length > 128 || /[\r\n\0]/.test(profileName.value)) fields.name = '名称需为 1–128 个字符，且不能换行。'
    else if (isNew.value && profiles.value.some(p => p.name === profileName.value.trim())) fields.name = '这个名称已存在，请换一个名称。'
    try { const u = new URL(cfg.url); if (!['https:', 'http:'].includes(u.protocol) || !u.hostname || u.hash) throw new Error() }
    catch { fields.url = '请输入完整的 HTTP 或 HTTPS 服务器地址。' }
    if (!['auto', 'ipv4', 'ipv6'].includes(opts.ipFamily)) fields.ipFamily = '选择自动、IPv4 或 IPv6。'
    if (!/^[a-z]:$/i.test(opts.drive)) fields.drive = '选择一个有效的盘符。'
    for (const [key, min, max] of [['readAheadMiB', 0, 4096], ['chunkKiB', 64, 8192], ['cacheMiB', 16, 16384], ['workers', 1, 32], ['ttlMinutes', 1, 1440]] as const) {
      if (!Number.isInteger(opts[key]) || opts[key] < min || opts[key] > max) fields[key] = `请输入 ${min}–${max} 的整数。`
    }
    if (proxyMode.value === 'custom') {
      try { const u = new URL(proxyUrl.value); if (!['http:', 'https:', 'socks5:', 'socks5h:'].includes(u.protocol) || !u.hostname) throw new Error() }
      catch { fields.proxy = '请输入 HTTP(S) 或 SOCKS5 代理地址。' }
    }
    if (fields.name || fields.url) editorTab.value = 'connection'
    else if (Object.keys(fields).length) editorTab.value = 'mount'
    return !Object.keys(fields).length
  }
  async function saveProfile() {
    if (!validate()) { await nextTick(); document.querySelector<HTMLElement>('[aria-invalid="true"]')?.focus(); return }
    await perform('保存连接', async () => {
      const name = profileName.value.trim()
      const value = { url: cfg.url.trim(), username: cfg.username, password: cfg.password, token: opts.backend === 'api' && authMode.value === 'token' ? cfg.token : '', options: payload() }
      const error = await api().SaveProfile(name, value)
      if (error) throw new Error(error)
      await loadProfiles(); notify('连接已保存，可以挂载磁盘。')
    })
  }
  function deleteProfile() {
    if (!active.value || isNew.value) return
    const name = active.value
    confirmation.value = { kind: 'delete', title: `删除「${name}」？`, description: '将先卸载此连接的所有已挂载磁盘，再删除配置。删除后无法恢复此配置。', run: async () => {
      await perform('卸载并删除连接', async () => { const error = await api().DeleteProfile(name); await poll(); if (error) throw new Error(error); await loadProfiles(); notify('连接已卸载并删除。') })
    } }
  }
  async function confirmAction() {
    const run = confirmation.value?.run; confirmation.value = null
    if (run) await run()
  }
  async function poll() {
    try {
      const value = await api().GetStatuses()
      if (disposed) return
      sessions.value = value.sessions || []; mountError.value = value.error || ''; pollError.value = ''
      const now = Date.now(), bytes = totalBytes.value, keys = sessions.value.map(s => `${s.mountpoint}:${s.profile}`).sort().join('|')
      rate.value = lastSample && keys === lastSessionKeys && bytes >= lastBytes ? (bytes - lastBytes) / ((now - lastSample) / 1000) : 0
      if (sessions.value.length) traffic.value = [...traffic.value.slice(-59), rate.value]
      else traffic.value = []
      lastBytes = bytes; lastSample = now; lastSessionKeys = keys
    } catch { if (!disposed) { pollError.value = '暂时无法更新磁盘状态，请稍后重试。'; rate.value = 0 } }
  }
  async function schedulePoll() {
    if (disposed) return
    if (!document.hidden) await poll()
    if (!disposed) timer = window.setTimeout(schedulePoll, 1000)
  }
  async function doMount() {
    if (!canMount.value) return
    await perform('挂载磁盘', async () => {
      await refreshDriver()
      if (!driver.ready) { notify('挂载驱动尚未就绪，请在应用设置中处理。', 'error'); return }
      const error = await api().Mount(payload())
      if (error) throw new Error(error)
      await poll(); notify(`${opts.drive} 已挂载，可在文件管理器中访问。`)
    })
  }
  async function doUnmount(drive: string) {
    await perform('卸载磁盘', async () => { const error = await api().Unmount(drive); if (error) throw new Error(error); await poll(); notify(`${drive} 已卸载。`) })
  }
  async function refreshDriver() {
    checkingDriver.value = true
    try { const [status, bundled] = await Promise.all([api().GetWinFspStatus(), api().GetWinFspInstallerStatus()]); Object.assign(driver, status); Object.assign(installer, bundled) }
    catch { driver.ready = false; driver.code = 'check_failed'; driver.message = '无法检查挂载驱动，请重新检测。' }
    finally { checkingDriver.value = false }
  }
  async function installDriver() {
    if (busy.value || installingDriver.value || sessions.value.length) return
    installingDriver.value = true; dependencyMsg.value = ''
    try { const error = await api().InstallWinFsp(); dependencyMsg.value = error || '安装或修复已完成。若挂载仍失败，请从托盘退出后重新启动应用。'; await refreshDriver() }
    catch (error) { dependencyMsg.value = errorMessage(error) }
    finally { installingDriver.value = false }
  }
  async function toggleStartup(key: 'autostart' | 'autoMount') {
    if (startupBusy.value) return
    startupBusy.value = true
    const target = key === 'autostart' ? autostart : autoMount, next = !target.value
    try { const error = await (key === 'autostart' ? api().SetAutostart(next) : api().SetAutoMount(next)); if (error) throw new Error(error); target.value = next; notify('启动设置已更新。') }
    catch (error) { notify(errorMessage(error), 'error') } finally { startupBusy.value = false }
  }
  function openImport() {
    guardDraft(async () => {
      await perform('准备导入', async () => {
        if (dirty.value) await loadProfiles()
        codeDialog.value = 'import'; importCode.value = ''; exportCode.value = ''; codeMessage.value = ''
      })
    })
  }
  async function openExport() {
    if (!active.value || isNew.value || dirty.value) { notify('请先保存当前连接，再导出配置码。', 'error'); return }
    codeDialog.value = 'export'; exportCode.value = ''; codeMessage.value = ''; transferring.value = true
    try { const result = await api().ExportProfileCode(active.value); if (result.error) throw new Error(result.error); exportCode.value = result.code }
    catch (error) { codeMessage.value = errorMessage(error) } finally { transferring.value = false }
  }
  function closeCode() { if (transferring.value) return; codeDialog.value = null; importCode.value = ''; exportCode.value = ''; codeMessage.value = '' }
  async function importProfileCode() {
    if (!importCode.value.trim() || transferring.value) return
    transferring.value = true; codeMessage.value = ''
    try {
      const result = await api().ImportProfileCode(importCode.value)
      if (result.error) throw new Error(result.error)
      await loadProfiles(); transferring.value = false; closeCode(); page.value = 'connections'; notify(`已新增「${result.name}」，检查盘符后即可挂载。`)
    } catch (error) { codeMessage.value = errorMessage(error) } finally { transferring.value = false }
  }
  async function copyCode() {
    try { codeMessage.value = await ClipboardSetText(exportCode.value) ? '配置码已复制。' : '复制失败，请在下方选择并手动复制。' }
    catch { codeMessage.value = '复制失败，请在下方选择并手动复制。' }
  }
  function openOfficial() { void api().OpenWinFspDownload().catch(error => notify(errorMessage(error), 'error')) }
  function openProject() { void api().OpenWinFspProject().catch(error => notify(errorMessage(error), 'error')) }
  watch(() => opts.backend, () => { showSecret.value = false })
  onMounted(async () => {
    await Promise.all([refreshDriver(), perform('载入连接', loadProfiles)])
    loading.value = false
    if (!disposed) void schedulePoll()
  })
  onUnmounted(() => { disposed = true; window.clearTimeout(timer); window.clearTimeout(noticeTimer) })
  return { page, editorTab, loading, busy, action, profiles, active, profileName, isNew, cfg, opts, proxyMode, proxyUrl, authMode, showSecret, autoMount, autostart, startupBusy, sessions, pollError, mountError, notice, fields, dirty, driver, installer, checkingDriver, installingDriver, dependencyMsg, codeDialog, importCode, exportCode, codeMessage, transferring, confirmation, traffic, rate, selectedSession, totalBytes, cacheRatio, canMount, mountHint, title, selectProfile, newProfile, saveProfile, deleteProfile, confirmAction, doMount, doUnmount, refreshDriver, installDriver, toggleStartup, openImport, openExport, closeCode, importProfileCode, copyCode, openOfficial, openProject }
}
