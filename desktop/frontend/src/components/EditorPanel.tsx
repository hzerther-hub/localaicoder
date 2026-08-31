// 编辑器面板：文件树 + 多标签 + CodeMirror 6 + LSP 智能补全/诊断（必做功能②）。
import { useEffect, useRef, useState } from 'react'
import { EditorView, keymap, Decoration, DecorationSet, ViewPlugin, ViewUpdate } from '@codemirror/view'
import { EditorState, StateEffect, StateField, Range } from '@codemirror/state'
import { basicSetup } from 'codemirror'
import { Compartment } from '@codemirror/state'
import { autocompletion, CompletionContext, CompletionResult } from '@codemirror/autocomplete'
import { syntaxHighlighting, HighlightStyle } from '@codemirror/language'
import { tags as t2 } from '@lezer/highlight'
import { python } from '@codemirror/lang-python'
import { javascript } from '@codemirror/lang-javascript'
import { go } from '@codemirror/lang-go'
import { markdown } from '@codemirror/lang-markdown'
import { json } from '@codemirror/lang-json'
import { cpp } from '@codemirror/lang-cpp'
import { html } from '@codemirror/lang-html'
import { css } from '@codemirror/lang-css'
import { java } from '@codemirror/lang-java'
import { rust } from '@codemirror/lang-rust'
import { php } from '@codemirror/lang-php'
import { StreamLanguage } from '@codemirror/language'
import { shell } from '@codemirror/legacy-modes/mode/shell'
import { yaml } from '@codemirror/legacy-modes/mode/yaml'
import { xml } from '@codemirror/legacy-modes/mode/xml'
import { sql } from '@codemirror/legacy-modes/mode/sql'
import { api, DirEntry, DiagItem, onEvent } from '../bridge'
import { useStore, t } from '../store'
import ThinkingDots from './ThinkingDots'

interface Tab { path: string; content: string; saved: string }

// 跨页签/面板开合保留的编辑器状态（选区/光标/滚动；EditorState 含完整撤销栈）
const savedEditorStates = new Map<string, { state: EditorState; scroll: number }>()

// 按项目记忆的编辑现场：切走项目时保存打开的页签，切回时恢复
const wsEditors = new Map<string, { tabs: Tab[]; active: string | null }>()

// 深色语法高亮（默认高亮是浅色主题配色，深底下几乎看不见）
const darkHighlight = HighlightStyle.define([
  { tag: t2.keyword, color: '#c792ea' },
  { tag: [t2.controlKeyword, t2.moduleKeyword], color: '#89ddff' },
  { tag: [t2.name, t2.deleted, t2.character, t2.propertyName, t2.macroName], color: '#e7eaf2' },
  { tag: [t2.function(t2.variableName), t2.labelName], color: '#82aaff' },
  { tag: [t2.color, t2.constant(t2.name), t2.standard(t2.name)], color: '#ffcb6b' },
  { tag: [t2.definition(t2.name), t2.separator], color: '#e7eaf2' },
  { tag: [t2.typeName, t2.className, t2.number, t2.changed, t2.annotation, t2.self, t2.namespace], color: '#f78c6c' },
  { tag: [t2.operator, t2.operatorKeyword], color: '#89ddff' },
  { tag: [t2.url, t2.escape, t2.regexp, t2.link], color: '#c3e88d' },
  { tag: [t2.meta, t2.comment], color: '#697098', fontStyle: 'italic' },
  { tag: t2.strong, fontWeight: 'bold' },
  { tag: t2.emphasis, fontStyle: 'italic' },
  { tag: t2.strikethrough, textDecoration: 'line-through' },
  { tag: t2.link, color: '#5ab8ff', textDecoration: 'underline' },
  { tag: t2.heading, fontWeight: 'bold', color: '#82aaff' },
  { tag: [t2.atom, t2.bool, t2.special(t2.variableName)], color: '#f78c6c' },
  { tag: [t2.processingInstruction, t2.string, t2.inserted], color: '#c3e88d' },
  { tag: t2.invalid, color: '#ff6b81' },
])

// 语言实例缓存（Compartment 重配置时避免重复构造）
const langCache = new Map<string, any>()
function langSupport(path: string) {
  const ext = path.slice(path.lastIndexOf('.') + 1).toLowerCase()
  if (!langCache.has(ext)) {
    langCache.set(ext, langOf(path) ?? null)
  }
  return langCache.get(ext)
}

