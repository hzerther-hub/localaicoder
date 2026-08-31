// 顶栏（深色，与整体主题一致）：模型切换 / 权限 / 工作区 / 功能入口。
import { useStore, t } from '../store'

function dirName(ws: string): string {
  if (!ws) return ''
  const parts = ws.split(/[\/]/).filter(Boolean)
  return parts.length ? parts[parts.length - 1] : ws
}

const ICONS: Record<string, string> = { always: '⚡', ask: '🛡', readonly: '🔒' }

export default function Toolbar() {
  const product = useStore((s) => s.product)
  const models = useStore((s) => s.models)
  const currentModel = useStore((s) => s.currentModel)
  const routeModel = useStore((s) => s.routeModel)
  const routingActive = useStore((s) => s.routingActive)
  const setModel = useStore((s) => s.setModel)
  const mode = useStore((s) => s.mode)
  const cycleMode = useStore((s) => s.cycleMode)
  const workspace = useStore((s) => s.workspace)
  const branch = useStore((s) => s.branch)
  const lang = useStore((s) => s.lang)
  const setLang = useStore((s) => s.setLang)
  const showEditor = useStore((s) => s.showEditor)
  const setShowEditor = useStore((s) => s.setShowEditor)
  const features = product.features || {}
  // 选择器显示本轮实际用到的模型：智排路由切换后回显真实模型（回合结束回到所选项）
  const shownModel = routeModel || currentModel
  const current = models.find((m) => m.key === shownModel)

  return (
    <div className="toolbar">
      <div className="brand">
        <div className="logo">L</div>
        <span>{product.title}</span>
      </div>

      <select
        className="tb-select"
        value={shownModel}
        onChange={(e) => setModel(e.target.value)}
        title={t('模型', 'Model')}
      >
        {models.map((m) => (
          <option key={m.key} value={m.key}>
            {m.display_name}{m.vision ? ' 👁' : ''}{m.reasoning ? ' 🧠' : ''}{m.local ? ' · 本地' : ''}
          </option>
        ))}
      </select>

      {current?.reasoning && current.reasoning_choices?.length > 0 && (
        <select
          className="tb-select" style={{ maxWidth: 110 }}
          value={current.reasoning_effort || ''}
          onChange={(e) => apiSetEffort(current.key, e.target.value)}
          title={t('推理等级', 'Reasoning effort')}
        >
          {current.reasoning_choices.map((c: string) => (
            <option key={c} value={c}>{c === '' ? t('默认', 'default') : c}</option>
          ))}
        </select>
      )}

      <button className="tb-btn" onClick={cycleMode} title={t('权限模式', 'Permission mode')}>
        {ICONS[mode] || '⚡'} {mode}
      </button>

      <div
        className="ws-chip"
        title={`${workspace} · ${t('点击切换工作目录', 'click to change')}`}
        onClick={async () => {
          const dir = await useStore.getState().pickWorkspace()
          if (dir) await useStore.getState().setWorkspace(dir)
        }}
      >
        <span>📂</span>
        <span className="path">{dirName(workspace) || '—'}</span>
        <span className="chg">⇄</span>
        {branch && <span className="git-badge">{branch}</span>}
      </div>

      <div className="spacer" />

      {features.editor !== false && (
        <button className={`tb-btn${showEditor ? ' active' : ''}`} onClick={() => setShowEditor(!showEditor)} title={t('编辑器', 'Editor')}>
          📄 {t('编辑器', 'Editor')}
        </button>
      )}
      <button className="tb-btn" onClick={() => useStore.getState().setShowModelPanel(true)}>⚙ {t('模型', 'Models')}</button>
      {features.rag !== false && (
        <button className="tb-btn" onClick={() => useStore.getState().setShowKBPanel(true)}>📚 {t('知识库', 'KB')}</button>
      )}
      {features.skills !== false && (
        <button className="tb-btn" onClick={() => useStore.getState().setShowSkillsPanel(true)} title={t('技能：经验沉淀 + 会话蒸馏', 'Skills: experience + distillation')}>🎓 {t('技能', 'Skills')}</button>
      )}
      <button className="tb-btn" onClick={() => useStore.getState().setShowChangesPanel(true)} title={t('Git 改动：会话变更/未提交/历史/分支', 'Git changes')}>🔀 {t('改动', 'Changes')}</button>
      <button className="tb-btn" onClick={() => useStore.getState().setShowMCPPanel(true)}>🔌 MCP</button>
      <button
        className={`tb-btn${routingActive ? ' routing-on' : ' routing-off'}`}
        onClick={() => useStore.getState().setShowDispatchPanel(true)}
        title={routingActive
          ? t('智排：本轮已按提问复杂度自动选模型', 'Smart routing active this turn')
          : t('智排：未生效（按所选模型直接聊天），点击配置', 'Smart routing off; click to configure')}
      >
        ⚡ {t('智排', 'Smart')}
        {routingActive && <ThinkingDots />}
      </button>
      <button className="tb-btn" onClick={() => useStore.getState().setShowCachePanel(true)} title={t('缓存', 'Cache')}>🗄</button>
      <button className="tb-btn" onClick={() => useStore.getState().captureAndAttach()} title={t('截屏 Ctrl+Shift+F', 'Screenshot')}>📸</button>
      <button className="tb-btn" onClick={() => useStore.getState().setShowTerminal(true)} title={t('终端', 'Terminal')}>⌨ {t('终端', 'Terminal')}</button>
      <button className="tb-btn" onClick={() => setLang(lang === 'zh' ? 'en' : 'zh')} title="Language">
        {lang === 'zh' ? '中/EN' : 'EN/中'}
      </button>
    </div>
  )
}

// 推理等级写入（避免组件顶部 import api 冗余）
import { api as __api } from '../bridge'
import ThinkingDots from './ThinkingDots'
function apiSetEffort(key: string, effort: string) { __api.setReasoningEffort(key, effort) }
