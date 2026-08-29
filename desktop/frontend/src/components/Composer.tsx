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

async function runCommand(raw: string, st: ReturnType<typeof useStore.getState>) {
  const [cmd, ...rest] = raw.slice(1).split(/\s+/)
  const arg = rest.join(' ')
  switch (cmd) {
    case 'help':
      st.notice('**斜杠命令**\n- `/new` 新会话 · `/clear` 清屏 · `/model` 模型\n- `/dir [路径]` 切目录 · `/permission` 权限 · `/context` 独立提问\n- `/index` 重建索引 · `/branch` 当前分支 · `/export` 导出会话\n- `/init` 生成 AGENTS.md · `/bug [范围]` 排查修复 BUG\n- `/kb` 知识库 · `/skills` 技能 · `/changes` 改动\n- `/mcp` MCP · `/dispatch` 派发 · `/cache` 缓存\n\n**其它前缀**\n- `@关键词` 文件补全（↑↓ 选择，Enter 插入路径）\n- `!命令` 直接在工作区执行（不经过模型）')
      break
    case 'bug': {
      // 对齐 Claude Code 式工作流：系统排查 bug → 修复 → 验证闭环
      const scope = rest.join(' ')
      st.send(t(
        '请排查并修复当前工作区代码中的 BUG。要求：\n' +
        '1. 先用 todo_write 列出排查计划，再系统性地找证据：构建/测试是否通过、明显的逻辑错误、边界条件、错误处理缺失、并发/资源泄漏等；' + (scope ? `本次重点范围：${scope}。\n` : '\n') +
        '2. 只修「真实存在、有证据」的缺陷（能指出文件:行号和错误原因），不做风格重构、不引入新功能；\n' +
        '3. 每个 BUG 用 write_file 实际修复，修一个验证一个（构建/测试/运行）；\n' +
        '4. 全部修完后输出清单：每个 BUG 的位置、原因、修复方式、验证结果；若最终没有发现确定性 BUG，如实说明并列出可疑点，不要为凑数乱改。',
        'Find and fix real bugs in the current workspace. Plan with todo_write first, gather evidence (build/tests, logic errors, edge cases, missing error handling, leaks)' + (scope ? `, focusing on: ${scope}.` : '. ') + ' Fix only evidence-backed defects (file:line + reason), no refactors or new features. Fix with write_file and verify each with build/tests. End with a report per bug (location/cause/fix/verification); if none found, say so honestly.'
      ))
      break
    }
    case 'init': {
      // 对齐 Claude Code /init：分析代码库 → 生成/修订 AGENTS.md（供后续 AI 助手了解项目）
      st.send(t(
        '请分析当前工作目录的代码库，并在工作区根目录生成/更新 AGENTS.md（供后续 AI 助手快速了解本项目）。要求：\n' +
        '1. 先浏览目录结构与关键入口（README、构建脚本、依赖清单、配置文件），再动笔；\n' +
        '2. 内容包含：项目一句话概述、技术栈与关键依赖、目录结构导览、构建/测试/运行命令、代码约定（命名/注释/错误处理）、注意事项；\n' +
        '3. 使用中文，信息必须来自真实文件内容，不要编造；若已存在 AGENTS.md，按现状修订而非推倒重写。',
        'Analyze the codebase in the current workspace and generate/update AGENTS.md at the workspace root for future AI assistants. Browse the directory structure and key entry files first; include an overview, tech stack, directory guide, build/test/run commands, and conventions. Use real file contents only; revise (do not blindly overwrite) an existing AGENTS.md. Write it in Chinese.'
      ))
      break
    }
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
    case 'export': {
      const items = useStore.getState().items
      if (items.length === 0) { st.notice(t('会话为空，无可导出内容', 'Empty session')); break }
      const md = items.map((it) => {
        if (it.kind === 'user') return `## 🧑 ${t('用户', 'User')}\n\n${it.text}`
        if (it.kind === 'tool' && it.tool) return `## 🔧 ${it.tool.name}\n\n\`\`\`\n${(it.tool.result || '').slice(0, 2000)}\n\`\`\``
        return `## 🤖 ${t('助手', 'Assistant')}\n\n${it.text}`
      }).join('\n\n')
      const path = `session-export-${new Date().toISOString().slice(0, 19).replace(/[:T]/g, '-')}.md`
      api.writeFileText(path, md)
      st.notice(t(`已导出到工作区：${path}`, `Exported: ${path}`))
      break
    }
    case 'compact': {
      const r = await api.compactHistory()
      st.notice(r?.ok
        ? t(`上下文已压缩：${r.before} → ${r.after} tokens`, `Compacted: ${r.before} → ${r.after} tokens`)
        : t(`压缩未执行：${r?.msg || '无历史'}`, `Not compacted: ${r?.msg || 'no history'}`))
      break
    }
    case 'cost': {
      const s = useStore.getState()
      const u = s.usage
      const bal = s.balance.ok && s.balance.total
        ? `\n- ${t('余额', 'balance')} ${s.balance.currency === 'CNY' ? '¥' : '$'}${s.balance.total}`
        : ''
      st.notice(`**${t('用量与费用', 'Usage & cost')}**\n- ${t('输入', 'in')} ${u.prompt} · ${t('输出', 'out')} ${u.completion} · ${t('缓存命中', 'cached')} ${u.cached}${u.reasoning ? ` · ${t('思考', 'reasoning')} ${u.reasoning}` : ''}\n- ${t('请求', 'requests')} ${u.requests} · ${t('本轮费用', 'cost')} $${u.cost.toFixed(4)} · ${t('累计', 'total')} $${u.costTotal.toFixed(4)}${bal}`)
      break
    }
    case 'doctor': {
      const r = await api.doctor()
      st.notice(`**${t('健康自检', 'Doctor')}**\n${(r?.checks || []).map((c) => `- ${c.ok ? '✅' : '❌'} ${c.name}：${c.detail}`).join('\n')}`)
      break
    }
    case 'memory': {
      const ws = useStore.getState().workspace
      if (!ws) { st.notice(t('未设置工作目录', 'No workspace')); break }
      const p = `AGENTS.md`
      const content = await api.readFileText(p)
      if (content) st.openFile(p)
      else st.notice(t('工作区尚无 AGENTS.md，可运行 /init 生成', 'No AGENTS.md yet; run /init'))
      break
    }
    case 'review': {
      const scope = rest.join(' ')
      st.send(t(
        '请对当前工作区代码做一次代码评审（只评审、不修改）。重点：正确性、边界条件、错误处理、并发安全、安全隐患。' + (scope ? `范围：${scope}。` : '') + '\n输出格式：按严重程度列出问题（文件:行号、问题描述、修复建议），没有问题就如实说明。最后用 todo_write 记录「发现的问题清单」。',
        'Review the workspace code (review only, no modifications). Focus on correctness, edge cases, error handling, concurrency, security. Report issues by severity (file:line, description, suggested fix); if clean, say so. Record findings with todo_write.'
      ))
      break
    }
    case 'status': {
      const s = useStore.getState()
      st.notice(`**${t('当前状态', 'Status')}**\n- ${t('模型', 'model')}：${s.currentModel || '—'}\n- ${t('工作区', 'workspace')}：${s.workspace || '—'}${s.branch ? `（${s.branch}）` : ''}\n- ${t('权限', 'mode')}：${s.mode} · ${t('语言', 'lang')}：${s.lang}\n- ${t('会话', 'session')}：${s.sessionId || '—'} · tokens ${s.usage.total.toLocaleString()} · ${t('轮次', 'turns')} ${s.turnCount}`)
      break
    }
    case 'todos': {
      const steps = useStore.getState().steps
      st.notice(steps.length
        ? `**${t('任务步骤', 'Steps')}**\n${steps.map((s2) => `- ${s2.status === 'done' ? '✅' : s2.status === 'run' ? '◐' : s2.status === 'wait' ? '○' : '✗'} ${s2.title}`).join('\n')}`
        : t('当前无进行中的任务步骤', 'No active steps'))
      break
    }
    case 'kb': st.setShowKBPanel(true); break
    case 'skills': st.setShowSkillsPanel(true); break
    case 'changes': st.setShowChangesPanel(true); break
    case 'mcp': st.setShowMCPPanel(true); break
    case 'dispatch': st.setShowDispatchPanel(true); break
    case 'cache': st.setShowCachePanel(true); break
    default: st.notice(t(`未知命令 ${cmd}（/help 查看全部）`, `Unknown ${cmd}`))
  }
}

