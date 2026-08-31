// 会话侧栏（深色）：按项目分组；当前会话高亮圆点；会话行右键改名、按钮删除；
// 项目悬停出现删除按钮（移入垃圾箱，可恢复）；底部常驻 垃圾箱/定时任务/设置 入口。
import { useMemo } from 'react'
import { api } from '../bridge'
import { useStore, t } from '../store'
import ThinkingDots from './ThinkingDots'

function timeAgo(ts: number): string {
  const d = Date.now() / 1000 - ts
  if (d < 60) return t('刚刚', 'now')
  if (d < 3600) return `${Math.floor(d / 60)} ${t('分钟', 'm')}`
  if (d < 86400) return `${Math.floor(d / 3600)} ${t('小时', 'h')}`
  return `${Math.floor(d / 86400)} ${t('天', 'd')}`
}

function dirName(ws: string): string {
  if (!ws) return t('（未分类）', '(none)')
  const parts = ws.split(/[\/]/).filter(Boolean)
  return parts.length ? parts[parts.length - 1] : ws
}

export default function Sidebar() {
  const sessions = useStore((s) => s.sessions)
  const workspace = useStore((s) => s.workspace)
  const sessionId = useStore((s) => s.sessionId)
  const runningSessionId = useStore((s) => s.runningSessionId)
  const trash = useStore((s) => s.trash)
  const newSession = useStore((s) => s.newSession)
  const loadSession = useStore((s) => s.loadSession)
  const refresh = useStore((s) => s.refreshSessions)

  const groups = useMemo(() => {
    const map = new Map<string, typeof sessions>()
    for (const s of sessions) {
      const k = s.workspace || ''
      if (trash.includes(k)) continue // 已删除项目：隐藏（会话保留，可从垃圾箱恢复）
      if (!map.has(k)) map.set(k, [])
      map.get(k)!.push(s)
    }
    const keys = Array.from(map.keys())
    keys.sort((a, b) => {
      if (a === workspace) return -1
      if (b === workspace) return 1
      const la = Math.max(...map.get(a)!.map((s) => s.updated))
      const lb = Math.max(...map.get(b)!.map((s) => s.updated))
      return lb - la
    })
    return keys.map((k) => ({ ws: k, items: map.get(k)! }))
  }, [sessions, workspace, trash])

  const rename = async (id: string, cur: string) => {
    const title = prompt(t('修改会话标题', 'Rename session'), cur)
    if (title && title.trim()) {
      const ok = await api.renameSession(id, title.trim())
      if (!ok) alert(t('「新会话」为保留名，不能使用', '"新会话" is a reserved name'))
      refresh()
    }
  }
  const remove = async (id: string, title: string) => {
    if (!confirm(t(`删除会话「${title}」？`, `Delete "${title}"?`))) return
    await api.deleteSession(id); refresh()
  }

  // trashProject 删除项目：仅移入垃圾箱（会话保留）；删的是当前项目时自动切走
  const trashProject = async (ws: string) => {
    if (!confirm(t(
      `删除项目「${dirName(ws)}」？项目将从侧栏隐藏，会话保留，可在垃圾箱恢复。`,
      `Remove project "${dirName(ws)}"? It will be hidden (sessions kept; restorable from Trash).`,
    ))) return
    const list = (await api.trashProject(ws)) || []
    useStore.setState({ trash: list })
    refresh()
    if (ws === workspace) {
      const next = sessions.map((s) => s.workspace)
        .find((w) => w && w !== ws && !list.includes(w))
      if (next) await useStore.getState().setWorkspace(next)
    }
  }

  return (
    <div className="sidebar">
      <div className="sidebar-head">
        <span>{t('项目', 'PROJECTS')}</span>
        <button title={t('新会话', 'New session')} onClick={newSession}>＋</button>
      </div>
      <div className="sessions">
        {groups.map((g) => (
          <div key={g.ws || '_'} className="proj-group">
            <div
              className={`proj-head${g.ws === workspace ? ' current' : ''}`}
              title={g.ws || ''}
              onClick={async () => {
                if (g.ws && g.ws !== workspace) await useStore.getState().setWorkspace(g.ws)
              }}
            >
              <span className="icon">{g.ws === workspace ? '📂' : '📁'}</span>
              <span className="name">{dirName(g.ws)}</span>
              <span className="count">{g.items.length}</span>
              <button
                className="op proj-del"
                title={t('删除项目（移入垃圾箱）', 'Remove project (to trash)')}
                onClick={(e) => { e.stopPropagation(); g.ws && trashProject(g.ws) }}
              >🗑</button>
            </div>
            {g.items.map((s) => (
              <div
                key={s.id}
                className={`session-item${s.id === sessionId ? ' active' : ''}`}
                onClick={() => loadSession(s.id)}
                onContextMenu={(e) => { e.preventDefault(); rename(s.id, s.title) }}
                title={`${s.title} · 右键改名`}
              >
                {s.id === sessionId && <span className="dot" />}
                {s.id === runningSessionId && <span className="run-mark" title={t('运行中', 'running')}><ThinkingDots className="sm" /></span>}
                <span className="tt">{s.title}</span>
                <span className="ops">
                  <span className="tm">{timeAgo(s.updated)}</span>
                  <button className="op" title={t('改名', 'Rename')}
                    onClick={(e) => { e.stopPropagation(); rename(s.id, s.title) }}>✎</button>
                  <button className="op del" title={t('删除', 'Delete')}
                    onClick={(e) => { e.stopPropagation(); remove(s.id, s.title) }}>🗑</button>
                </span>
              </div>
            ))}
          </div>
        ))}
        {groups.length === 0 && (
          <div style={{ padding: '10px', color: 'var(--text-faint)', fontSize: 12 }}>
            {t('暂无会话', 'No sessions')}
          </div>
        )}
      </div>
    </div>
  )
}