function langOf(path: string) {
  const ext = path.slice(path.lastIndexOf('.') + 1).toLowerCase()
  switch (ext) {
    case 'py': case 'pyw': case 'pyi': return python()
    case 'js': case 'mjs': case 'cjs': case 'jsx': return javascript()
    case 'ts': case 'tsx': case 'mts': return javascript({ typescript: true, jsx: ext === 'tsx' })
    case 'go': return go()
    case 'md': return markdown()
    case 'json': case 'jsonc': return json()
    case 'c': case 'h': case 'cpp': case 'hpp': case 'cc': case 'cxx': return cpp()
    case 'html': case 'htm': case 'vue': case 'svelte': return html()
    case 'css': case 'scss': case 'less': return css()
    case 'java': return java()
    case 'rs': return rust()
    case 'php': return php()
    case 'sh': case 'bash': case 'zsh': return StreamLanguage.define(shell as any)
    case 'yaml': case 'yml': return StreamLanguage.define(yaml as any)
    case 'xml': return StreamLanguage.define(xml as any)
    case 'sql': return StreamLanguage.define(sql as any)
    case 'erl': case 'hrl': return StreamLanguage.define(shell as any) // 近似高亮
    default: return undefined
  }
}

const setDiags = StateEffect.define<DiagItem[]>()

const diagMark = Decoration.mark({ class: 'cm-diag' })
const diagField = StateField.define<DecorationSet>({
  create: () => Decoration.none,
  update(value, tr) {
    value = value.map(tr.changes)
    for (const e of tr.effects) {
      if (e.is(setDiags)) {
        const builder: Range<Decoration>[] = []
        for (const d of e.value) {
          const line = tr.state.doc.line(Math.min(Math.max(d.line, 1), tr.state.doc.lines))
          builder.push(diagMark.range(line.from, Math.min(line.to, line.from + 400)))
        }
        value = Decoration.set(builder.map((r) => r))
      }
    }
    return value
  },
  provide: (f) => EditorView.decorations.from(f),
})

