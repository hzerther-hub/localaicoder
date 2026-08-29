// 全局状态：聊天事件流、模型、会话、编辑器、UI。
import { create } from 'zustand'
import { api, onEvent, AgentEvent, ModelInfo, SessionMeta, Prefs, RunInfo } from './bridge'

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
  reasoning: number   // 推理思考 tokens（服务端报告时才有）
  cost: number        // 本次会话累计费用（USD，仅官方定价模型）
  costTotal: number   // 全应用累计费用
}

// 任务步骤条（计划先行：模型经 todo_write 一次性给出全部步骤后逐条执行；
// 兼容无计划时的工具驱动模式）
export interface Step {
  id: number
  name: string
  title: string
  status: 'wait' | 'run' | 'done' | 'deny'
  planned?: boolean
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
  runningSessionId: string
  runs: RunInfo[]   // 进行中/暂停的后台任务（侧栏运行标记）
  steps: Step[]
  sessionStart: number   // 当前会话起始时间戳（耗时统计）
  attSent: number        // 本次会话累计附件数
  visionSent: number     // 其中识图（图片）附件数
  sessionTokens: number  // 本次会话累计 tokens
  turnCount: number      // 本次会话轮次（发送次数）
  lastThroughput: number // 上一轮输出吞吐（tokens/s；0 = 未知）
  turnStart: number      // 内部：本轮开始时间戳
  round: number          // 当前任务进行中的工具轮次（agent 循环）
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
  showSkillsPanel: boolean
  setShowSkillsPanel: (v: boolean) => void
  showChangesPanel: boolean
  setShowChangesPanel: (v: boolean) => void
  balance: { ok: boolean; total: string; currency: string }
  capturing: boolean // 正在截取屏幕（portal 可能需授权/数秒）
  showEditor: boolean
  setShowEditor: (v: boolean) => void
  editorFile: string | null
  init: () => Promise<void>
  setLang: (l: 'zh' | 'en') => void
  send: (text: string) => void
  stop: (sessionId?: string) => void
  pause: (sessionId?: string) => void
  resume: (sessionId?: string) => void
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
  shotPath: string | null // 截图原始文件路径（裁剪用；shotSrc 为 dataURL 仅显示）
  annotateSrc: string | null
  previewSrc: string | null
  setPreviewSrc: (v: string | null) => void
  confirmShot: (r: { x: number; y: number; w: number; h: number }) => Promise<void>
  cancelShot: () => void
  notice: (text: string) => void
  toggleStandalone: () => void
}

