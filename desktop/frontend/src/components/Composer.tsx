// 输入区（深色）：附件条 + 多行输入 + 模式/上下文/附件/截屏 + 发送。
import { KeyboardEvent, useEffect, useRef, useState } from 'react'
import { attLabel, isImgPath, cachedImageURL, useStore, t } from '../store'
import { api } from '../bridge'

function Thumb({ path, onClick }: { path: string; onClick: () => void }) {
  const [url, setUrl] = useState('')
  useEffect(() => { cachedImageURL(path).then((d: string) => setUrl(d || '')).catch(() => setUrl('')) }, [path])
  if (!url) return <span className="att-ico">🖼</span>
  return <img className="att-thumb" src={url} onClick={onClick} alt="" />
}

function runCommand(raw: string, st: ReturnType<typeof useStore.getState>) {
  const [cmd, ...rest] = raw.slice(1).split(/\s+/)
  const arg = rest.join(' ')
  switch (cmd) {
    case 'help':
      st.notice('**斜杠命令**\n- `/new` 新会话\n- `/model` 模型\n- `/dir [路径]` 切目录\n- `/permission` 权限\n- `/context` 独立提问\n- `/index` 重建索引\n- `/branch` 当前分支')
      break
    case 'new': st.newSession(); break
    case 'clear': useStore.setState({ items: [] }); break
    case 'model': st.setShowModelPanel(true); break
    case 'dir':
      if (arg) void st.setWorkspace(arg)
      else api.pickWorkspace().then((d) => { if (d) void st.setWorkspace(d) })
      break
    case 'permission': st.cycleMode(); break
    case 'context': st.toggleStandalone(); break
    case 'index': api.rebuildIndex().then((s: any) => st.notice(`索引重建完成：${s.files_indexed} 文件（${Number(s.seconds).toFixed(1)}s）`)); break
    case 'branch': api.gitBranch().then((b: string) => st.notice(b || t('（非 git 仓库）', '(not a git repo)'))); break
    default: st.notice(t(`未知命令 ${cmd}（/help 查看全部）`, `Unknown ${cmd}`))
  }
}

