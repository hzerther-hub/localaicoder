// MCP 服务器管理面板（对齐 Tk ui_panel_mcp.py）：列表/增删改/启停/只读/重连。
// 另含「浏览器 MCP」：下拉选 Chrome DevTools / Playwright，一键安装并自动连接。
import { useEffect, useState } from 'react'
import { api, onEvent } from '../bridge'
import { useStore, t } from '../store'
import ThinkingDots from './ThinkingDots'

interface BrowserOption { type: string; name: string; description: string; installed: boolean }

export default function MCPPanel() {
  const show = useStore((s) => s.showMCPPanel)
  const setShow = useStore((s) => s.setShowMCPPanel)
  const [servers, setServers] = useState<Record<string, any>>({})
  const [log, setLog] = useState<string[]>([])
  const [name, setName] = useState('')
  const [command, setCommand] = useState('')
  const [url, setURL] = useState('')
  // 浏览器 MCP 安装区
  const [browsers, setBrowsers] = useState<BrowserOption[]>([])
  const [browserType, setBrowserType] = useState('chrome-devtools')
  const [browserStatus, setBrowserStatus] = useState('not_installed')
  const [installing, setInstalling] = useState(false)
  const [browserMsg, setBrowserMsg] = useState('')

  const reload = () => {
    api.getMCPServers().then((d) => setServers(d.servers || {}))
  }
  const loadBrowsers = () => {
    api.getAvailableBrowsers().then((b) => setBrowsers(b || []))
    api.checkBrowserMCPStatus().then((s) => setBrowserStatus(s || 'not_installed'))
  }
  useEffect(() => {
    if (!show) return
    reload()
    loadBrowsers()
    const offLog = onEvent('mcp:log', (d) => setLog((p) => [...p.slice(-30), d.line]))
    // 连接/断开/重连事件触发即时刷新浏览器状态
    const offRe = onEvent('mcp:reconnected', () => loadBrowsers())
    // 面板打开期间轮询，让「✅ 已连接」徽标实时反映真实连接状态
    const timer = setInterval(loadBrowsers, 2500)
    return () => { offLog(); offRe(); clearInterval(timer) }
  }, [show])

  // 安装浏览器 MCP → 成功自动连接 → 刷新状态
  const doInstallBrowser = async () => {
    if (installing) return
    setInstalling(true); setBrowserMsg('')
    try {
      const r = await api.installBrowserMCP(browserType)
      if (!r?.ok) {
        setBrowserMsg(r?.error || r?.output || t('安装失败', 'Install failed'))
        return
      }
      // 装完自动连接（复用 ConnectBrowserMCP 的配置写入 + 进程启动）
      const c = await api.connectBrowserMCP(browserType)
      setBrowserMsg(c?.ok
        ? t('浏览器 MCP 已安装并连接', 'Browser MCP installed & connected')
        : t('已安装，连接失败: ', 'Installed, connect failed: ') + (c?.error || ''))
    } catch (e: any) {
      setBrowserMsg(t('安装异常: ', 'Install error: ') + (e?.message || ''))
    } finally {
      loadBrowsers()
      setInstalling(false)
    }
  }
  if (!show) return null

  const save = () => {
    if (!name.trim()) return
    const cfg: Record<string, any> = { enabled: true }
    if (url.trim()) cfg.url = url.trim()
    else if (command.trim()) {
      const parts = command.trim().split(/\s+/)
      cfg.command = parts[0]
      cfg.args = parts.slice(1)
    } else return
    api.saveMCPServer(name.trim(), cfg)
    setName(''); setCommand(''); setURL('')
  }

  return (
    <div className="modal-mask" onClick={(e) => e.target === e.currentTarget && setShow(false)}>
      <div className="modal">
        <h3>
          🔌 {t('MCP 服务器', 'MCP Servers')}
          <span style={{ display: 'flex', gap: 8 }}>
            <button className="btn" onClick={() => api.reconnectMCP()}>↻ {t('重连', 'Reconnect')}</button>
            <button className="x" onClick={() => setShow(false)}>✕</button>
          </span>
        </h3>

        {Object.entries(servers).map(([n, cfg]: [string, any]) => (
          <div className="mcp-row" key={n}>
            <span className={cfg.enabled ? 'dot on' : 'dot'} />
            <div style={{ minWidth: 0 }}>
              <div style={{ fontSize: 13 }}>{n} <span style={{ color: 'var(--text-faint)', fontSize: 11 }}>{cfg.readonly ? '· 只读' : ''}</span></div>
              <div className="mcp-cmd">{cfg.url || [cfg.command, ...(cfg.args || [])].join(' ')}</div>
            </div>
            <div style={{ marginLeft: 'auto', display: 'flex', gap: 4 }}>
              <button
                className="tb-btn" style={{ padding: '2px 8px' }}
                onClick={() => api.saveMCPServer(n, { ...cfg, enabled: !cfg.enabled })}
              >{cfg.enabled ? t('禁用', 'Off') : t('启用', 'On')}</button>
              <button
                className="tb-btn" style={{ padding: '2px 8px' }}
                onClick={() => api.saveMCPServer(n, { ...cfg, readonly: !cfg.readonly })}
                title={t('只读服务器的工具无需审批', 'Readonly server tools need no approval')}
              >🔒</button>
              <button
                className="tb-btn" style={{ padding: '2px 8px', color: 'var(--red)' }}
                onClick={() => api.deleteMCPServer(n)}
              >✕</button>
            </div>
          </div>
        ))}

        <div style={{ borderTop: '1px solid var(--border)', margin: '12px 0' }} />

        {/* 浏览器 MCP：下拉选后端，一键安装 + 自动连接（对应 🌐 内置浏览器面板） */}
        <div className="field">
          <label>🌐 {t('浏览器 MCP', 'Browser MCP')}</label>
          <div style={{ display: 'flex', gap: 8, alignItems: 'center', flexWrap: 'wrap' }}>
            <select value={browserType} onChange={(e) => setBrowserType(e.target.value)} style={{ flex: 1, minWidth: 160 }}>
              {browsers.map((b) => (
                <option key={b.type} value={b.type}>{b.name}</option>
              ))}
            </select>
            <span className={`status-badge ${browserStatus}`}>
              {browserStatus === 'connected' ? '✅ ' + t('已连接', 'Connected') :
               browserStatus === 'installed' ? '● ' + t('已安装', 'Installed') :
               '⚪ ' + t('未安装', 'Not installed')}
            </span>
            <button className="btn primary" disabled={installing} onClick={doInstallBrowser}
              title={t('全局安装所选浏览器 MCP 并连接', 'Install selected browser MCP globally & connect')}>
              {installing ? <ThinkingDots className="sm" /> : '⚡'} {t('安装并连接', 'Install & Connect')}
            </button>
          </div>
          {browsers.find((b) => b.type === browserType)?.installed && !installing && (
            <div className="hint">{t('已装，可直接在 🌐 浏览器面板连接', 'Installed; connect in Browser panel')}</div>
          )}
          {browserMsg && <div className="hint">{browserMsg}</div>}
          <div className="mcp-cmd" style={{ marginTop: 4 }}>
            {browsers.find((b) => b.type === browserType)?.description || ''}
          </div>
        </div>

        <div style={{ borderTop: '1px solid var(--border)', margin: '10px 0' }} />
        <div className="field-row">
          <div className="field">
            <label>{t('名称', 'Name')}</label>
            <input value={name} onChange={(e) => setName(e.target.value)} placeholder="filesystem" />
          </div>
          <div className="field">
            <label>URL（streamable HTTP，可选）</label>
            <input value={url} onChange={(e) => setURL(e.target.value)} placeholder="http://127.0.0.1:9000/mcp" />
          </div>
        </div>
        <div className="field">
          <label>{t('命令（stdio；URL 为空时生效）', 'Command (stdio; used when URL empty)')}</label>
          <input value={command} onChange={(e) => setCommand(e.target.value)}
            placeholder="npx -y @modelcontextprotocol/server-filesystem D:\docs" />
        </div>
        <div style={{ display: 'flex', justifyContent: 'flex-end' }}>
          <button className="btn primary" onClick={save}>＋ {t('添加', 'Add')}</button>
        </div>

        {log.length > 0 && (
          <pre className="mcp-log">{log.join('\n')}</pre>
        )}
      </div>
    </div>
  )
}
