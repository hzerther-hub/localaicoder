// 知识库面板（必做功能③）：多根目录 + 构建 + 混合检索配置 + 测试查询。
import { useEffect, useState } from 'react'
import { api, KBConfig, onEvent } from '../bridge'
import { useStore, t } from '../store'

export default function KBPanel() {
  const show = useStore((s) => s.showKBPanel)
  const setShow = useStore((s) => s.setShowKBPanel)
  const [cfg, setCfg] = useState<KBConfig>({ enabled: false, inject: false, auto: true, top_k: 4, embedding: '', roots: [] })
  const [stats, setStats] = useState<Record<string, any>>({})
  const [progress, setProgress] = useState<{ done: number; total: number } | null>(null)
  const [query, setQuery] = useState('')
  const [hits, setHits] = useState<any[]>([])

  useEffect(() => {
    if (!show) return
    api.getKBConfig().then(setCfg)
    api.kbStats().then(setStats)
    onEvent('kb:progress', (d) => setProgress({ done: d.done, total: d.total }))
    onEvent('kb:done', (d) => {
      setProgress(null)
      setStats(d || {})
    })
  }, [show])
  if (!show) return null

  const save = (patch: Partial<KBConfig>) => {
    const next = { ...cfg, ...patch }
    setCfg(next)
    api.setKBConfig(next)
  }

  return (
    <div className="modal-mask" onClick={(e) => e.target === e.currentTarget && setShow(false)}>
      <div className="modal">
        <h3>
          📚 {t('公司知识库 (RAG)', 'Knowledge Base (RAG)')}
          <button className="x" onClick={() => setShow(false)}>✕</button>
        </h3>

        <label className="checkline">
          <input type="checkbox" checked={cfg.enabled} onChange={(e) => save({ enabled: e.target.checked })} />
          {t('启用（暴露 kb_search 工具）', 'Enable (kb_search tool)')}
        </label>
        <label className="checkline">
          <input type="checkbox" checked={cfg.inject} onChange={(e) => save({ inject: e.target.checked })} />
          {t('自动注入：提问自动带检索片段', 'Auto-inject retrieved context')}
        </label>
        <label className="checkline">
          <input type="checkbox" checked={cfg.auto} onChange={(e) => save({ auto: e.target.checked })} />
          {t('自动增量刷新（节流 60s）', 'Auto incremental refresh')}
        </label>

        <div className="field-row">
          <div className="field">
            <label>{t('返回片段数 top_k', 'top_k')}</label>
            <input
              type="number" min={1} max={20} value={cfg.top_k}
              onChange={(e) => save({ top_k: Number(e.target.value) || 4 })}
            />
          </div>
          <div className="field">
            <label>{t('Embedding 模型 key（空 = 纯 TF-IDF）', 'Embedding model key (empty = TF-IDF)')}</label>
            <input value={cfg.embedding} onChange={(e) => save({ embedding: e.target.value })} placeholder="" />
          </div>
        </div>

        <div className="field">
          <label>{t('知识根目录', 'Roots')}</label>
          {cfg.roots.map((r) => (
            <div className="kb-root-row" key={r}>
              <span>📁 {r}</span>
              <button
                className="del" style={{ marginLeft: 'auto' }}
                onClick={() => save({ roots: cfg.roots.filter((x) => x !== r) })}
              >✕</button>
            </div>
          ))}
          <div style={{ display: 'flex', gap: 8, marginTop: 6 }}>
            <button
              className="btn"
              onClick={async () => {
                const dir = await api.pickDirectory()
                if (dir) save({ roots: [...cfg.roots, dir] })
              }}
            >＋ {t('添加目录', 'Add root')}</button>
            <button className="btn primary" onClick={() => api.buildKB(true)}>🔨 {t('重建索引', 'Rebuild')}</button>
            <button className="btn" onClick={() => api.buildKB(false)}>↻ {t('增量刷新', 'Refresh')}</button>
          </div>
          {progress && (
            <div className="progress-line">
              <div style={{ width: `${progress.total ? Math.round((progress.done / progress.total) * 100) : 5}%` }} />
            </div>
          )}
        </div>

        {stats?.files !== undefined && (
          <div style={{ fontSize: 12, color: 'var(--text-faint)', fontFamily: 'var(--mono)' }}>
            {t('文件', 'files')} {String(stats.files)} · {t('块', 'chunks')} {String(stats.chunks)}
          </div>
        )}

        <div style={{ borderTop: '1px solid var(--border)', margin: '12px 0' }} />
        <div className="field">
          <label>{t('测试检索', 'Test query')}</label>
          <div style={{ display: 'flex', gap: 8 }}>
            <input
              value={query}
              onChange={(e) => setQuery(e.target.value)}
              onKeyDown={async (ev) => {
                if (ev.key === 'Enter' && query.trim()) setHits((await api.kbQuery(query)) || [])
              }}
              placeholder={t('函数名 / 功能描述…', 'function name / feature…')}
            />
            <button className="btn" onClick={async () => setHits((await api.kbQuery(query)) || [])}>🔍</button>
          </div>
        </div>
        {hits.map((h, i) => (
          <div key={i} style={{ fontSize: 12, marginBottom: 8, fontFamily: 'var(--mono)', color: 'var(--text-dim)' }}>
            <div style={{ color: 'var(--blue)' }}>
              {h.root || ''}/{h.file}:{h.start_line}-{h.end_line} · {h.source} {typeof h.score === 'number' ? h.score.toFixed(3) : h.score}
            </div>
            <pre style={{ margin: '2px 0 0', whiteSpace: 'pre-wrap', color: 'var(--text-faint)', maxHeight: 90, overflow: 'hidden' }}>
              {String(h.content || '').slice(0, 300)}
            </pre>
          </div>
        ))}
      </div>
    </div>
  )
}
