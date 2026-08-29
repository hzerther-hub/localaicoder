// 定时任务面板：创建/编辑周期性 prompt 任务（分钟级），到期自动在任务专属
// 会话中运行；支持启停、立即运行、删除。调度在桌面端 schedule.go。
import { useEffect, useState } from 'react'
import { api, ScheduledTask } from '../bridge'
import { useStore, t } from '../store'

function dirName(ws: string): string {
  const parts = ws.split(/[\/]/).filter(Boolean)
  return parts.length ? parts[parts.length - 1] : ws
}

function fmtTime(unix: number): string {
  if (!unix) return t('未运行', 'never')
  const d = new Date(unix * 1000)
  const pad = (n: number) => String(n).padStart(2, '0')
  return `${d.getMonth() + 1}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}`
}

const emptyForm = { id: '', name: '', workspace: '', prompt: '', interval_min: 60, enabled: true }

export default function SchedulePanel() {
  const show = useStore((s) => s.showSchedulePanel)
  const setShow = useStore((s) => s.setShowSchedulePanel)
  const sessions = useStore((s) => s.sessions)
  const workspace = useStore((s) => s.workspace)
  const [tasks, setTasks] = useState<ScheduledTask[]>([])
  const [form, setForm] = useState<typeof emptyForm>(emptyForm)

  const reload = () => api.listScheduledTasks().then((l) => setTasks(l || []))

  useEffect(() => {
    if (!show) return
    reload()
    setForm({ ...emptyForm, workspace: workspace })
    const id = setInterval(reload, 30_000) // 面板打开期间轮询下次运行时间
    return () => clearInterval(id)
  }, [show, workspace])
  if (!show) return null

  const workspaces = Array.from(new Set(sessions.map((s) => s.workspace).filter(Boolean)))

  const save = async () => {
    if (!form.prompt.trim() || !form.workspace) {
      alert(t('请填写项目目录与提示词', 'Workspace and prompt are required'))
      return
    }
    setTasks((await api.saveScheduledTask(form)) || [])
    setForm({ ...emptyForm, workspace: form.workspace })
  }

  return (
    <div className="modal-mask" onClick={(e) => e.target === e.currentTarget && setShow(false)}>
      <div className="modal" style={{ minWidth: 520 }}>
        <h3>
          🕐 {t('定时任务', 'Scheduled tasks')}
          <button className="x" onClick={() => setShow(false)}>✕</button>
        </h3>
        <div style={{ fontSize: 12.5, color: 'var(--text-dim)', marginBottom: 12 }}>
          {t('到期后在任务专属会话中自动运行（运行时独占切换到任务目录）；同一时刻至多一个定时任务在跑。',
            'Due tasks run automatically in a dedicated session (workspace switched for the duration); one at a time.')}
        </div>

        {tasks.length > 0 && (
          <div className="sched-list">
            {tasks.map((task) => (
              <div key={task.id} className={`sched-row${task.enabled ? '' : ' off'}`}>
                <span className={`st-dot${task.enabled ? ' on' : ''}`} title={task.enabled ? t('启用中', 'enabled') : t('已停用', 'disabled')} />
                <span className="meta">
                  <span className="name" title={`${task.prompt} · ${t('点击编辑', 'click to edit')}`}
                    onClick={() => setForm({ id: task.id, name: task.name, workspace: task.workspace, prompt: task.prompt, interval_min: task.interval_min, enabled: task.enabled })}
                    style={{ cursor: 'pointer' }}>
                    {task.name}
                  </span>
                  <span className="sub">
                    {dirName(task.workspace)} · {t('每', 'every')} {task.interval_min} {t('分钟', 'min')}
                    {' · '}{t('下次', 'next')} {fmtTime(task.next_run)}
                    {task.last_run ? ` · ${t('上次', 'last')} ${fmtTime(task.last_run)}` : ''}
                  </span>
                </span>
                <span className="ops">
                  <button className="btn" title={t('立即运行一次', 'Run once now')}
                    onClick={() => api.runScheduledTaskNow(task.id)}>{t('运行', 'Run')}</button>
                  <button className="btn" onClick={async () => setTasks((await api.saveScheduledTask({ ...task, enabled: !task.enabled })) || [])}>
                    {task.enabled ? t('停用', 'Pause') : t('启用', 'Enable')}
                  </button>
                  <button className="btn" onClick={async () => { if (confirm(t(`删除任务「${task.name}」？`, `Delete task "${task.name}"?`))) setTasks((await api.deleteScheduledTask(task.id)) || []) }}>
                    {t('删除', 'Delete')}
                  </button>
                </span>
              </div>
            ))}
          </div>
        )}

        <div className="sched-form">
          <div className="field-row">
            <div className="field" style={{ flex: 1 }}>
              <label>{t('任务名', 'Name')}</label>
              <input value={form.name} placeholder={t('例如：每日提交摘要', 'e.g. daily commit digest')}
                onChange={(e) => setForm({ ...form, name: e.target.value })} />
            </div>
            <div className="field">
              <label>{t('间隔（分钟）', 'Interval (min)')}</label>
              <input type="number" min={1} value={form.interval_min}
                onChange={(e) => setForm({ ...form, interval_min: Number(e.target.value) || 60 })} />
            </div>
          </div>
          <div className="field">
            <label>{t('项目目录', 'Workspace')}</label>
            <select value={form.workspace} onChange={(e) => setForm({ ...form, workspace: e.target.value })}>
              {!workspaces.includes(form.workspace) && form.workspace && (
                <option value={form.workspace}>{form.workspace}</option>
              )}
              {workspaces.map((w) => <option key={w} value={w}>{dirName(w)}（{w}）</option>)}
            </select>
          </div>
          <div className="field">
            <label>{t('提示词（到期自动发送）', 'Prompt (sent when due)')}</label>
            <textarea rows={3} value={form.prompt}
              placeholder={t('总结最近一次提交的改动要点', 'Summarize the latest commit')}
              onChange={(e) => setForm({ ...form, prompt: e.target.value })} />
          </div>
          <div className="approval-row">
            <button className="btn primary" onClick={save}>
              {form.id ? t('保存修改', 'Save changes') : t('添加任务', 'Add task')}
            </button>
            {form.id && (
              <button className="btn" onClick={() => setForm({ ...emptyForm, workspace })}>
                {t('取消编辑', 'Cancel')}
              </button>
            )}
          </div>
        </div>
      </div>
    </div>
  )
}
