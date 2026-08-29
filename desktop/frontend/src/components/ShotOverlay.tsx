// 微信式截屏遮罩：整屏底图 + 拖拽框选 + Enter/双击确认、Esc 取消。
// 多屏：portal 抓到的是多显示器拼接图，遮罩按 contain 适配当前屏幕
// （保持宽高比、必要时留边），坐标经 显示比例+偏移 换算回拼图像素；
// 单屏时退化为精确 1:1。
import { useEffect, useRef, useState } from 'react'

export interface ShotRect { x: number; y: number; w: number; h: number }

export default function ShotOverlay({ src, onConfirm, onCancel }: {
  src: string
  onConfirm: (r: ShotRect) => void
  onCancel: () => void
}) {
  const [drag, setDrag] = useState<{ x0: number; y0: number; x1: number; y1: number } | null>(null)
  const dragRef = useRef<typeof drag>(null) // Enter 键路径读取最新拖拽（防闭包陈旧）
  dragRef.current = drag
  const [imgSize, setImgSize] = useState<{ w: number; h: number }>({ w: 0, h: 0 })
  const [vp, setVp] = useState<{ w: number; h: number }>({ w: window.innerWidth, h: window.innerHeight })
  const maskRef = useRef<HTMLDivElement>(null)

  useEffect(() => {
    const img = new Image()
    img.onload = () => setImgSize({ w: img.naturalWidth, h: img.naturalHeight })
    img.src = src
  }, [src])

  useEffect(() => {
    // 主窗口转全屏（微信式整屏框选）；仅挂载时执行一次，避免拖拽重渲染反复切换
    const rt = (window as any).runtime
    rt?.WindowUnminimise?.()
    rt?.WindowFullscreen?.()
    const onResize = () => setVp({ w: window.innerWidth, h: window.innerHeight })
    window.addEventListener('resize', onResize)
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') onCancel()
      if (e.key === 'Enter' && dragRef.current) finish()
    }
    window.addEventListener('keydown', onKey, true)
    window.addEventListener('resize', onResize)
    return () => {
      window.removeEventListener('keydown', onKey, true)
      window.removeEventListener('resize', onResize)
      rt?.WindowUnfullscreen?.()
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [src])

  // contain 适配：拼接图按宽高比缩放到视口内（多屏拼接图整体可见）
  const scale = imgSize.w > 0 && vp.w > 0 ? Math.min(vp.w / imgSize.w, vp.h / imgSize.h) : 0
  const dw = Math.round(imgSize.w * scale)
  const dh = Math.round(imgSize.h * scale)
  const ox = Math.round((vp.w - dw) / 2)
  const oy = Math.round((vp.h - dh) / 2)

  const rect = drag ? {
    x: Math.min(drag.x0, drag.x1), y: Math.min(drag.y0, drag.y1),
    w: Math.abs(drag.x1 - drag.x0), h: Math.abs(drag.y1 - drag.y0),
  } : null

  // 窗口坐标 → 拼图像素坐标（含 contain 偏移与缩放）
  const finish = () => {
    const cur = dragRef.current
    if (!cur || !rect || rect.w < 4 || rect.h < 4) { onCancel(); return }
    if (scale <= 0) { onCancel(); return }
    onConfirm({
      x: Math.round((rect.x - ox) / scale), y: Math.round((rect.y - oy) / scale),
      w: Math.round(rect.w / scale), h: Math.round(rect.h / scale),
    })
  }

  return (
    <div
      ref={maskRef}
      className="shot-mask"
      onMouseDown={(e) => {
        const d = { x0: e.clientX, y0: e.clientY, x1: e.clientX, y1: e.clientY }
        setDrag(d)
      }}
      onMouseMove={(e) => drag && setDrag({ ...drag, x1: e.clientX, y1: e.clientY })}
      onMouseUp={finish}
      onDoubleClick={finish}
    >
      {/* 底图（contain 适配）；遮罩变暗由选区 box-shadow 覆盖层实现 */}
      {dw > 0 && (
        <img src={src} alt="" draggable={false}
          style={{ position: 'absolute', left: ox, top: oy, width: dw, height: dh }} />
      )}
      {rect && (
        <>
          <div className="shot-rect" style={{ left: rect.x, top: rect.y, width: rect.w, height: rect.h }} />
          <div className="shot-size" style={{ left: rect.x, top: Math.max(2, rect.y - 22) }}>
            {Math.round(rect.w / scale)} × {Math.round(rect.h / scale)}
          </div>
        </>
      )}
      <div className="shot-tip">
        拖拽选择区域 · 双击/Enter 确认 · Esc 取消
      </div>
    </div>
  )
}
