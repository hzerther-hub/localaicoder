// 桥接层：window.go.main.App.* 绑定调用 + window.runtime 事件订阅。
// 浏览器直开（无 Wails）时自动进入 mock 模式，界面可独立预览。
declare global {
  interface Window {
    go?: any
    runtime?: any
  }
}

export interface ProductInfo { name: string; title: string; features: Record<string, boolean> }
export interface ModelInfo {
  key: string; provider_name: string; model_id: string; display_name: string
  base_url: string; vision: boolean; reasoning: boolean; reasoning_effort: string
  reasoning_choices: string[]; context_window: number
  is_default: boolean; is_current: boolean; local: boolean
}
export interface SessionMeta { id: string; title: string; updated: number; workspace: string }
export interface LoadedSession { id: string; title: string; workspace: string; messages: any[]; notes: any[] }
export interface Prefs { language: string; standalone: boolean; font_size: number; workspace: string }
export interface DirEntry { name: string; isDir: boolean; path: string }
export interface CompletionItem { label: string; detail: string }
export interface DiagItem { line: number; mark: string; msg: string }
export interface AgentEvent { type: string; [k: string]: any }
export interface KBConfig { enabled: boolean; inject: boolean; auto: boolean; top_k: number; embedding: string; roots: string[] }

export interface LocalModelInfo {
  key: string; provider_id: string; provider_name: string; model_id: string
  display_name: string; base_url: string; vision: boolean
  start_command?: string; stop_command?: string
  state?: 'active' | 'loading' | 'stopped'
}

const w = () => window as any
export const isDesktop = () => !!w().go?.main?.App

async function call<T>(method: string, ...args: any[]): Promise<T> {
  const fn = w().go?.main?.App?.[method]
  if (fn) return fn(...args)
  return mock<T>(method, args)
}