export default function Composer() {
  const [text, setText] = useState('')
  const [dragOver, setDragOver] = useState(false)
  const taRef = useRef<HTMLTextAreaElement>(null)
  const running = useStore((s) => s.running)
  const send = useStore((s) => s.send)
  const stop = useStore((s) => s.stop)
  const mode = useStore((s) => s.mode)
  const cycleMode = useStore((s) => s.cycleMode)
  const attachments = useStore((s) => s.attachments)
  const addAttachment = useStore((s) => s.addAttachment)
  const removeAttachment = useStore((s) => s.removeAttachment)
  const prefs = useStore((s) => s.prefs)
  const toggleStandalone = useStore((s) => s.toggleStandalone)
  const previewSrc = useStore((s) => s.previewSrc)

  const autoGrow = () => {
    const ta = taRef.current
    if (ta) { ta.style.height = 'auto'; ta.style.height = Math.min(ta.scrollHeight, 200) + 'px' }
  }

  const doSend = () => {
    if (running) return
    if (!text.trim() && attachments.length === 0) return
    if (text.startsWith('/') && attachments.length === 0) {
      runCommand(text.trim(), useStore.getState()); setText(''); return
    }
    send(text); setText(''); requestAnimationFrame(autoGrow)
  }

  const onKey = (e: KeyboardEvent<HTMLTextAreaElement>) => {
    if (e.key === 'Enter' && !e.shiftKey && !e.ctrlKey) { e.preventDefault(); doSend() }
  }

  const onPaste = (e: React.ClipboardEvent) => {
    const pasted = e.clipboardData.getData('text')
    if (!pasted || pasted.includes('\n')) return
    const m = pasted.trim().match(/^(?:file:\/\/)?((?:[A-Za-z]:[\\/]|\/)[^\s]+)$/u)
    if (m) { e.preventDefault(); addAttachment(m[1]) }
  }

  const onDrop = (e: React.DragEvent) => {
    e.preventDefault(); setDragOver(false)
    const path = e.dataTransfer.getData('text/localai-path')
    if (path) { addAttachment(path); return }
    for (const f of Array.from(e.dataTransfer.files)) addAttachment((f as any).path || f.name)
  }

  return (
    <div className="composer">
      {previewSrc && (
        <div className="lightbox" onClick={() => useStore.getState().setPreviewSrc(null)}>
          <img src={previewSrc} alt="" />
        </div>
      )}

      {attachments.length > 0 && (
        <div className="att-row">
          {attachments.map((a, i) => (
            <span key={i} className={`att-chip${typeof a === 'string' && isImgPath(a) ? ' img' : ''}`}>
              {typeof a === 'string'
                ? (isImgPath(a)
                    ? <Thumb path={a} onClick={() => useStore.getState().setPreviewSrc(a)} />
                    : '📄')
                : '✂️'} {attLabel(a)}
              {typeof a === 'string' && isImgPath(a) && (
                <button className="x" title={t('标注', 'Annotate')}
                  onClick={() => useStore.setState({ annotateSrc: a })}>✏️</button>
              )}
              <button className="x" onClick={() => removeAttachment(i)}>✕</button>
            </span>
          ))}
        </div>
      )}

      <div
        className={`composer-inner${dragOver ? ' drag-over' : ''}`}
        onDragOver={(e) => { e.preventDefault(); setDragOver(true) }}
        onDragLeave={() => setDragOver(false)}
        onDrop={onDrop}
      >
        {text.startsWith('/') && !text.includes(' ') && (
          <div className="slash-hint">
            <span><b>/help</b> 命令</span><span><b>/new</b> 新会话</span>
            <span><b>/model</b> 模型</span><span><b>/dir</b> 目录</span>
            <span><b>/index</b> 索引</span><span><b>/cache</b> 缓存</span>
          </div>
        )}
        <textarea
          ref={taRef}
          value={text}
          placeholder={t('输入问题，Enter 发送；/ 命令 · 拖拽/📎 附件 · 📸 截屏 (Ctrl+Shift+F)',
            'Type a message. / commands · drop/📎 attach · 📸 screenshot')}
          onChange={(e) => { setText(e.target.value); autoGrow() }}
          onKeyDown={onKey}
          onPaste={onPaste}
          rows={1}
        />
        <div className="composer-row">
          <span className={`mode-chip ${mode}`} onClick={cycleMode} title={t('权限模式', 'Permission mode')}>
            {mode === 'ask' ? t('🛡 ask', '🛡 ask') : mode === 'readonly' ? t('🔒 readonly', '🔒 readonly') : t('⚡ always', '⚡ always')}
          </span>
          <span className={`mode-chip${prefs?.standalone ? ' on' : ''}`} onClick={toggleStandalone}
            title={t('独立提问：每条消息不带历史', 'Standalone')}>
            {prefs?.standalone ? '✂️ 独立' : '🔗 上下文'}
          </span>
          <button className="icon-btn" title={t('添加附件（图片/文档）', 'Attach files')} onClick={async () => {
            const files = await api.pickFiles()
            for (const f of files || []) addAttachment(f)
          }}>📎</button>
          <button className="icon-btn" title={t('截屏（Ctrl+Shift+F）', 'Screenshot')}
            onClick={() => useStore.getState().captureAndAttach()}>📸</button>
          {running ? (
            <button className="send-btn stop" onClick={stop}>⏹ {t('停止', 'Stop')}</button>
          ) : (
            <button className="send-btn" disabled={!text.trim() && attachments.length === 0} onClick={doSend}>
              {t('发送', 'Send')} ↵
            </button>
          )}
        </div>
      </div>
    </div>
  )
}
