// 底部单行状态栏（类 Claude Code statusline）：
// ● 模型 · ⌂ 工作区 | ⎇ 分支 | 本次/平均命中 | 会话 tokens | 本次 tokens |
// 吞吐 | 输出 | 缓存 | 本次/会话费用 | 轮次 | 上下文 | 压缩阈值 | 余额。
// 无数据的字段显示 —，字段随模型或提供方报告自动填充。
import { useStore, t } from '../store'
import ThinkingDots from './ThinkingDots'

// fmtK 大数缩写：1234 → 1.2k（单行紧凑显示）
function fmtK(n: number): string {
  return n >= 1000 ? `${(n / 1000).toFixed(1)}k` : String(n)
}

// fmtComma 千分位：971859 → 971,859（会话累计 tokens）
function fmtComma(n: number): string {
  return n.toLocaleString('en-US')
}

// fmtCNY USD→CNY（费率 7.2，统计条口径），四位小数对齐 statusline 风格
function fmtCNY(usd: number): string {
  return `¥${(usd * 7.2).toFixed(4)}`
}

// shortPath 工作区缩写：超过 3 段时保留末两级（…/build/qwen-coder）
function shortPath(p: string): string {
  const segs = p.split(/[\\/]/).filter(Boolean)
  if (segs.length <= 3) return p
  return `…/${segs.slice(-2).join('/')}`
}

function Sep() {
  return <i className="sep">|</i>
}

export default function StatsBar() {
  const running = useStore((s) => s.running)
  const usage = useStore((s) => s.usage)
  const models = useStore((s) => s.models)
  const currentModel = useStore((s) => s.currentModel)
  const workspace = useStore((s) => s.workspace)
  const branch = useStore((s) => s.branch)
  const balance = useStore((s) => s.balance)
  const sessionTokens = useStore((s) => s.sessionTokens)
  const sessionPrompt = useStore((s) => s.sessionPrompt)
  const sessionCached = useStore((s) => s.sessionCached)
  const sessionCost = useStore((s) => s.sessionCost)
  const turnCount = useStore((s) => s.turnCount)
  const lastThroughput = useStore((s) => s.lastThroughput)
  const compact = useStore((s) => s.compact)
  const mi = models.find((m) => m.key === currentModel)
  const modelName = mi?.model_id || currentModel.split('/').pop() || '—'

  // 命中率：本次 = 本轮 cached/prompt；平均 = 会话累计 cached/prompt
  const turnHit = usage.prompt > 0 ? `${((usage.cached / usage.prompt) * 100).toFixed(1)}%` : '—'
  const avgHit = sessionPrompt > 0 ? `${((sessionCached / sessionPrompt) * 100).toFixed(2)}%` : '—'
  // 上下文占用：本轮输入 / 当前模型窗口
  const ctxWindow = mi?.context_window || 0
  const ctxPct = ctxWindow > 0 ? Math.min(100, (usage.prompt / ctxWindow) * 100) : 0
  // 压缩阈值：压缩预算 / 上下文窗口
  const compactPct = compact.window > 0 ? `${Math.min(100, (compact.budget / compact.window) * 100).toFixed(0)}%` : '—'
  const priced = mi?.priced ?? false

  return (
    <div className="usage-bar">
      <span className="model" title={`${mi?.display_name || currentModel}${running ? ` · ${t('运行中', 'running')}` : ''}`}>
        {running ? <ThinkingDots className="sm busy" /> : <span className="status-dot" />}
        {modelName}
      </span>
      <i className="sep">·</i>
      {workspace && (
        <span title={workspace}>⌂ {shortPath(workspace)}</span>
      )}
      <Sep />
      {branch && (
        <>
          <span title={t('当前 Git 分支', 'current git branch')}>⎇ {branch}</span>
          <Sep />
        </>
      )}
      <span title={t('本轮前缀缓存命中率（cached/prompt）', 'prefix-cache hit rate this turn')}>
        {t('本次命中', 'turn hit')} <b>{turnHit}</b>
      </span>
      <Sep />
      <span title={t('会话平均前缀缓存命中率', 'average prefix-cache hit rate this session')}>
        {t('平均命中', 'avg hit')} <b>{avgHit}</b>
      </span>
      <Sep />
      <span title={t('本次会话累计 tokens', 'total tokens this session')}>
        {t('会话 tokens', 'session tok')} <b>{fmtComma(sessionTokens)}</b>
      </span>
      <Sep />
      <span title={t('本轮 tokens（输入+输出）', 'tokens this turn (in+out)')}>
        {t('本次 tokens', 'turn tok')} <b>{usage.total > 0 ? fmtK(usage.total) : '—'}</b>
      </span>
      <Sep />
      <span title={t('上一轮输出吞吐（tokens/秒）', 'output throughput (tokens/s)')}>
        {t('吞吐速度', 'tok/s')} <b>{lastThroughput > 0 ? `${lastThroughput}/s` : '—'}</b>
      </span>
      <Sep />
      <span title={t('本轮输出 tokens', 'completion tokens this turn')}>
        {t('输出', 'out')} <b>{usage.completion > 0 ? fmtK(usage.completion) : '—'}</b>
      </span>
      <Sep />
      <span title={t('本轮前缀缓存命中 tokens', 'prefix-cache hit tokens this turn')}>
        {t('缓存', 'cache')} <b>{usage.cached > 0 ? fmtK(usage.cached) : '—'}</b>
      </span>
      <Sep />
      <span title={t('本轮费用（按官方定价折算）', 'cost this turn (official pricing)')}>
        {t('本次费用', 'turn cost')} <b>{priced ? fmtCNY(usage.cost) : '—'}</b>
      </span>
      <Sep />
      <span title={t('本次会话轮次（发送次数）', 'turns this session')}>
        {t('当前会话', 'session')} <b>{turnCount}{t('轮', ' turns')}</b>
      </span>
      <Sep />
      <span title={t('本轮输入占当前模型上下文窗口比例', 'prompt vs current model context window')}>
        {t('上下文', 'ctx')}{' '}
        <b style={{ color: ctxWindow > 0 && ctxPct >= 80 ? 'var(--amber)' : undefined }}>
          {ctxWindow > 0 ? `${ctxPct.toFixed(0)}%` : '—'}
        </b>
      </span>
      <Sep />
      <span title={t('触发上下文压缩的预算占窗口比例', 'compact budget as share of context window')}>
        {t('压缩阈值', 'compact')} <b>{compactPct}</b>
      </span>
      <Sep />
      <span title={t('本次会话累计费用', 'total cost this session')}>
        {t('会话费用', 'session cost')} <b>{priced ? fmtCNY(sessionCost) : '—'}</b>
      </span>
      {balance.ok && balance.total && (
        <>
          <Sep />
          <span title={t('模型提供方账户余额（当前仅 DeepSeek）', 'Provider account balance (DeepSeek only)')}>
            {t('余额', 'balance')}{' '}
            <b style={{ color: 'var(--green)' }}>
              {balance.currency === 'CNY' ? '¥' : '$'}{balance.total}
            </b>
          </span>
        </>
      )}
    </div>
  )
}
