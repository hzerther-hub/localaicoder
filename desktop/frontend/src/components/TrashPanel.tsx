// 垃圾箱面板：已删除项目列表（仅移出侧栏，会话保留），支持一键恢复。
import { useEffect, useState } from 'react'
import { api } from '../bridge'
import { useStore, t } from '../store'

function dirName(ws: string): string {
  const parts = ws.split(/[\/]/).filter(Boolean)
  return parts.length ? parts[parts.length - 1] : ws
}

export default function TrashPanel() {
  const show = useStore((s) => s.showTrashPanel)
  const setShow = useStore((s) => s.setShowTrashPanel)
  const [list, setList] = useState<string[]>([])

  useEffect(() => {
    if (!show) return
    api.getProjectTrash().then((l) => setList(l || []))
  }, [show])
  if (!show) return null

  const restore = async (ws: string) => {
    const next = (await api.restoreProject(ws)) || []
    useStore.setState({ trash: next })
    setList(next)
    useStore.getState().refreshSessions()
  }

  return (
    <div className="modal-mask" onClick={(e) => e.target === e.currentTarget && setShow(false)}>
      <div className="modal">
        <h3>
          🗑 {t('垃圾箱', 'Trash')}
          <button className="x" onClick={() => setShow(false)}>✕</button>
        </h3>
        <div style={{ fontSize: 12.5, color: 'var(--text-dim)', marginBottom: 12 }}>
          {t('删除的项目只是从侧栏隐藏，会话全部保留，可随时放回来。',
            'Removed projects are hidden from the sidebar only; sessions are kept and restorable.')}
        </div>
        {list.length === 0 && (
          <div style={{ padding: '14px 0', color: 'var(--text-faint)', fontSize: 12.5 }}>
            {t('垃圾箱是空的', 'Trash is empty')}
          </div>
        )}
        {list.map((ws) => (
          <div key={ws} className="trash-row" title={ws}>
            <span className="icon">📁</span>
            <span className="meta">
              <span className="name">{dirName(ws)}</span>
              <span className="path">{ws}</span>
            </span>
            <button className="btn" onClick={() => restore(ws)}>
              {t('放回来', 'Restore')}
            </button>
          </div>
        ))}
      </div>
    </div>
  )
}
