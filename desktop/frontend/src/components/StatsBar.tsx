// 统计条（置于窗口最底部）：耗时 / 输入输出思考分项 / 吞吐 / 缓存 /
// 会话 tokens·轮次 / 上下文占用 / 费用·累计（仅定价模型）/ 余额 / 附件识图 /
// 命中率 / 请求。字段仅在模型或提供方报告时显示。
import { useEffect, useState } from 'react'
import { useStore, t } from '../store'

// fmtK 大数缩写：1234 → 1.2k（统计条紧凑显示）
function fmtK(n: number): string {
  return n >= 1000 ? `${(n / 1000).toFixed(1)}k` : String(n)
}

function fmtCost(v: number): string {
  if (!v || v <= 0) return '$0'
  if (v < 0.01) return `¥${(v * 7.2).toFixed(3)}`
  return `¥${(v * 7.2).toFixed(2)}`
}

// useElapsed 会话耗时（每秒走表）
function useElapsed(): string {
  const sessionStart = useStore((s) => s.sessionStart)
  const [, tick] = useState(0)
  useEffect(() => {
    const id = setInterval(() => tick((n) => n + 1), 1000)
    return () => clearInterval(id)
  }, [])
  const sec = Math.max(0, Math.floor((Date.now() - sessionStart) / 1000))
  const h = Math.floor(sec / 3600)
  const m = Math.floor((sec % 3600) / 60)
  const s = sec % 60
  const pad = (n: number) => String(n).padStart(2, '0')
  return h > 0 ? `${h}:${pad(m)}:${pad(s)}` : `${m}:${pad(s)}`
}

export default function StatsBar() {
  const running = useStore((s) => s.running)
  const round = useStore((s) => s.round)
  const capturing = useStore((s) => s.capturing)
  const usage = useStore((s) => s.usage)
  const lang = useStore((s) => s.lang)
  const attSent = useStore((s) => s.attSent)
  const visionSent = useStore((s) => s.visionSent)
  const balance = useStore((s) => s.balance)
  const sessionTokens = useStore((s) => s.sessionTokens)
  const turnCount = useStore((s) => s.turnCount)
  const lastThroughput = useStore((s) => s.lastThroughput)
  const models = useStore((s) => s.models)
  const currentModel = useStore((s) => s.currentModel)
  const elapsed = useElapsed()
  const hitRate = usage.prompt > 0 ? (usage.cached / usage.prompt) * 100 : 0
  // 上下文占用：本轮输入 / 当前模型窗口（无窗口数据则不显示）
  const ctxWindow = models.find((m) => m.key === currentModel)?.context_window || 0
  const ctxPct = ctxWindow > 0 ? Math.min(100, (usage.prompt / ctxWindow) * 100) : 0

  return (
    <div className="usage-bar">
      {capturing && (
        <span className="running-chip" title={t('通过系统 portal 抓取，首次可能需要授权或数秒', 'capturing via system portal')}>
          ⏳ {t('正在截取屏幕…', 'capturing screen…')}
        </span>
      )}
      {running && (
        <span className="running-chip" title={t('任务执行中，完成前请勿关闭', 'task in progress')}>
          <span className="running-dot" />{t('进行中', 'running')}{round > 0 ? ` · ${t('第', 'round')} ${round} ${t('轮', '')}` : ''}
        </span>
      )}
      <span title={t('当前会话时长', 'session elapsed')}>{t('耗时', 'elapsed')} <b>{elapsed}</b></span>
      <span title={t('本轮输入 tokens（含缓存命中部分）', 'prompt tokens this turn (incl. cached)')}>
        {t('输入', 'in')} <b>{usage.prompt > 0 ? fmtK(usage.prompt) : '—'}</b>
      </span>
      <span title={t('本轮输出 tokens', 'completion tokens this turn')}>
        {t('输出', 'out')} <b>{usage.completion > 0 ? fmtK(usage.completion) : '—'}</b>
      </span>
      {usage.reasoning > 0 && (
        <span title={t('本轮推理思考 tokens（推理模型）', 'reasoning tokens this turn')}>
          {t('思考', 'think')} <b>{fmtK(usage.reasoning)}</b>
        </span>
      )}
      <span title={t('本轮输出吞吐（tokens/秒）', 'output throughput (tokens/s)')}>
        {t('吞吐', 'tok/s')} <b>{lastThroughput > 0 ? `${lastThroughput}/s` : '—'}</b>
      </span>
      <span title={t('本轮服务端前缀缓存命中 tokens', 'prefix-cache hit tokens this turn')}>
        {t('缓存', 'cache')} <b>{usage.cached > 0 ? fmtK(usage.cached) : '—'}</b>
      </span>
      <span title={t('本次会话累计 tokens / 轮次', 'session tokens / turns')}>
        {t('会话', 'session')} <b>{fmtK(sessionTokens)}</b> · {turnCount} {t('轮', 'turns')}
      </span>
      {ctxWindow > 0 && (
        <span title={t('本轮输入占当前模型上下文窗口比例', 'prompt vs current model context window')}>
          {t('上下文', 'ctx')} <b style={{ color: ctxPct >= 80 ? 'var(--amber)' : undefined }}>{ctxPct.toFixed(0)}%</b>
        </span>
      )}
      {(models.find((m) => m.key === currentModel)?.priced ?? false) && (
        <span title={t('按官方定价折算（缓存输入×hit价 + 其余输入×miss价 + 输出×out价）', 'Priced per official rate')}>
          {t('费用', 'cost')} <b>{fmtCost(usage.cost)}</b> · {t('累计', 'total')} <b>{fmtCost(usage.costTotal)}</b>
        </span>
      )}
      {balance.ok && balance.total && (
        <span title={t('模型提供方账户余额（当前仅 DeepSeek）', 'Provider account balance (DeepSeek only)')}>
          · {t('余额', 'balance')} <b style={{ color: 'var(--green)' }}>{balance.currency === 'CNY' ? '¥' : '$'}{balance.total}</b>
        </span>
      )}
      {attSent > 0 && (
        <span title={t('本次会话附件 / 其中识图', 'attachments / vision in this session')}>
          {t('附件', 'atts')} <b>{attSent}</b>{visionSent > 0 ? ` · ${t('识图', 'vision')} ${visionSent}` : ''}
        </span>
      )}
      <span className="spacer" style={{ flex: 1 }} />
      <span title={t('服务端前缀缓存命中率（cached/prompt）', 'Provider prefix-cache hit rate')}>
        {t('命中率', 'hit')} <b style={{ color: hitRate >= 50 ? 'var(--green)' : 'var(--text-dim)' }}>{hitRate.toFixed(0)}%</b>
      </span>
      <span>{t('请求', 'reqs')} <b>{usage.requests}</b></span>
      <span>{lang.toUpperCase()}</span>
    </div>
  )
}