export default function Composer() {
  const [text, setText] = useState('')
  const [dragOver, setDragOver] = useState(false)
  const [fileHits, setFileHits] = useState<string[]>([])
  const [fileIdx, setFileIdx] = useState(0)
  const taRef = useRef<HTMLTextAreaElement>(null)
  const dragDepth = useRef(0) // 拖拽高亮计数（进入/离开子元素会成对触发，计数防闪烁）
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
    // @ 文件补全：↑↓ 选择，Enter/Tab 插入
    if (text.startsWith('@') && fileHits.length > 0) {
      if (e.key === 'ArrowDown') { e.preventDefault(); setFileIdx((i) => (i + 1) % fileHits.length); return }
      if (e.key === 'ArrowUp') { e.preventDefault(); setFileIdx((i) => (i - 1 + fileHits.length) % fileHits.length); return }
      if (e.key === 'Enter' || e.key === 'Tab') { e.preventDefault(); pickFile(fileHits[fileIdx]); return }
    }
    if (e.key === 'Enter' && !e.shiftKey && !e.ctrlKey) {
      e.preventDefault()
      if (text.startsWith('!')) { void runTerminal(); return }
      doSend()
    }
  }

  // @ 文件补全：输入防抖搜索工作区文件
  useEffect(() => {
    if (!text.startsWith('@')) { setFileHits([]); setFileIdx(0); return }
    const q = text.slice(1)
    const id = setTimeout(() => {
      api.searchFiles(q).then((hits) => { setFileHits(hits || []); setFileIdx(0) })
    }, 150)
    return () => clearTimeout(id)
  }, [text])

  const pickFile = (p: string) => {
    setText('@' + p + ' ')
    setFileHits([])
    requestAnimationFrame(autoGrow)
    taRef.current?.focus()
  }

  // ! 终端：直接在工作区执行命令（不经过模型），输出以代码卡片入聊天
  const runTerminal = async () => {
    const cmd = text.slice(1).trim()
    setText('')
    requestAnimationFrame(autoGrow)
    if (!cmd) return
    const r = await api.runTerminalCommand(cmd)
    useStore.getState().notice(`**$ ${cmd}**\n\n\`\`\`\n${String(r.output || '').slice(0, 4000)}\n\`\`\``)
  }

  const onPaste = (e: React.ClipboardEvent) => {
    const pasted = e.clipboardData.getData('text')
    if (!pasted || pasted.includes('\n')) return
    const m = pasted.trim().match(/^(?:file:\/\/)?((?:[A-Za-z]:[\\/]|\/)[^\s]+)$/u)
    if (m) { e.preventDefault(); addAttachment(m[1]) }
  }

  // fileURIToPath 解码 file:// URI 为本地路径（兼容 file:///path 与 file://host/path）。
  const fileURIToPath = (u: string): string => {
    if (!u.startsWith('file://')) return ''
    let p = u.slice('file://'.length)
    if (p.startsWith('localhost/')) p = p.slice('localhost'.length)
    try { p = decodeURIComponent(p) } catch { /* 编码异常时按原样使用 */ }
    return p
  }
  const isAbsPath = (p: string) => /^(?:[A-Za-z]:[\\/]|\/)/.test(p)

  const onDrop = (e: React.DragEvent) => {
    e.preventDefault()
    dragDepth.current = 0
    setDragOver(false)
    const dt = e.dataTransfer
    let added = 0
    // 1) 应用内拖拽（文件树行 / 编辑器标签页）：自定义类型
    const local = dt.getData('text/localai-path')
    if (local) {
      for (const p of local.split('\n')) {
        const s = p.trim()
        if (s) { addAttachment(s); added++ }
      }
      if (added > 0) return
    }
    // 2) 系统文件管理器拖入：WebKitGTK 以 file:// URI 形式暴露在 uri-list/plain
    for (const type of ['text/uri-list', 'text/plain']) {
      const raw = dt.getData(type)
      if (!raw) continue
      for (const line of raw.split(/\r?\n/)) {
        const u = line.trim()
        if (!u || u.startsWith('#')) continue
        if (u.startsWith('file://')) {
          const p = fileURIToPath(u)
          if (p && isAbsPath(p)) { addAttachment(p); added++ }
        } else if (type === 'text/plain' && isAbsPath(u)) {
          addAttachment(u); added++ // 部分来源把本地路径直接放在纯文本里
        }
      }
      if (added > 0) return
    }
    // 3) File 对象：仅当带绝对路径时可用（部分 Wails 平台注入 path）；
    //    只有裸文件名的条目后端读不到，丢弃并提示。
    for (const f of Array.from(dt.files)) {
      const p = (f as any).path
      if (typeof p === 'string' && isAbsPath(p)) { addAttachment(p); added++ }
    }
    if (added === 0 && dt.files.length > 0) {
      useStore.getState().notice(t(
        '未能解析拖入文件的本地路径，请改用 📎 按钮选择。',
        'Could not resolve dropped file path; use the 📎 button instead.'
      ))
    }
  }

  return (
    <div className="composer">
      {previewSrc && (
        <div className="lightbox" onClick={() => useStore.getState().setPreviewSrc(null)}>
          <img src={previewSrc} alt="" />
        </div>
      )}

      <div
        className={`composer-inner${dragOver ? ' drag-over' : ''}`}
        onDragEnter={(e) => { e.preventDefault(); dragDepth.current++; setDragOver(true) }}
        onDragOver={(e) => e.preventDefault()}
        onDragLeave={() => {
          dragDepth.current = Math.max(0, dragDepth.current - 1)
          if (dragDepth.current === 0) setDragOver(false)
        }}
        onDrop={onDrop}
      >
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

        {text.startsWith('/') && !text.includes(' ') && (
          <div className="slash-hint">
            <span><b>/help</b> 全部命令</span><span><b>/new</b> 新会话</span>
            <span><b>/model</b> 模型</span><span><b>/dir</b> 目录</span>
            <span><b>/init</b> 生成 AGENTS.md</span><span><b>/bug</b> 修 BUG</span>
            <span><b>/kb</b> 知识库</span><span><b>/skills</b> 技能</span>
            <span><b>/export</b> 导出</span><span><b>/help</b> 更多…</span>
          </div>
        )}
        {text.startsWith('@') && (
          <div className="slash-hint file-hint">
            {fileHits.length === 0
              ? <span>🔍 {t('工作区文件搜索中…（继续输入可过滤）', 'searching workspace files…')}</span>
              : fileHits.map((f, i) => (
                <span key={f} className={i === fileIdx ? 'hit active' : 'hit'}
                  onClick={() => pickFile(f)} title={f}>{f}</span>
              ))}
          </div>
        )}
        {text.startsWith('!') && (
          <div className="slash-hint">
            <span>⌨ {t('Enter 在工作区直接执行命令（不经过模型，60s 超时）', 'Enter runs the command in the workspace (no model, 60s timeout)')}</span>
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
