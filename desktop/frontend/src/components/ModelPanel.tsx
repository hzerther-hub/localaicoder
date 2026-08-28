// 模型管理面板（Provider 优先，仿参考图）：左选供应商，右管理端点+该供应商模型列表。
// 深色主题，与整体一致。
import { useEffect, useState } from 'react'
import { api, LocalModelInfo, onEvent } from '../bridge'
import { useStore, t } from '../store'

interface ProviderView {
  id: string; name: string; base_url: string; api_key: string; api_format: string
  enabled: boolean; models: any[]
}

const STATE_DOT: Record<string, { c: string; label: string }> = {
  active: { c: 'var(--green)', label: '运行中' },
  loading: { c: 'var(--amber)', label: '加载中' },
  stopped: { c: 'var(--text-faint)', label: '未运行' },
}

const API_FORMATS = ['chat_completions', 'responses', 'opencode']

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

  const startEdit = (p: ProviderView) => {
    setDraft({ ...p })
    setEditing(true)
    setErr('')
  }
  const save = async () => {
    if (!draft) return
    if (!draft.base_url) { setErr(t('Base URL 必填', 'Base URL required')); return }
    await api.saveProvider(draft.id, draft.name, draft.base_url, draft.api_key, draft.api_format)
    setEditing(false)
    setCandidates(null)
    reload()
    refresh()
  }
  const addModel = async () => {
    if (!newModel.trim() || !cur) return
    await api.addProviderModel(cur.id, newModel.trim(), false)
    setNewModel('')
    reload()
    refresh()
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
              setDraft({ id: 'custom', name: t('自定义', 'Custom'), base_url: '', api_key: '', api_format: 'chat_completions', enabled: true, models: [] })
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
                      {fetching ? '⏳' : '🔍'} {t('获取模型', 'Fetch models')}
                    </button>
                  </div>
                  <label>API {t('格式', 'format')}</label>
                  <select value={draft.api_format} onChange={(e) => setDraft({ ...draft, api_format: e.target.value })}>
                    {API_FORMATS.map((f) => <option key={f} value={f}>{f}</option>)}
                  </select>
                  <label>API Key</label>
                  <input type="password" value={draft.api_key} onChange={(e) => setDraft({ ...draft, api_key: e.target.value })} />
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
                      <div
                        key={m.key}
                        className={`mm-row${m.key === currentModel ? ' active' : ''}`}
                        onClick={() => setModel(m.key)}
                      >
                        {dot && <span className="state-dot" style={{ background: dot.c }} title={dot.label} />}
                        <span className="mn">{m.display_name}</span>
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
                        <span className="cap">{(m.context_window / 1e6).toFixed(1)}M</span>
                        {local && (
                          <button className="mm-mini" onClick={async (e) => {
                            e.stopPropagation()
                            await api.localModelAction(m.key, local.state === 'stopped' ? 'start' : 'stop')
                          }}>{local.state === 'stopped' ? '▶' : '■'}</button>
                        )}
                        <button className="mm-mini" onClick={(e) => { e.stopPropagation(); setModel(m.key) }}>◉</button>
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
