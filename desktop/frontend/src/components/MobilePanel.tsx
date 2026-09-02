// 移动端远程控制面板：
// 左：手机扫码连接（LAN 中继 + 二维码 + SSE 流式；写工具远端一律拒绝）
// 右：Bot 渠道 —— 企业微信推送（webhook）/ 飞书机器人（长连接双向）；Lark/Telegram 占位。
import { useEffect, useRef, useState, type ReactNode } from 'react'
import { api } from '../bridge'
import { useStore, t } from '../store'

type MobileStatus = { running: boolean; connected: boolean; url: string; qr: string }

export default function MobilePanel() {
  const show = useStore((s) => s.showMobilePanel)
  const setShow = useStore((s) => s.setShowMobilePanel)
  const notice = useStore((s) => s.notice)
  const [st, setSt] = useState<MobileStatus>({ running: false, connected: false, url: '', qr: '' })
  const [weComUrl, setWeComUrl] = useState('')
  const [feishu, setFeishu] = useState({ app_id: '', app_secret: '', allowlist: '' })
  const [feishuRunning, setFeishuRunning] = useState(false)
  const [lark, setLark] = useState({ app_id: '', app_secret: '', allowlist: '' })
  const [larkRunning, setLarkRunning] = useState(false)
  const [tg, setTg] = useState({ bot_token: '', allowlist: '' })
  const [tgRunning, setTgRunning] = useState(false)
  const [relay, setRelay] = useState({ server_url: '', device_token: '', running: false, connected: false, phone_url: '', qr: '', error: '', fs_enabled: false, fs_safe: true })
  const [expanded, setExpanded] = useState('') // 展开配置的渠道：'' | 'wecom' | 'feishu'
  const started = useRef(false) // 面板打开期间只自动启动一次

  useEffect(() => {
    if (!show) return
    api.mobileStatus().then((s) => {
      setSt(s)
      // 首次打开自动启动中继（扫码即用）
      if (!s.running && !started.current) {
        started.current = true
        api.mobileStart().then((r) => r?.ok && setSt((p) => ({ ...p, running: true, url: r.url || '', qr: r.qr || '' })))
      }
    })
    api.getWeComWebhook().then(setWeComUrl)
    api.getFeishuConfig().then((c) => setFeishu({ app_id: c.app_id || '', app_secret: c.app_secret || '', allowlist: c.allowlist || '' }))
    api.feishuStatus().then((s) => setFeishuRunning(!!s.running))
    api.getLarkConfig().then((c) => { if (c) setLark({ app_id: c.app_id || '', app_secret: c.app_secret || '', allowlist: c.allowlist || '' }) })
    api.larkStatus().then((s) => setLarkRunning(!!s.running))
    api.getTelegramConfig().then((c) => { if (c) setTg({ bot_token: c.bot_token || '', allowlist: c.allowlist || '' }) })
    api.telegramStatus().then((s) => setTgRunning(!!s.running))
    api.getRelayConfig().then((c) => { if (c) setRelay((p) => ({ ...p, server_url: c.server_url || '', device_token: c.device_token || '', fs_enabled: !!c.fs_enabled, fs_safe: c.fs_safe !== false })) })
    api.relayStatus().then((s) => setRelay((p) => ({ ...p, running: !!s.running, connected: !!s.connected, phone_url: s.phone_url || '', qr: s.qr || '', error: s.error || '' })))
    const id = setInterval(() => {
      api.mobileStatus().then(setSt)
      api.feishuStatus().then((s) => setFeishuRunning(!!s.running))
      api.larkStatus().then((s) => setLarkRunning(!!s.running))
      api.telegramStatus().then((s) => setTgRunning(!!s.running))
      api.relayStatus().then((s) => setRelay((p) => ({ ...p, running: !!s.running, connected: !!s.connected, phone_url: s.phone_url || '', qr: s.qr || '', error: s.error || '' })))
    }, 2500)
    return () => clearInterval(id)
  }, [show])
  if (!show) return null

  const copyLink = async () => {
    // 优先复制中继（跨网）链接；未配置时复制局域网链接
    const link = relay.phone_url || st.url
    try {
      await navigator.clipboard.writeText(link)
      notice(t('链接已复制', 'Link copied'))
    } catch {
      prompt(t('复制失败，请手动复制', 'Copy failed, copy manually'), link)
    }
  }

  const saveWecom = async () => {
    await api.saveWeComWebhook(weComUrl.trim())
    notice(weComUrl.trim() ? t('企业微信 webhook 已保存，任务结果将推送到群里', 'WeCom webhook saved') : t('已清空 webhook', 'Webhook cleared'))
  }

  const connectFeishu = async () => {
    const r = await api.feishuConnect(feishu.app_id.trim(), feishu.app_secret.trim(), feishu.allowlist.trim())
    setFeishuRunning(!!r.ok)
    notice(r.msg || (r.ok ? t('已连接', 'Connected') : t('连接失败', 'Failed')))
  }

  const botCard = (icon: string, name: string, badge: string, desc: string, key: string, body?: ReactNode) => (
    <div className="bot-card" key={key}>
      <div className="bot-head" onClick={() => setExpanded(expanded === key ? '' : key)}>
        <span className="bot-icon">{icon}</span>
        <span className="bot-meta">
          <span className="bot-name">{name}{badge && <i className="bot-badge">{badge}</i>}</span>
          <span className="bot-desc">{desc}</span>
        </span>
        <span className="bot-go">{expanded === key ? t('收起', 'Collapse') : t('去 Bot Channels 配置', 'Configure')}</span>
      </div>
      {expanded === key && body}
    </div>
  )

  return (
    <div className="modal-mask" onClick={(e) => e.target === e.currentTarget && setShow(false)}>
      <div className="modal mobile-modal">
        <h3>
          📱 {t('移动端远程控制', 'Mobile remote control')}
          <button className="x" onClick={() => setShow(false)}>✕</button>
        </h3>
        <div style={{ fontSize: 12.5, color: 'var(--text-dim)', marginBottom: 14 }}>
          {t('扫码或在手机上打开链接，即可远程控制当前工作区。',
            'Scan or open the link on your phone to control this workspace remotely.')}
        </div>
        {/* 自建中继（跨网）：桌面出站连你的服务器，手机任意网络可用 */}
        <div className="mobile-card" style={{ marginBottom: 14 }}>
          <div className="card-title">🌐 {t('自建中继（跨网）', 'Relay (cross-network)')}</div>
          <div className="card-sub">
            {t('连你自己架设的中继服务器，手机在外网也能控制本机（无需公网 IP）。见 docs/relay。',
              'Connect your own relay server for internet access (no public IP needed). See docs/relay.')}
          </div>
          <div className="bot-cfg">
            <input value={relay.server_url} placeholder={t('服务器地址（如 wss://relay.example.com）', 'Server URL (e.g. wss://relay.example.com)')}
              onChange={(e) => setRelay({ ...relay, server_url: e.target.value })} />
            <div className="cfg-row">
              <input value={relay.device_token} placeholder={t('device token（粘贴到服务器白名单）', 'device token (paste into server allowlist)')}
                onChange={(e) => setRelay({ ...relay, device_token: e.target.value })} />
              <button className="btn" title={t('生成 64 位随机 token', 'Generate a 64-char token')}
                onClick={async () => setRelay({ ...relay, device_token: await api.relayGenToken() })}>
                {t('生成', 'Generate')}
              </button>
            </div>
            <div className="cfg-row">
              {relay.running ? (
                <button className="btn" onClick={async () => { await api.relayDisconnect(); setRelay({ ...relay, running: false }); notice(t('中继已断开', 'Relay disconnected')) }}>
                  ⏹ {t('断开', 'Disconnect')}
                </button>
              ) : (
                <button className="btn primary" onClick={async () => {
                  const r = await api.relayConnect(relay.server_url.trim(), relay.device_token.trim())
                  setRelay({ ...relay, running: !!r.ok })
                  notice(r.msg || (r.ok ? t('已连接', 'Connected') : t('连接失败', 'Failed')))
                }}>
                  🔗 {t('连接', 'Connect')}
                </button>
              )}
              <span className="conn-state" style={{ flex: 1 }}>
                {relay.running ? (relay.connected ? t('中继已连接', 'Relay connected') : t('重连中…', 'Reconnecting…')) : t('中继未连接', 'Relay disconnected')}
              </span>
              <span className={`st-dot${relay.running && relay.connected ? ' on' : ''}`} />
            </div>
            {relay.running && !relay.connected && (
              <div className="cfg-hint" style={{ color: 'var(--amber)' }}>⚠ {relay.error || t('连接中…', 'Connecting…')}</div>
            )}
            {/* WEB 端文件浏览/编辑开关：开启后手机控制台出现「📁 文件」面板（浏览/编辑/保存） */}
            <label style={{ display: 'flex', alignItems: 'flex-start', cursor: 'pointer', marginTop: 8, gap: 8 }}>
              <input
                type="checkbox"
                checked={relay.fs_enabled}
                onChange={async (e) => {
                  const on = e.target.checked
                  setRelay({ ...relay, fs_enabled: on })
                  await api.relaySetFsEnabled(on)
                  notice(on ? t('已允许 WEB 端浏览/编辑文件', 'WEB file access enabled') : t('已关闭 WEB 端文件访问', 'WEB file access disabled'))
                }}
                style={{ marginTop: 2, width: 'auto', flex: 'none' }}
              />
              <span>
                📂 {t('允许 WEB 端浏览 / 编辑文件', 'Allow WEB file browse / edit')}
                <span className="cfg-hint" style={{ display: 'block', color: 'var(--red, #e5484d)' }}>
                  ⚠ {t('关闭下方安全目录时，手机可读写电脑任意文件；token 泄露即全盘暴露。', 'With safe-mode off the phone can read/write ANY file; leaked token = full disk.')}
                </span>
              </span>
            </label>
            {/* 安全目录模式：把手机端文件访问限制在当前项目目录内（推荐常开） */}
            <label className="cfg-row" style={{ alignItems: 'flex-start', cursor: 'pointer', marginTop: 8, gap: 8, opacity: relay.fs_enabled ? 1 : 0.45 }}>
              <input
                type="checkbox"
                checked={relay.fs_safe}
                disabled={!relay.fs_enabled}
                onChange={async (e) => {
                  const on = e.target.checked
                  setRelay({ ...relay, fs_safe: on })
                  await api.relaySetFsSafe(on)
                  notice(on ? t('安全目录模式：手机仅可访问当前项目', 'Safe mode: workspace only') : t('已放开：手机可访问任意路径', 'Safe mode off: any path allowed'))
                }}
                style={{ marginTop: 2, width: 'auto', flex: 'none' }}
              />
              <span>
                🔒 {t('安全目录模式（仅当前项目目录）', 'Safe mode (current workspace only)')}
                <span className="cfg-hint" style={{ display: 'block' }}>
                  {t('开启后手机只能浏览/编辑当前项目内的文件；要开发其他目录，先在手机上「设为项目」。', 'Phone can only browse/edit inside the current workspace; use "set as project" on the phone to switch.')}
                </span>
              </span>
            </label>
          </div>
        </div>
        <div className="mobile-grid">
          {/* 左：扫码连接 */}
          <div className="mobile-card">
            <div className="card-title">⌛ {t('手机扫码连接', 'Scan to connect')}</div>
            <div className="card-sub">{t('用手机相机扫码，在手机上打开这个工作区。', 'Scan with your phone camera to open this workspace.')}</div>
            <div className="conn-row">
              <span className="conn-state">{st.running ? (st.connected ? t('已连接', 'Connected') : t('等待手机连接…', 'Waiting for phone…')) : t('服务未启动', 'Service stopped')}</span>
              <span className={`st-dot${st.connected ? ' on' : ''}`} />
              {st.running ? (
                <button className="btn" onClick={async () => { await api.mobileStop(); setSt({ running: false, connected: false, url: '', qr: '' }) }}>⏹ {t('停止', 'Stop')}</button>
              ) : (
                <button className="btn primary" onClick={async () => { const r = await api.mobileStart(); if (r?.ok) setSt({ running: true, connected: false, url: r.url || '', qr: r.qr || '' }) }}>
                  ▶ {t('启动服务', 'Start')}
                </button>
              )}
            </div>
            <div className="conn-hint">
              {t('无法扫码？可以在手机上打开链接。', "Can't scan? Open the link on your phone instead.")}
              <button className="btn" onClick={async () => { const r = await api.mobileStart(); if (r?.ok) setSt({ running: true, connected: false, url: r.url || '', qr: r.qr || '' }) }}>⟳ {t('刷新二维码', 'Refresh QR')}</button>
              <button className="btn" onClick={copyLink}>⧉ {t('复制链接', 'Copy link')}</button>
            </div>
            <div className="qr-box">
              {relay.phone_url ? (
                <img src={relay.qr} alt="QR" />
              ) : st.running ? (
                st.qr ? <img src={st.qr} alt="QR" /> : <span className="qr-fallback">{st.url}</span>
              ) : (
                <span className="qr-fallback">{t('服务未启动', 'Service stopped')}</span>
              )}
            </div>
            {relay.phone_url ? (
              <div className="cfg-hint" style={{ textAlign: 'center' }}>
                {t('跨网扫码：', 'Cross-network scan:')} <b style={{ color: 'var(--green)', fontFamily: 'var(--mono)' }}>{relay.phone_url.replace(/^https:\/\//, '').replace(/^http:\/\//, '')}</b>
              </div>
            ) : (
              <div className="cfg-hint" style={{ textAlign: 'center' }}>
                {t('当前为局域网扫码（同 WiFi 可用）；配置自建中继后此码变为跨网链接。', 'LAN QR (same WiFi); configure relay above for a cross-network link.')}
              </div>
            )}
          </div>
          {/* 右：Bot 渠道 */}
          <div className="mobile-card">
            <div className="card-title">🤖 {t('使用 Bot Channel', 'Bot channels')}</div>
            <div className="card-sub">{t('连接聊天 Bot，适合更长时间的移动端访问。', 'Chat bots for longer mobile sessions.')}</div>
            <div className="bot-list">
              {botCard('💬', t('微信', 'WeCom'), '', t('企业微信群机器人：推送任务结果到群里。', 'Push task results to a WeCom group.'), 'wecom',
                <div className="bot-cfg">
                  <input value={weComUrl} placeholder="https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=…"
                    onChange={(e) => setWeComUrl(e.target.value)} />
                  <div className="cfg-row">
                    <button className="btn primary" onClick={saveWecom}>{t('保存', 'Save')}</button>
                    <button className="btn" onClick={async () => { const r = await api.testWeComWebhook(); notice(r.msg) }}>{t('发送测试', 'Send test')}</button>
                  </div>
                  <div className="cfg-hint">{t('群设置 → 添加群机器人 → 复制 webhook 地址。', 'In a WeCom group: add a group robot, paste its webhook URL here.')}</div>
                </div>)}
              {botCard('🐦', t('飞书', 'Feishu'), t('中国', 'CN'), t('从飞书会话打开这个工作区。', 'Open this workspace from Feishu chats.'), 'feishu',
                <div className="bot-cfg">
                  <input value={feishu.app_id} placeholder="App ID（cli_…）" onChange={(e) => setFeishu({ ...feishu, app_id: e.target.value })} />
                  <input value={feishu.app_secret} type="password" placeholder="App Secret" onChange={(e) => setFeishu({ ...feishu, app_secret: e.target.value })} />
                  <input value={feishu.allowlist} placeholder={t('白名单 open_id（可选，逗号分隔）', 'Allowlist open_ids (optional, comma-separated)')}
                    onChange={(e) => setFeishu({ ...feishu, allowlist: e.target.value })} />
                  <div className="cfg-row">
                    {feishuRunning ? (
                      <button className="btn" onClick={async () => { await api.feishuDisconnect(); setFeishuRunning(false); notice(t('已断开', 'Disconnected')) }}>⏹ {t('断开', 'Disconnect')}</button>
                    ) : (
                      <button className="btn primary" onClick={connectFeishu}>🔗 {t('连接', 'Connect')}</button>
                    )}
                  </div>
                  <div className="cfg-hint">{t('开放平台建自建应用 → 开启机器人能力 → 事件订阅选「长连接」→ 订阅 im.message.receive_v1。', 'Create a custom app, enable bot, pick long-connection events, subscribe im.message.receive_v1.')}</div>
                </div>)}
              {botCard('🕊️', 'Lark', t('全球', 'Global'), t('从 Lark 会话打开这个工作区。', 'Open this workspace from Lark chats.'), 'lark',
                <div className="bot-cfg">
                  <input value={lark.app_id} placeholder="App ID（cli_…）" onChange={(e) => setLark({ ...lark, app_id: e.target.value })} />
                  <input value={lark.app_secret} type="password" placeholder="App Secret" onChange={(e) => setLark({ ...lark, app_secret: e.target.value })} />
                  <input value={lark.allowlist} placeholder={t('白名单 open_id（可选，逗号分隔）', 'Allowlist open_ids (optional, comma-separated)')}
                    onChange={(e) => setLark({ ...lark, allowlist: e.target.value })} />
                  <div className="cfg-row">
                    {larkRunning ? (
                      <button className="btn" onClick={async () => { await api.larkDisconnect(); setLarkRunning(false); notice(t('已断开', 'Disconnected')) }}>⏹ {t('断开', 'Disconnect')}</button>
                    ) : (
                      <button className="btn primary" onClick={async () => {
                        const r = await api.larkConnect(lark.app_id.trim(), lark.app_secret.trim(), lark.allowlist.trim())
                        setLarkRunning(!!r.ok); notice(r.msg || (r.ok ? t('已连接', 'Connected') : t('连接失败', 'Failed')))
                      }}>🔗 {t('连接', 'Connect')}</button>
                    )}
                    {larkRunning && <button className="btn" onClick={() => setLarkRunning(false)}>{t('同步迁移到飞书配置', 'Mirror Feishu')}</button>}
                  </div>
                  <div className="cfg-hint">{t('Lark 开放平台建应用（open.larksuite.com），事件订阅选「长连接」。', 'Create an app at open.larksuite.com, pick long-connection events.')}</div>
                </div>)}
              {botCard('✈️', 'Telegram', '', t('从 Telegram 打开这个工作区。', 'Open this workspace from Telegram.'), 'telegram',
                <div className="bot-cfg">
                  <input value={tg.bot_token} placeholder="Bot Token（找 @BotFather 申请）" onChange={(e) => setTg({ ...tg, bot_token: e.target.value })} />
                  <input value={tg.allowlist} placeholder={t('白名单 id/@username（可选，逗号分隔）', 'Allowlist ids/@username (optional, comma-separated)')}
                    onChange={(e) => setTg({ ...tg, allowlist: e.target.value })} />
                  <div className="cfg-row">
                    {tgRunning ? (
                      <button className="btn" onClick={async () => { await api.telegramDisconnect(); setTgRunning(false); notice(t('已断开', 'Disconnected')) }}>⏹ {t('断开', 'Disconnect')}</button>
                    ) : (
                      <button className="btn primary" onClick={async () => {
                        const r = await api.telegramConnect(tg.bot_token.trim(), tg.allowlist.trim())
                        setTgRunning(!!r.ok); notice(r.msg || (r.ok ? t('已连接', 'Connected') : t('连接失败', 'Failed')))
                      }}>🔗 {t('连接', 'Connect')}</button>
                    )}
                  </div>
                  <div className="cfg-hint">{t('长轮询模式，无需公网/证书；国内网络需自备代理（HTTPS_PROXY）。', 'Long-polling, no public URL needed; in CN set HTTPS_PROXY.')}</div>
                </div>)}
            </div>
            <button className="btn bot-manage" onClick={() => notice(t('机器人管理（多渠道统一管理）即将上线', 'Bot management coming soon'))}>
              🛠 {t('机器人管理', 'Manage bots')}
            </button>
          </div>
        </div>
      </div>
    </div>
  )
}
