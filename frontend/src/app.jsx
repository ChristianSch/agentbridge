import { Fragment, h, render } from 'preact'
import { useEffect, useRef, useState } from 'preact/hooks'
import { Terminal } from 'xterm'
import { FitAddon } from 'xterm-addon-fit'
import snarkdown from 'snarkdown'
import 'xterm/css/xterm.css'
import './style.css'

const token = new URLSearchParams(location.search).get('token') || localStorage.agentbridgeToken || ''
if (token) localStorage.agentbridgeToken = token

function authHeaders(extra = {}) { return { ...extra, ...(token ? { Authorization: `Bearer ${token}` } : {}) } }
async function api(path, opts = {}) { const res = await fetch(path, { ...opts, headers: authHeaders(opts.headers) }); if (!res.ok) throw new Error(await res.text()); return res.status === 204 ? null : res.json() }
function sortSessions(list) { return [...list].sort((a, b) => new Date(b.last_active || b.created_at) - new Date(a.last_active || a.created_at)) }

function App() {
  const [sessions, setSessions] = useState([])
  const [active, setActive] = useState(null)
  const [messages, setMessages] = useState({})
  const [flowKind, setFlowKind] = useState(null)
  const [socketState, setSocketState] = useState('connecting')
  const [detectInfo, setDetectInfo] = useState(null)
  const ws = useRef(null)
  const subscribed = useRef(new Set())
  const activeSession = sessions.find(s => s.id === active)
  function selectSession(id) { if (id) localStorage.agentbridgeActiveSession = id; setActive(id) }

  async function refresh() {
    const list = await api('/api/sessions')
    setSessions(sortSessions(list))
    setActive(cur => cur || (list.find(s => s.id === localStorage.agentbridgeActiveSession)?.id) || list[0]?.id || null)
  }

  function subscribe(id) {
    if (!id || subscribed.current.has(id) || ws.current?.readyState !== WebSocket.OPEN) return
    ws.current.send(JSON.stringify({ subscribe: id }))
    subscribed.current.add(id)
  }

  function addMessage(sessionId, msg) {
    setMessages(prev => {
      const list = prev[sessionId] || []
      if (msg.type === 'assistant_delta' || msg.type === 'thinking_delta') {
        const targetType = msg.type === 'assistant_delta' ? 'assistant' : 'thinking'
        const last = list[list.length - 1]
        if (last?.type === targetType) return { ...prev, [sessionId]: [...list.slice(0, -1), { ...last, text: last.text + msg.text }] }
        return { ...prev, [sessionId]: [...list, { type: targetType, text: msg.text }] }
      }
      if (msg.type === 'tool_delta') {
        const last = list[list.length - 1]
        if (last?.type === 'tool' && last.title === msg.title) return { ...prev, [sessionId]: [...list.slice(0, -1), { ...last, text: msg.text || last.text }] }
        return { ...prev, [sessionId]: [...list, { type: 'tool', title: msg.title, text: msg.text }] }
      }
      return { ...prev, [sessionId]: [...list, msg] }
    })
  }

  useEffect(() => { refresh().catch(console.error); api('/api/detect').then(setDetectInfo).catch(console.error) }, [])
  useEffect(() => {
    const url = `${location.protocol === 'https:' ? 'wss' : 'ws'}://${location.host}/ws${token ? `?token=${encodeURIComponent(token)}` : ''}`
    const sock = new WebSocket(url)
    ws.current = sock
    sock.onopen = () => { setSocketState('connected'); setSessions(list => { list.forEach(s => subscribe(s.id)); return list }) }
    sock.onerror = () => setSocketState('error')
    sock.onclose = () => setSocketState('closed')
    sock.onmessage = e => { const ev = JSON.parse(e.data); if (!ev.session_id) { if (ev.event === 'error') addMessage(active || sessions[0]?.id, { type: 'system', text: ev.content || 'WebSocket error' }); return } const msg = normalizeEvent(ev); if (msg) addMessage(ev.session_id, msg); if (ev.event === 'state_change') setSessions(list => sortSessions(list.map(s => s.id === ev.session_id ? { ...s, state: ev.state, last_active: new Date().toISOString() } : s))) }
    return () => sock.close()
  }, [])
  useEffect(() => { sessions.forEach(s => subscribe(s.id)) }, [sessions.length])

  async function create(req) {
    const s = await api('/api/sessions', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(req) })
    setSessions(prev => sortSessions([s, ...prev])); selectSession(s.id); setFlowKind(null); setTimeout(() => subscribe(s.id), 0)
  }

  function send(action, text = '', extra = {}, sessionId = active) { if (!sessionId || !ws.current) return; ws.current.send(JSON.stringify({ action, session_id: sessionId, text, ...extra })) }
  async function renameSession(id, name) { const s = await api(`/api/sessions/${encodeURIComponent(id)}`, { method: 'PATCH', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ name }) }); setSessions(list => sortSessions(list.map(x => x.id === id ? s : x))) }
  async function deleteSession(id) { await api(`/api/sessions/${encodeURIComponent(id)}`, { method: 'DELETE' }); subscribed.current.delete(id); setMessages(prev => { const next = { ...prev }; delete next[id]; return next }); const list = sortSessions(await api('/api/sessions')); setSessions(list); setActive(cur => cur === id ? (list.find(s => s.id === localStorage.agentbridgeActiveSession)?.id || list[0]?.id || null) : cur) }

  return <div class="app">
    <aside class="sidebar"><div class="brand">AgentBridge <small>{socketState}</small></div><div class="create"><button class="new-session" onClick={() => setFlowKind('pi')}>＋ New session</button></div>{detectInfo && !detectInfo.hermes_profiles?.length && <div class="notice">Hermes not detected</div>}<div class="sessions">{sessions.map(s => <SessionItem session={s} active={s.id === active} onSelect={() => selectSession(s.id)} onRename={renameSession} onDelete={deleteSession} />)}</div></aside>
    <main class="pane">{!activeSession && <Empty onNew={setFlowKind} />}{activeSession?.kind === 'terminal' && <TerminalPane sessionId={activeSession.id} />}{activeSession && activeSession.kind !== 'terminal' && <AgentChat session={activeSession} messages={messages[active] || []} onSend={(action, text, extra) => send(action, text, extra, activeSession.id)} />}</main>
    {flowKind && <NewSessionFlow initialKind={flowKind} hermesAvailable={!!detectInfo?.hermes_profiles?.length} onCancel={() => setFlowKind(null)} onCreate={create} />}
  </div>
}

