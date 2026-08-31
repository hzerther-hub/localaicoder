// 任务步骤条（对齐 Python 版 todo 步骤条）：工具调用驱动，
// ◐进行中/✅完成/✗被拒，发送即显示"处理中…"占位，整轮结束消失。
// 最多可见 6 条（内部滚动，新步骤自动滚入视野），整条可折叠。
import { useEffect, useRef, useState } from 'react'
import { useStore, t } from '../store'
import ThinkingDots from './ThinkingDots'

export default function StepBar() {
  const steps = useStore((s) => s.steps)
  const running = useStore((s) => s.running)
  const round = useStore((s) => s.round)
  const [collapsed, setCollapsed] = useState(false)
  const bodyRef = useRef<HTMLDivElement>(null)
  // 新步骤出现时自动滚到底部（最新步骤始终可见）
  useEffect(() => {
    const el = bodyRef.current
    if (el) el.scrollTop = el.scrollHeight
  }, [steps, collapsed])
  if (!steps.length) return null
  const done = steps.filter((s) => s.status === 'done').length
  return (
    <div className="stepbar">
      <div className="stepbar-head" onClick={() => setCollapsed(!collapsed)}>
        <span>
          📋 {t('任务步骤', 'Steps')} {done}/{steps.length}
          {running && <span className="running-chip"><ThinkingDots /> {t('进行中', 'running')}{round > 0 ? ` · ${t('第', 'round')} ${round} ${t('轮', '')}` : ''}</span>}
        </span>
        <span className="fold">{collapsed ? '▴' : '▾'}</span>
      </div>
      {!collapsed && (
        <div className="stepbar-body" ref={bodyRef}>
          {steps.map((s) => (
            <div key={s.id} className={`step-row ${s.status}`} title={s.title}>
              <span className="st-icon">{s.status === 'done' ? '✅' : s.status === 'deny' ? '✗' : s.status === 'wait' ? '○' : '◐'}</span>
              <span className="st-title">{s.title}</span>
            </div>
          ))}
        </div>
      )}
    </div>
  )
}