export const api = {
  getProductInfo: () => call<ProductInfo>('GetProductInfo'),
  getPrefs: () => call<Prefs>('GetPrefs'),
  setPrefs: (p: Prefs) => call<void>('SetPrefs', p),
  getWorkspace: () => call<string>('GetWorkspace'),
  setWorkspace: (d: string) => call<string>('SetWorkspace', d),
  pickWorkspace: () => call<string>('PickWorkspace'),
  pickDirectory: () => call<string>('PickDirectory'),
  pickFiles: () => call<string[]>('PickFiles'),
  gitBranch: () => call<string>('GitBranch'),
  listModels: () => call<ModelInfo[]>('ListModels'),
  listProviders: () => call<any[]>('ListProviders'),
  saveProvider: (id: string, name: string, baseURL: string, apiKey: string, apiFormat: string) => call<boolean>('SaveProvider', id, name, baseURL, apiKey, apiFormat),
  addProviderModel: (id: string, modelID: string, vision: boolean) => call<boolean>('AddProviderModel', id, modelID, vision),
  setModelCapability: (key: string, vision?: boolean, reasoning?: boolean, effort?: string) => call<any>('SetModelCapability', key, vision, reasoning, effort),
  setCurrentModel: (k: string) => call<void>('SetCurrentModel', k),
  setReasoningEffort: (k: string, e: string) => call<void>('SetReasoningEffort', k, e),
  fetchEndpointModels: (base: string, key: string) => call<string[]>('FetchEndpointModels', base, key),
  addModels: (ids: string[], base: string, key: string, vision: boolean) => call<string[]>('AddModels', ids, base, key, vision),
  removeModel: (k: string) => call<boolean>('RemoveModel', k),
  sendMessage: (text: string, atts: any[]) => call<void>('SendMessage', text, atts),
  captureScreen: () => call<string>('CaptureScreen'),
  cropImage: (path: string, x: number, y: number, w: number, h: number) => call<string>('CropImage', path, x, y, w, h),
  readImageDataURL: (p: string) => call<string>('ReadImageDataURL', p),
  saveDataURL: (d: string) => call<string>('SaveDataURL', d),
  listLocalModels: () => call<LocalModelInfo[]>('ListLocalModels'),
  localModelAction: (key: string, action: 'start' | 'stop') => call<{ ok: boolean; msg?: string } | any>('LocalModelAction', key, action),
  getDispatchConfig: () => call<Record<string, any>>('GetDispatchConfig'),
  setDispatchConfig: (cfg: Record<string, any>) => call<void>('SetDispatchConfig', cfg),
  getMCPServers: () => call<Record<string, any>>('GetMCPServers'),
  saveMCPServer: (name: string, cfg: Record<string, any>) => call<void>('SaveMCPServer', name, cfg),
  deleteMCPServer: (name: string) => call<void>('DeleteMCPServer', name),
  reconnectMCP: () => call<void>('ReconnectMCP'),
  saveCacheSettings: (backend: string, llmTTL: number, toolTTL: number) => call<Record<string, any>>('SaveCacheSettings', backend, llmTTL, toolTTL),
  cacheInfo: () => call<Record<string, any>>('CacheInfo'),
  getUsage: () => call<Record<string, any>>('GetUsage'),
  clearCache: () => call<boolean>('ClearCache'),
  stopRun: () => call<void>('StopRun'),
  respondApproval: (id: string, allow: boolean) => call<void>('RespondApproval', id, allow),
  setPermissionMode: (m: string) => call<void>('SetPermissionMode', m),
  getPermissionMode: () => call<string>('GetPermissionMode'),
  newSession: () => call<string>('NewSession'),
  currentSession: () => call<string>('CurrentSession'),
  loadSession: (id: string) => call<LoadedSession | null>('LoadSession', id),
  listSessions: (ws: string, q: string) => call<SessionMeta[]>('ListSessions', ws, q),
  deleteSession: (id: string) => call<boolean>('DeleteSession', id),
  renameSession: (id: string, t: string) => call<boolean>('RenameSession', id, t),
  readFileText: (p: string) => call<string>('ReadFileText', p),
  writeFileText: (p: string, c: string) => call<void>('WriteFileText', p, c),
  listDir: (rel: string) => call<DirEntry[]>('ListDir', rel),
  deletePath: (p: string) => call<{ ok: boolean; msg?: string } | any>('DeletePath', p),
  lspComplete: (p: string, t: string, l: number, c: number) => call<CompletionItem[]>('LspComplete', p, t, l, c),
  lspDiag: (p: string, t: string) => call<DiagItem[]>('LspDiag', p, t),
  lspServerStatus: (p: string) => call<{ supported: boolean; lang?: string; server?: string; available?: boolean; install_cmd?: string }>('LspServerStatus', p),
  lspInstall: (p: string) => call<{ ok: boolean; output: string }>('LspInstall', p),
  termStart: (cwd: string) => call<string>('TermStart', cwd),
  termWrite: (id: string, data: string) => call<void>('TermWrite', id, data),
  termResize: (id: string, cols: number, rows: number) => call<void>('TermResize', id, cols, rows),
  termStop: (id: string) => call<void>('TermStop', id),
  getKBConfig: () => call<KBConfig>('GetKBConfig'),
  setKBConfig: (c: KBConfig) => call<void>('SetKBConfig', c),
  buildKB: (force: boolean) => call<void>('BuildKB', force),
  kbStats: () => call<Record<string, any>>('KBStats'),
  kbQuery: (q: string) => call<any[]>('KBQuery', q),
  rebuildIndex: () => call<Record<string, any>>('RebuildIndex'),
}

export function onEvent(name: string, cb: (data: any) => void) {
  const rt = w().runtime
  if (rt?.EventsOn) {
    rt.EventsOn(name, cb)
    return () => {}
  }
  // mock 模式：agent:event 等由 mockRun 模拟
  mockListeners(name, cb)
  return () => {}
}

/* ---------------- Mock 模式（浏览器预览） ---------------- */

