// 设置面板：界面语言 / 独立提问 / 聊天字号。
import { useEffect, useState } from 'react'
import { api, Prefs } from '../bridge'
import { useStore, t } from '../store'

export default function SettingsPanel() {
  const show = useStore((s) => s.showSettingsPanel)
  const setShow = useStore((s) => s.setShowSettingsPanel)
  const prefs = useStore((s) => s.prefs)
  const workspace = useStore((s) => s.workspace)
  const [lang, setLang] = useState('zh')
  const [standalone, setStandalone] = useState(false)
  const [fontSize, setFontSize] = useState(10)

  useEffect(() => {
    if (!show || !prefs) return
    setLang(prefs.language === 'en' ? 'en' : 'zh')
    setStandalone(!!prefs.standalone)
    setFontSize(prefs.font_size || 10)
  }, [show, prefs])
  if (!show) return null

  const save = async () => {
    const p: Prefs = { ...(prefs as Prefs), language: lang, standalone, font_size: fontSize }
    await api.setPrefs(p)
    useStore.setState({ prefs: p, lang: lang === 'en' ? 'en' : 'zh' })
    setShow(false)
  }

  return (
    <div className="modal-mask" onClick={(e) => e.target === e.currentTarget && setShow(false)}>
      <div className="modal">
        <h3>
          ⚙ {t('设置', 'Settings')}
          <button className="x" onClick={() => setShow(false)}>✕</button>
        </h3>
        <div className="field">
          <label>{t('界面语言', 'Language')}</label>
          <select value={lang} onChange={(e) => setLang(e.target.value)}>
            <option value="zh">中文</option>
            <option value="en">English</option>
          </select>
        </div>
        <div className="field">
          <label>{t('聊天字号', 'Chat font size')}</label>
          <input type="number" min={8} max={24} value={fontSize}
            onChange={(e) => setFontSize(Number(e.target.value) || 10)} />
        </div>
        <label className="check-row" style={{ display: 'flex', alignItems: 'center', gap: 8, margin: '10px 0 14px', fontSize: 12.5, color: 'var(--text-dim)' }}>
          <input type="checkbox" checked={standalone} onChange={(e) => setStandalone(e.target.checked)} />
          {t('独立提问（每条消息不带历史上下文）', 'Standalone mode (send each message without history)')}
        </label>
        <div style={{ fontSize: 12, color: 'var(--text-faint)', marginBottom: 12 }}>
          {t('当前工作区', 'Workspace')}: <b style={{ color: 'var(--text-dim)' }}>{workspace || '—'}</b>
        </div>
        <div className="approval-row">
          <button className="btn primary" onClick={save}>{t('保存', 'Save')}</button>
        </div>
      </div>
    </div>
  )
}
