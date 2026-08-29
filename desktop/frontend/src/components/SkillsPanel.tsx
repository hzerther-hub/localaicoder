// 技能面板（对齐 Python 版 ui_panel_skills）：总开关 + 蒸馏模型设置；
// 已生效技能列表（用户级/项目级，可删除）；蒸馏草稿区（载入编辑 → 保存/转正/丢弃）。
// 设计要点：LLM 只产草稿，人握转正权。
import { useEffect, useState } from 'react'
import { api, SkillInfo, SkillsSettings } from '../bridge'
import { useStore, t } from '../store'

export default function SkillsPanel() {
  const show = useStore((s) => s.showSkillsPanel)
  const setShow = useStore((s) => s.setShowSkillsPanel)
  const models = useStore((s) => s.models)
  const notice = useStore((s) => s.notice)
  const [skills, setSkills] = useState<SkillInfo[]>([])
  const [drafts, setDrafts] = useState<SkillInfo[]>([])
  const [settings, setSettings] = useState<SkillsSettings>({ enabled: false, distill_model: '' })
  const [editing, setEditing] = useState<{ path: string; text: string; draft: boolean } | null>(null)
  const [dirty, setDirty] = useState(false)
  const [filter, setFilter] = useState('')
  const [installURL, setInstallURL] = useState('')
  const [installing, setInstalling] = useState(false)

  useEffect(() => {
    if (!show) return
    api.listSkills().then(setSkills)
    api.listSkillDrafts().then(setDrafts)
    api.getSkillsSettings().then(setSettings)
  }, [show])

  if (!show) return null

  const refresh = () => {
    api.listSkills().then(setSkills)
    api.listSkillDrafts().then(setDrafts)
  }

  const saveSettings = (patch: Partial<SkillsSettings>) => {
    const next = { ...settings, ...patch }
    setSettings(next)
    api.setSkillsSettings(next.enabled, next.distill_model)
  }

  const scopeBadge = (scope: string) =>
    scope === 'project' ? t('项目', 'project')
      : scope === 'draft' ? t('草稿', 'draft')
        : scope === 'claude' ? 'Claude'
          : scope === 'opencode' ? 'OpenCode'
            : t('用户', 'user')

  // editableScope 只有本工具自有作用域可编辑/删除；外部源（Claude/OpenCode）只读
  const editableScope = (scope: string) => scope === 'user' || scope === 'project'

  const doSave = async () => {
    if (!editing) return
    const ok = editing.draft
      ? await api.saveSkillDraft(editing.path, editing.text)
      : await api.saveSkillText(editing.path, editing.text)
    if (ok) {
      setDirty(false)
      if (!editing.draft) refresh()
    } else {
      useStore.getState().notice(t(
        '保存失败：内容需保持合法 frontmatter（name/description/when）',
        'Save failed: frontmatter (name/description/when) must stay valid'
      ))
    }
  }

  // 过滤：匹配名称/描述/触发词（不分大小写）
  const q = filter.trim().toLowerCase()
  const visible = q
    ? skills.filter((sk) =>
        `${sk.name} ${sk.description} ${sk.when}`.toLowerCase().includes(q))
    : skills
  const visibleDrafts = q
    ? drafts.filter((d) =>
        `${d.name} ${d.description} ${d.when}`.toLowerCase().includes(q))
    : drafts

  const doAccept = async (d: SkillInfo) => {
    if (await api.acceptDraft(d.path)) {
      notice(t(`技能「${d.name}」已转正，后续会话自动注入。`, `Skill "${d.name}" accepted.`))
      refresh()
    }
  }
  const doDiscard = async (d: SkillInfo) => {
    if (await api.discardDraft(d.path)) refresh()
  }
  const doDelete = async (sk: SkillInfo) => {
    if (!confirm(t(`删除技能「${sk.name}」？`, `Delete skill "${sk.name}"?`))) return
    if (await api.deleteSkill(sk.path)) refresh()
  }
  const doSaveDraft = async () => {
    if (!editing) return
    if (await api.saveSkillDraft(editing.path, editing.text)) setDirty(false)
  }

  const doInstall = async () => {
    const u = installURL.trim()
    if (!u || installing) return
    setInstalling(true)
    try {
      const r = await api.installSkill(u)
      if (r?.error) {
        useStore.getState().notice(t(`安装失败：${r.error}`, `Install failed: ${r.error}`))
      } else {
        const names = (r?.installed || []) as string[]
        const skipped = Number(r?.skipped || 0)
        notice(names.length
          ? t(`已安装 ${names.length} 个技能：${names.join('、')}${skipped ? `（跳过同名 ${skipped}）` : ''}`,
              `Installed ${names.length} skills${skipped ? `, skipped ${skipped}` : ''}`)
          : t('未发现可安装的技能（需要 SKILL.md 或带 frontmatter 的 .md）',
              'No installable skills found (SKILL.md or frontmatter .md required)'))
        if (names.length) { refresh(); setInstallURL('') }
      }
    } finally {
      setInstalling(false)
    }
  }

  return (
    <div className="modal-mask" onClick={(e) => e.target === e.currentTarget && setShow(false)}>
      <div className="modal">
        <h3>
          ✦ {t('技能系统（经验沉淀 + 会话蒸馏）', 'Skills (experience + distillation)')}
          <button className="x" onClick={() => setShow(false)}>✕</button>
        </h3>

        <label className="checkline">
          <input
            type="checkbox" checked={settings.enabled}
            onChange={(e) => saveSettings({ enabled: e.target.checked })}
          />
          {t('启用：技能注入 system prompt + 会话结束自动蒸馏草稿', 'Enable: prompt injection + session distillation')}
        </label>
        <div className="field" style={{ marginTop: 8 }}>
          <label>{t('蒸馏模型（空 = 默认模型）', 'Distillation model (empty = default)')}</label>
          <select
            value={settings.distill_model}
            onChange={(e) => saveSettings({ distill_model: e.target.value })}
          >
            <option value="">{t('默认模型', 'Default model')}</option>
            {models.map((m) => (
              <option key={m.key} value={m.key}>{m.display_name}</option>
            ))}
          </select>
        </div>

        <div className="skills-filter">
          <input
            value={filter}
            onChange={(e) => setFilter(e.target.value)}
            placeholder={t('过滤技能（名称/描述/触发词）…', 'Filter skills (name/description/when)…')}
          />
        </div>

        <div className="skills-install">
          <input
            value={installURL}
            onChange={(e) => setInstallURL(e.target.value)}
            onKeyDown={(e) => { if (e.key === 'Enter') void doInstall() }}
            placeholder={t('从 GitHub 仓库或 .md 链接安装技能（如 anthropics/skills）…',
              'Install from a GitHub repo or a .md URL…')}
          />
          <button className="btn primary" disabled={installing || !installURL.trim()} onClick={doInstall}>
            {installing ? '⏳' : '⬇'} {t('安装', 'Install')}
          </button>
        </div>

        <div className="skills-sec">
          <div className="skills-sec-title">{t('已生效技能', 'Active skills')}（{visible.length}/{skills.length}）</div>
          {skills.length === 0 && (
            <div className="skills-empty">{t(
              '暂无技能。成功完成编码任务的会话结束后会自动蒸馏出草稿，在这里确认转正。',
              'No skills yet. Successful sessions will produce drafts here for confirmation.'
            )}</div>
          )}
          <div className="skills-list">
            {visible.map((sk) => (
              <div className="skills-row" key={sk.path} title={sk.body}>
                <span className="sk-badge">{scopeBadge(sk.scope)}</span>
                <span className="sk-name">{sk.name} — {sk.description}</span>
                {editableScope(sk.scope) && (
                  <>
                    <button className="btn" style={{ marginLeft: 'auto' }}
                      onClick={async () => setEditing({ path: sk.path, text: await api.loadSkillText(sk.path), draft: false })}>
                      {t('编辑', 'Edit')}
                    </button>
                    <button className="btn danger" onClick={() => doDelete(sk)}>🗑</button>
                  </>
                )}
              </div>
            ))}
            {skills.length > 0 && visible.length === 0 && (
              <div className="skills-empty">{t('无匹配技能', 'No matching skills')}</div>
            )}
          </div>
        </div>

        <div className="skills-sec">
          <div className="skills-sec-title">
            {t('蒸馏草稿', 'Drafts')}（{visibleDrafts.length}）{drafts.length > 0 && ' · ' + t('编辑后转正', 'edit then accept')}
          </div>
          {drafts.length === 0 && (
            <div className="skills-empty">{t('暂无待确认草稿', 'No pending drafts')}</div>
          )}
          <div className="skills-list">
            {visibleDrafts.map((d) => (
              <div className="skills-row" key={d.path} title={d.body}>
                <span className="sk-badge draft">{scopeBadge(d.scope)}</span>
                <span className="sk-name">{d.name} — {d.description}</span>
                <button className="btn" style={{ marginLeft: 'auto' }}
                  onClick={async () => setEditing({ path: d.path, text: await api.loadSkillText(d.path), draft: true })}>
                  {t('载入', 'Edit')}
                </button>
                <button className="btn primary" onClick={() => doAccept(d)}>✔ {t('转正', 'Accept')}</button>
                <button className="btn danger" onClick={() => doDiscard(d)}>✕ {t('丢弃', 'Discard')}</button>
              </div>
            ))}
          </div>
          {editing && (
            <div className="skills-editor">
              <textarea
                value={editing.text}
                onChange={(e) => { setEditing({ ...editing, text: e.target.value }); setDirty(true) }}
                rows={10}
                spellCheck={false}
              />
              <div className="skills-editor-ops">
                <span className="hint">{editing.draft
                  ? t('frontmatter：name / description / when', 'frontmatter: name / description / when')
                  : t('frontmatter 需保持完整，保存后立即生效', 'frontmatter must stay valid; takes effect on save')}</span>
                <button className="btn" disabled={!dirty} onClick={doSave}>
                  💾 {editing.draft ? t('保存草稿', 'Save draft') : t('保存', 'Save')}
                </button>
                <button className="btn" onClick={() => setEditing(null)}>{t('收起', 'Close')}</button>
              </div>
            </div>
          )}
        </div>
      </div>
    </div>
  )
}
