import { useEffect } from 'react'
import { useStore } from './store'
import Toolbar from './components/Toolbar'
import Sidebar from './components/Sidebar'
import ChatView from './components/ChatView'
import Composer from './components/Composer'
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
          <Composer />
        </div>
        {editor && <EditorPanel />}
      </div>
      <TerminalDrawer />
      <ApprovalDialog />
      <ModelPanel />
      <KBPanel />
      <MCPPanel />
      <DispatchPanel />
      <CachePanel />
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
