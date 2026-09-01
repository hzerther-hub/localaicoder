import { useEffect } from 'react'
import { useStore, t } from './store'
import Toolbar from './components/Toolbar'
import Sidebar from './components/Sidebar'
import ChatView from './components/ChatView'
import Composer from './components/Composer'
import StepBar from './components/StepBar'
import SkillsPanel from './components/SkillsPanel'
import ChangesPanel from './components/ChangesPanel'
import StatsBar from './components/StatsBar'
import TrashPanel from './components/TrashPanel'
import SchedulePanel from './components/SchedulePanel'
import SettingsPanel from './components/SettingsPanel'
import MobilePanel from './components/MobilePanel'
import EditorPanel from './components/EditorPanel'
import ApprovalDialog from './components/ApprovalDialog'
import ModelPanel from './components/ModelPanel'
import KBPanel from './components/KBPanel'
import MCPPanel from './components/MCPPanel'
import DispatchPanel from './components/DispatchPanel'
import CachePanel from './components/CachePanel'
import ShotOverlay from './components/ShotOverlay'
import TerminalDrawer from './components/TerminalDrawer'
import AnnotateModal from './components/AnnotateModal'

export default function App() {
  const init = useStore((s) => s.init)
  const showEditor = useStore((s) => s.showEditor)
  const editor = useStore((s) => s.product.features.editor !== false && s.showEditor)
  const shotSrc = useStore((s) => s.shotSrc)
  const annotateSrc = useStore((s) => s.annotateSrc)

  useEffect(() => {
    init()
    // 截屏快捷键 Ctrl+Shift+F + Esc 停止（对齐 Tk 版全局键）
    const onKey = (e: KeyboardEvent) => {
      if (e.ctrlKey && e.shiftKey && (e.key === 'F' || e.key === 'f')) {
        e.preventDefault()
        useStore.getState().captureAndAttach()
      } else if (e.key === 'Escape' && useStore.getState().running) {
        e.preventDefault()
        useStore.getState().stop()
      }
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [])

  return (
    <div className="app">
      <Toolbar />
      <div className={`main${editor ? '' : ' no-editor'}`}>
        <Sidebar />
        <div className="chat-col">
          <ChatView />
          <StepBar />
          <Composer />
        </div>
        {editor && <EditorPanel />}
      </div>
      {/* 窗口最底部通栏一行：三入口 + 状态栏 */}
      <div className="bottom-line">
        <div className="bottom-icons">
          <button title={t('垃圾箱（已删除项目，可恢复）', 'Trash (removed projects, restorable)')}
            onClick={() => useStore.getState().setShowTrashPanel(true)}>🗑</button>
          <button title={t('定时任务', 'Scheduled tasks')}
            onClick={() => useStore.getState().setShowSchedulePanel(true)}>🕐</button>
          <button title={t('移动端远程控制（扫码 / Bot 渠道）', 'Mobile remote (QR / bot channels)')}
            onClick={() => useStore.getState().setShowMobilePanel(true)}>📱</button>
          <button title={t('内置浏览器（在编辑器中打开）', 'Browser (opens in editor)')}
            onClick={() => { useStore.getState().setShowEditor(true); useStore.getState().setShowBrowserPanel(true) }}>🌐</button>
          <button title={t('设置', 'Settings')}
            onClick={() => useStore.getState().setShowSettingsPanel(true)}>⚙</button>
        </div>
        <StatsBar />
      </div>
      <TerminalDrawer />
      <ApprovalDialog />
      <ModelPanel />
      <KBPanel />
      <ChangesPanel />
      <SkillsPanel />
      <MCPPanel />
      <DispatchPanel />
      <CachePanel />
      <TrashPanel />
      <SchedulePanel />
      <SettingsPanel />
      <MobilePanel />
      {annotateSrc && <AnnotateModal />}
      {shotSrc && (
        <ShotOverlay
          src={shotSrc}
          onConfirm={(r) => void useStore.getState().confirmShot(r)}
          onCancel={() => useStore.getState().cancelShot()}
        />
      )}
    </div>
  )
}
