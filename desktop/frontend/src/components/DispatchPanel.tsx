// 派发设置面板（对齐 Tk ui_panel_dispatch.py）：总开关/智排/本地大脑/三个云端目标。
import { useEffect, useState } from 'react'
import { api, ModelInfo } from '../bridge'
import { useStore, t } from '../store'

export default function DispatchPanel() {
  const show = useStore((s) => s.showDispatchPanel)
  const setShow = useStore((s) => s.setShowDispatchPanel)
  const models = useStore((s) => s.models)
  const [cfg, setCfg] = useState<Record<string, any>>({})
  const [saved, setSaved] = useState(false)

  useEffect(() => {
    if (show) api.getDispatchConfig().then(setCfg)
  }, [show])
  if (!show) return null

  const save = (patch: Record<string, any>) => {
    const next = { ...cfg, ...patch }
    setCfg(next)
    api.setDispatchConfig(next)
    setSaved(true)
    setTimeout(() => setSaved(false), 1200)
  }

  const localModels = models.filter((m: ModelInfo) => m.local)
  const cloudModels = models.filter((m: ModelInfo) => !m.local)

  return (
    <div className="modal-mask" onClick={(e) => e.target === e.currentTarget && setShow(false)}>
      <div className="modal">
        <h3>
          ⚡ {t('模型派发', 'Model Dispatch')}
          <span style={{ display: 'flex', alignItems: 'center', gap: 10 }}>
            {saved && <span style={{ fontSize: 12, color: 'var(--green)' }}>✓</span>}
            <button className="x" onClick={() => setShow(false)}>✕</button>
          </span>
        </h3>

        <label className="checkline">
          <input type="checkbox" checked={!!cfg.model_dispatch} onChange={(e) => save({ model_dispatch: e.target.checked })} />
          {t('总开关：暴露 call_model 工具（本地大脑可委派云端）', 'Expose call_model tool')}
        </label>
        <label className="checkline">
          <input type="checkbox" checked={!!cfg.dispatch_smart} onChange={(e) => save({ dispatch_smart: e.target.checked })} />
          {t('智排：按任务类型自动路由 / 识图预路由', 'Smart routing / vision pre-route')}
        </label>
        <label className="checkline">
          <input type="checkbox" checked={!!cfg.auto_cloud_fallback} onChange={(e) => save({ auto_cloud_fallback: e.target.checked })} />
          {t('本地模型不可用时自动回退云端', 'Auto cloud fallback')}
        </label>

        <div className="field">
          <label>{t('本地大脑（call_model 编排者）', 'Local brain (orchestrator)')}</label>
          <select value={cfg.dispatch_model || ''} onChange={(e) => save({ dispatch_model: e.target.value })}>
            <option value="">—</option>
            {localModels.map((m) => <option key={m.key} value={m.key}>{m.display_name}</option>)}
          </select>
        </div>
        <div className="field-row">
          <div className="field">
            <label>{t('云端 · 简单任务', 'Cloud · simple')}</label>
            <select value={cfg.dispatch_flash || ''} onChange={(e) => save({ dispatch_flash: e.target.value })}>
              <option value="">—</option>
              {cloudModels.map((m) => <option key={m.key} value={m.key}>{m.display_name}</option>)}
            </select>
          </div>
          <div className="field">
            <label>{t('云端 · 复杂/重推理', 'Cloud · complex')}</label>
            <select value={cfg.dispatch_pro || ''} onChange={(e) => save({ dispatch_pro: e.target.value })}>
              <option value="">—</option>
              {cloudModels.map((m) => <option key={m.key} value={m.key}>{m.display_name}</option>)}
            </select>
          </div>
        </div>
        <div className="field">
          <label>{t('云端 · 识图兜底', 'Cloud · vision')}</label>
          <select value={cfg.dispatch_vision || ''} onChange={(e) => save({ dispatch_vision: e.target.value })}>
            <option value="">—</option>
            {cloudModels.filter((m) => m.vision).map((m) => <option key={m.key} value={m.key}>{m.display_name} 👁</option>)}
          </select>
        </div>
      </div>
    </div>
  )
}