let nextId = 1
const emptyUsage: Usage = { prompt: 0, completion: 0, total: 0, cached: 0, requests: 0, reasoning: 0, cost: 0, costTotal: 0 }

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
  runningSessionId: '',
  runs: [],
  steps: [],
  sessionStart: Date.now(),
  attSent: 0,
  visionSent: 0,
  sessionTokens: 0,
  turnCount: 0,
  lastThroughput: 0,
  turnStart: 0,
  round: 0,
  usage: { ...emptyUsage },
  attachments: [],
  shotSrc: null,
  shotPath: null,
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
  showSkillsPanel: false,
  setShowSkillsPanel: (v) => set({ showSkillsPanel: v }),
  showChangesPanel: false,
  setShowChangesPanel: (v) => set({ showChangesPanel: v }),
  balance: { ok: false, total: '', currency: '' },
  capturing: false,
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
    api.getBalance().then((b) => { if (b?.ok) set({ balance: { ok: true, total: b.total || '', currency: b.currency || '' } }) })

    onEvent('agent:event', (e: AgentEvent) => {
      const st = get()
      // 后台会话事件分流：非当前会话的事件不进入本会话的 items/steps 渲染，
      // 但仍更新 runs 状态（该会话在后台运行中）。
      const evSid = e?.sessionId || st.sessionId
      const isCurrent = evSid === st.sessionId
      if (!isCurrent) {
        // 仅同步运行列表状态（暂停/恢复在 run:* 事件处理），不渲染内容
        return
      }
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
          // todo_write：计划先行 —— 一次性给出全部步骤，整单替换（不产生工具卡片）
          if (e.name === 'todo_write') {
            const todos = Array.isArray(e.args?.todos) ? e.args.todos : []
            if (todos.length) set({ steps: stepPlan(todos) })
            break
          }
          const items = [...st.items]
          items.push({
            id: nextId++, kind: 'tool', text: '', reasoning: '',
            tool: {
              id: nextId, name: e.name,
              args: JSON.stringify(e.args || {}, null, 2),
              result: '', done: false,
            },
          })
          // 已有计划（todo_write）时步骤由计划驱动，不再按工具追加
          const hasPlan = st.steps.some((s) => s.planned)
          set({ items, steps: hasPlan ? st.steps : stepStart(st.steps, e.name, e.args || {}) })
          break
        }
        case 'tool_result': {
          if (e.name === 'todo_write') break // 计划工具本身不打进度
          const items = [...st.items]
          for (let i = items.length - 1; i >= 0; i--) {
            const it = items[i]
            if (it.kind === 'tool' && it.tool && it.tool.name === e.name && !it.tool.done) {
              it.tool = { ...it.tool, result: String(e.result ?? ''), done: true }
              break
            }
          }
          const hasPlan = st.steps.some((s) => s.planned)
          set({ items, steps: hasPlan ? st.steps : stepDone(st.steps, e.name) })
          break
        }
        case 'tool_denied':
          set({
            items: [...st.items, { id: nextId++, kind: 'tool', text: '', reasoning: '',
              tool: { id: nextId, name: e.name, args: '', result: '用户拒绝', done: true } }],
            steps: stepDeny(st.steps, e.name),
          })
          break
        case 'usage': {
          const u = e.usage || {}
          const t = e.total || {}
          set({ usage: {
            prompt: num(t.prompt_tokens ?? u.prompt_tokens),
            completion: num(t.completion_tokens ?? u.completion_tokens),
            total: num(t.total_tokens ?? u.total_tokens),
            cached: num(t.cached_tokens ?? u.cached_tokens),
            reasoning: num(t.reasoning_tokens ?? u.reasoning_tokens),
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
        case 'round':
          set({ round: num(e.n) })
          break
      }
    })

    onEvent('run:started', (d) => {
      const sid = d?.sessionId || ''
      const st = get()
      // 更新后台任务列表
      const runs = (st.runs || []).filter((r) => r.sessionId !== sid)
      runs.push({ sessionId: sid, model: d?.model || '', paused: false, start: Date.now() })
      set({ runs })
      // 仅当启动的是当前会话时才进入 running 态
      if (sid === st.sessionId || !sid) {
        set({ running: true, runningSessionId: sid, turnStart: Date.now(), round: 0 })
      }
    })
    onEvent('run:finished', (d) => {
      const finSid = d?.sessionId || get().sessionId
      const st = get()
      // 从后台任务列表移除该会话
      set({ runs: (st.runs || []).filter((r) => r.sessionId !== finSid) })
      // 仅当前会话结束时才重置 running 态并结算统计
      if (finSid !== st.sessionId) {
        get().refreshSessions()
        return
      }
      // 整轮结束：任务步骤条消失（对齐 Python 版 _clear_todo）；
      // 会话累计 tokens 与输出吞吐按本轮实际用量结算。
      const durSec = st.turnStart > 0 ? (Date.now() - st.turnStart) / 1000 : 0
      set({
        running: false, approval: null, steps: [], runningSessionId: '',
        sessionTokens: st.sessionTokens + st.usage.total,
        lastThroughput: durSec > 0.5 && st.usage.completion > 0
          ? Math.round(st.usage.completion / durSec)
          : 0,
      })
      api.getUsage().then((u) => {
        set({ usage: {
          prompt: num(u.prompt_tokens), completion: num(u.completion_tokens),
          total: num(u.total_tokens), cached: num(u.cached_tokens),
          reasoning: num(u.reasoning_tokens),
          requests: num(u.requests), cost: num(u.cost_usd),
          costTotal: get().usage.costTotal + num(u.cost_usd),
        } })
      })
      api.getBalance().then((b) => { if (b?.ok) set({ balance: { ok: true, total: b.total || '', currency: b.currency || '' } }) })
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
    const imgs = atts.filter((a) => typeof a === 'string' && isImgPath(a)).length
    set({
      items: [...get().items, {
        id: nextId++, kind: 'user', reasoning: '',
        text: text + (atts.length ? `\n\n📎 ${attPaths.join('  ')}` : ''),
      }],
      attachments: [],
      // 附件/识图计数（统计条用）+ 轮次
      attSent: get().attSent + atts.length,
      visionSent: get().visionSent + imgs,
      turnCount: get().turnCount + 1,
      // 发送后立即可见"处理中…"占位步骤（首个工具启动时被替换）
      steps: [{ id: nextId++, name: '…', title: t('处理中…', 'Working…'), status: 'run' }],
    })
    api.sendMessage(text, atts)
  },

  stop: () => api.stopRun(get().sessionId),
  pause: (sessionId?: string) => api.pauseRun(sessionId ?? get().sessionId),
  resume: (sessionId?: string) => api.resumeRun(sessionId ?? get().sessionId),

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
    set({ items: [], usage: { ...emptyUsage }, sessionStart: Date.now(), attSent: 0, visionSent: 0, steps: [], sessionTokens: 0, turnCount: 0, lastThroughput: 0 })
    get().refreshSessions()
  },

  loadSession: (id) => {
    set({ sessionId: id, sessionStart: Date.now(), attSent: 0, visionSent: 0, steps: [], sessionTokens: 0, turnCount: 0, lastThroughput: 0 })
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
    // 微信式流程（全平台统一）：抓全屏 → 全屏遮罩拉框选区 → 裁剪入附件。
    // 显示用 dataURL（webview 不允许直接加载磁盘 file:// 路径），
    // 裁剪用原始文件路径（shotPath），二者分离。
    if (get().capturing) return
    set({ capturing: true })
    try {
      const p = await api.captureScreen()
      if (!p) {
        get().notice(t('截图失败：未能捕获屏幕画面（可尝试安装 grim，或在系统设置中允许截图）',
          'Screenshot failed: unable to capture screen (try installing grim or allow screenshots in system settings)'))
        return
      }
      const dataUrl = await api.readImageDataURL(p)
      if (!dataUrl) {
        get().notice(t('截图失败：图片读取失败', 'Screenshot failed: unable to read image'))
        return
      }
      set({ shotPath: p, shotSrc: dataUrl })
    } finally {
      set({ capturing: false })
    }
  },

  setPreviewSrc: (v) => set({ previewSrc: v }),

  confirmShot: async (r) => {
    const path = get().shotPath
    set({ shotSrc: null, shotPath: null })
    if (!path) return
    // 微信式：框选完成直接入附件；标注按需（附件条 ✏️ 按钮）
    // 注意：裁剪用原始文件路径（shotPath），非 dataURL
    const cropped = await api.cropImage(path, r.x, r.y, r.w, r.h)
    if (cropped) set({ attachments: [...get().attachments, cropped] })
  },

  cancelShot: () => set({ shotSrc: null, shotPath: null }),

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

// ---------------- 任务步骤（工具驱动，对齐 Python 版 todo 步骤条） ----------------

// toolTitle 生成步骤标题：优先展示常用参数值，否则截断 JSON。
export function toolTitle(name: string, args: Record<string, unknown>): string {
  const keys = ['path', 'file', 'query', 'command', 'pattern', 'url', 'dir', 'glob', 'server', 'tool', 'model', 'q']
  for (const k of keys) {
    const v = args[k]
    if (typeof v === 'string' && v.trim()) {
      const s = v.trim()
      return `${name} ${s.length > 48 ? s.slice(0, 48) + '…' : s}`
    }
  }
  const json = JSON.stringify(args)
  if (json === '{}') return name
  const s = `${name} ${json}`
  return s.length > 60 ? s.slice(0, 60) + '…' : s
}

// stepPlan todo_write 计划先行：整单替换步骤清单（每项标题截断 80 字）。
function stepPlan(todos: any[]): Step[] {
  return todos.slice(0, 20).map((td: any, i: number) => {
    const status = td?.status === 'completed' ? 'done'
      : td?.status === 'in_progress' ? 'run' : 'wait'
    return {
      id: nextId++,
      name: 'todo',
      title: String(td?.title || `步骤 ${i + 1}`).slice(0, 80),
      status,
      planned: true,
    } as Step
  })
}

// stepStart 工具启动：移除"处理中…"占位并追加进行中步骤。
function stepStart(steps: Step[], name: string, args: Record<string, unknown>): Step[] {
  const next = steps.filter((s) => s.name !== '…')
  next.push({ id: nextId++, name, title: toolTitle(name, args), status: 'run' })
  return next
}

// stepDone 工具完成：从后往前找同名进行中步骤打勾（✅）。
function stepDone(steps: Step[], name: string): Step[] {
  for (let i = steps.length - 1; i >= 0; i--) {
    if (steps[i].name === name && steps[i].status === 'run') {
      const next = [...steps]
      next[i] = { ...steps[i], status: 'done' }
      return next
    }
  }
  return steps
}

// stepDeny 工具被拒：同名进行中步骤标 ✗；找不到（如审批前拒绝）则追加一条。
function stepDeny(steps: Step[], name: string): Step[] {
  for (let i = steps.length - 1; i >= 0; i--) {
    if (steps[i].name === name && steps[i].status === 'run') {
      const next = [...steps]
      next[i] = { ...steps[i], status: 'deny' }
      return next
    }
  }
  return [...steps, { id: nextId++, name, title: `${name}（${t('用户拒绝', 'denied')}）`, status: 'deny' }]
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
