// 微信式截屏遮罩：整屏底图 + 拖拽框选 + Enter/双击确认、Esc 取消。
// 仅 Windows 走此流程；Linux/macOS 按 Tk 原方式整屏直接入附件。
import { useEffect, useRef, useState } from 'react'

export interface ShotRect { x: number; y: number; w: number; h: number }

export default function ShotOverlay({ src, onConfirm, onCancel }: {
  src: string
  onConfirm: (r: ShotRect) => void
  onCancel: () => void
}) {
  const [drag, setDrag] = useState<{ x0: number; y0: number; x1: number; y1: number } | null>(null)
  const imgSize = useRef<{ w: number; h: number }>({ w: 0, h: 0 })
  const maskRef = useRef<HTMLDivElement>(null)

  useEffect(() => {
    const img = new Image()
    img.onload = () => { imgSize.current = { w: img.naturalWidth, h: img.naturalHeight } }
    img.src = src

    // 主窗口转全屏（微信式整屏框选）
    const rt = (window as any).runtime
    rt?.WindowFullscreen?.()

    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') onCancel()
      if (e.key === 'Enter' && drag) finish()
    }
    window.addEventListener('keydown', onKey, true)
    return () => {
      window.removeEventListener('keydown', onKey, true)
      rt?.WindowUnfullscreen?.()
    }
  })

  const rect = drag ? {
    x: Math.min(drag.x0, drag.x1), y: Math.min(drag.y0, drag.y1),
    w: Math.abs(drag.x1 - drag.x0), h: Math.abs(drag.y1 - drag.y0),
  } : null

  // 窗口坐标 → 截图像素坐标（底图 100% 拉伸铺满窗口）
  const finish = () => {
    if (!drag || !rect || rect.w < 4 || rect.h < 4) { onCancel(); return }
    const el = maskRef.current
    if (!el) { onCancel(); return }
    const sx = imgSize.current.w / el.clientWidth
    const sy = imgSize.current.h / el.clientHeight
    onConfirm({
      x: Math.round(rect.x * sx), y: Math.round(rect.y * sy),
      w: Math.round(rect.w * sx), h: Math.round(rect.h * sy),
    })
  }

  return (
    <div
      ref={maskRef}
      className="shot-mask"
      style={{ backgroundImage: `url("${src}")` }}
      onMouseDown={(e) => setDrag({ x0: e.clientX, y0: e.clientY, x1: e.clientX, y1: e.clientY })}
      onMouseMove={(e) => drag && setDrag({ ...drag, x1: e.clientX, y1: e.clientY })}
      onMouseUp={finish}
      onDoubleClick={finish}
    >
      {rect && (
        <>
          <div className="shot-rect" style={{ left: rect.x, top: rect.y, width: rect.w, height: rect.h }} />
          <div className="shot-size" style={{ left: rect.x, top: Math.max(2, rect.y - 22) }}>
            {rect.w} × {rect.h}
          </div>
        </>
      )}
      <div className="shot-tip">
        拖拽选择区域 · 双击/Enter 确认 · Esc 取消
      </div>
    </div>
  )
}
