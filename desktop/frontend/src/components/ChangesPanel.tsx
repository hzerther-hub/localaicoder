// Git 改动面板：本会话 AI 变更 / 工作区未提交改动 / 提交历史 / 分支切换。
// 对齐参考设计（Claude Code 式 changes 视图）：三段式 + 可点开文件。
import { useEffect, useState } from 'react'
import { api, GitBranchesInfo, GitChangesInfo } from '../bridge'
import { useStore, t } from '../store'

const statusColor = (s: string) =>
  s === '未跟踪' ? 'var(--amber)' : s === '修改' ? 'var(--blue)' : s === '删除' ? 'var(--red)' : 'var(--green)'

export default function ChangesPanel() {
  const show = useStore((s) => s.showChangesPanel)
  const setShow = useStore((s) => s.setShowChangesPanel)
  const openFile = useStore((s) => s.openFile)
  const branch = useStore((s) => s.branch)
  const notice = useStore((s) => s.notice)
  const [info, setInfo] = useState<GitChangesInfo | null>(null)
  const [branches, setBranches] = useState<GitBranchesInfo | null>(null)
  const [showHistory, setShowHistory] = useState(false)
  const [switching, setSwitching] = useState(false)

  useEffect(() => {
    if (!show) return
    api.gitChanges().then(setInfo)
    api.gitBranches().then(setBranches)
  }, [show])

  if (!show) return null

  const refresh = () => {
    api.gitChanges().then(setInfo)
    api.gitBranches().then(setBranches)
    api.gitBranch().then((b) => useStore.setState({ branch: b || '' }))
  }

  const doSwitch = async (name: string) => {
    if (!name || name === branch || switching) return
    if (!confirm(t(`切换到分支「${name}」？（未提交改动可能冲突）`, `Switch to "${name}"? (uncommitted changes may conflict)`))) return
    setSwitching(true)
    try {
      const r = await api.switchBranch(name)
      if (r.ok) {
        useStore.setState({ branch: name })
        notice(t(`已切换到分支 ${name}`, `Switched to ${name}`))
        refresh()
      } else {
        notice(t(`切换失败：${r.error || '未知错误'}`, `Switch failed: ${r.error || 'unknown'}`))
      }
    } finally {
      setSwitching(false)
    }
  }

  const Section = ({ title, count, children }: { title: string; count: number; children: React.ReactNode }) => (
    <div className="changes-sec">
      <div className="changes-sec-head">
        <span>{title}</span>
        <span className="count">{count} {t('条', '')}</span>
      </div>
      {children}
    </div>
  )

  return (
    <div className="modal-mask" onClick={(e) => e.target === e.currentTarget && setShow(false)}>
      <div className="modal">
        <h3>
          🔀 {t('改动', 'Changes')}
          {info?.branch && <span className="changes-branch">⎇ {info.branch}</span>}
          <button className="x" onClick={() => setShow(false)}>✕</button>
        </h3>

        {!info?.is_git ? (
          <div className="skills-empty">{t('当前工作目录不是 git 仓库', 'Current workspace is not a git repo')}</div>
        ) : (
          <>
            {branches?.ok && branches.branches.length > 0 && (
              <div className="changes-branch-row">
                <span className="lbl">{t('分支', 'Branch')}</span>
                <select
                  value={info?.branch || branch}
                  disabled={switching}
                  onChange={(e) => void doSwitch(e.target.value)}
                >
                  {branches.branches.map((b) => (
                    <option key={b} value={b}>{b}{b === branches.current ? `（${t('当前', 'current')}）` : ''}</option>
                  ))}
                </select>
              </div>
            )}

            <Section title={t('本会话变更', 'Changed this session')} count={info.session.length}>
              {info.session.length === 0 && <div className="skills-empty">{t('AI 本会话尚未修改文件', 'No AI-modified files yet')}</div>}
              {info.session.map((p) => (
                <div className="changes-row" key={p} onClick={() => openFile(p)} title={t('点击在编辑器打开', 'open in editor')}>
                  <span className="f-ico">📄</span>
                  <span className="f-name">{p.split(/[\\/]/).pop()}</span>
                  <span className="f-dir">{p.split(/[\\/]/).slice(0, -1).join('/')}</span>
                  <span className="f-status" style={{ color: 'var(--green)' }}>AI</span>
                </div>
              ))}
            </Section>

            <Section title={t('工作区未提交改动', 'Uncommitted changes')} count={info.changes.length}>
              {info.changes.length === 0 && <div className="skills-empty">{t('工作区干净', 'Workspace clean')}</div>}
              {info.changes.map((c) => (
                <div className="changes-row" key={c.path + c.status} onClick={() => openFile(c.path)} title={t('点击在编辑器打开', 'open in editor')}>
                  <span className="f-ico">📄</span>
                  <span className="f-name">{c.path.split(/[\\/]/).pop()}</span>
                  <span className="f-dir">{c.dir}</span>
                  <span className="f-status" style={{ color: statusColor(c.status) }}>{c.status}</span>
                </div>
              ))}
            </Section>

            <div className="changes-sec">
              <div className="changes-sec-head" onClick={() => setShowHistory(!showHistory)} style={{ cursor: 'pointer' }}>
                <span>{showHistory ? '▾' : '▸'} {t('提交历史', 'History')}</span>
                <span className="count">{info.history.length}</span>
              </div>
              {showHistory && info.history.map((c) => (
                <div className="changes-row" key={c.hash}>
                  <span className="f-ico hash">{c.hash.slice(0, 7)}</span>
                  <span className="f-name">{c.subject}</span>
                </div>
              ))}
            </div>

            <div className="changes-foot">
              <button className="btn" onClick={refresh}>↻ {t('刷新', 'Refresh')}</button>
            </div>
          </>
        )}
      </div>
    </div>
  )
}
