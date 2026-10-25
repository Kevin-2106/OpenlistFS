<script setup lang="ts">
import { computed, nextTick, ref, watch } from 'vue'
import AppIcon from './components/AppIcon.vue'
import WindowBar from './components/WindowBar.vue'
import AppDialog from './components/AppDialog.vue'
import EndfieldSelect from './components/EndfieldSelect.vue'
import { backendName, fmtBytes, fmtUptime, hostName, useWorkspace } from './useWorkspace'
import { useAppearance } from './useAppearance'

const { appearance } = useAppearance()

const {
  page, editorTab, loading, busy, action, profiles, active, profileName, isNew, cfg, opts,
  proxyMode, proxyUrl, authMode, showSecret, autoMount, autostart, startupBusy, sessions,
  pollError, mountError, notice, fields, dirty, driver, installer, checkingDriver, installingDriver,
  dependencyMsg, codeDialog, importCode, exportCode, codeMessage, transferring, confirmation,
  traffic, rate, selectedSession, totalBytes, cacheRatio, canMount, mountHint, title,
  selectProfile, newProfile, saveProfile, deleteProfile, confirmAction, doMount, doUnmount,
  refreshDriver, installDriver, toggleStartup, openImport, openExport, closeCode,
  importProfileCode, copyCode, openOfficial, openProject,
} = useWorkspace()
const driveLetters = 'DEFGHIJKLMNOPQRSTUVWXYZ'.split('').map(letter => `${letter}:`)
const advancedOpen = ref(false)
const mobileConnections = ref(false)
const categories = { connections: 'WORKSPACE', activity: 'MONITOR', settings: 'PREFERENCES' }
function tabKey(e: KeyboardEvent) {
 if (!['ArrowLeft', 'ArrowRight', 'Home', 'End'].includes(e.key)) return
 e.preventDefault(); editorTab.value = e.key === 'Home' ? 'connection' : e.key === 'End' ? 'mount' : editorTab.value === 'connection' ? 'mount' : 'connection'
 nextTick(() => document.getElementById(`${editorTab.value}-tab`)?.focus())
}
const content = ref<HTMLElement>()
watch([page, active, isNew], () => nextTick(() => { if (content.value) content.value.scrollTop = 0 }))
watch(() => Object.keys(fields).join(','), () => {
  if (['ipFamily', 'readAheadMiB', 'cacheMiB', 'chunkKiB', 'workers', 'ttlMinutes'].some(key => fields[key])) advancedOpen.value = true
})
const nav = [{ key: 'connections', icon: 'grid', label: '连接与挂载' }, { key: 'activity', icon: 'activity', label: '活动监控' }, { key: 'settings', icon: 'settings', label: '应用设置' }] as const
const graphPath = computed(() => {
  const samples = traffic.value, max = Math.max(65536, ...samples)
  return samples.map((value, index) => `${index ? 'L' : 'M'} ${samples.length <= 1 ? 0 : index / (samples.length - 1) * 600} ${94 - value / max * 80}`).join(' ')
})
const occupiedByOther = computed(() => !!selectedSession.value && selectedSession.value.profile !== active.value)
const driveStatus = computed(() => selectedSession.value ? occupiedByOther.value ? '盘符使用中' : '已挂载' : busy.value && action.value === '挂载磁盘' ? '正在挂载' : '未挂载')
const setPage = (value: typeof page.value) => { page.value = value }
</script>

