// 内置浏览器视图（嵌入编辑器标签页，非弹窗）：支持 Chrome DevTools 和 Playwright 两种后端。
// 配合 chrome-devtools-mcp 或 @playwright/mcp MCP Server 使用。
// 无头模式开启时不弹出浏览器窗口（截图经 MCP 落盘转 data URL 回显）。
import { useEffect, useRef, useState } from 'react'
import { api, onEvent } from '../bridge'
import { useStore, t } from '../store'
import ThinkingDots from './ThinkingDots'

interface LogEntry { time: string; level: 'info' | 'warn' | 'error'; msg: string }
interface BrowserOption { type: string; name: string; description: string; installed: boolean }

export default function BrowserView() {
  const [url, setUrl] = useState('')
  const [inputUrl, setInputUrl] = useState('')
  const [status, setStatus] = useState<'disconnected' | 'connecting' | 'connected' | 'error'>('disconnected')
  const [error, setError] = useState('')
  const [screenshot, setScreenshot] = useState<string | null>(null)
  const [logs, setLogs] = useState<LogEntry[]>([])
  const [autoRefresh, setAutoRefresh] = useState(false)
  const [availableBrowsers, setAvailableBrowsers] = useState<BrowserOption[]>([])
  const [selectedBrowser, setSelectedBrowser] = useState(() => {
    // 从 localStorage 记住上次选的；没有则默认 chrome-devtools
    try {
      return localStorage.getItem('browserType') || 'chrome-devtools'
    } catch {
      return 'chrome-devtools'
    }
  })
  const [headless, setHeadless] = useState(() => localStorage.getItem('browserHeadless') !== '0')
  const screenshotRef = useRef<HTMLDivElement>(null)
  const intervalRef = useRef<ReturnType<typeof setInterval> | null>(null)
  const connectTimer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined)
  const statusRef = useRef(status)
  statusRef.current = status

  const addLog = (msg: string, level: 'info' | 'warn' | 'error' = 'info') => {
    const now = new Date()
    const time = `${now.getHours().toString().padStart(2, '0')}:${now.getMinutes().toString().padStart(2, '0')}:${now.getSeconds().toString().padStart(2, '0')}`
    setLogs(prev => [...prev.slice(-99), { time, level, msg }])
  }

  const loadAvailableBrowsers = async () => {
    try {
      const browsers = await api.getAvailableBrowsers()
      setAvailableBrowsers(browsers || [])
    } catch (e) {
      console.error('Failed to load browsers:', e)
    }
  }

  const connect = async () => {
    setStatus('connecting')
    addLog(`正在连接 ${selectedBrowser === 'playwright' ? 'Playwright' : 'Chrome DevTools'}${headless ? '（无头）' : ''}...`)
    try {
      const res = await api.connectBrowserMCP(selectedBrowser, headless)
      if (res.ok) {
        addLog('MCP 进程已启动，等待握手（首次约需 30-90 秒）...')
        clearTimeout(connectTimer.current)
        // 更激进的超时：60 秒
        connectTimer.current = setTimeout(async () => {
          if (statusRef.current !== 'connecting') return
          const st = await api.checkBrowserMCPStatus()
          if (st !== 'connected') {
            setStatus('disconnected')
            setError('连接超时：握手未完成')
            addLog('连接超时：握手未完成，可重试或到 MCP 面板查看日志', 'error')
            // 强制刷新页面
            window.location.reload()
          }
        }, 60000) // 1 分钟超时
      } else {
        setStatus('error')
        setError(res.error || '连接失败')
        addLog('连接失败: ' + (res.error || '未知错误'), 'error')
      }
    } catch (e: any) {
      setStatus('error')
      setError(e.message)
      addLog('连接异常: ' + e.message, 'error')
    }
  }

  const disconnect = async () => {
    clearTimeout(connectTimer.current)
    try {
      await api.disconnectBrowserMCP()
    } catch { /* 忽略断开错误 */ }
    setStatus('disconnected')
    setError('')
    setScreenshot(null)
    addLog('浏览器 MCP 已断开')
  }

  // 轮询等待 MCP 握手完成（connect() 只代表进程拉起）
  const waitHandshake = (ms: number) => new Promise<boolean>((resolve) => {
    const t0 = Date.now()
    const tick = async () => {
      const st = await api.checkBrowserMCPStatus()
      if (st === 'connected') return resolve(true)
      if (Date.now() - t0 > ms) return resolve(false)
      // 每 5 秒打印一次等待进度
      if (Date.now() - t0 > 0 && (Date.now() - t0) % 5000 < 1000) {
        addLog(`等待握手中... (${Math.round((Date.now() - t0) / 1000)}s)`, 'info')
      }
      setTimeout(tick, 800)
    }
    tick()
  })

  // ensureConnected 未连接时自动走连接流程（地址栏直接导航的体验：输入网址即用）
  const ensureConnected = async (): Promise<boolean> => {
    if (statusRef.current === 'connected') return true
    const st0 = await api.checkBrowserMCPStatus()
    if (st0 === 'connected') {
      setStatus('connected')
      return true
    }
    addLog('未连接，正在自动连接浏览器 MCP…')
    await connect()
    const ok = await waitHandshake(60000) // 20s → 60s
    clearTimeout(connectTimer.current)
    if (ok) {
      setStatus('connected')
      addLog('浏览器 MCP 已连接')
    } else {
      setStatus('disconnected')
      addLog('自动连接失败：确认已安装对应 MCP（见下方安装说明）', 'error')
    }
    return ok
  }

  const navigate = async () => {
    if (!inputUrl.trim()) return
    if (!(await ensureConnected())) return
    let u = inputUrl.trim()
    if (!/^[a-z][a-z0-9+.-]*:\/\//i.test(u)) u = 'https://' + u // 无协议自动补 https
    setUrl(u)
    addLog('导航: ' + u)
    try {
      const res = await api.browserNavigate(u)
      if (res.error) {
        addLog('导航失败: ' + res.error, 'error')
        syncStatus()
      } else {
        addLog('页面加载完成')
        if (res.screenshot) {
          setScreenshot(res.screenshot)
          addLog('截图已更新')
        }
      }
    } catch (e: any) {
      addLog('导航异常: ' + e.message, 'error')
    }
  }

  const takeScreenshot = async () => {
    if (!(await ensureConnected())) return
    addLog('截图...')
    try {
      const res = await api.browserScreenshot()
      if (res.screenshot) {
        setScreenshot(res.screenshot)
        addLog('截图已更新')
      }
      if (res.error) {
        addLog('截图失败: ' + res.error, 'error')
        syncStatus()
      }
    } catch (e: any) {
      addLog('截图异常: ' + e.message, 'error')
    }
  }

  const getSnapshot = async () => {
    if (!(await ensureConnected())) return
    addLog('获取页面内容...')
    try {
      const res = await api.browserSnapshot()
      if (res.content) {
        addLog('内容已提取 (' + res.content.length + ' 字符)')
        await navigator.clipboard.writeText(res.content)
        addLog('内容已复制到剪贴板')
      }
      if (res.error) {
        addLog('获取内容失败: ' + res.error, 'error')
        syncStatus()
      }
    } catch (e: any) {
      addLog('获取内容异常: ' + e.message, 'error')
    }
  }

  // 操作报错后回查真实 MCP 状态：进程中途退出时把「已连接」徽章拉回现实
  const syncStatus = async () => {
    try {
      const st = await api.checkBrowserMCPStatus()
      if (st !== 'connected' && statusRef.current === 'connected') {
        setStatus('disconnected')
        setAutoRefresh(false)
        addLog('浏览器 MCP 已断开（进程退出）', 'warn')
      }
    } catch { /* 忽略 */ }
  }

  const clearLogs = () => setLogs([])

  // 自动刷新截图
  useEffect(() => {
    if (autoRefresh && status === 'connected') {
      intervalRef.current = setInterval(takeScreenshot, 3000)
    } else if (intervalRef.current) {
      clearInterval(intervalRef.current)
      intervalRef.current = null
    }
    return () => {
      if (intervalRef.current) clearInterval(intervalRef.current)
    }
  }, [autoRefresh, status])

  // 检查连接状态；mcp:reconnected 到达时 Go 侧同步握手已结束，
  // 复查真实状态把 connecting 落定为 connected / disconnected。
  useEffect(() => {
    const check = async () => {
      const st = await api.checkBrowserMCPStatus()
      if (st === 'connected') {
        setStatus('connected')
      } else {
        setStatus('disconnected')
      }
      await loadAvailableBrowsers()
    }
    check()
    const offRe = onEvent('mcp:reconnected', async () => {
      const st = await api.checkBrowserMCPStatus()
      clearTimeout(connectTimer.current)
      if (st === 'connected') {
        setStatus('connected')
        addLog('浏览器 MCP 已连接', 'info')
      } else if (statusRef.current === 'connecting') {
        setStatus('disconnected')
        addLog('连接失败：握手未完成，可重试或到 MCP 面板查看日志', 'error')
      }
    })
    return offRe
  }, [])

  return (
    <>
      {/* 连接状态栏 */}
      <div className="browser-status-bar">
        <span className={`status-badge ${status}`}>
          {status === 'connected' ? '✅ 已连接' :
           status === 'connecting' ? <><ThinkingDots className="sm" /> {t('连接中', 'Connecting')}</> :
           status === 'error' ? '❌ ' + error :
           '⚪ 未连接'}
        </span>
        {status === 'disconnected' && (
          <>
            <select
              value={selectedBrowser}
              onChange={async (e) => {
                const newType = e.target.value
                // 切换浏览器类型：先断开旧的，再连新的
                if (statusRef.current === 'connected') {
                  await disconnect()
                }
                setSelectedBrowser(newType)
                try { localStorage.setItem('browserType', newType) } catch { /* 忽略 */ }
                // 用单服务器 upsert 写配置；getMCPServers+saveMCPServers 会把整个文档
                // 再包一层 "servers"，把 mcp.json 写成递归嵌套的垃圾结构
                try {
                  const cfg = newType === 'playwright'
                    ? { enabled: true, command: 'playwright-mcp.cmd', args: headless ? ['--headless'] : [] }
                    : { enabled: true, command: 'chrome-devtools-mcp', args: headless ? ['--headless'] : [] }
                  await api.saveMCPServer('browser', cfg)
                  addLog(`已更新 mcp.json：command=${cfg.command}`)
                } catch (err) {
                  addLog(`更新 mcp.json 失败：${err}`, 'warn')
                }
                addLog(`切换到 ${newType === 'playwright' ? 'Playwright' : 'Chrome DevTools'}，正在连接...`)
                await connect()
              }}
              style={{ marginRight: 8 }}
            >
              {availableBrowsers.map(b => (
                <option key={b.type} value={b.type} disabled={!b.installed}>
                  {b.name} {!b.installed && '（未安装）'}
                </option>
              ))}
            </select>
            <button className="btn small" onClick={connect}>连接</button>
          </>
        )}
        {/* 连接中/错误状态也显示断开按钮（可手动放弃） */}
        {(status === 'connecting' || status === 'error') && (
          <button className="btn small" onClick={disconnect}>
            {status === 'connecting' ? '放弃' : '重试'}
          </button>
        )}
        {status === 'connected' && (
          <button className="btn small" onClick={disconnect}>断开</button>
        )}
        {status === 'disconnected' && (
          <label className="auto-refresh" title={t('不弹出浏览器窗口，仅截图回显', 'No browser window; screenshots only')}>
            <input type="checkbox" checked={headless} onChange={e => {
              setHeadless(e.target.checked)
              try { localStorage.setItem('browserHeadless', e.target.checked ? '1' : '0') } catch { /* 忽略 */ }
            }} />
            {t('无头模式', 'Headless')}
          </label>
        )}
        {status === 'connected' && (
          <label className="auto-refresh">
            <input type="checkbox" checked={autoRefresh} onChange={e => setAutoRefresh(e.target.checked)} />
            自动截图
          </label>
        )}
      </div>

      {/* 地址栏（常驻：未连接时输入网址会自动连接再导航） */}
      <div className="browser-url-bar">
        <input
          type="text"
          value={inputUrl}
          onChange={e => setInputUrl(e.target.value)}
          onKeyDown={e => e.key === 'Enter' && navigate()}
          placeholder={status === 'connected' ? t('输入网址，按回车导航...', 'Type a URL and press Enter...') : t('输入网址，回车自动连接并打开...', 'Type a URL to auto-connect and open...')}
        />
        <button onClick={navigate} title={t('导航', 'Navigate')}>→</button>
        <button onClick={takeScreenshot} title={t('截图', 'Screenshot')}>📸</button>
        <button onClick={getSnapshot} title={t('提取内容', 'Get Content')}>📄</button>
      </div>

      {/* 截图预览（未连接时不渲染，避免大片空占位） */}
      {status === 'connected' && (
        <div className="browser-preview" ref={screenshotRef}>
          {screenshot ? (
            <img src={screenshot} alt="Browser screenshot" onClick={() => setScreenshot(null)} />
          ) : (
            <div className="browser-preview-placeholder">
              {t('点击"📸截图"预览页面', 'Click "📸Screenshot" to preview')}
            </div>
          )}
        </div>
      )}

      {/* 操作日志 */}
      <div className="browser-logs">
        <div className="logs-header">
          <span>{t('操作日志', 'Logs')}</span>
          <button className="btn tiny" onClick={clearLogs}>{t('清空', 'Clear')}</button>
        </div>
        <div className="logs-list">
          {logs.length === 0 && <div className="log-empty">{t('暂无日志', 'No logs')}</div>}
          {logs.map((log, i) => (
            <div key={i} className={`log-entry ${log.level}`}>
              <span className="log-time">{log.time}</span>
              <span className="log-msg">{log.msg}</span>
            </div>
          ))}
        </div>
      </div>

      {/* 底部说明 */}
      <div className="browser-footer">
        <details>
          <summary>{t('安装说明', 'Installation Guide')}</summary>
          <div className="install-guide">
            <h4>选项 1: Chrome DevTools（推荐）</h4>
            <p>使用已登录的 Chrome 浏览器，保留会话状态。</p>
            <pre>npm install -g chrome-devtools-mcp</pre>

            <h4>选项 2: Playwright（无需预装 Chrome）</h4>
            <p>自动管理浏览器实例，适合自动化场景。</p>
            <pre>npm install -g @playwright/mcp
npx playwright install chromium</pre>

            <h4>关于验证码</h4>
            <p>如有验证码，请在 AI 调用浏览器工具后，在聊天框输入验证码内容，AI 会自动填写。</p>
          </div>
        </details>
      </div>
    </>
  )
}
