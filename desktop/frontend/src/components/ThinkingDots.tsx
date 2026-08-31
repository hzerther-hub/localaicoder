// 统一「等待」指示：三个软圆点波浪式脉冲（对齐参考图，全应用一致）。
// 所有等待/进行中状态都用它，避免各处 emoji/符号/文本拼凑的样式漂移。
export default function ThinkingDots({ className = '' }: { className?: string }) {
  return (
    <span className={`tdots ${className}`} aria-hidden>
      <span /><span /><span />
    </span>
  )
}
