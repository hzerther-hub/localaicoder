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

function fmtCost(v: number): string {
  if (!v || v <= 0) return '—'
  if (v < 0.01) return `¥${(v * 7.2).toFixed(3)}`
  return `¥${(v * 7.2).toFixed(2)}`
}

export default function ChatView() {
  const items = useStore((s) => s.items)
  const running = useStore((s) => s.running)
  const usage = useStore((s) => s.usage)
  const lang = useStore((s) => s.lang)
  const scrollRef = useRef<HTMLDivElement>(null)
  const hitRate = usage.prompt > 0 ? (usage.cached / usage.prompt) * 100 : 0

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
      <div className="usage-bar">
        <span>{t('会话耗时', 'elapsed')} <b>{fmtCost(usage.cost)}</b></span>
        {usage.cost > 0 && (
          <span title={t('按官方定价折算（缓存输入×hit价 + 其余输入×miss价 + 输出×out价）', 'Priced per official rate')}>
            {t('每千token', '/1k tok')} <b>${((usage.cost / Math.max(usage.total, 1)) * 1000).toFixed(3)}</b>
          </span>
        )}
        <span>{t('累计费用', 'total cost')} <b>{fmtCost(usage.costTotal)}</b></span>
        <span className="spacer" style={{ flex: 1 }} />
        <span title={t('服务端前缀缓存命中率（cached/prompt）', 'Provider prefix-cache hit rate')}>
          {t('命中率', 'hit')} <b style={{ color: hitRate >= 50 ? 'var(--green)' : 'var(--text-dim)' }}>{hitRate.toFixed(0)}%</b>
        </span>
        <span>Tokens <b>{usage.total.toLocaleString()}</b></span>
        <span>{t('请求', 'reqs')} <b>{usage.requests}</b></span>
        <span>{lang.toUpperCase()}</span>
      </div>
    </>
  )
}
