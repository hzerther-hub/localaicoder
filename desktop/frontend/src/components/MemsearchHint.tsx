import { useEffect, useRef, useState } from 'react'
import { useStore, t } from '../store'
import { api, onEvent } from '../bridge'
import ThinkingDots from './ThinkingDots'

const DISMISS_KEY = 'memsearch_hint_dismissed'

type Info = {
  native_ok: boolean
  pykb_deps: boolean
  wsl_available: boolean
  install_cmd: string
}

// 项目知识检索引导条：启动时检测一次，不可用则显示安装引导（全部走异步安装事件
// memsearch:install:log / :done，安装中在条内实时滚动日志）。
// 路线按钮按平台给出：Windows 主推 pykb（自研 Python 语义检索）；Linux/macOS 主推
// 原生 memsearch（uv）；两平台都可退回内置知识库（零安装）或 WSL/Python 版。
export default function MemsearchHint() {
  const [info, setInfo] = useState<Info | null>(null)
  const [st, setSt] = useState<'hidden' | 'show' | 'installing'>('hidden')
  const [via, setVia] = useState<'' | 'native' | 'wsl' | 'pykb'>('')
  const [msg, setMsg] = useState('')
  const [log, setLog] = useState<string[]>([])
  const logRef = useRef<HTMLDivElement>(null)
  const notice = useStore((s) => s.notice)

  useEffect(() => {
    if (localStorage.getItem(DISMISS_KEY) === '1') return
    api.memsearchStatus()
      .then((r) => { if (r && !r.usable) { setInfo(r); setSt('show') } })
      .catch(() => {})
  }, [])

  // 安装进度事件（后端异步推送；本组件是唯一订阅方，卸载时退订）
  useEffect(() => {
    const offLog = onEvent('memsearch:install:log', (d) => {
      setLog((p) => [...p.slice(-300), String(d?.line || '')])
    })
    const offDone = onEvent('memsearch:install:done', (d) => {
      if (d?.ok) {
        setSt('hidden')
        localStorage.removeItem(DISMISS_KEY)
        notice(t(`✅ ${d.msg || '语义检索已就绪'}。用法见 /help 或 docs/memsearch.md。`, `✅ ${d.msg || 'Semantic search ready'}. See /help or docs/memsearch.md.`))
      } else {
        setSt('show')
        setMsg(d?.output || d?.msg || t('安装失败', 'Install failed'))
      }
    })
    return () => { offLog(); offDone() }
  }, [])

  // 日志区自动滚到底部（新行持续追加时）
  useEffect(() => {
    const el = logRef.current
    if (el) el.scrollTop = el.scrollHeight
  }, [log])

  if (st === 'hidden') return null

  const install = async (route: '' | 'native' | 'wsl' | 'pykb') => {
    setVia(route); setSt('installing'); setMsg(''); setLog([])
    try {
      const r = await api.installMemsearch(route)
      // ok 只代表安装流程已启动；真实结果等 memsearch:install:done 事件
      if (!r?.ok) {
        setSt('show')
        setMsg(r?.error || t('无法启动安装', 'Cannot start install'))
      }
    } catch (e: any) {
      setSt('show')
      setMsg(t('安装异常: ', 'Install error: ') + (e?.message || ''))
    }
  }

  const isWin = info != null && !info.native_ok
  const installingLabel = via === 'pykb'
    ? t('正在安装 pykb 依赖（pip install fastembed，约 100-200MB）…', 'Installing pykb deps (pip install fastembed, ~100-200MB)…')
    : via === 'wsl'
      ? t('WSL2 内安装中…（含 uv/onnxruntime 下载，可能需要几分钟）', 'Installing inside WSL2… (downloads uv/onnxruntime, may take minutes)')
      : t('memsearch 安装中…（含 onnxruntime 下载，可能需要几分钟）', 'Installing memsearch… (downloads onnxruntime, may take minutes)')

  return (
    <div className="mem-hint">
      <div className="mh-row">
        <span className="mh-icon">🔎</span>
        <span className="mh-text">
          {st === 'installing'
            ? <><ThinkingDots /> {installingLabel}</>
            : isWin
              ? t('语义检索（pykb）未就绪 —— 一键安装 Python 依赖即可启用（无需 WSL/Docker）；或用内置知识库（零安装）', 'Semantic search (pykb) not ready — install Python deps to enable (no WSL/Docker needed); or use the built-in KB')
              : t('未检测到 memsearch —— 项目文档语义检索不可用（详见 docs/memsearch.md）', 'memsearch not found — semantic search over project docs unavailable (see docs/memsearch.md)')}
          {msg && <span className="mh-err">{msg}</span>}
        </span>
        {st === 'show' && isWin && (
          <button className="mh-btn" onClick={() => void install('pykb')}>
            {t('安装语义检索', 'Install')}
          </button>
        )}
        {st === 'show' && !isWin && (
          <button className="mh-btn" title={info?.install_cmd} onClick={() => void install('')}>
            {t('一键安装 memsearch', 'Install memsearch')}
          </button>
        )}
        {st === 'show' && !isWin && (
          <button className="mh-btn ghost" onClick={() => void install('pykb')}>
            {t('Python 版（pykb）', 'Python (pykb)')}
          </button>
        )}
        {st === 'show' && isWin && info?.wsl_available && (
          <button className="mh-btn ghost" title={t('安装 memsearch 到 WSL2（向量库在 WSL 侧）', 'Install memsearch inside WSL2')} onClick={() => void install('wsl')}>
            {t('WSL2 版', 'WSL2')}
          </button>
        )}
        <button
          className="mh-btn ghost"
          title={t('零安装：TF-IDF 检索，/memsearch 也会自动回退到它', 'Zero-install TF-IDF search; /memsearch falls back to it automatically')}
          onClick={() => { useStore.getState().setShowKBPanel(true); setSt('hidden'); localStorage.setItem(DISMISS_KEY, '1') }}
        >
          📚 {t('内置知识库', 'Built-in KB')}
        </button>
        {st === 'show' && !isWin && (
          <button className="mh-btn ghost" onClick={() => navigator.clipboard?.writeText(info?.install_cmd || '')}>
            {t('复制命令', 'Copy cmd')}
          </button>
        )}
        <button
          className="mh-x"
          title={t('不再提示', "Don't show again")}
          onClick={() => { localStorage.setItem(DISMISS_KEY, '1'); setSt('hidden') }}
        >×</button>
      </div>
      {st === 'installing' && log.length > 0 && (
        <div className="mh-log" ref={logRef}>
          {log.map((l, i) => <div key={i}>{l}</div>)}
        </div>
      )}
    </div>
  )
}
