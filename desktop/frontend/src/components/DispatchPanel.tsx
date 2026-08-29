// 派发设置面板（对齐 Tk ui_panel_dispatch.py）：总开关/智排/本地大脑/三个云端目标。
import { useEffect, useState } from 'react'
import { api, ModelInfo } from '../bridge'
import { useStore, t } from '../store'

export default function DispatchPanel() {
  const show = useStore((s) => s.showDispatchPanel)
  const setShow = useStore((s) => s.setShowDispatchPanel)
  const models = useStore((s) => s.models)
  const [cfg, setCfg] = useState<Record<string, any>>({})
  const [sr, setSr] = useState<Record<string, any>>({})
  const [saved, setSaved] = useState(false)

  useEffect(() => {
    if (show) {
      api.getDispatchConfig().then(setCfg)
      api.getSmartRouting().then(setSr)
    }
  }, [show])
  if (!show) return null

  const save = (patch: Record<string, any>) => {
    const next = { ...cfg, ...patch }
    setCfg(next)
    api.setDispatchConfig(next)
    setSaved(true)
    setTimeout(() => setSaved(false), 1200)
  }

  // 智能路由：简单/复杂轮次分流（与 call_model 派发互补：前者自动换模型，后者模型自己委派）
  const saveSr = (patch: Record<string, any>) => {
    const next = { ...sr, ...patch }
    setSr(next)
    api.setSmartRouting(next)
    setSaved(true)
    setTimeout(() => setSaved(false), 1200)
  }
  const num = (v: any) => (typeof v === 'number' && v > 0 ? v : '')

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

        <div style={{ borderTop: '1px solid var(--border)', margin: '14px 0 10px' }} />
        <div style={{ fontWeight: 600, marginBottom: 8 }}>🧭 {t('智能路由（简单/复杂轮次分流）', 'Smart routing (simple/strong per turn)')}</div>
        <label className="checkline">
          <input type="checkbox" checked={!!sr.enabled} onChange={(e) => saveSr({ enabled: e.target.checked })} />
          {t('按提问复杂度自动选模型：简单走轻量、复杂走强力', 'Auto-pick lightweight vs strong model per turn')}
        </label>
        <div className="field-row">
          <div className="field">
            <label>{t('简单轮模型', 'Simple model')}</label>
            <select value={sr.simple_model || ''} onChange={(e) => saveSr({ simple_model: e.target.value })}>
              <option value="">{t('跟随「云端 · 简单任务」', 'Follow cloud simple')}</option>
              {cloudModels.map((m) => <option key={m.key} value={m.key}>{m.display_name}</option>)}
            </select>
          </div>
          <div className="field">
            <label>{t('复杂轮模型', 'Strong model')}</label>
            <select value={sr.strong_model || ''} onChange={(e) => saveSr({ strong_model: e.target.value })}>
              <option value="">{t('跟随「云端 · 复杂/重推理」', 'Follow cloud complex')}</option>
              {cloudModels.map((m) => <option key={m.key} value={m.key}>{m.display_name}</option>)}
            </select>
          </div>
        </div>
        <div className="field-row">
          <div className="field">
            <label>{t('简单判定·字符上限', 'Simple max chars')}</label>
            <input
              type="number" min={20} value={num(sr.simple_max_chars)}
              placeholder="160"
              onChange={(e) => saveSr({ simple_max_chars: Number(e.target.value) || 0 })}
            />
          </div>
          <div className="field">
            <label>{t('简单判定·词数上限', 'Simple max words')}</label>
            <input
              type="number" min={4} value={num(sr.simple_max_words)}
              placeholder="28"
              onChange={(e) => saveSr({ simple_max_words: Number(e.target.value) || 0 })}
            />
          </div>
        </div>
        <label className="checkline">
          <input type="checkbox" checked={sr.arbitrate !== false} onChange={(e) => saveSr({ arbitrate: e.target.checked })} />
          {t('拿不准时让本地大脑仲裁（推荐）', 'Arbitrate borderline turns with local brain')}
        </label>
        <div className="mm-sub" style={{ marginTop: 6 }}>
          {t('规则：首轮/含代码/含关键词/超长→复杂；命中不确定时由本地大脑快速判断；简单模型出错自动升级重试。', 'Turn 1 / code / keywords / long → strong; borderline turns arbitrated by local brain; simple-model errors auto-escalate.')}
        </div>
      </div>
    </div>
  )
}
