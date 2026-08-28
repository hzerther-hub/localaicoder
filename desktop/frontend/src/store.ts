// 全局状态：聊天事件流、模型、会话、编辑器、UI。
import { create } from 'zustand'
import { api, onEvent, AgentEvent, ModelInfo, SessionMeta, Prefs } from './bridge'

export interface ToolBlock {
  id: number
  name: string
  args: string
  result: string
  done: boolean
}

export interface ChatItem {
  id: number
  kind: 'user' | 'assistant' | 'tool'
  text: string
  reasoning: string
  atts?: Attachment[]
  tool?: ToolBlock
}

export interface Usage {
  prompt: number; completion: number; total: number; cached: number; requests: number
  cost: number        // 本次会话累计费用（USD，仅官方定价模型）
  costTotal: number   // 全应用累计费用
}

// 附件：文件路径 或 代码片段（编辑器选区）
export type Attachment = string | { kind: 'snippet'; path: string; start: number; end: number; text: string }

export function attLabel(a: Attachment): string {
  if (typeof a === 'string') {
    const i = a.lastIndexOf('\\') >= 0 ? a.lastIndexOf('\\') : a.lastIndexOf('/')
    return a.slice(i + 1)
  }
  const i = a.path.lastIndexOf('\\') >= 0 ? a.path.lastIndexOf('\\') : a.path.lastIndexOf('/')
  return `${a.path.slice(i + 1)}:${a.start}-${a.end}`
}

export function isImgPath(a: Attachment): boolean {
  if (typeof a !== 'string') return false
  return ['.png', '.jpg', '.jpeg', '.gif', '.webp', '.bmp'].some((e) => a.toLowerCase().endsWith(e))
}

interface UIState {
  product: { name: string; title: string; features: Record<string, boolean> }
  lang: 'zh' | 'en'
  prefs: Prefs | null
  models: ModelInfo[]
  currentModel: string
  mode: string
  workspace: string
  branch: string
  sessions: SessionMeta[]
  sessionId: string
  items: ChatItem[]
  running: boolean
  usage: Usage
  attachments: Attachment[]
  approval: { id: string; name: string; summary: string } | null
  showModelPanel: boolean
  setShowModelPanel: (v: boolean) => void
  showMCPPanel: boolean
  setShowMCPPanel: (v: boolean) => void
  showDispatchPanel: boolean
  setShowDispatchPanel: (v: boolean) => void
  showCachePanel: boolean
  setShowCachePanel: (v: boolean) => void
  showTerminal: boolean
  setShowTerminal: (v: boolean) => void
  showKBPanel: boolean
  setShowKBPanel: (v: boolean) => void
  showEditor: boolean
  setShowEditor: (v: boolean) => void
  editorFile: string | null
  init: () => Promise<void>
  setLang: (l: 'zh' | 'en') => void
  send: (text: string) => void
  stop: () => void
  respond: (allow: boolean) => void
  cycleMode: () => void
  setModel: (k: string) => void
  newSession: () => void
  loadSession: (id: string) => void
  refreshSessions: () => Promise<void>
  setWorkspace: (dir: string) => Promise<void>
  pickWorkspace: () => Promise<string | null>
  openFile: (path: string) => void
  addAttachment: (a: Attachment) => void
  removeAttachment: (i: number) => void
  captureAndAttach: () => Promise<void>
  shotSrc: string | null
  annotateSrc: string | null
  previewSrc: string | null
  setPreviewSrc: (v: string | null) => void
  confirmShot: (r: { x: number; y: number; w: number; h: number }) => Promise<void>
  cancelShot: () => void
  notice: (text: string) => void
  toggleStandalone: () => void
}

let nextId = 1
const emptyUsage: Usage = { prompt: 0, completion: 0, total: 0, cached: 0, requests: 0, cost: 0, costTotal: 0 }

