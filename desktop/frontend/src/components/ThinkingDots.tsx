import { useEffect, useState } from 'react'

// 统一「等待」指示：布莱叶点字 8 帧旋转（oh-my-pi 同款 status spinner，80ms/帧）。
// 所有等待/进行中状态都用它，避免各处 emoji/符号/文本拼凑的样式漂移。
// 帧号由共享时钟推导，多处实例同相转动（对齐 oh-my-pi 的 sharedSpinnerFrame）。
const FRAMES = ['⣾', '⣽', '⣻', '⢿', '⡿', '⣟', '⣯', '⣷']
const FRAME_MS = 80

export default function ThinkingDots({ className = '' }: { className?: string }) {
  const [frame, setFrame] = useState(0)
  useEffect(() => {
    const id = setInterval(
      () => setFrame(Math.floor(performance.now() / FRAME_MS) % FRAMES.length),
      FRAME_MS,
    )
    return () => clearInterval(id)
  }, [])
  return (
    <span className={`tdots ${className}`} aria-hidden>
      {FRAMES[frame]}
    </span>
  )
}