const mockState = {
  models: <ModelInfo[]>[
    { key: 'deepseek/deepseek-chat', provider_name: 'DeepSeek', model_id: 'deepseek-chat', display_name: 'DeepSeek Chat', base_url: 'https://api.deepseek.com/v1', vision: false, reasoning: false, reasoning_effort: '', reasoning_choices: [], context_window: 128000, is_default: true, is_current: true, local: false },
    { key: 'deepseek/deepseek-reasoner', provider_name: 'DeepSeek', model_id: 'deepseek-reasoner', display_name: 'DeepSeek Reasoner', base_url: 'https://api.deepseek.com/v1', vision: false, reasoning: true, reasoning_effort: 'high', reasoning_choices: ['', 'low', 'medium', 'high'], context_window: 128000, is_default: false, is_current: false, local: false },
    { key: 'qwen38-local/qwen3.8-27b-q8', provider_name: '本地 Qwen', model_id: 'qwen3.8-27b-q8', display_name: 'Qwen3.8-27B (本地)', base_url: 'http://127.0.0.1:8097/v1', vision: false, reasoning: false, reasoning_effort: '', reasoning_choices: [], context_window: 131072, is_default: false, is_current: false, local: true },
  ],
  current: 'deepseek/deepseek-chat',
  mode: 'always',
  sessions: <SessionMeta[]>[
    { id: 'a1b2c3d4e5f6', title: 'Go 内核移植计划', updated: Date.now() - 3600e3, workspace: 'D:\\localai_code' },
    { id: 'b2c3d4e5f6a1', title: 'LSP 补全调试', updated: Date.now() - 86400e3, workspace: 'D:\\localai_code' },
  ],
  ws: 'D:\\localai_code',
  branch: 'master',
}

const listeners: Record<string, ((d: any) => void)[]> = {}
function mockListeners(name: string, cb: (d: any) => void) {
  ;(listeners[name] ||= []).push(cb)
}
function emitMock(name: string, data: any) {
  ;(listeners[name] || []).forEach((cb) => cb(data))
}

async function mock<T>(method: string, args: any[]): Promise<T> {
  await sleep(60)
  const s = mockState
  switch (method) {
    case 'GetProductInfo':
      return { name: 'devtool_local', title: 'Local AI Studio', features: { editor: true, rag: true, dispatch: true, quant: false, zh_only: false } } as T
    case 'GetPrefs':
      return { language: 'zh', standalone: false, font_size: 14, workspace: s.ws } as T
    case 'SetPrefs': return undefined as T
    case 'GetWorkspace': return s.ws as T
    case 'SetWorkspace':
      s.ws = args[0]
      return s.ws as T
    case 'PickWorkspace': return 'D:\\projects\\demo' as T
    case 'PickDirectory': return 'D:\\docs' as T
    case 'PickFiles':
      return ['D:\\demo\\架构图.png', 'D:\\demo\\需求说明.md'] as T
    case 'CaptureScreen':
      return 'D:\\demo\\shot_20260828.png' as T
    case 'ListLocalModels':
      return s.models.filter((m) => m.local).map((m) => ({
        key: m.key, provider_id: m.key.split('/')[0], provider_name: m.provider_name,
        model_id: m.model_id, display_name: m.display_name, base_url: m.base_url,
        vision: m.vision, state: 'active',
      })) as T
    case 'LocalModelAction':
      return { ok: true } as T
    case 'GitBranch': return s.branch as T
    case 'ListModels':
      return s.models.map((m) => ({ ...m, is_current: m.key === s.current })) as T
    case 'SetCurrentModel':
      s.current = args[0]
      return undefined as T
    case 'SendMessage':
      mockRun(args[0], (args[1] as string[]) || [])
      return undefined as T
    case 'StopRun': return undefined as T
    case 'RespondApproval': return undefined as T
    case 'SetPermissionMode':
      s.mode = args[0]
      return undefined as T
    case 'GetPermissionMode': return s.mode as T
    case 'NewSession': return 'newsess12345' as T
    case 'CurrentSession': return 'a1b2c3d4e5f6' as T
    case 'ListSessions': return s.sessions as T
    case 'LoadSession':
      return { id: args[0], title: '历史会话', workspace: s.ws, messages: [], notes: [] } as T
    case 'DeleteSession': return true as T
    case 'ListDir':
      return [
        { name: 'cmd', isDir: true, path: 'cmd' },
        { name: 'internal', isDir: true, path: 'internal' },
        { name: 'desktop', isDir: true, path: 'desktop' },
        { name: 'go.mod', isDir: false, path: 'go.mod' },
        { name: 'README.md', isDir: false, path: 'README.md' },
      ] as T
    case 'ReadFileText':
      return '// 演示文件（mock 模式）\npackage main\n\nfunc main() {\n\tprintln("hello")\n}\n' as T
    case 'WriteFileText': return undefined as T
    case 'LspComplete':
      return [
        { label: 'println', detail: 'func(a ...any) int' },
        { label: 'print', detail: 'func(a ...any) int' },
        { label: 'main', detail: 'func()' },
      ] as T
    case 'LspDiag': return [] as T
    case 'GetKBConfig':
      return { enabled: true, inject: false, auto: true, top_k: 4, embedding: '', roots: ['D:\\company\\docs'] } as T
    case 'SetKBConfig': return undefined as T
    case 'KBStats': return { files: 128, chunks: 1840, db: 'kb/demo.db' } as T
    case 'KBQuery': return [] as T
    default:
      return undefined as T
  }
}

