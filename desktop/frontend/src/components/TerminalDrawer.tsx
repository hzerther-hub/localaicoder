// 内置终端抽屉：多标签 PowerShell（ConPTY），xterm.js 渲染。
// + 新建终端；每标签独立关闭；抽屉可整体隐藏（终端保活）。
import { useEffect, useRef, useState } from 'react'
import { Terminal } from '@xterm/xterm'
import { FitAddon } from '@xterm/addon-fit'
import '@xterm/xterm/css/xterm.css'
import { api, onEvent } from '../bridge'
import { useStore, t } from '../store'

interface TermTab { id: string; label: string }

const TERM_THEME = {
  background: '#0b0d13', foreground: '#e7eaf2',
  cursor: '#7c6cff', cursorAccent: '#0b0d13',
  selectionBackground: 'rgba(124,108,255,0.3)',
  black: '#1b1f2d', red: '#ff6b81', green: '#3ddc97', yellow: '#ffc46b',
  blue: '#5ab8ff', magenta: '#c792ea', cyan: '#89ddff', white: '#e7eaf2',
}

export default function TerminalDrawer() {
  const show = useStore((s) => s.showTerminal)
  const setShow = useStore((s) => s.setShowTerminal)
  const workspace = useStore((s) => s.workspace)
  const [tabs, setTabs] = useState<TermTab[]>([])
  const [activeId, setActiveId] = useState<string | null>(null)
  const [counter, setCounter] = useState(0)
  const hostsRef = useRef<Map<string, HTMLDivElement>>(new Map())
  const termsRef = useRef<Map<string, { term: Terminal; fit: FitAddon }>>(new Map())
  const activeRef = useRef<string | null>(null)
  activeRef.current = activeId

  const fitActive = () => {
    const a = activeRef.current
    if (!a) return
    const e = termsRef.current.get(a)
    if (e) { try { e.fit.fit() } catch { /* 容器未布局好 */ } }
  }

  const createTerm = async () => {
    const id = await api.termStart(workspace)
    const n = counter + 1
    setCounter(n)
    setTabs((prev) => [...prev, { id, label: `PowerShell ${n}` }])
    setActiveId(id)
    // 等容器渲染后再挂 xterm
    setTimeout(() => {
      const host = hostsRef.current.get(id)
      if (!host) return
      const term = new Terminal({
        fontSize: 12.5, fontFamily: 'Cascadia Code, Consolas, monospace',
        theme: TERM_THEME, cursorBlink: true, allowProposedApi: true,
      })
      const fit = new FitAddon()
      term.loadAddon(fit)
      term.open(host)
      try { fit.fit() } catch { /* ignore */ }
      term.onData((d) => api.termWrite(id, d))
      term.onResize(({ cols, rows }) => api.termResize(id, cols, rows))
      termsRef.current.set(id, { term, fit })
      term.focus()
      api.termResize(id, term.cols, term.rows)
    }, 60)
  }

  const closeTerm = (id: string) => {
    api.termStop(id)
    termsRef.current.get(id)?.term.dispose()
    termsRef.current.delete(id)
    hostsRef.current.delete(id)
    setTabs((prev) => {
      const next = prev.filter((x) => x.id !== id)
      if (activeRef.current === id) setActiveId(next.length ? next[next.length - 1].id : null)
      return next
    })
  }

  useEffect(() => {
    if (!show) return
    const off = onEvent('term:data', (d: any) => {
      const e = termsRef.current.get(d.id)
      if (e) e.term.write(d.data)
    })
    // 抽屉打开且无终端 → 自动建一个
    if (termsRef.current.size === 0) {
      void createTerm()
    } else {
      setTimeout(fitActive, 50)
    }
    const onWinResize = () => fitActive()
    window.addEventListener('resize', onWinResize)
    return () => {
      off()
      window.removeEventListener('resize', onWinResize)
    }
  }, [show])

  // 激活标签时自适应尺寸
  useEffect(() => { setTimeout(fitActive, 30) }, [activeId, show])

  if (!show) return null

  return (
    <div className="term-drawer">
      <div className="term-tabs">
        <span className="term-title">⌨ {t('终端', 'TERMINAL')}</span>
        {tabs.map((tb) => (
          <div
            key={tb.id}
            className={`term-tab${tb.id === activeId ? ' active' : ''}`}
            onClick={() => setActiveId(tb.id)}
          >
            {tb.label}
            <span className="close" onClick={(e) => { e.stopPropagation(); closeTerm(tb.id) }}>✕</span>
          </div>
        ))}
        <button className="term-add" title={t('新建终端', 'New terminal')} onClick={createTerm}>＋</button>
        <div style={{ flex: 1 }} />
        <button className="term-add" title={t('收起终端', 'Hide')} onClick={() => setShow(false)}>✕</button>
      </div>
      <div className="term-body">
        {tabs.map((tb) => (
          <div
            key={tb.id}
            ref={(el) => { if (el) hostsRef.current.set(tb.id, el); else hostsRef.current.delete(tb.id) }}
            className="term-host"
            style={{ display: tb.id === activeId ? 'block' : 'none' }}
          />
        ))}
        {tabs.length === 0 && (
          <div className="term-empty">{t('暂无终端', 'No terminal')}</div>
        )}
      </div>
    </div>
  )
}
