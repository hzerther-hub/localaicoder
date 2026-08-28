// 截图标注模态（对齐 Tk ui_panel_annotate.py 工具集）：
// 矩形/椭圆/直线/箭头/画笔/马赛克/文字 · 8 色 · 线宽 · 撤销/重做 · 烧录 PNG。
import { useEffect, useRef, useState } from 'react'
import { api } from '../bridge'
import { useStore, t } from '../store'

type Tool = 'rect' | 'ellipse' | 'line' | 'arrow' | 'pen' | 'mosaic' | 'text'

const COLORS = ['#ff4d5e', '#ffc46b', '#3ddc97', '#5ab8ff', '#c792ea', '#ff8f3d', '#111111', '#ffffff']
const WIDTHS = [2, 4, 6]

export default function AnnotateModal() {
  const src = useStore((s) => s.annotateSrc)
  const clearSrc = () => useStore.setState({ annotateSrc: null })
  if (!src) return null
  return <Canvas src={src} onClose={clearSrc} />
}

function Canvas({ src, onClose }: { src: string; onClose: () => void }) {
  const canvasRef = useRef<HTMLCanvasElement>(null)
  const [ready, setReady] = useState(false)
  const [tool, setTool] = useState<Tool>('rect')
  const [color, setColor] = useState(COLORS[0])
  const [width, setWidth] = useState(4)
  const undoRef = useRef<string[]>([])
  const redoRef = useRef<string[]>([])
  const [histTick, setHistTick] = useState(0) // 触发按钮可用态刷新
  const origRef = useRef<HTMLImageElement | null>(null)
  const drawing = useRef(false)
  const startRef = useRef<{ x: number; y: number } | null>(null)
  const snapshotRef = useRef<string | null>(null)
  const scaleRef = useRef(1)

  // 载入底图
  useEffect(() => {
    let alive = true
    api.readImageDataURL(src).then((dataURL) => {
      if (!alive || !dataURL) return
      const img = new Image()
      img.onload = () => {
        const cv = canvasRef.current
        if (!cv) return
        cv.width = img.naturalWidth
        cv.height = img.naturalHeight
        const ctx = cv.getContext('2d')!
        ctx.drawImage(img, 0, 0)
        origRef.current = img
        undoRef.current = [cv.toDataURL('image/png')]
        redoRef.current = []
        setReady(true)
        setHistTick((x) => x + 1)
      }
      img.src = dataURL
    })
    return () => { alive = false }
  }, [src])

  const pushUndo = () => {
    const cv = canvasRef.current!
    undoRef.current.push(cv.toDataURL('image/png'))
    if (undoRef.current.length > 30) undoRef.current.shift()
    redoRef.current = []
    setHistTick((x) => x + 1)
  }

  const restore = (data: string) => {
    const cv = canvasRef.current!
    const ctx = cv.getContext('2d')!
    const img = new Image()
    img.onload = () => {
      ctx.clearRect(0, 0, cv.width, cv.height)
      ctx.drawImage(img, 0, 0)
    }
    img.src = data
  }

  const undo = () => {
    if (undoRef.current.length <= 1) return
    const cv = canvasRef.current!
    redoRef.current.push(cv.toDataURL('image/png'))
    const prev = undoRef.current[undoRef.current.length - 2]
    undoRef.current.pop()
    restore(prev)
    setHistTick((x) => x + 1)
  }

  const redo = () => {
    if (!redoRef.current.length) return
    const cv = canvasRef.current!
    undoRef.current.push(cv.toDataURL('image/png'))
    const next = redoRef.current.pop()!
    restore(next)
    setHistTick((x) => x + 1)
  }

  // 事件坐标 → 画布像素坐标（CSS 缩放换算）
  const pos = (e: React.PointerEvent): { x: number; y: number } => {
    const cv = canvasRef.current!
    const r = cv.getBoundingClientRect()
    return { x: (e.clientX - r.left) / scaleRef.current, y: (e.clientY - r.top) / scaleRef.current }
  }

  const onDown = (e: React.PointerEvent) => {
    if (!ready) return
    const p = pos(e)
    if (tool === 'text') {
      pushUndo()
      const text = prompt(t('标注文字：', 'Text:'))
      const cv = canvasRef.current!
      if (text) {
        const ctx = cv.getContext('2d')!
        ctx.fillStyle = color
        ctx.font = `bold ${14 + width * 4}px 'Microsoft YaHei', sans-serif`
        ctx.textBaseline = 'top'
        ctx.fillText(text, p.x, p.y)
      } else {
        undoRef.current.pop()
        setHistTick((x) => x - 1)
      }
      return
    }
    pushUndo()
    drawing.current = true
    startRef.current = p
    const cv = canvasRef.current!
    snapshotRef.current = cv.toDataURL('image/png')
    if (tool === 'pen' || tool === 'mosaic') {
      const ctx = cv.getContext('2d')!
      ctx.beginPath()
      ctx.moveTo(p.x, p.y)
    }
  }

  const onMove = (e: React.PointerEvent) => {
    if (!drawing.current) return
    const p = pos(e)
    const cv = canvasRef.current!
    const ctx = cv.getContext('2d')!
    const s = startRef.current!
    ctx.lineCap = 'round'
    ctx.lineJoin = 'round'
    ctx.lineWidth = width
    ctx.strokeStyle = color
    ctx.fillStyle = color

    if (tool === 'pen') {
      ctx.lineTo(p.x, p.y)
      ctx.stroke()
      return
    }
    if (tool === 'mosaic') {
      // 马赛克：取原图对应块，缩小再放大回画（像素化）
      const img = origRef.current
      if (!img) return
      const m = 10 + width * 2
      const bx = Math.max(0, Math.min(p.x - m / 2, cv.width - m))
      const by = Math.max(0, Math.min(p.y - m / 2, cv.height - m))
      const tiny = document.createElement('canvas')
      tiny.width = 1; tiny.height = 1
      const tctx = tiny.getContext('2d')!
      tctx.drawImage(img, bx, by, m, m, 0, 0, 1, 1)
      ctx.imageSmoothingEnabled = false
      ctx.drawImage(tiny, 0, 0, 1, 1, bx, by, m, m)
      ctx.imageSmoothingEnabled = true
      return
    }
    // 形状类：用快照重放实现拖拽预览
    if (snapshotRef.current) {
      const img = new Image()
      img.onload = () => {
        ctx.clearRect(0, 0, cv.width, cv.height)
        ctx.drawImage(img, 0, 0)
        ctx.beginPath()
        if (tool === 'rect') {
          ctx.rect(s.x, s.y, p.x - s.x, p.y - s.y)
          ctx.stroke()
        } else if (tool === 'ellipse') {
          ctx.ellipse((s.x + p.x) / 2, (s.y + p.y) / 2,
            Math.abs(p.x - s.x) / 2, Math.abs(p.y - s.y) / 2, 0, 0, Math.PI * 2)
          ctx.stroke()
        } else if (tool === 'line') {
          ctx.moveTo(s.x, s.y); ctx.lineTo(p.x, p.y); ctx.stroke()
        } else if (tool === 'arrow') {
          ctx.moveTo(s.x, s.y); ctx.lineTo(p.x, p.y); ctx.stroke()
          const ang = Math.atan2(p.y - s.y, p.x - s.x)
          const head = 10 + width * 2
          ctx.beginPath()
          ctx.moveTo(p.x, p.y)
          ctx.lineTo(p.x - head * Math.cos(ang - 0.45), p.y - head * Math.sin(ang - 0.45))
          ctx.moveTo(p.x, p.y)
          ctx.lineTo(p.x - head * Math.cos(ang + 0.45), p.y - head * Math.sin(ang + 0.45))
          ctx.stroke()
        }
      }
      img.src = snapshotRef.current
    }
  }

  const onUp = () => { drawing.current = false; snapshotRef.current = null }

  // 完成：烧录当前画布 → PNG → 附件
  const finish = async () => {
    const cv = canvasRef.current!
    const dataURL = cv.toDataURL('image/png')
    const p = await api.saveDataURL(dataURL)
    if (p) {
      useStore.setState({ annotateSrc: null })
      useStore.getState().addAttachment(p)
    }
  }

  const skip = () => {
    // 跳过标注：直接用裁剪原图
    useStore.setState({ annotateSrc: null })
    useStore.getState().addAttachment(src)
  }

  return (
    <div className="anno-mask">
      <div className="anno-bar">
        <span className="anno-title">✏️ {t('标注', 'Annotate')}</span>
        {([['rect', '▭', '矩形'], ['ellipse', '◯', '椭圆'], ['line', '／', '直线'], ['arrow', '➚', '箭头'],
           ['pen', '✎', '画笔'], ['mosaic', '▓', '马赛克'], ['text', 'T', '文字']] as [Tool, string, string][]).map(
          ([tl, icon, label]) => (
            <button key={tl} className={`anno-btn${tool === tl ? ' on' : ''}`}
              title={t(label, label)} onClick={() => setTool(tl)}>{icon}</button>
          ))}
        <span className="anno-sep" />
        {COLORS.map((c) => (
          <button key={c} className={`anno-color${color === c ? ' on' : ''}`}
            style={{ background: c }} onClick={() => setColor(c)} />
        ))}
        <span className="anno-sep" />
        {WIDTHS.map((w) => (
          <button key={w} className={`anno-btn${width === w ? ' on' : ''}`} onClick={() => setWidth(w)}>
            <span style={{ display: 'inline-block', width: w + 4, height: w + 4, borderRadius: '50%', background: 'currentColor' }} />
          </button>
        ))}
        <span className="anno-sep" />
        <button className="anno-btn" disabled={undoRef.current.length <= 1} key={'u' + histTick}
          title={t('撤销', 'Undo')} onClick={undo}>↩</button>
        <button className="anno-btn" disabled={!redoRef.current.length} key={'r' + histTick}
          title={t('重做', 'Redo')} onClick={redo}>↪</button>
        <div style={{ flex: 1 }} />
        <button className="btn" onClick={onClose}>✕ {t('放弃', 'Discard')}</button>
        <button className="btn" onClick={skip}>{t('跳过标注', 'Skip')}</button>
        <button className="btn primary" onClick={finish}>✓ {t('完成并加入对话', 'Done')}</button>
      </div>
      <div className="anno-stage">
        <canvas
          ref={canvasRef}
          className="anno-canvas"
          style={{ cursor: tool === 'text' ? 'text' : 'crosshair' }}
          onPointerDown={onDown}
          onPointerMove={onMove}
          onPointerUp={onUp}
          onPointerLeave={onUp}
        />
      </div>
    </div>
  )
}
