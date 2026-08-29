import { useEffect, useRef, useState } from 'react'
import ReactMarkdown from 'react-markdown'
import remarkGfm from 'remark-gfm'
import { attLabel, isImgPath, cachedImageURL, useStore, t } from '../store'

function MsgThumb({ path }: { path: string }) {
  const [url, setUrl] = useState('')
  useEffect(() => { cachedImageURL(path).then((d: string) => setUrl(d || '')) }, [path])
  const st = useStore.getState()
  return (
    <img
      className="msg-thumb"
      src={url}
      alt=""
      onClick={() => url && st.setPreviewSrc(url)}
      onError={(e) => { (e.target as HTMLImageElement).style.display = 'none' }}
    />
  )
}

// CopyBtn 消息复制按钮：悬停显示，点击复制原文（剪贴板 API 失败时回退 execCommand）
function CopyBtn({ text }: { text: string }) {
  const [ok, setOk] = useState(false)
  const copy = async () => {
    try {
      await navigator.clipboard.writeText(text)
    } catch {
      const ta = document.createElement('textarea')
      ta.value = text
      ta.style.position = 'fixed'
      ta.style.opacity = '0'
      document.body.appendChild(ta)
      ta.select()
      document.execCommand('copy')
      document.body.removeChild(ta)
    }
    setOk(true)
    setTimeout(() => setOk(false), 1200)
  }
  return (
    <button className="msg-copy" title={t('复制', 'Copy')} onClick={copy}>
      {ok ? t('已复制', 'Copied') : t('复制', 'Copy')}
    </button>
  )
}

function ToolCard({ name, args, result, done }: { name: string; args: string; result: string; done: boolean }) {
  const [open, setOpen] = useState(true)
  return (
    <div className="tool-card">
      <div className="head" onClick={() => setOpen(!open)}>
        <span className="chev">{open ? '▾' : '▸'}</span>
        <span>🔧 {name}</span>
        <span className={`status${done ? ' ok' : ''}`}>{done ? t('完成', 'done') : t('运行中…', 'running…')}</span>
      </div>
      {open && (
        <>
          {args && <div className="args">{args}</div>}
          {result && <div className="result">{result}</div>}
        </>
      )}
    </div>
  )
}

export default function ChatView() {
  const items = useStore((s) => s.items)
  const running = useStore((s) => s.running)
  const scrollRef = useRef<HTMLDivElement>(null)

  useEffect(() => {
    const el = scrollRef.current
    if (el) el.scrollTop = el.scrollHeight
  }, [items, running])

  const lastAssistant = [...items].reverse().find((i) => i.kind === 'assistant')

  return (
    <>
      <div
        className="chat-scroll"
        ref={scrollRef}
        onDragOver={(e) => e.preventDefault()}
        onDrop={(e) => {
          // 文件树拖入：取自定义路径数据成为附件
          e.preventDefault()
          const path = e.dataTransfer.getData('text/localai-path')
          if (path) useStore.getState().addAttachment(path)
        }}
      >
        <div className="chat-inner">
          {items.length === 0 && !running && (
            <div className="empty-chat">
              <div className="hero">
                <div className="big">L</div>
                <h2>{t('有什么可以帮你？', 'How can I help?')}</h2>
                <p>{t('读文件 · 搜代码 · 执行命令 · 联网检索 · 多语言智能提示', 'Read files · search code · run shell · web search · LSP')}</p>
              </div>
            </div>
          )}
          {items.map((it) => {
            if (it.kind === 'user') {
              const imgs = (it.atts || []).filter(
                (a): a is string => typeof a === 'string' && isImgPath(a))
              const others = (it.atts || [])
                .filter((a) => !(typeof a === 'string' && isImgPath(a)))
                .map(attLabel)
              return (
                <div className="msg msg-user" key={it.id}>
                  <div className="bubble">
                    {it.text}
                    {imgs.length > 0 && (
                      <div className="msg-thumbs">
                        {imgs.map((p) => <MsgThumb key={p} path={p} />)}
                      </div>
                    )}
                    {others.length > 0 && (
                      <div className="msg-atts">📎 {others.join('　')}</div>
                    )}
                  </div>
                  <CopyBtn text={it.text} />
                </div>
              )
            }
            if (it.kind === 'tool' && it.tool) {
              return (
                <div className="msg" key={it.id}>
                  <ToolCard {...it.tool} />
                </div>
              )
            }
            return (
              <div className="msg msg-assistant" key={it.id}>
                <div className="who">
                  <span className="dot">L</span>
                  <span>Local AI</span>
                </div>
                {it.reasoning && <div className="reasoning">{it.reasoning}</div>}
                <div className="msg-body md">
                  <ReactMarkdown remarkPlugins={[remarkGfm]}>{it.text}</ReactMarkdown>
                  {running && it === lastAssistant && (
                    <div className="typing"><span /><span /><span /></div>
                  )}
                </div>
                {it.text && <CopyBtn text={it.text} />}
              </div>
            )
          })}
          {running && !lastAssistant && (
            <div className="msg msg-assistant">
              <div className="who"><span className="dot">L</span><span>Local AI</span></div>
              <div className="typing"><span /><span /><span /></div>
            </div>
          )}
        </div>
      </div>
    </>
  )
}