function SessionItem({ session, active, onSelect, onRename, onDelete }) {
  const [menu, setMenu] = useState(false)
  async function rename() { const name = prompt('Rename session', session.name); if (name && name.trim() && name !== session.name) await onRename(session.id, name.trim()) }
  async function remove() { if (confirm(`Delete ${session.name}? This stops the process and removes it from AgentBridge.`)) await onDelete(session.id) }
  return <div class={`session-wrap ${active ? 'active' : ''}`}><button class="session" onClick={onSelect}><span>{session.name}</span><small>{session.kind} · {session.state}</small></button><button class="session-menu-button" title="Session actions" onClick={() => setMenu(v => !v)}>⋯</button>{menu && <div class="session-menu"><button onClick={() => { setMenu(false); rename() }}>Rename</button><button onClick={() => { setMenu(false); remove() }}>Delete</button></div>}</div>
}

function Empty({ onNew }) { return <div class="empty"><h1>No session selected</h1><p>Start with the defaults, tune paths only if you need to.</p><button onClick={() => onNew('pi')}>New session</button></div> }

function NewSessionFlow({ initialKind, hermesAvailable, onCancel, onCreate }) {
  const [kind, setKind] = useState(initialKind || 'pi'), [info, setInfo] = useState(null), [browse, setBrowse] = useState(null), [cwd, setCwd] = useState(''), [name, setName] = useState(''), [resumeID, setResumeID] = useState(''), [error, setError] = useState(''), [advanced, setAdvanced] = useState(false)
  useEffect(() => { api('/api/detect').then(d => { setInfo(d); pickDefaults(kind, d) }).catch(e => setError(String(e))) }, [])
  useEffect(() => { if (info) pickDefaults(kind, info) }, [kind])
  useEffect(() => { const onKey = e => { if (e.key === 'Escape') onCancel() }; window.addEventListener('keydown', onKey); return () => window.removeEventListener('keydown', onKey) }, [])
  function pickDefaults(k, d) { if (k === 'hermes' && !d.hermes_profiles?.length) setError('Hermes not detected. Pick Pi or browse to a Hermes repo in advanced options.'); else setError(''); const initial = k === 'hermes' ? (d.hermes_profiles?.[0]?.path || d.cwd || d.home) : (d.projects?.find(p => p.agent === k)?.path || d.cwd || d.home); setCwd(initial); setName(k === 'terminal' ? 'shell' : (initial?.split('/').filter(Boolean).pop() || k)); api('/api/browse?path=' + encodeURIComponent(initial)).then(setBrowse).catch(() => {}) }
  async function open(path) { try { setError(''); const b = await api('/api/browse?path=' + encodeURIComponent(path || cwd || '/')); setBrowse(b); setCwd(b.path); if (!name || name === kind || name === 'shell') setName(b.path.split('/').filter(Boolean).pop() || kind) } catch (e) { setError(String(e)) } }
  async function submit(e) { e.preventDefault(); setError(''); try { await onCreate({ kind, cwd, name, resume_id: resumeID }) } catch (err) { setError(String(err).trim()) } }
  return <div class="modal-backdrop" onClick={onCancel}><form class="modal" onClick={e => e.stopPropagation()} onSubmit={submit}>
    <header><div><strong>New session</strong><small>Choose an agent and get moving.</small></div><button type="button" class="ghost" onClick={onCancel}>Esc</button></header>
    {error && <div class="error">{error}</div>}
    <div class="kind-picker"><button type="button" class={kind === 'pi' ? 'selected' : ''} onClick={() => setKind('pi')}>Pi<small>coding agent</small></button><button type="button" disabled={!hermesAvailable} class={kind === 'hermes' ? 'selected' : ''} onClick={() => setKind('hermes')}>Hermes<small>{hermesAvailable ? 'local gateway' : 'not detected'}</small></button><button type="button" class={kind === 'terminal' ? 'selected' : ''} onClick={() => setKind('terminal')}>Term<small>shell</small></button></div>
    <div class="quick-fields"><label>Name<input value={name} onInput={e => setName(e.currentTarget.value)} /></label><label>Directory<input value={cwd} onInput={e => setCwd(e.currentTarget.value)} /></label></div>
    <details class="advanced" open={advanced} onToggle={e => setAdvanced(e.currentTarget.open)}><summary>Advanced options</summary>{kind === 'hermes' && <><div class="chips">{(info?.hermes_profiles || []).map(p => <button type="button" onClick={() => open(p.path)}>{p.name}</button>)}</div><label>Resume session key<input value={resumeID} onInput={e => setResumeID(e.currentTarget.value)} placeholder="optional" /></label></>}<div class="current-path">{browse?.path || cwd}</div><div class="browser"><button type="button" title="Parent directory" onClick={() => open(browse?.parent || cwd)}>../</button>{(browse?.dirs || []).map(d => <button type="button" title={d.path} onClick={() => open(d.path)}>{d.name}</button>)}</div></details>
    <footer><button type="button" class="ghost" onClick={onCancel}>Cancel</button><button class="primary">Create {kind}</button></footer>
  </form></div>
}

