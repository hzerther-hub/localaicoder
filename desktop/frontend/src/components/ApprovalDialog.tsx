import { useStore, t } from '../store'

export default function ApprovalDialog() {
  const approval = useStore((s) => s.approval)
  const respond = useStore((s) => s.respond)
  if (!approval) return null
  return (
    <div className="approval-mask">
      <div className="approval-card">
        <h3>🛡 {t('工具执行审批', 'Tool approval')}</h3>
        <div className="tool-name">{approval.name}</div>
        <pre>{approval.summary}</pre>
        <div className="approval-row">
          <button className="btn danger" onClick={() => respond(false)}>{t('拒绝 (Esc)', 'Deny (Esc)')}</button>
          <button className="btn primary" autoFocus onClick={() => respond(true)}>{t('允许 (Enter)', 'Allow (Enter)')}</button>
        </div>
        <KeyHandler onAllow={() => respond(true)} onDeny={() => respond(false)} />
      </div>
    </div>
  )
}

function KeyHandler({ onAllow, onDeny }: { onAllow: () => void; onDeny: () => void }) {
  useKey('Enter', onAllow)
  useKey('Escape', onDeny)
  return null
}

import { useEffect } from 'react'
function useKey(key: string, fn: () => void) {
  useEffect(() => {
    const h = (e: KeyboardEvent) => {
      if (e.key === key) {
        e.preventDefault()
        e.stopPropagation()
        fn()
      }
    }
    window.addEventListener('keydown', h, true)
    return () => window.removeEventListener('keydown', h, true)
  })
}