export const useStore = create<UIState>((set, get) => ({
  product: { name: '', title: 'Local AI Studio', features: {} },
  lang: 'zh',
  prefs: null,
  models: [],
  currentModel: '',
  mode: 'always',
  workspace: '',
  branch: '',
  sessions: [],
  sessionId: '',
  items: [],
  running: false,
  usage: { ...emptyUsage },
  attachments: [],
  shotSrc: null,
  annotateSrc: null,
  previewSrc: null,
  approval: null,
  showModelPanel: false,
  setShowModelPanel: (v) => set({ showModelPanel: v }),
  showMCPPanel: false,
  setShowMCPPanel: (v) => set({ showMCPPanel: v }),
  showDispatchPanel: false,
  setShowDispatchPanel: (v) => set({ showDispatchPanel: v }),
  showCachePanel: false,
  setShowCachePanel: (v) => set({ showCachePanel: v }),
  showTerminal: false,
  setShowTerminal: (v) => set({ showTerminal: v }),
  showKBPanel: false,
  setShowKBPanel: (v) => set({ showKBPanel: v }),
  showEditor: false,
  setShowEditor: (v) => set({ showEditor: v }),
  editorFile: null,

  init: async () => {
    const product = await api.getProductInfo()
    const prefs = await api.getPrefs()
    const models = await api.listModels()
    const mode = await api.getPermissionMode()
    set({
      product,
      prefs,
      lang: prefs.language === 'en' ? 'en' : 'zh',
      models,
      currentModel: models.find((m) => m.is_current)?.key || models[0]?.key || '',
      mode: mode || 'always',
      workspace: prefs.workspace,
      // 编辑器特性开启时默认显示文件面板（文件列表 + LSP 编辑是核心能力）
      showEditor: product.features.editor !== false,
    })
    get().refreshSessions()
    api.currentSession().then((id) => set({ sessionId: id }))
    api.gitBranch().then((b) => set({ branch: b || '' }))

    onEvent('agent:event', (e: AgentEvent) => {
      const st = get()
      switch (e.type) {
        case 'text': {
          const items = [...st.items]
          const last = items[items.length - 1]
          if (last && last.kind === 'assistant') {
            last.text += e.delta
            set({ items })
          } else {
            items.push({ id: nextId++, kind: 'assistant', text: e.delta, reasoning: '' })
            set({ items })
          }
          break
        }
        case 'reasoning': {
          const items = [...st.items]
          const last = items[items.length - 1]
          if (last && last.kind === 'assistant' && !last.text) {
            last.reasoning += e.delta
            set({ items })
          }
          break
        }
        case 'tool_start': {
          const items = [...st.items]
          items.push({
            id: nextId++, kind: 'tool', text: '', reasoning: '',
            tool: {
              id: nextId, name: e.name,
              args: JSON.stringify(e.args || {}, null, 2),
              result: '', done: false,
            },
          })
          set({ items })
          break
        }
        case 'tool_result': {
          const items = [...st.items]
          for (let i = items.length - 1; i >= 0; i--) {
            const it = items[i]
            if (it.kind === 'tool' && it.tool && it.tool.name === e.name && !it.tool.done) {
              it.tool = { ...it.tool, result: String(e.result ?? ''), done: true }
              break
            }
          }
          set({ items })
          break
        }
        case 'tool_denied':
          set({ items: [...st.items, { id: nextId++, kind: 'tool', text: '', reasoning: '',
            tool: { id: nextId, name: e.name, args: '', result: '用户拒绝', done: true } }] })
          break
        case 'usage': {
          const u = e.usage || {}
          const t = e.total || {}
          set({ usage: {
            prompt: num(t.prompt_tokens ?? u.prompt_tokens),
            completion: num(t.completion_tokens ?? u.completion_tokens),
            total: num(t.total_tokens ?? u.total_tokens),
            cached: num(t.cached_tokens ?? u.cached_tokens),
            requests: num(t.requests ?? 1),
            cost: num(t.cost_usd),
            costTotal: get().usage.costTotal,
          } })
          break
        }
        case 'context_compact':
          set({ items: [...st.items, { id: nextId++, kind: 'assistant',
            text: `_${e.before} → ${e.after} tokens_`, reasoning: '' }] })
          break
        case 'model_switch':
          if (e.to?.key) set({ currentModel: e.to.key })
          break
      }
    })

    onEvent('run:started', () => set({ running: true }))
    onEvent('run:finished', () => {
      set({ running: false, approval: null })
      api.getUsage().then((u) => {
        set({ usage: {
          prompt: num(u.prompt_tokens), completion: num(u.completion_tokens),
          total: num(u.total_tokens), cached: num(u.cached_tokens),
          requests: num(u.requests), cost: num(u.cost_usd),
          costTotal: get().usage.costTotal + num(u.cost_usd),
        } })
      })
      get().refreshSessions()
    })
    onEvent('approval:request', (d) =>
      set({ approval: { id: d.id, name: d.name, summary: d.summary || '' } }))
    onEvent('model:changed', (k) => {
      api.listModels().then((models) => set({ models, currentModel: k }))
    })
    onEvent('workspace:changed', (ws) => {
      set({ workspace: ws })
      api.gitBranch().then((b) => set({ branch: b || '' }))
    })
  },

  setLang: (l) => {
    const prefs = get().prefs
    if (prefs) {
      api.setPrefs({ ...prefs, language: l })
      set({ prefs: { ...prefs, language: l }, lang: l })
    }
  },

  send: (text) => {
    if ((!text.trim() && get().attachments.length === 0) || get().running) return
    const atts = get().attachments
    const attPaths = atts.map(attLabel)
    set({
      items: [...get().items, {
        id: nextId++, kind: 'user', reasoning: '',
        text: text + (atts.length ? `\n\n📎 ${attPaths.join('  ')}` : ''),
      }],
      attachments: [],
    })
    api.sendMessage(text, atts)
  },

  stop: () => api.stopRun(),

  respond: (allow) => {
    const a = get().approval
    if (a) {
      api.respondApproval(a.id, allow)
      set({ approval: null })
    }
  },

  cycleMode: () => {
    const order = ['always', 'ask', 'readonly']
    const next = order[(order.indexOf(get().mode) + 1) % order.length]
    api.setPermissionMode(next)
    set({ mode: next })
  },

  setModel: (k) => {
    api.setCurrentModel(k)
    set({ currentModel: k })
  },

  newSession: () => {
    api.newSession().then((id) => set({ sessionId: id }))
    set({ items: [], usage: { ...emptyUsage } })
    get().refreshSessions()
  },

  loadSession: (id) => {
    set({ sessionId: id })
    api.loadSession(id).then((s) => {
      if (!s) return
      const items: ChatItem[] = []
      for (const m of s.messages || []) {
        if (m.role === 'user') {
          const c = typeof m.content === 'string' ? m.content : (m.content || []).filter((p: any) => p.type === 'text').map((p: any) => p.text).join('\n')
          if (c) items.push({ id: nextId++, kind: 'user', text: c, reasoning: '' })
        } else if (m.role === 'assistant' && m.content) {
          items.push({ id: nextId++, kind: 'assistant', text: m.content, reasoning: '' })
        }
      }
      set({ items, workspace: s.workspace || get().workspace })
    })
  },

  refreshSessions: async () => {
    const list = await api.listSessions('', '')
    set({ sessions: list || [] })
  },

  setWorkspace: async (dir) => {
    if (!dir) return
    const changed = dir !== get().workspace
    const ws = await api.setWorkspace(dir)
    set({ workspace: ws })
    const b = await api.gitBranch()
    set({ branch: b || '' })
    if (changed) get().newSession() // 新目录=新项目，自动开新会话
    get().refreshSessions()
  },

  pickWorkspace: async () => {
    const dir = await api.pickWorkspace()
    if (dir) await get().setWorkspace(dir)
    return dir
  },

  openFile: (path) => set({ showEditor: true, editorFile: path }),

  addAttachment: (a) => set({ attachments: [...get().attachments, a] }),

  removeAttachment: (i) => {
    const next = [...get().attachments]
    next.splice(i, 1)
    set({ attachments: next })
  },

  captureAndAttach: async () => {
    const p = await api.captureScreen()
    if (!p) return
    if (navigator.userAgent.includes('Windows')) {
      set({ shotSrc: p }) // 微信式：全屏遮罩框选后裁剪
    } else {
      // Linux/macOS：Tk 原方式——整屏抓取 → 标注面板 → 附件
      set({ annotateSrc: p })
    }
  },

  setPreviewSrc: (v) => set({ previewSrc: v }),

  confirmShot: async (r) => {
    const src = get().shotSrc
    set({ shotSrc: null })
    if (!src) return
    // 微信式：框选完成直接入附件；标注按需（附件条 ✏️ 按钮）
    const cropped = await api.cropImage(src, r.x, r.y, r.w, r.h)
    if (cropped) set({ attachments: [...get().attachments, cropped] })
  },

  cancelShot: () => set({ shotSrc: null }),

  // 系统级提示（斜杠命令反馈等）：作为 assistant meta 消息插入聊天
  notice: (text: string) => {
    set({ items: [...get().items, { id: nextId++, kind: 'assistant', text, reasoning: '' }] })
  },

  toggleStandalone: () => {
    const prefs = get().prefs
    if (!prefs) return
    const next = !prefs.standalone
    api.setPrefs({ ...prefs, standalone: next })
    set({ prefs: { ...prefs, standalone: next } })
  },
}))

function num(v: any): number {
  const n = Number(v)
  return Number.isFinite(n) ? n : 0
}

export const t = (zh: string, en: string) => (useStore.getState().lang === 'zh' ? zh : en)

// 图片附件 dataURL 缓存（缩略图/预览共用；绕过 webview file:// 限制）
const imgCache = new Map<string, string>()
export function cachedImageURL(path: string): Promise<string> {
  const hit = imgCache.get(path)
  if (hit) return Promise.resolve(hit)
  return api.readImageDataURL(path).then((d) => {
    if (d) imgCache.set(path, d)
    return d
  })
}