const bridgeCommands = [
  { name: '/abort', hint: 'Stop the current turn', action: 'abort' },
  { name: '/compact', hint: 'Ask the agent to compress context', action: 'compact' },
  { name: '/steer', hint: 'Add guidance while a turn is running', action: 'steer', takesText: true },
  { name: '/follow-up', hint: 'Queue a follow-up prompt', action: 'follow_up', takesText: true },
]

function AgentChat({ session, messages, onSend }) {
  const [text, setText] = useState(''), [visibleCount, setVisibleCount] = useState(300), [showCommands, setShowCommands] = useState(false), bottom = useRef(null)
  useEffect(() => { setText(''); setVisibleCount(300); setShowCommands(false) }, [session.id])
  useEffect(() => bottom.current?.scrollIntoView({ block: 'end' }), [messages.length])
  const canSend = session.state !== 'starting' && session.state !== 'error' && session.state !== 'exited'
  const grouped = groupMessages(messages)
  const hidden = Math.max(0, grouped.length - visibleCount)
  const visible = hidden ? grouped.slice(hidden) : grouped
  const slashOpen = showCommands || text.startsWith('/')
  function submit(e) { e.preventDefault(); if (!text.trim() || !canSend) return; const cmd = parseBridgeCommand(text); if (cmd) onSend(cmd.action, cmd.text); else onSend('prompt', text); setText(''); setShowCommands(false) }
  return <><header class="top"><div><strong>{session.name}</strong><span>{session.kind} · {session.state}</span></div><code>{session.id}</code></header><section class="messages">{hidden > 0 && <button class="older" onClick={() => setVisibleCount(n => n + 250)}>Show {Math.min(250, hidden)} older items ({hidden} hidden)</button>}{visible.map((m, i) => <Message key={`${hidden}-${i}`} msg={m} onSend={onSend} />)}<div ref={bottom} /></section><form class="composer" onSubmit={submit}>{slashOpen && <CommandMenu text={text} onPick={cmd => { setText(cmd.takesText ? `${cmd.name} ` : cmd.name); setShowCommands(false) }} />}<button class="command-trigger" type="button" title="Commands" onClick={() => setShowCommands(v => !v)}>/</button><input value={text} onInput={e => setText(e.currentTarget.value)} placeholder={`Message ${session.kind}. Type / for commands.`} /><button class="send" disabled={!canSend}>Send</button></form></>
}
function CommandMenu({ text, onPick }) { const q = text.startsWith('/') ? text.split(/\s+/)[0].toLowerCase() : ''; const items = bridgeCommands.filter(c => !q || c.name.startsWith(q)); return <div class="command-menu"><div class="command-help">Bridge commands. Unknown slash commands are sent through to the agent.</div>{items.map(cmd => <button type="button" onClick={() => onPick(cmd)}><span>{cmd.name}</span><small>{cmd.hint}</small></button>)}</div> }
function parseBridgeCommand(text) { const trimmed = text.trim(); const [name, ...rest] = trimmed.split(/\s+/); const cmd = bridgeCommands.find(c => c.name === name.toLowerCase()); if (!cmd) return null; return { action: cmd.action, text: cmd.takesText ? rest.join(' ') : '' } }
function Message({ msg, onSend }) { if (msg.type === 'activity') return <ActivityGroup msg={msg} />; if (msg.type === 'history') return <div class="msg history">{msg.text}</div>; if (msg.type === 'thinking') return <details class="msg thinking"><summary>Thinking</summary><pre>{msg.text}</pre></details>; if (msg.type === 'tool') return <details class="msg tool"><summary>{msg.title}</summary><pre>{msg.text}</pre></details>; if (msg.type === 'approval') return <div class="msg approval"><strong>Approval needed</strong><p>{msg.text}</p><div class="approval-actions"><button onClick={() => onSend('approve', '', { request_id: msg.requestId, approved: true })}>Allow</button><button onClick={() => onSend('approve', '', { request_id: msg.requestId, approved: false })}>Deny</button></div></div>; if (msg.type === 'assistant') return <div class="msg assistant markdown" dangerouslySetInnerHTML={{ __html: renderMarkdown(msg.text) }} />; return <div class={`msg ${msg.type}`}>{msg.text}</div> }
function ActivityGroup({ msg }) { const thoughts = msg.items.filter(i => i.type === 'thinking').length, tools = msg.items.filter(i => i.type === 'tool').length; return <details class="msg activity"><summary>Activity{thoughts ? ` · ${thoughts} thinking` : ''}{tools ? ` · ${tools} tool${tools === 1 ? '' : 's'}` : ''}</summary>{msg.items.map((item, i) => <div class={`activity-item ${item.type}`} key={i}><strong>{item.type === 'tool' ? item.title : 'Thinking'}</strong><pre>{item.text}</pre></div>)}</details> }
function groupMessages(messages) { const out = []; for (const msg of messages) { if (msg.type === 'thinking' || msg.type === 'tool') { const last = out[out.length - 1]; if (last?.type === 'activity') last.items.push(msg); else out.push({ type: 'activity', items: [msg] }); } else out.push(msg); } return out }
function renderMarkdown(text) { return snarkdown(text || '') }
function TerminalPane({ sessionId }) { const ref = useRef(null); useEffect(() => { const term = new Terminal({ fontSize: 14, cursorBlink: true, theme: { background: '#15171d' } }); const fit = new FitAddon(); term.loadAddon(fit); term.open(ref.current); fit.fit(); const ws = new WebSocket(`${location.protocol === 'https:' ? 'wss' : 'ws'}://${location.host}/ws/term/${sessionId}${token ? `?token=${encodeURIComponent(token)}` : ''}`); ws.binaryType = 'arraybuffer'; ws.onmessage = e => { if (typeof e.data !== 'string') term.write(new Uint8Array(e.data)) }; ws.onopen = () => resize(term, ws, fit); term.onData(data => ws.readyState === WebSocket.OPEN && ws.send(data)); const onResize = () => resize(term, ws, fit); window.addEventListener('resize', onResize); const timer = setTimeout(onResize, 50); return () => { clearTimeout(timer); window.removeEventListener('resize', onResize); ws.close(); term.dispose() } }, [sessionId]); return <div class="terminal" ref={ref} /> }
function resize(term, ws, fit) { fit.fit(); if (ws.readyState === WebSocket.OPEN) ws.send(JSON.stringify({ type: 'resize', cols: term.cols, rows: term.rows })) }
function normalizeEvent(ev) { if (ev.event === 'state_change' || ev.event === 'response' || ev.event === 'message_start' || ev.event === 'message_end' || ev.event === 'message_update') return null; if (ev.event === 'history_source') return { type: 'history', text: ev.content || 'Loaded conversation history' }; if (ev.event === 'user_message') return { type: 'user', text: ev.content || '' }; if (ev.event === 'delta') return { type: 'assistant_delta', text: ev.content || '' }; if (ev.event === 'thinking_delta') return { type: 'thinking_delta', text: ev.content || '' }; if (ev.event === 'tool_delta') return { type: 'tool_delta', title: `tool: ${ev.tool || 'running'}`, text: ev.output || ev.content || '' }; if (ev.event === 'tool_start') return { type: 'tool', title: `tool: ${ev.tool || 'start'}`, text: JSON.stringify(ev.args || ev.raw || {}, null, 2) }; if (ev.event === 'tool_end') return { type: 'tool', title: `tool complete: ${ev.tool || ''}`, text: ev.output || JSON.stringify(ev.raw || {}, null, 2) }; if (ev.event === 'approval_request') return { type: 'approval', requestId: ev.request_id, text: ev.prompt || JSON.stringify(ev.raw || {}) }; if (ev.event === 'error' || ev.event === 'stderr') return { type: 'system', text: `${ev.event}: ${ev.content || ''}` }; return null }
render(<App />, document.getElementById('app'))
