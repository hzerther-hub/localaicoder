// MCP 服务器管理面板（对齐 Tk ui_panel_mcp.py）：列表/增删改/启停/只读/重连。
import { useEffect, useState } from 'react'
import { api, onEvent } from '../bridge'
import { useStore, t } from '../store'

export default function MCPPanel() {
  const show = useStore((s) => s.showMCPPanel)
  const setShow = useStore((s) => s.setShowMCPPanel)
  const [servers, setServers] = useState<Record<string, any>>({})
  const [log, setLog] = useState<string[]>([])
  const [name, setName] = useState('')
  const [command, setCommand] = useState('')
  const [url, setURL] = useState('')

  const reload = () => {
    api.getMCPServers().then((d) => setServers(d.servers || {}))
  }
  useEffect(() => {
    if (!show) return
    reload()
    const off = onEvent('mcp:log', (d) => setLog((p) => [...p.slice(-30), d.line]))
    return off
  }, [show])
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