<template>
  <WindowBar />
  <div class="app-shell" :inert="!!codeDialog || !!confirmation || undefined">
    <div class="spatial-background" aria-hidden="true"><div class="space-contours" /><div class="space-blueprint" /></div>
    <aside class="sidebar" aria-label="主导航">
      <nav class="main-nav">
        <button v-for="item in nav" :key="item.key" :class="{ active: page === item.key }" :aria-current="page === item.key ? 'page' : undefined" @click="setPage(item.key)">
          <AppIcon :name="item.icon" :size="18" /><span>{{ item.label }}</span>
          <span v-if="item.key === 'activity' && sessions.length" class="nav-count ef-data">{{ sessions.length }}</span>
        </button>
      </nav>
      <button class="mobile-connection-toggle" :aria-expanded="mobileConnections" aria-controls="saved-connections" @click="mobileConnections = !mobileConnections"><AppIcon name="folder" :size="18" /><span>{{ profileName || '我的连接' }}</span><span class="ef-data">{{ profiles.length }}</span><AppIcon name="down" :size="16" /></button>
      <div class="connection-index" :class="{ expanded: mobileConnections }" id="saved-connections"><div class="sidebar-section-heading"><span>我的连接 <small class="ef-data">{{ profiles.length }}</small></span><button class="icon-button" aria-label="新建连接" :disabled="busy || loading" @click="newProfile()"><AppIcon name="plus" :size="17" /></button></div>
      <div class="profile-list" aria-label="已保存的连接">
        <div v-if="loading" class="sidebar-empty"><span class="spinner" />正在载入连接</div>
        <button v-for="profile in profiles" :key="profile.name" class="profile-item" :class="{ selected: !isNew && active === profile.name }" :disabled="busy" @click="selectProfile(profile.name); mobileConnections = false">
          <span class="profile-item-icon"><AppIcon :name="profile.backend === 's3' ? 'folder' : 'globe'" :size="16" /></span>
          <span class="profile-item-text"><strong>{{ profile.name }}</strong><small class="ef-data">{{ profile.host }}</small></span>
          <span v-if="sessions.some(s => s.profile === profile.name)" class="status-dot blue" title="已挂载" />
        </button>
        <div v-if="!loading && !profiles.length" class="sidebar-empty">还没有连接<br />添加你的第一个存储空间</div>
        <button v-if="isNew && !loading" class="profile-item draft-item"><span class="profile-item-icon"><AppIcon name="plus" :size="16" /></span><span class="profile-item-text"><strong>{{ profileName || '新连接' }}</strong><small>尚未保存</small></span><span class="status-dot amber" /></button>
      </div>
      <div class="sidebar-import"><button class="text-button" :disabled="loading || busy" @click="openImport"><AppIcon name="import" :size="16" />导入配置码</button></div></div>
      <button class="sidebar-environment" @click="setPage('settings')">
        <span class="status-dot" :class="checkingDriver ? 'amber' : driver.ready ? 'blue' : 'amber'" />
        <span>{{ checkingDriver ? '正在检查环境' : driver.ready ? '挂载环境就绪' : '挂载环境需要处理' }}<small>{{ driver.ready ? `WinFsp ${driver.version.split('.').slice(0, 2).join('.')}` : '点击查看并修复' }}</small></span>
        <AppIcon name="chevron" :size="14" />
      </button>
    </aside>

    <div class="workspace">
      <main ref="content" class="main-content">
        <header class="page-heading"><div><span class="page-category" lang="en">{{ categories[page] }}</span><h1>{{ title }}</h1></div><button v-if="page === 'connections'" class="button secondary" :disabled="busy || loading" @click="newProfile()"><AppIcon name="plus" :size="16" />新建连接</button><span v-else-if="page === 'activity'" class="live-label"><span class="status-dot blue" />每秒更新</span></header>
        <div v-if="notice" class="notice" :class="notice.kind" :role="notice.kind === 'error' ? 'alert' : 'status'">
          <AppIcon :name="notice.kind === 'error' ? 'warning' : 'check'" :size="18" /><span>{{ notice.text }}</span><button class="icon-button" aria-label="关闭提示" @click="notice = null"><AppIcon name="close" :size="16" /></button>
        </div>
        <div v-if="pollError" class="notice error" role="alert"><AppIcon name="warning" :size="18" /><span>{{ pollError }}</span></div>
        <div v-if="mountError && notice?.text !== mountError" class="notice error" role="alert"><AppIcon name="warning" :size="18" /><span>{{ mountError }}</span></div>

        <template v-if="page === 'connections'">
          <div v-if="!driver.ready && !checkingDriver" class="environment-banner"><span class="banner-icon"><AppIcon name="shield" /></span><div><strong>挂载前，还需要准备一下环境</strong></div><button class="text-button" @click="setPage('settings')">处理环境<AppIcon name="arrow" :size="16" /></button></div>
          <div v-if="loading" class="loading-panel"><span class="spinner" />正在准备工作台…</div>
          <template v-else>
            <section class="connection-hero" aria-label="当前连接">
              <div class="drive-emblem" :class="{ mounted: selectedSession }"><span class="drive-caption">挂载盘符</span><strong class="ef-data">{{ opts.drive }}</strong><AppIcon name="drive" :size="22" /></div>
              <div class="connection-summary"><h2>{{ profileName || '新建连接' }}<span v-if="dirty" class="unsaved-label">未保存</span></h2><p class="ef-data"><span class="summary-host">{{ hostName(cfg.url) }}</span><span class="summary-backend"><span class="summary-separator">·</span>{{ backendName(opts.backend) }}</span></p><div class="connection-tags"><span class="state-tag" :class="{ connected: selectedSession }"><span class="status-dot" :class="selectedSession ? 'blue' : 'muted'" />{{ driveStatus }}</span><span class="quiet-tag"><AppIcon :name="opts.writable ? 'folder' : 'lock'" :size="12" />{{ opts.writable ? '允许写入' : '只读访问' }}</span></div></div>
              <div class="hero-actions"><button v-if="!selectedSession" class="button primary" :aria-busy="busy && action === '挂载磁盘'" :disabled="!canMount" :title="mountHint" @click="doMount"><AppIcon name="link" :size="17" /><span>{{ busy && action === '挂载磁盘' ? '正在挂载…' : '挂载磁盘' }}</span></button><button v-else class="button secondary" :disabled="busy" @click="doUnmount(opts.drive)"><AppIcon name="stop" :size="16" />{{ busy && action === '卸载磁盘' ? '正在卸载…' : `卸载 ${opts.drive}` }}</button><small v-if="!selectedSession && mountHint">{{ mountHint }}</small><small v-else-if="selectedSession">{{ occupiedByOther ? `由「${selectedSession.profile || '本地配置'}」使用` : `已运行 ${fmtUptime(selectedSession.uptimeSec)}` }}</small></div>
            </section>

            <section class="editor-panel" aria-label="连接配置">
              <div class="editor-tabs" role="tablist" aria-label="连接编辑器" @keydown="tabKey"><button id="connection-tab" :tabindex="editorTab === 'connection' ? 0 : -1" :class="{ selected: editorTab === 'connection' }" role="tab" :aria-selected="editorTab === 'connection'" aria-controls="connection-fields" @click="editorTab = 'connection'"><AppIcon name="globe" :size="16" />连接信息</button><button id="mount-tab" :tabindex="editorTab === 'mount' ? 0 : -1" :class="{ selected: editorTab === 'mount' }" role="tab" :aria-selected="editorTab === 'mount'" aria-controls="mount-fields" @click="editorTab = 'mount'"><AppIcon name="drive" :size="16" />挂载选项</button></div>
              <form class="connection-form" novalidate @submit.prevent="saveProfile">
                <fieldset :disabled="busy" class="editor-fieldset">
                  <div v-show="editorTab === 'connection'" id="connection-fields" role="tabpanel" aria-labelledby="connection-tab" class="tab-body">

                    <div class="backend-selector" role="group" aria-label="连接方式"><button v-for="backend in [{ value: 'api', name: 'OpenList', detail: '原生 API', icon: 'globe' }, { value: 'webdav', name: 'WebDAV', detail: '通用文件协议', icon: 'link' }, { value: 's3', name: 'S3', detail: '对象存储', icon: 'folder' }]" :key="backend.value" type="button" :class="{ selected: opts.backend === backend.value }" :aria-pressed="opts.backend === backend.value" @click="opts.backend = backend.value"><AppIcon :name="backend.icon" :size="21" /><span><strong>{{ backend.name }}</strong></span><span v-if="opts.backend === backend.value" class="backend-check"><AppIcon name="check" :size="12" /></span></button></div>
                    <div class="form-grid" :class="{ single: !isNew }">
                      <div v-if="isNew" class="field"><label for="profile-name">连接名称<span v-if="isNew" class="required" aria-hidden="true">*</span></label><input id="profile-name" v-model="profileName" :readonly="!isNew" :aria-invalid="!!fields.name" :aria-describedby="fields.name ? 'name-error' : undefined" placeholder="例如：我的云盘" maxlength="128" autocomplete="off" /><p v-if="fields.name" id="name-error" class="field-error">{{ fields.name }}</p></div>
                      <div class="field"><label for="server-url">{{ opts.backend === 's3' ? '端点 / 存储桶地址' : '服务器地址' }}<span class="required" aria-hidden="true">*</span></label><input id="server-url" v-model="cfg.url" type="url" :aria-invalid="!!fields.url" :aria-describedby="fields.url ? 'url-error' : opts.backend !== 'api' ? 'url-help' : undefined" :placeholder="opts.backend === 's3' ? 'https://s3.example.com/bucket' : 'https://openlist.example.com'" spellcheck="false" autocomplete="off" /><p v-if="fields.url" id="url-error" class="field-error">{{ fields.url }}</p><p v-else-if="opts.backend !== 'api'" id="url-help" class="field-help">{{ opts.backend === 'webdav' ? '可直接填写 WebDAV 路径；无路径时使用 /dav。' : opts.backend === 's3' ? '包含存储桶路径；可添加 ?region= 指定区域。' : '填写 OpenList 服务的完整地址。' }}</p></div>
                    </div>
                    <div class="auth-heading"><h4>{{ opts.backend === 's3' ? '访问凭据' : '身份验证' }}</h4><div v-if="opts.backend === 'api'" class="segmented" role="group" aria-label="身份验证方式"><button type="button" :class="{ selected: authMode === 'password' }" :aria-pressed="authMode === 'password'" @click="authMode = 'password'">账号密码</button><button type="button" :class="{ selected: authMode === 'token' }" :aria-pressed="authMode === 'token'" @click="authMode = 'token'">Token</button></div><span v-else-if="opts.backend === 's3'" class="field-help">可留空以匿名访问</span></div>
                    <div v-if="opts.backend !== 'api' || authMode === 'password'" class="form-grid">
                      <div class="field"><label for="username">{{ opts.backend === 's3' ? 'Access Key' : '用户名' }}</label><input id="username" v-model="cfg.username" autocomplete="off" :placeholder="opts.backend === 's3' ? '访问密钥 ID' : '输入用户名'" spellcheck="false" /></div>
                      <div class="field"><label for="password">{{ opts.backend === 's3' ? 'Secret Key' : '密码' }}</label><div class="secret-input"><input id="password" v-model="cfg.password" :type="showSecret ? 'text' : 'password'" autocomplete="off" :placeholder="opts.backend === 's3' ? '访问密钥' : '输入密码'" /><button type="button" class="icon-button" :aria-label="showSecret ? '隐藏凭据' : '显示凭据'" :aria-pressed="showSecret" @click="showSecret = !showSecret"><AppIcon :name="showSecret ? 'eyeOff' : 'eye'" :size="17" /></button></div></div>
                    </div>
                    <div v-else class="field"><label for="token">访问 Token</label><div class="secret-input"><input id="token" v-model="cfg.token" :type="showSecret ? 'text' : 'password'" autocomplete="off" placeholder="粘贴 OpenList Token" /><button type="button" class="icon-button" :aria-label="showSecret ? '隐藏凭据' : '显示凭据'" :aria-pressed="showSecret" @click="showSecret = !showSecret"><AppIcon :name="showSecret ? 'eyeOff' : 'eye'" :size="17" /></button></div></div>
                    <div class="inline-note"><AppIcon name="lock" :size="14" /><span>凭据以明文保存在本机。</span></div>
                  </div>

                  <div v-show="editorTab === 'mount'" id="mount-fields" role="tabpanel" aria-labelledby="mount-tab" class="tab-body">
                    <p v-if="selectedSession" class="field-help mounted-options-note">修改将在下次挂载时生效。</p>

                    <div class="form-grid"><div class="field"><label for="drive-letter">挂载盘符</label><EndfieldSelect id="drive-letter" v-model="opts.drive" :invalid="!!fields.drive" :options="(driveLetters.includes(opts.drive) ? driveLetters : [opts.drive, ...driveLetters]).map(drive => ({ value: drive, label: drive + (sessions.some(s => s.mountpoint.toUpperCase() === drive) ? ' · 使用中' : '') }))" /><p v-if="fields.drive" class="field-error">{{ fields.drive }}</p></div><div class="field"><label for="proxy-mode">网络代理</label><EndfieldSelect id="proxy-mode" v-model="proxyMode" :options="[{ value: 'none', label: '直接连接' }, { value: 'system', label: '使用系统代理' }, { value: 'custom', label: '自定义代理' }]" /></div></div>
                    <div v-if="proxyMode === 'custom'" class="field custom-proxy"><label for="proxy-url">代理地址</label><input id="proxy-url" v-model="proxyUrl" placeholder="http://127.0.0.1:7897 或 socks5://host:port" spellcheck="false" :aria-invalid="!!fields.proxy" /><p v-if="fields.proxy" class="field-error">{{ fields.proxy }}</p></div>
                    <label class="setting-row"><span class="setting-row-icon"><AppIcon name="globe" :size="18" /></span><span class="setting-row-text"><strong>显示为网络磁盘</strong><small>适合远程存储，减少文件管理器的预读取。</small></span><input class="switch" type="checkbox" v-model="opts.network" role="switch" /></label>
                    <label class="setting-row"><span class="setting-row-icon"><AppIcon :name="opts.writable ? 'folder' : 'lock'" :size="18" /></span><span class="setting-row-text"><strong>允许写入</strong><small>{{ opts.writable ? '文件关闭时上传修改，需要服务端写入权限。' : '只读模式，保护远端文件免于误操作。' }}</small></span><input class="switch" type="checkbox" v-model="opts.writable" role="switch" /></label>
                    <div class="advanced-section"><button type="button" class="advanced-toggle" :aria-expanded="advancedOpen" aria-controls="advanced-options" @click="advancedOpen = !advancedOpen"><AppIcon name="settings" :size="16" /><span>高级选项</span><small class="ef-data">{{ opts.ipFamily === 'auto' ? '自动 IP' : opts.ipFamily === 'ipv4' ? 'IPv4' : 'IPv6' }} · {{ opts.cacheMiB }} MiB 缓存</small><AppIcon name="down" :size="16" :class="{ rotated: advancedOpen }" /></button>
                      <div v-show="advancedOpen" id="advanced-options" class="advanced-body"><div class="field ip-family-field"><label for="ip-family">连接地址族</label><EndfieldSelect id="ip-family" v-model="opts.ipFamily" :invalid="!!fields.ipFamily" describedby="ip-family-help" :options="[{ value: 'auto', label: '自动选择 IPv4 / IPv6' }, { value: 'ipv4', label: '仅使用 IPv4' }, { value: 'ipv6', label: '仅使用 IPv6' }]" /><p id="ip-family-help" class="field-help">限制 API 与文件传输的本机连接，不会回退到另一地址族。{{ proxyMode !== 'none' ? '使用代理时仅限制到代理的连接，代理到远端由代理决定。' : '' }}</p><p v-if="fields.ipFamily" class="field-error">{{ fields.ipFamily }}</p></div><p class="field-help">预读窗口设为 0 可关闭预读缓存。</p><div class="form-grid advanced-grid"><div v-for="setting in [{ key: 'readAheadMiB' as const, label: '预读窗口', unit: 'MiB', min: 0, max: 4096 }, { key: 'cacheMiB' as const, label: '内存缓存', unit: 'MiB', min: 16, max: 16384 }, { key: 'chunkKiB' as const, label: '分块大小', unit: 'KiB', min: 64, max: 8192 }, { key: 'workers' as const, label: '并发下载', unit: '个', min: 1, max: 32 }, { key: 'ttlMinutes' as const, label: '元数据缓存', unit: '分钟', min: 1, max: 1440 }]" :key="setting.key" class="field"><label :for="setting.key">{{ setting.label }}</label><div class="unit-input"><input :id="setting.key" v-model.number="opts[setting.key]" class="ef-data" type="number" :min="setting.min" :max="setting.max" step="1" :aria-invalid="!!fields[setting.key]" /><span class="ef-data">{{ setting.unit }}</span></div><p v-if="fields[setting.key]" class="field-error">{{ fields[setting.key] }}</p></div></div></div>
                    </div>
                  </div>
                </fieldset>
                <footer class="editor-footer"><span class="editor-save-state"><span class="status-dot" :class="dirty || isNew ? 'amber' : 'blue'" />{{ dirty ? '有未保存的修改' : isNew ? '保存后即可挂载' : '所有修改已保存' }}</span><button v-if="!isNew" type="button" class="text-button" :disabled="busy" @click="newProfile(true)">另存为</button><button type="submit" class="button primary" :aria-busy="busy && action === '保存连接'" :disabled="busy || (!isNew && !dirty)"><span v-if="busy && action === '保存连接'" class="spinner" /><AppIcon v-else name="save" :size="16" />{{ busy && action === '保存连接' ? '正在保存…' : '保存连接' }}</button></footer>
              </form>
            </section>
            <div v-if="!isNew" class="connection-tools"><button class="text-button" :disabled="busy || dirty" @click="openExport"><AppIcon name="export" :size="15" />导出配置码</button><button class="text-button destructive" :disabled="busy" @click="deleteProfile"><AppIcon name="trash" :size="15" />删除连接</button></div>
            <section class="mounted-section"><div class="section-heading"><h3>已挂载磁盘<span class="number-label ef-data">{{ sessions.length }}</span></h3><button v-if="sessions.length" class="text-button" @click="setPage('activity')">查看活动<AppIcon name="arrow" :size="14" /></button></div><div v-if="!sessions.length" class="empty-inline"><span class="empty-icon"><AppIcon name="drive" :size="22" /></span><div><strong>暂无挂载磁盘</strong></div></div><div v-else class="mounted-list"><div v-for="session in sessions" :key="session.mountpoint" class="mounted-row"><div class="mini-drive ef-data">{{ session.mountpoint }}</div><div class="mounted-row-info"><strong>{{ session.profile || '本地配置' }}</strong><small class="ef-data">{{ hostName(session.server) }} · {{ fmtUptime(session.uptimeSec) }}</small></div><span class="mounted-row-stat ef-data">{{ fmtBytes(session.bytesFetched) }}</span><span class="state-tag connected"><span class="status-dot blue" />已挂载</span><button class="icon-button" :disabled="busy" :aria-label="`卸载 ${session.mountpoint}`" @click="doUnmount(session.mountpoint)"><AppIcon name="stop" :size="16" /></button></div></div></section>
          </template>
        </template>

        <template v-else-if="page === 'activity'">
          <div class="metrics-grid"><div class="metric"><span>挂载磁盘</span><strong class="ef-data">{{ sessions.length }}<small>个</small></strong></div><div class="metric"><span>累计下载</span><strong class="ef-data">{{ fmtBytes(totalBytes) }}</strong></div><div class="metric"><span>实时下载</span><strong class="ef-data">{{ fmtBytes(rate) }}<small>/s</small></strong></div><div class="metric"><span>缓存命中</span><strong class="ef-data">{{ cacheRatio }}<small v-if="cacheRatio !== '—'">%</small></strong></div></div>
          <section class="traffic-panel"><div class="section-heading"><div><h3>下载活动</h3><p class="field-help">最近 60 个采样点 · 前台运行时记录</p></div><span class="chart-current ef-data">{{ fmtBytes(rate) }}<small>/s</small></span></div><div v-if="sessions.length && traffic.length > 1" class="traffic-chart"><svg viewBox="0 0 600 100" preserveAspectRatio="none" role="img" aria-label="实际下载速率曲线"><defs><linearGradient id="traffic-fill" x1="0" y1="0" x2="0" y2="1"><stop offset="0%" stop-color="var(--ef-yellow)" stop-opacity=".18" /><stop offset="100%" stop-color="var(--ef-yellow)" stop-opacity="0" /></linearGradient></defs><path :d="`${graphPath} L 600 100 L 0 100 Z`" fill="url(#traffic-fill)" /><path :d="graphPath" fill="none" stroke="var(--ef-yellow)" stroke-width="2" vector-effect="non-scaling-stroke" /></svg><div class="chart-axis"><span>较早</span><span>现在</span></div></div><div v-else class="chart-empty"><AppIcon name="activity" :size="27" /><p>{{ sessions.length ? '正在收集活动数据…' : '挂载磁盘后，这里会显示实际下载活动。' }}</p></div></section>
          <div class="section-heading activity-heading"><h3>磁盘详情</h3></div>
          <div v-if="!sessions.length" class="empty-state"><div class="empty-orbit"><AppIcon name="drive" :size="32" /></div><h3>还没有正在运行的磁盘</h3><button class="button secondary" @click="setPage('connections')">前往连接与挂载<AppIcon name="arrow" :size="15" /></button></div>
          <section v-for="session in sessions" :key="session.mountpoint" class="session-panel"><header><div class="mini-drive ef-data">{{ session.mountpoint }}</div><div><h3>{{ session.profile || '本地配置' }}</h3><p class="ef-data">{{ hostName(session.server) }} · 运行 {{ fmtUptime(session.uptimeSec) }}</p></div><button class="button secondary small" :disabled="busy" @click="doUnmount(session.mountpoint)"><AppIcon name="stop" :size="14" />卸载</button></header><div class="session-metrics"><div><span>下载量</span><strong class="ef-data">{{ fmtBytes(session.bytesFetched) }}</strong></div><div><span>缓存命中</span><strong class="ef-data">{{ session.hitRatio.toFixed(1) }}<small>%</small></strong></div><div><span>平均首字节</span><strong class="ef-data">{{ session.avgTtfbMs.toFixed(0) }}<small>ms</small></strong></div><div><span>缓存占用</span><strong class="ef-data">{{ fmtBytes(session.ramInUse) }}</strong></div></div><details class="session-details"><summary>更多读取统计<AppIcon name="down" :size="14" /></summary><dl><div><dt>读取次数</dt><dd class="ef-data">{{ session.readAtCalls.toLocaleString() }}</dd></div><div><dt>HTTP 请求</dt><dd class="ef-data">{{ session.httpReqs.toLocaleString() }}</dd></div><div><dt>请求重试</dt><dd class="ef-data">{{ session.retries.toLocaleString() }}</dd></div><div><dt>预读下载</dt><dd class="ef-data">{{ fmtBytes(session.prefetchBytes) }}</dd></div><div><dt>未使用预读</dt><dd class="ef-data">{{ fmtBytes(session.prefetchWaste) }}</dd></div><div><dt>跳转取消</dt><dd class="ef-data">{{ session.seekCancels.toLocaleString() }}</dd></div></dl></details></section>
        </template>

        <template v-else>
          <section class="settings-panel appearance-panel">
            <h3 id="appearance-heading">外观</h3>
            <div class="theme-picker" role="radiogroup" aria-labelledby="appearance-heading">
              <label v-for="theme in [{ value: 'light', label: '浅色', icon: 'sun' }, { value: 'dark', label: '深色', icon: 'moon' }, { value: 'system', label: '跟随系统', icon: 'monitor' }]" :key="theme.value" :class="{ selected: appearance === theme.value }">
                <input v-model="appearance" type="radio" name="appearance" :value="theme.value" />
                <AppIcon :name="theme.icon" :size="17" /><span>{{ theme.label }}</span>
              </label>
            </div>
          </section>
          <section class="settings-panel"><header class="panel-heading"><span class="panel-icon"><AppIcon name="power" :size="19" /></span><div><h3>启动与后台</h3></div></header><label class="setting-row"><span class="setting-row-text"><strong>开机启动</strong><small>登录 Windows 后在托盘启动应用，并尝试挂载当前连接。</small></span><input type="checkbox" class="switch" role="switch" :checked="autostart" :disabled="startupBusy" @change="toggleStartup('autostart')" /></label><label class="setting-row"><span class="setting-row-text"><strong>启动时自动挂载</strong><small>手动打开应用时，也自动挂载当前保存的连接。</small></span><input type="checkbox" class="switch" role="switch" :checked="autoMount" :disabled="startupBusy" @change="toggleStartup('autoMount')" /></label><div class="panel-note"><AppIcon name="help" :size="15" /><span>关闭窗口后应用保留在托盘。彻底退出请使用托盘菜单「退出」。</span></div></section>
          <section class="settings-panel driver-panel"><header class="panel-heading"><span class="panel-icon"><AppIcon name="shield" :size="19" /></span><div><h3>挂载环境</h3></div><span class="state-tag" :class="{ connected: driver.ready }"><span class="status-dot" :class="driver.ready ? 'blue' : 'amber'" />{{ checkingDriver ? '检查中' : driver.ready ? '已就绪' : '需要处理' }}</span></header><div class="driver-info"><div class="driver-logo"><AppIcon name="drive" :size="28" /></div><div><strong>WinFsp</strong><p class="ef-data">{{ driver.version ? `${driver.version} · ${driver.architecture}` : '尚未检测到可用的驱动' }}</p></div><button class="button secondary small" :disabled="checkingDriver || installingDriver" @click="refreshDriver"><AppIcon name="refresh" :size="14" :class="{ spinning: checkingDriver }" />重新检测</button></div><div v-if="!driver.ready" class="driver-warning"><AppIcon name="warning" :size="17" /><div><strong>安装或修复后，即可挂载磁盘</strong><p>安装需要管理员权限。按 Windows 提示确认；必要时重新启动应用或系统。</p></div></div><div class="driver-actions"><button v-if="installer.available" class="button" :class="driver.ready ? 'secondary' : 'primary'" :disabled="busy || installingDriver || checkingDriver || !!sessions.length" @click="installDriver"><span v-if="installingDriver" class="spinner" /><AppIcon v-else name="shield" :size="16" />{{ installingDriver ? '正在安装 / 修复…' : driver.installed ? '修复 WinFsp' : '安装 WinFsp' }}</button><button v-else class="button secondary" @click="openOfficial"><AppIcon name="export" :size="15" />获取官方安装程序</button><button class="text-button" @click="openOfficial">官方安装说明<AppIcon name="arrow" :size="14" /></button></div><p v-if="sessions.length" class="field-help">修复前，请先卸载所有磁盘。</p><p v-if="dependencyMsg" class="dependency-result" role="status">{{ dependencyMsg }}</p><details class="driver-details"><summary>查看检测详情<AppIcon name="down" :size="14" /></summary><p>{{ driver.message || '等待检测完成。' }}</p><p v-if="driver.dllPath" class="file-path">{{ driver.dllPath }}</p><p v-if="installer.message">{{ installer.message }}</p></details></section>
          <section class="about-panel"><img class="brand-symbol" src="/favicon.svg" width="36" height="36" alt="" /><div><h3>OpenListFS</h3><p class="about-note">默认只读。写入功能为实验功能，修改在文件关闭时上传。</p></div><span class="version-label ef-data">v0.2.0</span></section><footer class="license-credit">WinFsp - Windows File System Proxy, Copyright (C) Bill Zissimopoulos<button class="text-button" @click="openProject">项目与许可<AppIcon name="arrow" :size="12" /></button></footer>
        </template>
      </main>
    </div>

    <AppDialog v-if="codeDialog" :title="codeDialog === 'import' ? '导入配置码' : '导出配置码'" :description="codeDialog === 'import' ? '把另一个设备上的连接，添加到这里。' : `分享「${active}」的已保存配置。`" :locked="transferring" wide @close="closeCode">
      <div class="code-warning"><AppIcon name="lock" :size="18" /><p>配置码包含密码或密钥，Base64 编码不提供加密。<strong>请只与可信的人分享。</strong></p></div>
      <div class="field code-field"><label :for="codeDialog === 'import' ? 'import-code' : 'export-code'">{{ codeDialog === 'import' ? '粘贴配置码' : '当前连接的配置码' }}</label><textarea v-if="codeDialog === 'import'" id="import-code" v-model="importCode" rows="5" placeholder="openlistfs:v1:…" spellcheck="false" autocomplete="off" :disabled="transferring" /><textarea v-else id="export-code" :value="exportCode" rows="5" readonly spellcheck="false" :placeholder="transferring ? '正在生成配置码…' : ''" @focus="($event.target as HTMLTextAreaElement).select()" /><p class="field-help">{{ codeDialog === 'import' ? '同名配置会自动编号。导入后不会自动挂载。' : '包含凭据、后端、代理、地址族和挂载参数。' }}</p></div>
      <p v-if="codeMessage" class="code-result" role="status">{{ codeMessage }}</p>
      <footer class="dialog-actions"><button class="button secondary" :disabled="transferring" @click="closeCode">关闭</button><button v-if="codeDialog === 'import'" class="button primary" :disabled="transferring || !importCode.trim()" @click="importProfileCode"><span v-if="transferring" class="spinner" /><AppIcon v-else name="import" :size="16" />{{ transferring ? '正在导入…' : '导入为新连接' }}</button><button v-else class="button primary" :disabled="transferring || !exportCode" @click="copyCode"><AppIcon name="copy" :size="16" />复制配置码</button></footer>
    </AppDialog>
    <AppDialog v-if="confirmation" :title="confirmation.title" :description="confirmation.description" @close="confirmation = null"><footer class="dialog-actions"><button class="button secondary" data-autofocus @click="confirmation = null">{{ confirmation.kind === 'delete' ? '取消' : '继续编辑' }}</button><button class="button" :class="confirmation.kind === 'delete' ? 'danger' : 'primary'" @click="confirmAction">{{ confirmation.kind === 'delete' ? '删除连接' : '放弃修改' }}</button></footer></AppDialog>
  </div>
</template>