const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms))

const IMAGE_EXTS = ['.png', '.jpg', '.jpeg', '.gif', '.webp', '.bmp']
export function isImage(path: string): boolean {
  const i = path.lastIndexOf('.')
  return i >= 0 && IMAGE_EXTS.includes(path.slice(i).toLowerCase())
}

// mock 一次带工具调用的流式回复
let mockBusy = false
function mockRun(text: string, attachments: string[] = []) {
  if (mockBusy) return
  mockBusy = true
  emitMock('run:started', { sessionId: 'a1b2c3d4e5f6' })
  const attNote = attachments.length
    ? `\n\n> 已收到 ${attachments.length} 个附件：${attachments.join('、')}${attachments.some((a) => isImage(a)) ? '（含图片，将作为视觉输入）' : ''}\n`
    : ''
  const reply =
    (attNote || '') +
    '好的，我已经看过这个项目结构。整体如下：\n\n' +
    '1. **内核**（Go）：`agent / llm / tools / mcp / lsp / codeindex / codera`\n2. **CLI**：`localai chat`\n3. **桌面**：Wails + React\n\n' +
    '```go\nfunc main() {\n\tfmt.Println("Local AI Studio")\n}\n```\n\n> 以上为浏览器 mock 演示；在 Wails 桌面模式中，这里由真实模型流式输出。'
  const steps: Array<() => void> = [
    () => emitMock('agent:event', { type: 'tool_start', name: 'list_dir', args: { path: '.' } }),
    () => emitMock('agent:event', { type: 'tool_result', name: 'list_dir', result: 'cmd/\ninternal/\ndesktop/\ngo.mod\nREADME.md' }),
    () => emitMock('agent:event', { type: 'usage', usage: { prompt_tokens: 210, completion_tokens: 96, total_tokens: 306 }, total: { prompt_tokens: 210, completion_tokens: 96, total_tokens: 306, requests: 1 } }),
  ]
  let i = 0
  const run = async () => {
    for (const s of steps) {
      await sleep(500)
      s()
    }
    for (const ch of reply) {
      emitMock('agent:event', { type: 'text', delta: ch })
      await sleep(8)
    }
    mockBusy = false
    emitMock('run:finished', { sessionId: 'a1b2c3d4e5f6', error: '' })
  }
  run()
}
