// 内置浏览器面板：支持 Chrome DevTools 和 Playwright 两种后端。
// 配合 chrome-devtools-mcp 或 @playwright/mcp MCP Server 使用。
import { useEffect, useRef, useState } from 'react'
import { api, onEvent } from '../bridge'
import { useStore, t } from '../store'
import ThinkingDots from './ThinkingDots'

interface LogEntry { time: string; level: 'info' | 'warn' | 'error'; msg: string }
interface BrowserOption { type: string; name: string; description: string; installed: boolean }

export default function BrowserPanel() {
  const show = useStore((s) => s.showBrowserPanel)
  const setShow = useStore((s) => s.setShowBrowserPanel)
  const [url, setUrl] = useState('')
  const [inputUrl, setInputUrl] = useState('')
  const [status, setStatus] = useState<'disconnected' | 'connecting' | 'connected' | 'error'>('disconnected')
  const [error, setError] = useState('')
  const [screenshot, setScreenshot] = useState<string | null>(null)
  const [logs, setLogs] = useState<LogEntry[]>([])
  const [autoRefresh, setAutoRefresh] = useState(false)
  const [availableBrowsers, setAvailableBrowsers] = useState<BrowserOption[]>([])
  const [selectedBrowser, setSelectedBrowser] = useState('chrome-devtools')
  const screenshotRef = useRef<HTMLDivElement>(null)
  const intervalRef = useRef<ReturnType<typeof setInterval> | null>(null)

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
    addLog(`正在连接 ${selectedBrowser === 'playwright' ? 'Playwright' : 'Chrome DevTools'}...`)
    try {
      const res = await api.connectBrowserMCP(selectedBrowser)
      if (res.ok) {
        setStatus('connected')
        addLog('浏览器 MCP 已连接', 'info')
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
    await api.disconnectBrowserMCP()
    setStatus('disconnected')
    setScreenshot(null)
    addLog('浏览器 MCP 已断开')
  }

  const navigate = async () => {
    if (!inputUrl.trim()) return
    const u = inputUrl.trim()
    setUrl(u)
    addLog('导航: ' + u)
    try {
      const res = await api.browserNavigate(u)
      if (res.error) {
        addLog('导航失败: ' + res.error, 'error')
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
    addLog('截图...')
    try {
      const res = await api.browserScreenshot()
      if (res.screenshot) {
        setScreenshot(res.screenshot)
        addLog('截图已更新')
      }
      if (res.error) {
        addLog('截图失败: ' + res.error, 'error')
      }
    } catch (e: any) {
      addLog('截图异常: ' + e.message, 'error')
    }
  }

  const getSnapshot = async () => {
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
      }
    } catch (e: any) {
      addLog('获取内容异常: ' + e.message, 'error')
    }
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

  // 检查连接状态
  useEffect(() => {
    const check = async () => {
      const st = await api.checkBrowserMCPStatus()
      if (st === 'connected') {
        setStatus('connected')
      } else if (st === 'installed') {
        setStatus('disconnected')
      } else {
        setStatus('disconnected')
      }
      await loadAvailableBrowsers()
    }
    if (show) check()
  }, [show])

  if (!show) return null

  return (
    <div className="modal-mask" onClick={(e) => e.target === e.currentTarget && setShow(false)}>
      <div className="modal browser-panel">
        <h3>
          🌐 {t('内置浏览器', 'Browser')}
          <button className="x" onClick={() => setShow(false)}>✕</button>
        </h3>

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
                onChange={e => setSelectedBrowser(e.target.value)}
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
          {status === 'connected' && (
            <button className="btn small" onClick={disconnect}>断开</button>
          )}
          {status === 'connected' && (
            <label className="auto-refresh">
              <input type="checkbox" checked={autoRefresh} onChange={e => setAutoRefresh(e.target.checked)} />
              自动截图
            </label>
          )}
        </div>

        {/* 地址栏 */}
        {status === 'connected' && (
          <div className="browser-url-bar">
            <input
              type="text"
              value={inputUrl}
              onChange={e => setInputUrl(e.target.value)}
              onKeyDown={e => e.key === 'Enter' && navigate()}
              placeholder="输入网址，按回车导航..."
            />
            <button onClick={navigate} title={t('导航', 'Navigate')}>→</button>
            <button onClick={takeScreenshot} title={t('截图', 'Screenshot')}>📸</button>
            <button onClick={getSnapshot} title={t('提取内容', 'Get Content')}>📄</button>
          </div>
        )}

        {/* 截图预览 */}
        <div className="browser-preview" ref={screenshotRef}>
          {screenshot ? (
            <img src={screenshot} alt="Browser screenshot" onClick={() => setScreenshot(null)} />
          ) : (
            <div className="browser-preview-placeholder">
              {status === 'connected'
                ? t('点击"📸截图"预览页面', 'Click "📸Screenshot" to preview')
                : t('未连接浏览器', 'Browser not connected')}
            </div>
          )}
        </div>

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
      </div>
    </div>
  )
}