export default function EditorPanel() {
  const workspace = useStore((s) => s.workspace)
  const setShowEditor = useStore((s) => s.setShowEditor)
  const [tabs, setTabs] = useState<Tab[]>([])
  const [active, setActive] = useState<string | null>(null)
  const [tree, setTree] = useState<DirEntry[]>([])
  const [diagCount, setDiagCount] = useState<{ e: number; w: number }>({ e: 0, w: 0 })
  const [sel, setSel] = useState<{ from: number; to: number; startLine: number; endLine: number } | null>(null)
  const [lspInfo, setLspInfo] = useState<{ supported: boolean; lang?: string; server?: string; available?: boolean; install_cmd?: string } | null>(null)
  const [installing, setInstalling] = useState(false)
  const [panelW, setPanelW] = useState(() => Number(localStorage.getItem('las_editor_w')) || 520)
  const dragRef = useRef<{ startX: number; startW: number } | null>(null)
  const dragWRef = useRef(0)
  const [panelMin, setPanelMin] = useState(false)
  const [ctxMenu, setCtxMenu] = useState<{ x: number; y: number; path: string; isDir: boolean } | null>(null)
  const [treeV, setTreeV] = useState(0)
  const running = useStore((s) => s.running)
  const locked = false // 不再锁定编辑器（并发编辑）；仅用 running 做提示
  const readOnlyComp = useRef(new Compartment())
  const hostRef = useRef<HTMLDivElement>(null)
  const viewRef = useRef<EditorView | null>(null)
  const tabsRef = useRef<Tab[]>([])
  const activeRef = useRef<string | null>(null)
  tabsRef.current = tabs
  activeRef.current = active

  // 工作区（项目）切换：按项目记忆编辑现场——
  // 切走时保存该项目打开的页签，切回时原样恢复（选区/滚动经 savedEditorStates 一并还原）；
  // 首次进入新项目则为空白编辑区。
  const prevWsRef = useRef(workspace)
  useEffect(() => {
    const prevWs = prevWsRef.current
    if (prevWs === workspace) return
    wsEditors.set(prevWs, { tabs: tabsRef.current, active: activeRef.current })
    prevWsRef.current = workspace
    const saved = wsEditors.get(workspace)
    if (saved) {
      setTabs(saved.tabs)
      setActive(saved.active)
    } else {
      setTabs([])
      setActive(null)
      setDiagCount({ e: 0, w: 0 })
      setSel(null)
    }
  }, [workspace])

  const refreshTree = async (root: string) => {
    const list = await api.listDir(root)
    setTree(list || [])
  }
  useEffect(() => { refreshTree('') }, [workspace])

  const openFile = async (path: string) => {
    if (tabs.some((x) => x.path === path)) {
      setActive(path)
      return
    }
    const content = await api.readFileText(path)
    setTabs((prev) => [{ path, content, saved: content }, ...prev])
    setActive(path)
  }

  const saveActive = async () => {
    const path = activeRef.current
    const tab = tabsRef.current.find((x) => x.path === path)
    if (!path || !tab) return
    await api.writeFileText(path, tab.content)
    setTabs((prev) => prev.map((x) => (x.path === path ? { ...x, saved: x.content } : x)))
  }

  // ---- 常驻视图：跨页签/面板开合保留编辑器状态（选区/光标/滚动/撤销栈） ----
  const prevActiveRef = useRef<string | null>(null)

  const scheduleDiag = (view: EditorView) => {
    const path = activeRef.current
    if (!path) return
    setTimeout(async () => {
      const diags = await api.lspDiag(path, view.state.doc.toString())
      if (!diags) return
      setDiagCount({
        e: diags.filter((d) => d.mark === '✗').length,
        w: diags.filter((d) => d.mark === '⚠').length,
      })
      view.dispatch({ effects: setDiags.of(diags) })
    }, 600)
  }

  // 真异步 LSP 补全源（路径取 activeRef，视图常驻）
  const lspSource = async (ctx: CompletionContext): Promise<CompletionResult | null> => {
    const path = activeRef.current
    if (!path) return null
    const word = ctx.matchBefore(/[\w$一-鿿.]+/)
    if (!word) return null
    const line = ctx.state.doc.lineAt(ctx.pos)
    const items = await api.lspComplete(
      path, ctx.state.doc.toString(), line.number - 1, ctx.pos - line.from)
    if (!items || !items.length) return null
    return {
      from: word.from,
      options: items.map((i) => ({
        label: i.label, detail: i.detail || '',
        type: /[(]/.test(i.detail || '') ? 'function' : 'variable',
      })),
      validFor: /^[\w$一-鿿.]*$/,
    }
  }

  const createStateFor = (path: string, doc: string) => EditorState.create({
    doc,
    extensions: [
      basicSetup,
      syntaxHighlighting(darkHighlight),
      langSupport(path) || [],
      autocompletion({ override: [lspSource] }),
      diagField,
      // AI 运行期间锁定手动编辑（write_file 由 agent 自动刷新标签）
      readOnlyComp.current.of(EditorState.readOnly.of(false)),
      EditorView.theme({
        '.cm-diag': { textDecoration: 'underline wavy rgba(255,107,129,0.8)', textDecorationSkipInk: 'none' },
        '&': { color: '#e7eaf2' },
        '.cm-content': { caretColor: '#7c6cff' },
      }),
      keymap.of([{ key: 'Mod-s', run: () => { saveActive(); return true } }]),
      EditorView.updateListener.of((u) => {
        if (u.docChanged) {
          const p = activeRef.current
          if (p) {
            const text = u.state.doc.toString()
            setTabs((prev) => prev.map((x) => (x.path === p ? { ...x, content: text } : x)))
          }
        }
        if (u.selectionSet) {
          const { from, to } = u.state.selection.main
          if (to > from) {
            setSel({
              from, to,
              startLine: u.state.doc.lineAt(from).number,
              endLine: u.state.doc.lineAt(to).number,
            })
          } else {
            setSel(null)
          }
        }
      }),
    ],
  })

  // 视图只在挂载时创建一次
  useEffect(() => {
    const host = hostRef.current
    if (!host || viewRef.current) return
    const view = new EditorView({ state: createStateFor('', ''), parent: host })
    viewRef.current = view
  }, [])

  // 切页签：保存旧状态（含选区/滚动），恢复新状态
  useEffect(() => {
    const view = viewRef.current
    if (!view) return
    const prev = prevActiveRef.current
    if (prev && prev !== active) {
      savedEditorStates.set(prev, { state: view.state, scroll: view.scrollDOM.scrollTop })
    }
    prevActiveRef.current = active

    const tab = tabs.find((x) => x.path === active)
    if (!active || !tab) return // 文件页签：不动视图
    const saved = savedEditorStates.get(active)
    if (saved) {
      view.setState(saved.state)
      view.scrollDOM.scrollTop = saved.scroll
    } else {
      view.setState(createStateFor(active, tab.content))
      savedEditorStates.set(active, { state: view.state, scroll: 0 })
    }
    setDiagCount({ e: 0, w: 0 })
    view.dispatch({ effects: readOnlyComp.current.reconfigure(EditorState.readOnly.of(locked)) })
    // 激活后聚焦，让光标可见可编辑
    setTimeout(() => {
      try { view.focus(); view.requestMeasure() } catch { /* ignore */ }
    }, 30)
    scheduleDiag(view)
  }, [active])

  // LSP 服务器状态指示（未安装时明确提示，不再静默）
  useEffect(() => {
    if (!active) { setLspInfo(null); return }
    api.lspServerStatus(active).then(setLspInfo)
  }, [active])

  // 运行状态变化 → 切换编辑锁
  useEffect(() => {
    const view = viewRef.current
    if (view) {
      view.dispatch({ effects: readOnlyComp.current.reconfigure(EditorState.readOnly.of(locked)) })
    }
  }, [locked])

  // 运行中：周期重读当前活动标签，任何写入（write_file / run_shell）都同步到编辑器
  useEffect(() => {
    if (!running || !active) return
    const id = setInterval(() => {
      const path = activeRef.current
      if (!path || !viewRef.current) return
      api.readFileText(path).then((c) => {
        if (c == null) return
        const tab = tabsRef.current.find((x) => x.path === path)
        if (tab && c !== tab.content) {
          setTabs((p2) => p2.map((y) => (y.path === path ? { ...y, content: c, saved: c } : y)))
          savedEditorStates.delete(path)
          viewRef.current!.setState(createStateFor(path, c))
          savedEditorStates.set(path, { state: viewRef.current!.state, scroll: 0 })
        }
      }).catch(() => {})
    }, 1500)
    return () => clearInterval(id)
  }, [running, active])

  // agent write_file 完成 → 自动刷新打开的同路径标签（对齐 Tk _reload_editor_tab）
  useEffect(() => {
    return onEvent('agent:event', (e: any) => {
      if (e?.type !== 'tool_result' || e?.name !== 'write_file') return
      const written = String(e?.args?.path ?? '')
      if (!written) return
      const normP = (x: string) => x.split('\\').join('/').toLowerCase()
      for (const key of Array.from(savedEditorStates.keys())) {
        if (normP(key).endsWith(normP(written)) || normP(written).endsWith(normP(key))) {
          savedEditorStates.delete(key) // 下次打开该页签用新内容重建
        }
      }
      setTabs((prev) => prev.map((x) => {
        const norm = (s: string) => s.replace(/\\/g, '/').toLowerCase()
        if (norm(x.path).endsWith(norm(written)) || norm(written).endsWith(norm(x.path))) {
          api.readFileText(x.path).then((c) => {
            setTabs((p2) => p2.map((y) => (y.path === x.path ? { ...y, content: c, saved: c } : y)))
            // 同频编辑：若是当前活动标签，立即刷新编辑器视图
            if (activeRef.current === x.path && viewRef.current) {
              savedEditorStates.delete(x.path)
              viewRef.current.setState(createStateFor(x.path, c))
              savedEditorStates.set(x.path, { state: viewRef.current.state, scroll: 0 })
            }
          }).catch(() => {})
        }
        return x
      }))
    })
  }, [])

  const addSnippetToChat = () => {
    const view = viewRef.current
    const path = activeRef.current
    if (!view || !path || !sel) return
    const text = view.state.sliceDoc(sel.from, sel.to)
    useStore.getState().addAttachment({
      kind: 'snippet', path, start: sel.startLine, end: sel.endLine, text,
    })
    setSel(null)
  }

  const activeTab = tabs.find((x) => x.path === active)

  return (
    <div className={`editor-panel${panelMin ? ' mini' : ''}`} style={panelMin ? undefined : { width: panelW, minWidth: 340 }}>
      {/* 分隔条：拖拽调整面板宽度（最大到窗口 80%；最小化时隐藏） */}
      {!panelMin && (<div
        className="editor-divider"
        onMouseDown={(e) => {
          dragRef.current = { startX: e.clientX, startW: panelW }
          const maxW = Math.round(window.innerWidth * 0.8)
          const onMove = (ev: MouseEvent) => {
            if (!dragRef.current) return
            const w = Math.min(maxW, Math.max(340, dragRef.current.startW - (ev.clientX - dragRef.current.startX)))
            dragWRef.current = w
            setPanelW(w)
          }
          const onUp = () => {
            dragRef.current = null
            localStorage.setItem('las_editor_w', String(dragWRef.current || panelW))
            window.removeEventListener('mousemove', onMove)
            window.removeEventListener('mouseup', onUp)
          }
          window.addEventListener('mousemove', onMove)
          window.addEventListener('mouseup', onUp)
        }}
      />)}
      <div className="editor-panel-inner">
      <div className="editor-tabs">
        <div
          className={`editor-tab${active === null ? ' active' : ''}`}
          onClick={() => setActive(null)}
        >
          📁 {t('文件', 'Files')}
        </div>
        {tabs.map((tab) => (
          <div
            key={tab.path}
            className={`editor-tab${tab.path === active ? ' active' : ''}`}
            onClick={() => setActive(tab.path)}
            // 标签页可拖入聊天作为附件（与文件树行同一数据类型）
            draggable
            onDragStart={(e) => {
              e.dataTransfer.setData('text/localai-path', tab.path)
              e.dataTransfer.effectAllowed = 'copy'
            }}
            title={`${tab.path} · ${t('可拖拽到聊天作为附件', 'drag to chat to attach')}`}
          >
            {tab.path.split('/').pop()}
            {tab.content !== tab.saved && <span className="dirty"> ●</span>}
            <span
              className="close"
              onClick={(e) => {
                e.stopPropagation()
                setTabs((prev) => prev.filter((x) => x.path !== tab.path))
                savedEditorStates.delete(tab.path)
                if (active === tab.path) setActive(tabs.find((x) => x.path !== tab.path)?.path ?? null)
              }}
            >✕</span>
          </div>
        ))}
        <div style={{ flex: 1 }} />
        <button className="tb-btn" title={t('最小化', 'Minimize')} onClick={() => setPanelMin(true)}>—</button>
        <button className="tb-btn" title={t('关闭（工具栏「📄 编辑器」可重新打开）', 'Close (reopen via toolbar)')} onClick={() => setShowEditor(false)}>✕</button>
      </div>

      {/* Tk 式页签视图：两视图常驻（CSS 隐藏切换），目录展开状态不丢失 */}
      <div className={`file-tree full${active === null || !activeTab ? '' : ' hidden'}`} key={workspace + ':' + treeV}>
        <TreeNode path="" name={workspace || '.'} isDir depth={0}
          onFile={openFile} defaultOpen
          onCtx={(path, isDir, x, y) => setCtxMenu({ x, y, path, isDir })} />
      </div>
      <div className={`editor-main${active === null || !activeTab ? ' hidden' : ''}`}>
        <div className="cm-host" ref={hostRef} />
        {sel && (
          <button
            className="sel-to-chat"
            style={{ top: Math.max(60, (sel as any).y ?? 60) }}
            onClick={addSnippetToChat}
          >
            💬 {t(`加到聊天（第 ${sel.startLine}-${sel.endLine} 行）`, `Add to chat (L${sel.startLine}-${sel.endLine})`)}
          </button>
        )}
        <div className="editor-status">
          <span>{activeTab ? activeTab.path : ''}</span>
          <span className="err">✗ {diagCount.e}</span>
          <span className="warn">⚠ {diagCount.w}</span>
          {lspInfo && (
            lspInfo.supported ? (
              lspInfo.available
                ? <span className="lsp-ok" title={`LSP: ${lspInfo.server}`}>LSP {lspInfo.server} ●</span>
                : <span className="lsp-no">
                    {installing ? <><ThinkingDots className="sm" /> {t('安装中', 'Installing')}</> : `LSP 未安装 ${lspInfo.server}`}
                    {!installing && lspInfo.install_cmd && (
                      <button
                        className="btn tiny" style={{ marginLeft: 6 }}
                        title={lspInfo.install_cmd}
                        onClick={async () => {
                          setInstalling(true)
                          const r = await api.lspInstall(activeRef.current || '')
                          setInstalling(false)
                          useStore.getState().notice(
                            r.ok ? `✅ LSP 安装完成：${lspInfo.server}` : `❌ 安装失败：
${r.output}`)
                          api.lspServerStatus(activeRef.current || '').then(setLspInfo)
                        }}
                      >⚙ 安装</button>
                    )}
                  </span>
            ) : <span className="dim">{t('高亮 ✓ · 无 LSP', 'highlight only')}</span>
          )}
          <span style={{ marginLeft: 'auto' }} />
          <button className="btn tiny" onClick={saveActive} disabled={!activeTab || activeTab.content === activeTab.saved}>
            💾 {t('保存', 'Save')}
          </button>
        </div>
      </div>
      {ctxMenu && (
        <div className="ctx-mask" onClick={() => setCtxMenu(null)}
          onContextMenu={(e) => { e.preventDefault(); setCtxMenu(null) }}>
          <div className="ctx-menu" style={{ left: ctxMenu.x, top: ctxMenu.y }}>
            <button onClick={() => {
              useStore.getState().addAttachment(ctxMenu.path)
              setCtxMenu(null)
            }}>📎 {t('加入对话框', 'Add to chat')}</button>
            {!ctxMenu.isDir && (
              <button onClick={() => { openFile(ctxMenu.path); setCtxMenu(null) }}>
                📝 {t('编辑', 'Edit')}
              </button>
            )}
            <button
              className="danger"
              onClick={async () => {
                const m = ctxMenu
                setCtxMenu(null)
                if (!confirm(t(`确认删除 ${m.path}？`, `Delete ${m.path}?`))) return
                const r = await api.deletePath(m.path)
                if (!r?.ok) alert(r?.msg || '删除失败')
                setTreeV((v) => v + 1)
              }}
            >🗑 {t('删除', 'Delete')}</button>
          </div>
        </div>
      )}
      {panelMin && (
        <button className="mini-restore" title={t('展开', 'Restore')} onClick={() => setPanelMin(false)}>📄</button>
      )}
      {running && (
        <div className="ai-lock-banner">🤖 {t('AI 正在修改代码，可并行编辑（运行结束后同步）', 'AI is editing — you can keep editing; changes sync after the run')}</div>
      )}
      </div>
    </div>
  )
}

// ---------------- 内联展开文件树（行可拖拽到聊天） ----------------

function TreeNode({ path, name, isDir, depth, onFile, onCtx, defaultOpen }:
  { path: string; name: string; isDir?: boolean; depth: number; onFile: (p: string) => void;
    onCtx?: (path: string, isDir: boolean, x: number, y: number) => void; defaultOpen?: boolean }) {
  const [open, setOpen] = useState(!!defaultOpen)
  const [kids, setKids] = useState<DirEntry[] | null>(null)
  const [loading, setLoading] = useState(false)

  const toggle = () => {
    if (!isDir) return
    const next = !open
    setOpen(next)
    if (next && kids === null && !loading) {
      setLoading(true)
      api.listDir(path).then((list) => {
        setKids(list || [])
        setLoading(false)
      })
    }
  }

  return (
    <>
      <div
        className="file-row"
        style={{ paddingLeft: 8 + depth * 14 }}
        onClick={() => (isDir ? toggle() : onFile(path))}
        // 全部行可拖拽：文件/目录路径都可作为附件拖入聊天
        draggable
        onDragStart={(e) => {
          e.dataTransfer.setData('text/localai-path', path)
          e.dataTransfer.effectAllowed = 'copy'
        }}
        onContextMenu={(e) => {
          e.preventDefault()
          onCtx?.(path, !!isDir, e.clientX, e.clientY)
        }}
      >
        <span className="icon">{isDir ? (open ? '📂' : '📁') : iconFor(name)}</span> {name}
        {isDir && open && kids === null && <span className="dim">…</span>}
      </div>
      {isDir && open && (kids ?? []).map((k) => (
        <TreeNode
          key={k.path}
          path={k.path}
          name={k.name}
          isDir={k.isDir}
          depth={depth + 1}
          onFile={onFile}
          onCtx={onCtx}
        />
      ))}
    </>
  )
}

function iconFor(name: string): string {
  const ext = name.slice(name.lastIndexOf('.') + 1)
  return ({
    go: '🐹', py: '🐍', ts: '🟦', tsx: '⚛️', js: '🟨', md: '📘', json: '🧾',
    rs: '🦀', java: '☕', c: '🔧', cpp: '🔧', h: '🔧', html: '🌐', css: '🎨',
  } as Record<string, string>)[ext] || '📄'
}

function debounce<T extends (...a: any[]) => void>(fn: T, ms: number) {
  let timer: any
  return (...args: Parameters<T>) => {
    clearTimeout(timer)
    timer = setTimeout(() => fn(...args), ms)
  }
}
