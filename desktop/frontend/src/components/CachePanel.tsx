// 缓存管理面板（对齐 Tk ui_panel_cache.py）：后端选择 / TTL / 统计 / 清空。
import { useEffect, useState } from 'react'
import { api } from '../bridge'
import { useStore, t } from '../store'

export default function CachePanel() {
  const show = useStore((s) => s.showCachePanel)
  const setShow = useStore((s) => s.setShowCachePanel)
  const [info, setInfo] = useState<Record<string, any>>({})
  const [backend, setBackend] = useState('auto')
  const [llmTTL, setLlmTTL] = useState(3600)
  const [toolTTL, setToolTTL] = useState(300)

  useEffect(() => {
    if (!show) return
    api.cacheInfo().then((s) => {
      setInfo(s)
      setBackend(String(s.configured || 'auto'))
    })
  }, [show])
  if (!show) return null

  return (
    <div className="modal-mask" onClick={(e) => e.target === e.currentTarget && setShow(false)}>
      <div className="modal">
        <h3>
          🗄 {t('缓存管理', 'Cache')}
          <button className="x" onClick={() => setShow(false)}>✕</button>
        </h3>
        <div style={{ fontSize: 12.5, color: 'var(--text-dim)', marginBottom: 12 }}>
          {t('当前后端', 'Active backend')}: <b style={{ color: 'var(--green)' }}>{String(info.backend)}</b>
          {' · '}{t('条目', 'entries')}: <b>{String(info.entries)}</b>
        </div>
        <div className="field">
          <label>{t('后端', 'Backend')}</label>
          <select value={backend} onChange={(e) => setBackend(e.target.value)}>
            <option value="auto">auto（SQLite，不可用退内存）</option>
            <option value="sqlite">SQLite</option>
            <option value="memory">Memory</option>
          </select>
        </div>
        <div className="field-row">
          <div className="field">
            <label>LLM TTL（{t('秒', 'sec')}）</label>
            <input type="number" value={llmTTL} onChange={(e) => setLlmTTL(Number(e.target.value) || 0)} />
          </div>
          <div className="field">
            <label>{t('工具结果 TTL（秒）', 'Tool TTL (sec)')}</label>
            <input type="number" value={toolTTL} onChange={(e) => setToolTTL(Number(e.target.value) || 0)} />
          </div>
        </div>
        <div className="approval-row">
          <button
            className="btn danger"
            onClick={async () => { await api.clearCache(); api.cacheInfo().then((s2) => setInfo(s2)) }}
          >{t('清空缓存', 'Clear')}</button>
          <button
            className="btn primary"
            onClick={async () => { setInfo(await api.saveCacheSettings(backend, llmTTL, toolTTL)) }}
          >{t('保存', 'Save')}</button>
        </div>
      </div>
    </div>
  )
}
