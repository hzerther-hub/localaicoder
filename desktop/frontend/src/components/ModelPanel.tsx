// 模型管理面板（Provider 优先，仿参考图）：左选供应商，右管理端点+该供应商模型列表。
// 深色主题，与整体一致。
import { useEffect, useState } from 'react'
import { api, LocalModelInfo, onEvent } from '../bridge'
import { useStore, t } from '../store'
import ThinkingDots from './ThinkingDots'

interface ProviderView {
  id: string; name: string; base_url: string; api_key: string; api_format: string
  api_keys?: string[]  // 凭据池（编辑框里一行一个）
  enabled: boolean; models: any[]
}

const STATE_DOT: Record<string, { c: string; label: string }> = {
  active: { c: 'var(--green)', label: '运行中' },
  loading: { c: 'var(--amber)', label: '加载中' },
  stopped: { c: 'var(--text-faint)', label: '未运行' },
}

const API_FORMATS = ['chat_completions', 'anthropic_messages', 'responses', 'gemini', 'opencode']

export default function ModelPanel() {
  const show = useStore((s) => s.showModelPanel)
  const setShow = useStore((s) => s.setShowModelPanel)
  const currentModel = useStore((s) => s.currentModel)
  const setModel = useStore((s) => s.setModel)
  const refresh = useStore((s) => s.init)

  const [providers, setProviders] = useState<ProviderView[]>([])
  const [sel, setSel] = useState<string>('')
  const [locals, setLocals] = useState<LocalModelInfo[]>([])
  const [editing, setEditing] = useState(false)
  const [draft, setDraft] = useState<ProviderView | null>(null)
  const [newModel, setNewModel] = useState('')
  const [err, setErr] = useState('')
  const [tip, setTip] = useState('')
  const [fetching, setFetching] = useState(false)
  const [candidates, setCandidates] = useState<string[] | null>(null)
  const [checked, setChecked] = useState<Set<string>>(new Set())

  const fetchModels = async () => {
    if (!draft) return
    setFetching(true)
    setErr('')
    const list = await api.fetchEndpointModels(draft.base_url, draft.api_key)
    setFetching(false)
    if (!list || !list.length) {
      setErr(t('探测失败或端点未返回模型，请检查 Base URL / API Key', 'No models returned; check URL/key'))
      return
    }
    setCandidates(list)
    setChecked(new Set())
  }

  const confirmAdd = async () => {
    if (!cur && !draft) return
    const pid = draft?.id || cur?.id || ''
    const ids = Array.from(checked)
    let ok = false
    for (const id of ids) ok = (await api.addProviderModel(pid, id, false)) || ok
    setCandidates(null)
    if (ids.length) { reload(); refresh(); setTip(t('已添加', 'Added')) }
  }

  const setCap = async (m: any, patch: any) => {
    await api.setModelCapability(m.key, patch.vision, patch.reasoning, patch.effort)
    reload()
    refresh()
    setTip(t('已更新', 'Updated'))
    setTimeout(() => setTip(''), 1500)
  }

  // 定价编辑状态（$/百万：缓存命中输入/未命中输入/输出）
  const [priceEdit, setPriceEdit] = useState<{ key: string; hit: string; miss: string; out: string } | null>(null)
  // 上下文窗口编辑状态（tokens）
  const [cwEdit, setCwEdit] = useState<{ key: string; val: string } | null>(null)
  const [moEdit, setMoEdit] = useState<{ key: string; val: string } | null>(null)

  const reload = () => {
    api.listProviders().then((ps) => {
      setProviders(ps || [])
      if (!sel && ps?.length) setSel(ps[0].id)
    })
  }
  useEffect(() => {
    if (!show) return
    reload()
    api.listLocalModels().then(setLocals)
    const off = onEvent('local:status', () => api.listLocalModels().then(setLocals))
    return off
  }, [show])
  if (!show) return null

  const cur = providers.find((p) => p.id === sel)
  // DeepSeek 端点预填官方现价（$/百万：hit=缓存命中输入 miss=未命中输入 out=输出）
  const isDeepseek = (cur?.base_url || '').includes('deepseek.com')
  const OFFICIAL_DS = { hit: '0.27', miss: '1.1', out: '4.4' }
  const savePrice = async (m: any) => {
    if (!priceEdit) return
    const num = (s: string) => { const v = parseFloat(s); return Number.isFinite(v) && v > 0 ? v : 0 }
    await api.setModelPricing(m.key, num(priceEdit.hit), num(priceEdit.miss), num(priceEdit.out))
    setPriceEdit(null)
    reload()
    refresh()
    setTip(t('定价已保存，统计条将显示费用', 'Pricing saved'))
    setTimeout(() => setTip(''), 1800)
  }

  const saveCw = async (m: any) => {
    if (!cwEdit || cwEdit.key !== m.key) return
    const n = parseInt(cwEdit.val, 10)
    setCwEdit(null)
    if (!(Number.isFinite(n) && n > 0)) return
    await api.setModelContextWindow(m.key, n)
    reload()
    refresh()
    setTip(t('上下文窗口已更新', 'Context window updated'))
    setTimeout(() => setTip(''), 1800)
  }

  const saveMo = async (m: any) => {
    if (!moEdit || moEdit.key !== m.key) return
    const n = parseInt(moEdit.val, 10)
    setMoEdit(null)
    if (!(Number.isFinite(n) && n > 0)) return
    await api.setModelMaxOutputTokens(m.key, n)
    reload()
    refresh()
    setTip(t('最大输出 token 已更新', 'Max output tokens updated'))
    setTimeout(() => setTip(''), 1800)
  }

  const startEdit = (p: ProviderView) => {
    setDraft({ ...p })
    setEditing(true)
    setErr('')
  }
  const save = async () => {
    if (!draft) return
    if (!draft.base_url) { setErr(t('Base URL 必填', 'Base URL required')); return }
    const apiKeys = (draft.api_keys || []).filter((k) => k.trim())
    await api.saveProvider(draft.id, draft.name, draft.base_url, draft.api_key, draft.api_format, apiKeys)
    setEditing(false)
    setCandidates(null)
    setSel(draft.id) // 新建后立即选中（0 模型的供应商也要可见可选）
    reload()
    refresh()
  }
  const addModel = async () => {
    if (!newModel.trim() || !cur) return
    const ok = await api.addProviderModel(cur.id, newModel.trim(), false)
    setNewModel('')
    reload()
    refresh()
    // 不再静默：失败/重复时给出可见反馈，避免「点了没反应」
    setTip(ok ? t('已添加', 'Added') : t('模型已存在或添加失败', 'Already exists or failed'))
    setTimeout(() => setTip(''), 1800)
  }

  return (
    <div className="modal-mask" onClick={(e) => e.target === e.currentTarget && setShow(false)}>
      <div className="model-mgr">
        <h3 className="mm-title">
          {t('模型设置', 'Model Settings')} {tip && <span className="mm-tip">{tip}</span>}
          <button className="x" onClick={() => setShow(false)}>✕</button>
        </h3>
        <div className="mm-sub">{t('管理自定义模型供应商，配置后可在聊天时选择使用。', 'Manage providers; select in chat afterward.')}</div>

        <div className="mm-body">
          {/* 左：供应商列表 */}
          <div className="mm-side">
            <div className="mm-side-label">{t('供应商', 'Providers')}</div>
            {providers.map((p) => (
              <div
                key={p.id}
                className={`mm-sel${p.id === sel ? ' active' : ''}`}
                onClick={() => { setSel(p.id); setEditing(false) }}
              >
                <span className="ico">{(p.base_url || '').includes('127.0.0.1') || (p.base_url || '').includes('localhost') ? '🖥' : '◇'}</span>
                <span className="nm">{p.name || p.id}</span>
                <span className="dot" style={{ background: 'var(--green)' }} />
              </div>
            ))}
            <button className="mm-add-provider" onClick={() => {
              // 每个新供应商必须拿唯一 id：历史上这里写死 'custom'，第二个供应商
              // 保存时按 id 覆盖第一个（name/base_url/key 全被带走，模型却留下）——合并事故。
              const id = 'custom-' + Date.now().toString(36)
              setDraft({ id, name: t('自定义', 'Custom'), base_url: '', api_key: '', api_format: 'chat_completions', api_keys: [], enabled: true, models: [] })
              setEditing(true); setErr('')
            }}>＋ {t('添加供应商', 'Add provider')}</button>
          </div>

          {/* 右：供应商详情 + 模型列表 */}
          <div className="mm-main">
            {editing && draft ? (
              <>
                <div className="mm-head">
                  <div className="mm-pname">✏️ {draft.id} <span className="mm-enable on">{t('编辑中', 'Editing')}</span></div>
                </div>
                <div className="mm-fields">
                  <label>{t('供应商 ID', 'Provider ID')}</label>
                  <input value={draft.id} disabled onChange={(e) => setDraft({ ...draft, id: e.target.value })} />
                  <label>{t('名称', 'Name')}</label>
                  <input value={draft.name} onChange={(e) => setDraft({ ...draft, name: e.target.value })} />
                  <label>Base URL</label>
                  <div style={{ display: 'flex', gap: 6 }}>
                    <input value={draft.base_url} onChange={(e) => setDraft({ ...draft, base_url: e.target.value })} placeholder="https://api.example.com/v1" />
                    <button className="btn" onClick={fetchModels} disabled={fetching || !draft.base_url}>
                      {fetching ? <ThinkingDots className="sm" /> : '🔍'} {t('获取模型', 'Fetch models')}
                    </button>
                  </div>
                  <label>API {t('格式', 'format')}</label>
                  <select value={draft.api_format} onChange={(e) => setDraft({ ...draft, api_format: e.target.value })}>
                    {API_FORMATS.map((f) => <option key={f} value={f}>{f}</option>)}
                  </select>
                  <label>API Key</label>
                  <input type="password" value={draft.api_key} onChange={(e) => setDraft({ ...draft, api_key: e.target.value })} />
                  <label>API Keys {t('池（一行一个，轮换/冷却容错）', 'pool (one per line, rotation on 401/429)')}</label>
                  <textarea
                    rows={3}
                    value={(draft.api_keys || []).join('\n')}
                    onChange={(e) => setDraft({ ...draft, api_keys: e.target.value.split('\n') })}
                    placeholder={'sk-a\nsk-b'}
                  />
                  {err && <div className="mm-err">{err}</div>}
                  <div className="mm-actions">
                    <button className="btn" onClick={() => setEditing(false)}>{t('取消', 'Cancel')}</button>
                    <button className="btn primary" onClick={save}>{t('保存', 'Save')}</button>
                  </div>
                </div>
              </>
            ) : cur ? (
              <>
                <div className="mm-head">
                  <div className="mm-pname">◇ {cur.name || cur.id} <span className="mm-enable on">{t('已启用', 'Enabled')}</span></div>
                  <div style={{ display: 'flex', gap: 8 }}>
                    <button className="btn" onClick={() => startEdit(cur)}>✏️ {t('编辑', 'Edit')}</button>
                    <button className="mm-del" title={t('重置', 'Reset')} onClick={() => reload()}>↻</button>
                  </div>
                </div>
                <div className="mm-meta">
                  <div><span>Base URL</span><code>{cur.base_url}</code></div>
                  {(Boolean(cur.api_key)) && <div><span>API Key</span><code>● 已配置</code></div>}
                  <div><span>API {t('格式', 'format')}</span><code>{cur.api_format || 'chat_completions'}</code></div>
                </div>

                <div className="mm-models-label">{t('模型列表', 'Models')}</div>
                <div className="mm-models">
                  {cur.models.map((m) => {
                    const local = locals.find((l) => l.key === m.key)
                    const dot = local ? STATE_DOT[local.state || 'stopped'] : null
                    return (
                      <div key={m.key}>
                      <div
                        className={`mm-row${m.key === currentModel ? ' active' : ''}${m.disabled ? ' disabled' : ''}`}
                        onClick={() => { if (!m.disabled) setModel(m.key) }}
                      >
                        {dot && <span className="state-dot" style={{ background: dot.c }} title={dot.label} />}
                        <span className="mn">{m.display_name}</span>
                        {m.disabled && <span className="mm-tag" title={t('已隐藏/禁用', 'Hidden/disabled')}>🙈</span>}
                        <select
                          className="mm-effort"
                          value={m.reasoning_effort || ''}
                          onClick={(e) => e.stopPropagation()}
                          onChange={(e) => setCap(m, { effort: e.target.value })}
                          title={t('推理等级', 'Reasoning effort')}
                        >
                          {(m.reasoning_choices && m.reasoning_choices.length ? m.reasoning_choices : ['', 'low', 'medium', 'high']).map((c: string) => (
                            <option key={c} value={c}>{c === '' ? t('默认', 'default') : c}</option>
                          ))}
                        </select>
                        <span
                          className={`mm-vision${m.vision ? ' on' : ''}`}
                          title={t('识图模型（多模态输入）', 'Vision model')}
                          onClick={(e) => { e.stopPropagation(); setCap(m, { vision: !m.vision }) }}
                        >👁</span>
                        <span
                          className="cap"
                          title={t('上下文窗口（tokens，可编辑）：决定 max_tokens 输出预算（=窗口/4）与压缩阈值', 'Context window (tokens, editable): sets max_tokens budget & compaction threshold')}
                          onClick={(e) => e.stopPropagation()}
                        >
                          <input
                            className="cap-edit"
                            type="number"
                            min={0}
                            lang="en"
                            value={cwEdit && cwEdit.key === m.key ? cwEdit.val : (m.context_window || '')}
                            placeholder={'—'}
                            onChange={(e) => setCwEdit({ key: m.key, val: e.target.value })}
                            onBlur={() => void saveCw(m)}
                            onKeyDown={(e) => { if (e.key === 'Enter') (e.target as HTMLInputElement).blur() }}
                          />
                          {m.context_window > 0 ? `${(m.context_window / 1e6).toFixed(1)}M` : ''}
                        </span>
                        <span
                          className="cap"
                          title={t('最大输出 token（可编辑）：未配置时用默认/窗口/4', 'Max output tokens (editable); defaults if unset')}
                          onClick={(e) => e.stopPropagation()}
                        >
                          <input
                            className="cap-edit"
                            type="number"
                            min={0}
                            lang="en"
                            value={moEdit && moEdit.key === m.key ? moEdit.val : (m.max_output_tokens || '')}
                            placeholder={t('默认', 'default')}
                            onChange={(e) => setMoEdit({ key: m.key, val: e.target.value })}
                            onBlur={() => void saveMo(m)}
                            onKeyDown={(e) => { if (e.key === 'Enter') (e.target as HTMLInputElement).blur() }}
                          />
                          {m.max_output_tokens > 0 ? `${(m.max_output_tokens / 1024).toFixed(0)}K` : ''}
                        </span>
                        {local && (
                          <button className="mm-mini" onClick={async (e) => {
                            e.stopPropagation()
                            await api.localModelAction(m.key, local.state === 'stopped' ? 'start' : 'stop')
                          }}>{local.state === 'stopped' ? '▶' : '■'}</button>
                        )}
                        <button className="mm-mini priced" title={t('定价（$/百万），配置后统计条显示费用', 'Pricing, enables cost in stats')}
                          onClick={(e) => {
                            e.stopPropagation()
                            setPriceEdit(priceEdit?.key === m.key ? null : {
                              key: m.key,
                              hit: String(m.price_in_hit_per_m || (isDeepseek ? OFFICIAL_DS.hit : '')),
                              miss: String(m.price_in_miss_per_m || (isDeepseek ? OFFICIAL_DS.miss : '')),
                              out: String(m.price_out_per_m || (isDeepseek ? OFFICIAL_DS.out : '')),
                            })
                          }}>{m.priced ? '💰' : '🏷'}</button>
                        <button className="mm-mini" onClick={(e) => { e.stopPropagation(); setModel(m.key) }}>◉</button>
                        <button
                          className="mm-mini hide"
                          title={m.disabled ? t('重新启用（显示）', 'Enable (show)') : t('隐藏/禁用（从选择器移除，不参与派发）', 'Hide/disable (remove from selector)')}
                          onClick={async (e) => {
                            e.stopPropagation()
                            await api.setModelDisabled(m.key, !m.disabled)
                            reload(); refresh()
                          }}
                        >{m.disabled ? '✅' : '🙈'}</button>
                        <button
                          className="mm-mini del"
                          onClick={async (e) => {
                            e.stopPropagation()
                            if (confirm(t(`删除模型 ${m.key}？`, `Remove ${m.key}?`))) {
                              await api.removeModel(m.key)
                              reload(); refresh()
                            }
                          }}
                        >🗑</button>
                      </div>
                      {priceEdit && priceEdit.key === m.key && (
                        <div className="mm-price-edit" onClick={(e) => e.stopPropagation()}>
                          <span className="lbl">{t('定价 $/百万', 'Pricing $/M')}</span>
                          <input value={priceEdit.hit} placeholder={`hit ${isDeepseek ? OFFICIAL_DS.hit : '0.27'}`}
                            onChange={(e) => setPriceEdit({ ...priceEdit, hit: e.target.value })} />
                          <input value={priceEdit.miss} placeholder={`miss ${isDeepseek ? OFFICIAL_DS.miss : '1.1'}`}
                            onChange={(e) => setPriceEdit({ ...priceEdit, miss: e.target.value })} />
                          <input value={priceEdit.out} placeholder={`out ${isDeepseek ? OFFICIAL_DS.out : '4.4'}`}
                            onChange={(e) => setPriceEdit({ ...priceEdit, out: e.target.value })} />
                          {isDeepseek && (
                            <button className="btn" title={t('填入 DeepSeek 官方现价', 'Fill official DeepSeek price')}
                              onClick={() => setPriceEdit({ ...priceEdit, hit: OFFICIAL_DS.hit, miss: OFFICIAL_DS.miss, out: OFFICIAL_DS.out })}>
                              {t('官方价', 'official')}
                            </button>
                          )}
                          <button className="btn primary" onClick={() => void savePrice(m)}>{t('保存', 'Save')}</button>
                        </div>
                      )}
                      </div>
                    )
                  })}
                </div>

                <div className="mm-add-row">
                  <input value={newModel} onChange={(e) => setNewModel(e.target.value)}
                    placeholder={t('输入模型 ID，如 gpt-5.2 / deepseek-v4-pro', 'Model ID, e.g. deepseek-v4-pro')}
                    onKeyDown={(e) => { if (e.key === 'Enter') addModel() }} />
                  <button className="btn primary" onClick={addModel}>＋ {t('添加模型', 'Add model')}</button>
                </div>
              </>
            ) : (
              <div className="mm-empty">{t('选择左侧供应商，或点击「添加供应商」', 'Select a provider or add one')}</div>
            )}
          </div>
        </div>
      </div>
      {candidates && (
        <div className="mm-cand-mask" onClick={() => setCandidates(null)}>
          <div className="mm-cand" onClick={(e) => e.stopPropagation()}>
            <h4>🔍 {t('选择要添加的模型', 'Choose models to add')}</h4>
            <div className="grid">
              {candidates.map((id) => (
                <div key={id} className={`row${checked.has(id) ? ' checked' : ''}`}
                  onClick={() => {
                    const next = new Set(checked)
                    if (next.has(id)) next.delete(id); else next.add(id)
                    setChecked(next)
                  }}>
                  <span>{checked.has(id) ? '☑' : '☐'}</span> {id}
                </div>
              ))}
            </div>
            <div className="foot">
              <span style={{ fontSize: 11, color: 'var(--text-faint)', alignSelf: 'center' }}>
                {t(`已选 ${checked.size} 个`, `${checked.size} selected`)}
              </span>
              <button className="btn" onClick={() => setCandidates(null)}>{t('取消', 'Cancel')}</button>
              <button className="btn primary" onClick={confirmAdd}>{t('确定添加', 'Add')}</button>
            </div>
          </div>
        </div>
      )}
    </div>
  )
}
