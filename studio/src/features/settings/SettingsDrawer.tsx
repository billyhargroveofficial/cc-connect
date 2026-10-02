import { useEffect, useId, useRef, useState, type FormEvent, type ReactNode } from 'react'
import { Archive, ArrowLeft, Check, Clock3, Copy, Download, FileText, FolderOpen, Laptop, LoaderCircle, Monitor, Plus, RefreshCw, Search, Shield, Trash2, Wrench, X } from 'lucide-react'
import type { Bot, Capabilities, Maintenance, MaintenanceRun, NodeEnrollment, NodeInfo, Skill } from '../../lib/types'
import { api } from '../../lib/api'
import { isWorking,telegramLabel } from '../../lib/events'
import { AnimatePresence, LayoutGroup, m, useIsPresent, useReducedMotion, fadeUp, controlMotion, motionSpring, motionTransition } from '../../lib/motion'
import { BotFields, botPayload, draftFromBot, errorMessage, MarkdownEditor, ModalShell, Notice, RuntimeFields, SaveButton, type BotDraft } from './shared'

interface SettingsDrawerProps {
  bot: Bot | null
  bots?: Bot[]
  capabilities: Capabilities | null
  onClose: () => void
  onBotChange: (bot: Bot) => void
  onArchive: (id: string) => void
  initialTab?: string
  global?: boolean
  nodes?: NodeInfo[]
  activeNode?: NodeInfo
  onSelectNode?: (id: string) => void
  onCreateNodeEnrollment?: (name: string) => Promise<NodeEnrollment>
  onRemoveNode?: (id: string) => Promise<void>
  onRefreshNodes?: (options?: { background?: boolean }) => Promise<unknown>
  nodeBinaryURL?: (platform: 'darwin', arch: 'arm64' | 'amd64') => string
}

export function SettingsDrawer({ bot, bots = [], capabilities, onClose, onBotChange, onArchive, initialTab, global = false, ...nodeProps }: SettingsDrawerProps) {
  if (!global && !bot) return null
  return <ModalShell title={global ? 'Shared settings' : bot!.name}
    subtitle={global ? 'Instructions, skills, and connected hosts.' : "Your bot's workspace."}
    onClose={onClose} drawer={!global} wide={global}>
    <SettingsContent key={global ? 'shared' : bot!.id} bot={bot} bots={bots} capabilities={capabilities}
      onClose={onClose} onBotChange={onBotChange} onArchive={onArchive} initialTab={initialTab} global={global} {...nodeProps} />
  </ModalShell>
}

export function BotSettingsPanel({ bot, capabilities, onClose, onBotChange, onArchive }: {
  bot: Bot; capabilities: Capabilities | null; onClose: () => void;
  onBotChange: (bot: Bot) => void; onArchive: (id: string) => void;
}) {
  const present = useIsPresent()
  return <div className="cb-settings-embedded" inert={!present}>
    <header className="cb-settings-header">
      <div><h2>{bot.name}</h2><p>Your bot's workspace.</p></div>
      <m.button {...controlMotion} type="button" className="cb-settings-icon-button" onClick={onClose} aria-label="Close bot settings"><X size={20} /></m.button>
    </header>
    <SettingsContent key={bot.id} bot={bot} capabilities={capabilities} onClose={onClose}
      onBotChange={onBotChange} onArchive={onArchive} />
  </div>
}

function SettingsContent({ bot, bots = [], capabilities, onClose, onBotChange, onArchive, initialTab, global = false, ...nodeProps }: SettingsDrawerProps) {
  const tabs = global
    ? [{ id: 'instructions', title: 'Instructions' }, { id: 'skills', title: 'Skills' }, { id: 'maintenance', title: 'Maintenance' }, { id: 'hosts', title: 'Hosts' }]
    : [{ id: 'profile', title: 'Profile' }, { id: 'instructions', title: 'Instructions' }, { id: 'skills', title: 'Skills' }]
  const [tab, setTab] = useState(() => tabs.some((entry) => entry.id === initialTab) ? initialTab! : tabs[0].id)
  const tabsId = useId()
  const scopeId = global ? undefined : bot?.id
  return <>
    <LayoutGroup id={tabsId}><nav className="cb-settings-tabs" role="tablist" aria-label="Settings sections">
      {tabs.map((entry) => <m.button key={entry.id} type="button" role="tab" aria-selected={tab === entry.id}
        aria-controls={`${tabsId}-tab-${entry.id}`} id={`${tabsId}-tab-button-${entry.id}`}
        whileTap={{ scale: 0.97 }} transition={motionSpring.control}
        className={tab === entry.id ? 'is-active' : ''} onClick={() => setTab(entry.id)}>{entry.title}
        {tab === entry.id && <m.span className="cb-settings-tab-indicator" layoutId="settings-active-tab" transition={motionSpring.control} />}
      </m.button>)}
    </nav></LayoutGroup>
    <AnimatePresence initial={false} mode="wait">
    <SettingsTabPanel key={tab} id={`${tabsId}-tab-${tab}`} labelledBy={`${tabsId}-tab-button-${tab}`}>
      {tab === 'profile' && bot && <ProfilePane key={bot.id} bot={bot} capabilities={capabilities} onBotChange={onBotChange}
        onArchive={(id) => { onArchive(id); onClose() }} />}
      {tab === 'instructions' && <InstructionsPane key={scopeId ?? 'shared'} id={scopeId}
        bot={global ? null : bot} onBotChange={onBotChange} />}
      {tab === 'skills' && <SkillsPane key={scopeId ?? 'shared'} bot={global ? null : bot} onBotChange={onBotChange} />}
      {tab === 'maintenance' && <MaintenancePane capabilities={capabilities} bots={bots} />}
      {tab === 'hosts' && <HostsPane {...nodeProps} />}
    </SettingsTabPanel>
    </AnimatePresence>
  </>
}

type HostPaneProps = Pick<SettingsDrawerProps, 'nodes' | 'activeNode' | 'onSelectNode' | 'onCreateNodeEnrollment' | 'onRemoveNode' | 'onRefreshNodes' | 'nodeBinaryURL'>

export function nodePairCommand(enrollment: NodeEnrollment, fallbackUrl: string): string {
  const url = enrollment.serverUrl || fallbackUrl
  const quotedUrl = `'${url.replace(/'/g, "'\\''")}'`
  return `./connect-bots-node pair --server ${quotedUrl}${url.startsWith('http:') ? ' --allow-insecure' : ''}`
}

export function HostsPane({ nodes = [], activeNode, onSelectNode, onCreateNodeEnrollment, onRemoveNode, onRefreshNodes, nodeBinaryURL }: HostPaneProps) {
  const [adding, setAdding] = useState(false)
  const [name, setName] = useState('')
  const [enrollment, setEnrollment] = useState<NodeEnrollment | null>(null)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [copied, setCopied] = useState('')
  const [removing, setRemoving] = useState<string | null>(null)
  const [now, setNow] = useState(Date.now())
  const lifecycle = useRef({ active: true, epoch: 0 })
  const present = useIsPresent()
  lifecycle.current.active = present
  useEffect(() => {
    lifecycle.current.active = true
    return () => { lifecycle.current.active = false; lifecycle.current.epoch++ }
  }, [])
  useEffect(() => {
    if (!enrollment || !present) return
    const timer = window.setInterval(() => setNow(Date.now()), 1000)
    return () => window.clearInterval(timer)
  }, [enrollment, present])
  const paired = enrollment ? nodes.find(node => node.id === enrollment.nodeId && node.online) : null
  const expiresAt = enrollment ? Date.parse(enrollment.expiresAt) : 0
  const expired = Boolean(enrollment && (!Number.isFinite(expiresAt) || expiresAt <= now))
  useEffect(() => {
    if (!enrollment || paired || expired || !present || !onRefreshNodes) return
    let refreshing = false
    const refresh = async () => {
      if (refreshing) return
      refreshing = true
      try { await onRefreshNodes({ background: true }) } catch { /* A transient reconnect is reflected in host status. */ }
      finally { refreshing = false }
    }
    void refresh()
    const timer = window.setInterval(() => void refresh(), 2500)
    return () => window.clearInterval(timer)
  }, [enrollment, paired, expired, present, onRefreshNodes])
  useEffect(() => {
    if (!copied) return
    const timer = window.setTimeout(() => setCopied(''), 1800)
    return () => window.clearTimeout(timer)
  }, [copied])
  async function add(event: FormEvent) {
    event.preventDefault()
    if (busy || !name.trim() || !onCreateNodeEnrollment) return
    const epoch = ++lifecycle.current.epoch
    setBusy(true); setError('')
    try {
      const result = await onCreateNodeEnrollment(name.trim())
      if (!lifecycle.current.active || epoch !== lifecycle.current.epoch) return
      setEnrollment(result); setNow(Date.now())
    } catch (cause) { if (lifecycle.current.active && epoch === lifecycle.current.epoch) setError(errorMessage(cause)) }
    finally { if (lifecycle.current.active && epoch === lifecycle.current.epoch) setBusy(false) }
  }
  async function remove(id: string) {
    if (busy || !onRemoveNode) return
    const epoch = ++lifecycle.current.epoch
    setBusy(true); setError('')
    try {
      await onRemoveNode(id)
      if (!lifecycle.current.active || epoch !== lifecycle.current.epoch) return
      setRemoving(null)
    } catch (cause) { if (lifecycle.current.active && epoch === lifecycle.current.epoch) setError(errorMessage(cause)) }
    finally { if (lifecycle.current.active && epoch === lifecycle.current.epoch) setBusy(false) }
  }
  async function copy(value: string, kind: string) {
    try { await navigator.clipboard.writeText(value); if (lifecycle.current.active) { setCopied(kind); setError('') } }
    catch { if (lifecycle.current.active) setError('Copy is unavailable here. Select and copy the text below.') }
  }
  function reset() { lifecycle.current.epoch++; setAdding(false); setEnrollment(null); setError(''); setCopied(''); setBusy(false) }
  const command = enrollment ? nodePairCommand(enrollment, typeof window === 'undefined' ? '' : window.location.origin) : ''
  const remaining = enrollment ? Math.max(0, Math.ceil((expiresAt - now) / 60000)) : 0
  return <div className="cb-settings-form cb-hosts-pane">
    <div className="cb-settings-scroll">
      <AnimatePresence initial={false} mode="wait">
      {adding ? <SettingsView key="pair-host">
        <m.button {...controlMotion} type="button" className="cb-settings-text-button cb-settings-back" onClick={reset} disabled={busy}>
          <ArrowLeft size={15} />All hosts
        </m.button>
        {!enrollment ? <form className="cb-host-add-form" onSubmit={add}>
          <div className="cb-host-pair-heading"><Laptop size={25} /><h3>Add your computer</h3><p>Bots run with the Codex account and files on this host.</p></div>
          <label className="cb-settings-field">Host name<input value={name} onChange={event => setName(event.target.value)}
            autoFocus placeholder="My MacBook" required maxLength={80} autoComplete="off" disabled={busy} /></label>
          <SaveButton busy={busy} disabled={!name.trim() || !onCreateNodeEnrollment}>Continue</SaveButton>
        </form> : paired ? <div className="cb-host-pair-success" role="status">
          <span className="cb-host-success-icon"><Check size={24} /></span><h3>{paired.name} is connected</h3>
          <p>You can now create bots and use this computer's Codex.</p>
          <m.button {...controlMotion} type="button" className="cb-settings-button cb-settings-button--primary"
            onClick={() => { onSelectNode?.(paired.id); reset() }}>Use this host</m.button>
        </div> : expired ? <div className="cb-host-pair-success" role="status"><Clock3 size={25} /><h3>Pairing code expired</h3>
          <p>Generate a new code to connect your computer.</p><m.button {...controlMotion} type="button" className="cb-settings-button"
            onClick={() => { setEnrollment(null); setError('') }}>Try again</m.button>
        </div> : <div className="cb-host-pair-steps">
          <div className="cb-host-pair-heading"><Laptop size={25} /><h3>Connect {name.trim()}</h3>
            <p>Open Terminal on your Mac and follow these three steps.</p></div>
          <section className="cb-host-pair-step"><span className="cb-host-step-number">1</span><div><h4>Download the node</h4>
            <div className="cb-host-downloads">{(['arm64', 'amd64'] as const).map(arch => <m.a key={arch} {...controlMotion}
              className="cb-settings-button" href={nodeBinaryURL?.('darwin', arch)} download="connect-bots-node"
              aria-disabled={!nodeBinaryURL || undefined}><Download size={14} />{arch === 'arm64' ? 'Apple silicon' : 'Intel Mac'}</m.a>)}</div>
            <p>Make it executable: <code>chmod +x connect-bots-node*</code></p></div>
          </section>
          <section className="cb-host-pair-step"><span className="cb-host-step-number">2</span><div><h4>Run the pairing command</h4>
            <div className="cb-host-copy-block"><code>{command}</code><m.button {...controlMotion} type="button"
              onClick={() => void copy(command, 'command')} aria-label="Copy pairing command" title="Copy command">
              {copied === 'command' ? <Check size={15} /> : <Copy size={15} />}</m.button></div>
            <p>Use the downloaded filename if it includes an architecture suffix.</p></div>
          </section>
          <section className="cb-host-pair-step"><span className="cb-host-step-number">3</span><div><h4>Enter this code when asked</h4>
            <div className="cb-host-pair-code"><input readOnly value={enrollment.code} aria-label="Pairing code" spellCheck={false}
              onFocus={event => event.target.select()} /><m.button {...controlMotion} type="button" onClick={() => void copy(enrollment.code, 'code')}
                aria-label="Copy pairing code" title="Copy code">{copied === 'code' ? <Check size={17} /> : <Copy size={17} />}</m.button></div>
            <p><LoaderCircle size={12} className="cb-settings-spin" />Waiting for your host · expires in {remaining} {remaining === 1 ? 'minute' : 'minutes'}</p>
          </div></section>
        </div>}
      </SettingsView> : <SettingsView key="host-list">
        <div className="cb-hosts-heading"><div><h3>Your hosts</h3><p>Choose where your bots run.</p></div>
          <m.button {...controlMotion} type="button" className="cb-settings-button" onClick={() => { setAdding(true); setName(''); setError('') }}
            disabled={!onCreateNodeEnrollment}><Plus size={15} />Add host</m.button></div>
        <div className="cb-host-list">{nodes.map(node => <m.div key={node.id} className={`cb-host-entry${activeNode?.id === node.id ? ' is-active' : ''}`}
          layout="position" transition={motionSpring.layout}>
          <div className="cb-host-row"><span className="cb-host-icon"><Monitor size={19} /></span>
            <div className="cb-host-info"><strong>{node.name}{node.local && <small>This server</small>}</strong>
              <span>{[node.hostname, node.os || node.platform, node.arch].filter(Boolean).join(' · ') || (node.local ? 'Built-in host' : 'Remote host')}</span>
            </div><span className={`cb-host-online${node.online ? ' is-online' : ''}`}><i />{node.online ? 'Online' : 'Offline'}</span>
            {!node.local && <m.button {...(!busy ? controlMotion : {})} type="button" className="cb-settings-icon-button cb-host-remove"
              onClick={() => { setRemoving(removing === node.id ? null : node.id); setError('') }} aria-label={`Remove ${node.name}`} disabled={busy}><Trash2 size={16} /></m.button>}
          </div>
          {node.error && <p className="cb-host-error">{node.error}</p>}
          <AnimatePresence initial={false}>{removing === node.id && <HostRemovalConfirmation key={node.id} name={node.name} busy={busy}
            onCancel={() => setRemoving(null)} onRemove={() => void remove(node.id)} />}</AnimatePresence>
        </m.div>)}</div>
        {!nodes.length && <p className="cb-settings-empty">Your hosts will appear here.</p>}
      </SettingsView>}
      </AnimatePresence>
      <Notice error={error} />
    </div>
    <footer className="cb-settings-footer"><span className="cb-settings-hint">Remote hosts use their own Codex login and workspace.</span>
      {onRefreshNodes && !adding && <m.button {...(!busy ? controlMotion : {})} type="button" className="cb-settings-icon-button"
        aria-label="Refresh hosts" disabled={busy} onClick={async () => {
          setBusy(true); setError('')
          try { await onRefreshNodes() } catch (cause) { if (lifecycle.current.active) setError(errorMessage(cause)) }
          finally { if (lifecycle.current.active) setBusy(false) }
        }}><RefreshCw size={17} className={busy ? 'cb-settings-spin' : ''} /></m.button>}
    </footer>
  </div>
}

function HostRemovalConfirmation({ name, busy, onCancel, onRemove }: {
  name: string; busy: boolean; onCancel: () => void; onRemove: () => void;
}) {
  const present = useIsPresent()
  const reduced = useReducedMotion()
  const disabled = busy || !present
  return <m.div className="cb-host-remove-confirm" inert={!present} aria-hidden={!present}
    initial={{ height: 0, opacity: 0 }} animate={{ height: 'auto', opacity: 1 }} exit={{ height: 0, opacity: 0 }}
    transition={reduced ? { duration: 0 } : motionTransition.disclosure}>
    <p>Disconnect {name}? Its bots and files stay on that computer.</p><div className="cb-settings-inline-actions">
      <m.button {...(!disabled ? controlMotion : {})} type="button" className="cb-settings-button"
        onClick={() => { if (!disabled) onCancel() }} disabled={disabled}>Cancel</m.button>
      <m.button {...(!disabled ? controlMotion : {})} type="button" className="cb-settings-button cb-settings-button--danger"
        onClick={() => { if (!disabled) onRemove() }} disabled={disabled}>{busy ? <LoaderCircle size={14} className="cb-settings-spin" /> : <Trash2 size={14} />}Remove host</m.button>
    </div>
  </m.div>
}

function SettingsTabPanel({ id, labelledBy, children }: { id: string; labelledBy: string; children: ReactNode }) {
  const present = useIsPresent()
  return <m.div className="cb-settings-tab-content" role="tabpanel" id={id} aria-labelledby={labelledBy}
    inert={!present} variants={fadeUp} initial="hidden" animate="visible" exit="exit">{children}</m.div>
}

function SettingsView({ children }: { children: ReactNode }) {
  const present = useIsPresent()
  return <m.div className="cb-settings-pane-motion" inert={!present}
    variants={fadeUp} initial="hidden" animate="visible" exit="exit">{children}</m.div>
}

function ProfilePane({ bot, capabilities, onBotChange, onArchive }: {
  bot: Bot; capabilities: Capabilities | null; onBotChange: (bot: Bot) => void; onArchive: (id: string) => void;
}) {
  const initialDraft = draftFromBot(bot, capabilities)
  const sourceDraft = useRef(initialDraft)
  const [draft, setDraft] = useState(initialDraft)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [success, setSuccess] = useState('')
  const [confirmArchive, setConfirmArchive] = useState(false)
  useEffect(() => {
    const next = draftFromBot(bot, capabilities)
    const previous = sourceDraft.current
    sourceDraft.current = next
    setDraft((current) => reconcileBotDraft(current, previous, next))
  }, [bot, capabilities])
  async function save(event: FormEvent) {
    event.preventDefault()
    if (busy) return
    setBusy(true); setError(''); setSuccess('')
    try {
      const updated = await api.updateBot(bot.id, botPayload(draft, { includeRole: false }))
      setDraft(draftFromBot(updated, capabilities)); onBotChange(updated); setSuccess('Settings saved.')
    } catch (cause) { setError(errorMessage(cause)) }
    finally { setBusy(false) }
  }
  async function archive() {
    setBusy(true); setError(''); setSuccess('')
    try { await api.archiveBot(bot.id); onArchive(bot.id) }
    catch (cause) { setError(errorMessage(cause)); setBusy(false) }
  }
  return <form onSubmit={save} className="cb-settings-form">
    <div className="cb-settings-scroll">
      <BotFields value={draft} onChange={(next) => { setDraft(next); setSuccess('') }} capabilities={capabilities}
        running={isWorking(bot.status)} includeRole={false} />
      {bot.telegram?.enabled && <div className={`cb-settings-notice cb-settings-connection-status ${bot.telegram.status === 'error' ? 'cb-settings-notice--error' : ''}`}
        role={bot.telegram.status === 'error' ? 'alert' : 'status'}>
        <span>{telegramLabel(bot.telegram)}</span>{bot.telegram.error && <p>{bot.telegram.error}</p>}
      </div>}
      <div className="cb-settings-location"><FolderOpen size={16} /><span>{bot.workDir}</span></div>
      <section className="cb-settings-section cb-settings-archive-section">
        <AnimatePresence initial={false} mode="wait">{confirmArchive ?
          <ArchiveConfirmation key="confirmation" busy={busy} onCancel={() => setConfirmArchive(false)} onArchive={archive} />
        : <m.button {...(!busy ? controlMotion : {})} key="archive" type="button" className="cb-settings-text-button"
          variants={fadeUp} initial="hidden" animate="visible" exit="exit" onClick={() => setConfirmArchive(true)} disabled={busy}>
          <Archive size={15} />Archive bot
        </m.button>}</AnimatePresence>
      </section>
      <Notice error={error} success={success} />
    </div>
    <footer className="cb-settings-footer"><span /><SaveButton busy={busy} disabled={!draft.name.trim()} /></footer>
  </form>
}

function ArchiveConfirmation({ busy, onCancel, onArchive }: { busy: boolean; onCancel: () => void; onArchive: () => void }) {
  const present = useIsPresent()
  const reduced = useReducedMotion()
  return <m.div className="cb-settings-disclosure" inert={!present}
    initial={{ height: 0, opacity: 0 }} animate={{ height: 'auto', opacity: 1 }} exit={{ height: 0, opacity: 0 }}
    transition={reduced ? { duration: 0 } : motionTransition.disclosure}>
    <p>The bot will leave the list. Its history and workspace files will be kept.</p>
    <div className="cb-settings-inline-actions">
      <m.button {...(!busy ? controlMotion : {})} type="button" className="cb-settings-button" onClick={onCancel} disabled={busy || !present}>Cancel</m.button>
      <m.button {...(!busy ? controlMotion : {})} type="button" className="cb-settings-button cb-settings-button--danger" onClick={onArchive} disabled={busy || !present}>
        {busy ? <LoaderCircle size={15} className="cb-settings-spin" /> : <Archive size={15} />}Archive
      </m.button>
    </div>
  </m.div>
}

export function reconcileBotDraft(current: BotDraft, previous: BotDraft, next: BotDraft): BotDraft {
  let changed = false
  const merged = { ...current }
  const runtimeKeys = ['backend', 'model', 'effort'] as const
  const runtimeDirty = runtimeKeys.some((key) => current[key] !== previous[key])
  if (!runtimeDirty) {
    for (const key of runtimeKeys) {
      if (current[key] !== next[key]) {
        merged[key] = next[key]
        changed = true
      }
    }
  }
  const independentKeys = (Object.keys(next) as (keyof BotDraft)[])
    .filter((key) => !runtimeKeys.includes(key as typeof runtimeKeys[number]))
  for (const key of independentKeys) {
    if (current[key] === previous[key] && current[key] !== next[key]) {
      Object.assign(merged, { [key]: next[key] })
      changed = true
    }
  }
  return changed ? merged : current
}

function InstructionsPane({ id, bot, onBotChange }: { id?: string; bot?: Bot | null; onBotChange?: (bot: Bot) => void }) {
  const sourceRole = useRef(bot?.role ?? '')
  const [role, setRole] = useState(bot?.role ?? '')
  const [savedRole, setSavedRole] = useState(bot?.role ?? '')
  const [content, setContent] = useState('')
  const [saved, setSaved] = useState('')
  const [path, setPath] = useState('')
  const [loading, setLoading] = useState(true)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [success, setSuccess] = useState('')
  useEffect(() => {
    const next = bot?.role ?? ''
    const previous = sourceRole.current
    sourceRole.current = next
    setRole(current => current === previous ? next : current)
    setSavedRole(next)
  }, [bot?.role])
  useEffect(() => {
    let alive = true
    api.instructions(id).then((result) => {
      if (!alive) return
      setContent(result.content); setSaved(result.content); setPath(result.path)
    }).catch((cause) => { if (alive) setError(errorMessage(cause)) })
      .finally(() => { if (alive) setLoading(false) })
    return () => { alive = false }
  }, [id])
  async function save(event: FormEvent) {
    event.preventDefault()
    if (busy) return
    setBusy(true); setError(''); setSuccess('')
    try {
      if (bot && role.trim() !== savedRole) {
        const updated = await api.updateBot(bot.id, { role: role.trim() })
        sourceRole.current = updated.role
        setRole(updated.role); setSavedRole(updated.role); onBotChange?.(updated)
      }
      if (content !== saved) {
        const result = await api.saveInstructions(id, content)
        setContent(result.content); setSaved(result.content); setPath(result.path)
      }
      setSuccess('Instructions saved.')
    } catch (cause) { setError(errorMessage(cause)) }
    finally { setBusy(false) }
  }
  return <form onSubmit={save} className="cb-settings-form">
    <div className="cb-settings-scroll">
      {bot && <label className="cb-settings-field cb-settings-purpose">Purpose
        <textarea value={role} onChange={event => { setRole(event.target.value); setSuccess('') }}
          rows={2} maxLength={4000} disabled={busy} placeholder="What this bot helps with and the results it owns" />
        <small>A short description of this bot's responsibility.</small>
      </label>}
      <div className="cb-settings-intro"><FileText size={20} />
        <p>{id ? 'AGENTS.md defines detailed working rules for this bot. Shared instructions and user skills are also available.'
          : 'This AGENTS.md adds shared rules to all bots. It is stored in a separate app folder.'}</p>
      </div>
      {loading ? <Loading /> : !error || path ? <MarkdownEditor content={content} onChange={(next) => { setContent(next); setSuccess('') }} readOnly={busy} label="AGENTS.md"
        placeholder="Describe habits, constraints, and working rules…" /> : null}
      {path && <div className="cb-settings-location"><FolderOpen size={15} /><span>{path}</span></div>}
      <Notice error={error} success={success} />
    </div>
    <footer className="cb-settings-footer"><span className="cb-settings-hint">Changes apply to future messages.</span>
      <SaveButton busy={busy} disabled={loading || !path || (content === saved && (!bot || role.trim() === savedRole))} /></footer>
  </form>
}

const scopeNames: Record<string, string> = {
  nativeUser: 'User', productUser: 'Shared', project: 'This bot', system: 'Built-in',
}

function SkillsPane({ bot, onBotChange }: { bot: Bot | null; onBotChange: (bot: Bot) => void }) {
  const [skills, setSkills] = useState<Skill[]>([])
  const [disabled, setDisabled] = useState<string[]>(bot?.disabledSkills ?? [])
  const [loading, setLoading] = useState(true)
  const [busy, setBusy] = useState(false)
  const [query, setQuery] = useState('')
  const [error, setError] = useState('')
  const [success, setSuccess] = useState('')
  const [editing, setEditing] = useState<Skill | null>(null)
  const [creating, setCreating] = useState(false)
  const id = bot?.id
  const backend = bot?.backend
  async function load() {
    const result = await api.skills(id)
    setSkills(result.skills ?? [])
  }
  useEffect(() => {
    let alive = true
    setLoading(true)
    api.skills(id).then((result) => { if (alive) setSkills(result.skills ?? []) })
      .catch((cause) => { if (alive) setError(errorMessage(cause)) })
      .finally(() => { if (alive) setLoading(false) })
    return () => { alive = false }
  }, [id, backend])
  async function save(event: FormEvent) {
    event.preventDefault()
    if (!bot || busy) return
    setBusy(true); setError(''); setSuccess('')
    try {
      const updated = await api.saveDisabledSkills(bot.id, disabled)
      onBotChange(updated); setDisabled(updated.disabledSkills ?? []); await load(); setSuccess('Skills saved.')
    } catch (cause) { setError(errorMessage(cause)) }
    finally { setBusy(false) }
  }
  async function edited() {
    setCreating(false); setEditing(null)
    try { await load() } catch (cause) { setError(errorMessage(cause)) }
  }
  const filtered = skills.filter((skill) => `${skill.name} ${skill.description}`.toLocaleLowerCase().includes(query.toLocaleLowerCase()))
  const changed = bot && [...disabled].sort().join('\n') !== [...(bot.disabledSkills ?? [])].sort().join('\n')
  return <AnimatePresence initial={false} mode="wait">
    {creating || editing ? <SettingsView key="skill-editor">
      <SkillEditor skill={editing} botId={id} onBack={() => { setCreating(false); setEditing(null) }} onSaved={edited} />
    </SettingsView> : <SettingsView key="skill-list"><form onSubmit={save} className="cb-settings-form">
    <div className="cb-settings-scroll">
      <div className="cb-settings-intro"><Shield size={20} /><p>{bot
        ? 'Choose the skills available to this bot. Shared and user skills are inherited; you can also disable them here.'
        : 'Shared skills are available to every bot. Built-in and user skills are also listed here.'}</p></div>
      <div className="cb-settings-skill-toolbar">
        <label className="cb-settings-search"><Search size={16} /><input aria-label="Search skills" placeholder="Find a skill" value={query} onChange={(event) => setQuery(event.target.value)} /></label>
        <m.button {...controlMotion} type="button" className="cb-settings-button" onClick={() => setCreating(true)}><Plus size={16} />Add</m.button>
      </div>
      {loading ? <Loading /> : <div className="cb-settings-skill-list">
        <AnimatePresence initial={false}>
        {filtered.map((skill) => <SkillRow key={skill.id} skill={skill} toggle={!!bot} busy={busy} enabled={!disabled.includes(skill.id)}
          onEdit={() => setEditing(skill)} onEnable={(enabled) => {
            setDisabled((previous) => enabled ? previous.filter((entry) => entry !== skill.id) : [...new Set([...previous, skill.id])]); setSuccess('')
          }} />)}
        </AnimatePresence>
        {filtered.length === 0 && <p className="cb-settings-empty">{query ? 'No skills match this name.' : 'No skills yet. Add your first one.'}</p>}
      </div>}
      <Notice error={error} success={success} />
    </div>
    {bot && <footer className="cb-settings-footer"><span className="cb-settings-hint">This selection only applies to this bot.</span>
      <SaveButton busy={busy} disabled={loading || !changed} /></footer>}
  </form></SettingsView>}
  </AnimatePresence>
}

function SkillRow({ skill, toggle, busy, enabled, onEdit, onEnable }: {
  skill: Skill; toggle: boolean; busy: boolean; enabled: boolean; onEdit: () => void; onEnable: (enabled: boolean) => void;
}) {
  const present = useIsPresent()
  const reduced = useReducedMotion()
  return <m.div className="cb-settings-skill-entry" inert={!present}
    initial={{ height: 0, opacity: 0 }} animate={{ height: 'auto', opacity: 1 }} exit={{ height: 0, opacity: 0 }}
    transition={reduced ? { duration: 0 } : motionTransition.disclosure}>
    <div className="cb-settings-skill-row">
      <m.button whileTap={{ scale: 0.99 }} transition={motionSpring.control} className="cb-settings-skill-info" type="button" onClick={onEdit} disabled={!present}>
        <span><strong>{skill.name}</strong><em>{scopeNames[skill.scope] ?? skill.scope}</em></span>
        <p>{skill.description || 'No description provided.'}</p>
        {!skill.editable && <small>Read-only</small>}
      </m.button>
      {toggle && <label className="cb-settings-switch-target"><input type="checkbox" className="cb-settings-switch" aria-label={`Enable skill ${skill.name}`}
        checked={enabled} disabled={busy || !present} onChange={(event) => onEnable(event.target.checked)} /></label>}
    </div>
  </m.div>
}

function SkillEditor({ skill, botId, onBack, onSaved }: { skill: Skill | null; botId?: string; onBack: () => void; onSaved: () => void }) {
  const [name, setName] = useState('')
  const [description, setDescription] = useState('')
  const [content, setContent] = useState('')
  const [path, setPath] = useState(skill?.path ?? '')
  const [loading, setLoading] = useState(Boolean(skill))
  const [loaded, setLoaded] = useState(!skill)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [saved, setSaved] = useState('')
  const readOnly = skill?.editable === false
  const hasFrontmatter = /^\s*---(?:\r?\n|$)/.test(content)
  useEffect(() => {
    if (!skill) return
    let alive = true
    api.skillContent(skill.path).then((result) => {
      if (!alive) return
      setContent(result.content); setSaved(result.content); setPath(result.path); setLoaded(true)
    }).catch((cause) => { if (alive) setError(errorMessage(cause)) }).finally(() => { if (alive) setLoading(false) })
    return () => { alive = false }
  }, [skill])
  async function save(event: FormEvent) {
    event.preventDefault()
    if (busy || readOnly) return
    setBusy(true); setError('')
    try {
      if (skill) await api.saveSkillContent(skill.path, content, skill.scope === 'project' ? botId : undefined)
      else {
        const source = hasFrontmatter ? content : `---\nname: ${JSON.stringify(name.trim())}\ndescription: ${JSON.stringify(description.trim())}\n---\n\n${content}\n`
        await api.createSkill(botId, name.trim(), source)
      }
      onSaved()
    } catch (cause) { setError(errorMessage(cause)) }
    finally { setBusy(false) }
  }
  return <form onSubmit={save} className="cb-settings-form">
    <div className="cb-settings-scroll">
      <m.button {...controlMotion} type="button" className="cb-settings-text-button cb-settings-back" onClick={onBack}><ArrowLeft size={16} />All skills</m.button>
      <h3 className="cb-settings-pane-heading">{skill?.name ?? 'New skill'}</h3>
      {!skill && <>
        <label className="cb-settings-field">Skill folder name
          <input value={name} onChange={(event) => setName(event.target.value)} placeholder="research-notes" required pattern={'[A-Za-z0-9][A-Za-z0-9_\\-]{0,63}'} maxLength={64} spellCheck={false} />
          <small>{botId ? "The skill will be saved in this bot's folder." : 'The skill will be available to all bots.'}</small>
        </label>
        {!hasFrontmatter && <label className="cb-settings-field">When to use
          <textarea value={description} onChange={(event) => setDescription(event.target.value)} rows={2} required
            placeholder="For example, when collecting notes on a research paper" maxLength={2000} />
        </label>}
        <p className="cb-settings-hint">Describe the workflow. You can also paste an existing SKILL.md with YAML frontmatter.</p>
      </>}
      {loading ? <Loading /> : loaded && <MarkdownEditor label={skill ? 'SKILL.md' : 'Workflow'} content={content} onChange={setContent} readOnly={readOnly || busy}
        placeholder="Describe the skill's purpose and workflow steps…" />}
      {path && <div className="cb-settings-location"><FolderOpen size={15} /><span>{path}</span></div>}
      <Notice error={error} />
    </div>
    {!readOnly && <footer className="cb-settings-footer"><span /><SaveButton busy={busy}
      disabled={loading || !loaded || (skill ? content === saved : !name.trim() || (!hasFrontmatter && !description.trim()))}>{skill ? 'Save' : 'Create skill'}</SaveButton></footer>}
  </form>
}

interface MaintenanceDraft { enabled: boolean; backend: string; model: string; effort: string; retentionHours: number }
function maintenanceDraft(value: Maintenance): MaintenanceDraft {
  return { enabled: Boolean(value.enabled), backend: value.backend ?? 'codex', model: value.model ?? '', effort: value.effort ?? '',
    retentionHours: typeof value.retentionHours === 'number' ? value.retentionHours : 24 }
}

function MaintenancePane({ capabilities, bots }: { capabilities: Capabilities | null; bots: Bot[] }) {
  const [value, setValue] = useState<Maintenance | null>(null)
  const [draft, setDraft] = useState<MaintenanceDraft | null>(null)
  const [loading, setLoading] = useState(true)
  const [busy, setBusy] = useState(false)
  const [running, setRunning] = useState(false)
  const [error, setError] = useState('')
  const [connectionError, setConnectionError] = useState('')
  const [success, setSuccess] = useState('')
  const [expanded, setExpanded] = useState<string | null>(null)
  useEffect(() => {
    let alive = true
    api.maintenance().then((result) => { if (alive) { setValue(result); setDraft(maintenanceDraft(result)); setConnectionError('') } })
      .catch((cause) => { if (alive) setConnectionError(errorMessage(cause)) }).finally(() => { if (alive) setLoading(false) })
    const timer = window.setInterval(() => {
      api.maintenance().then((result) => { if (alive) { setValue(result); setDraft((previous) => previous ?? maintenanceDraft(result)); setConnectionError('') } })
        .catch((cause) => { if (alive) setConnectionError(errorMessage(cause)) })
    }, 5000)
    return () => { alive = false; window.clearInterval(timer) }
  }, [])
  async function save(event: FormEvent) {
    event.preventDefault()
    if (!draft || busy) return
    setBusy(true); setError(''); setSuccess('')
    try {
      const result = await api.saveMaintenance({ ...draft })
      setValue(result); setDraft(maintenanceDraft(result)); setSuccess('Maintenance settings saved.')
    } catch (cause) { setError(errorMessage(cause)) }
    finally { setBusy(false) }
  }
  async function run() {
    if (running || value?.running) return
    setRunning(true); setError(''); setSuccess('')
    try { await api.runMaintenance(); setValue(await api.maintenance()); setSuccess('Inventory started. Reports will appear below.') }
    catch (cause) { setError(errorMessage(cause)) }
    finally { setRunning(false) }
  }
  const reports = value?.reports ?? value?.runs ?? []
  const lastRunAt = typeof value?.lastRunAt === 'string' ? value.lastRunAt : null
  const locked = busy || running || Boolean(value?.running)
  return <form onSubmit={save} className="cb-settings-form">
    <div className="cb-settings-scroll">
      <div className="cb-settings-intro"><Wrench size={20} /><p>A lightweight model takes inventory of workspaces once a day. Old tmp files are removed automatically; reports stay here.</p></div>
      {loading ? <Loading /> : draft && <>
        <label className="cb-settings-toggle-row"><span><strong>Daily inventory</strong><small>Runs in the background while the app server is running.</small></span>
          <input type="checkbox" className="cb-settings-switch" checked={draft.enabled} disabled={locked} onChange={(event) => { setDraft({ ...draft, enabled: event.target.checked }); setSuccess('') }} />
        </label>
        <section className="cb-settings-section"><h3>Inventory harness</h3>
          <RuntimeFields backend={draft.backend} model={draft.model} effort={draft.effort} capabilities={capabilities} disabled={locked} excludedEfforts={['ultra']}
            onChange={(next) => { setDraft({ ...draft, ...next }); setSuccess('') }} />
          <label className="cb-settings-field">Temporary file retention, hours
            <input type="number" min={24} max={8760} step={1} value={draft.retentionHours} disabled={locked}
              onChange={(event) => { setDraft({ ...draft, retentionHours: event.target.valueAsNumber }); setSuccess('') }} required />
          </label>
        </section>
        <section className="cb-settings-section">
          <div className="cb-settings-section-heading"><h3>Recent checks</h3>
            <m.button {...(!running && !value?.running ? controlMotion : {})} type="button" className="cb-settings-button" onClick={run} disabled={running || Boolean(value?.running)}>
              {running || value?.running ? <LoaderCircle size={15} className="cb-settings-spin" /> : <RefreshCw size={15} />}Run
            </m.button>
          </div>
          {lastRunAt && <p className="cb-settings-hint">Last run: {formatDate(lastRunAt)}</p>}
          {reports.length === 0 ? <p className="cb-settings-empty">No inventory runs yet.</p> : <div className="cb-settings-report-list">
            {reports.slice().reverse().map((report, index) => {
              const key = report.id ?? `${report.botId}-${report.startedAt}-${index}`
              return <MaintenanceReport key={key} report={report} botName={bots.find(bot => bot.id === report.botId)?.name} expanded={expanded === key} onToggle={() => setExpanded(expanded === key ? null : key)} />
            })}
          </div>}
        </section>
      </>}
      <Notice error={error || connectionError} success={success} />
    </div>
    <footer className="cb-settings-footer"><span /><SaveButton busy={busy} disabled={loading || locked || !draft || !Number.isFinite(draft.retentionHours)} /></footer>
  </form>
}

function MaintenanceReport({ report, botName, expanded, onToggle }: { report: MaintenanceRun; botName?: string; expanded: boolean; onToggle: () => void }) {
  const reduced = useReducedMotion()
  const failed = report.status === 'failed' || report.status === 'error' || report.status === 'interrupted' || Boolean(report.error)
  const inProgress = report.status === 'running'
  const deferred = report.status === 'deferred'
  const removedFiles = typeof report.removedFiles === 'number' ? report.removedFiles : undefined
  const bytesRemoved = typeof report.bytesRemoved === 'number' ? report.bytesRemoved : undefined
  const inventory = Array.isArray(report.inventory) ? report.inventory.filter((entry): entry is string => typeof entry === 'string') : []
  return <div className={`cb-settings-report ${failed ? 'is-failed' : ''}`}>
    <m.button whileTap={{ scale: 0.995 }} transition={motionSpring.control} type="button" className="cb-settings-report-header" aria-expanded={expanded} onClick={onToggle}>
      {inProgress ? <LoaderCircle size={15} className="cb-settings-spin" /> : failed ? <Wrench size={15} /> : deferred ? <Clock3 size={15} /> : <Check size={15} />}
      <span><strong>{botName ?? report.botName ?? (report.botId ? 'Archived bot' : 'All bots')}</strong><small>{formatDate(report.startedAt ?? report.time ?? '')}</small></span>
      <em>{inProgress ? 'Checking' : failed ? 'Error' : deferred ? 'After response' : 'Done'}</em>
    </m.button>
    <AnimatePresence initial={false}>{expanded && <m.div key="report" className="cb-settings-disclosure"
      initial={{ height: 0, opacity: 0 }} animate={{ height: 'auto', opacity: 1 }} exit={{ height: 0, opacity: 0 }}
      transition={reduced ? { duration: 0 } : motionTransition.disclosure}><div className="cb-settings-report-details">
      {report.summary && <p>{report.summary}</p>}
      {removedFiles !== undefined && <p>Temporary files removed: {removedFiles}{bytesRemoved !== undefined ? ` · ${formatBytes(bytesRemoved)}` : ''}</p>}
      {inventory.length > 0 && <ul>{inventory.map((file) => <li key={file}>{file}</li>)}</ul>}
      {report.error && <p className="cb-settings-report-error">{report.error}</p>}
    </div></m.div>}</AnimatePresence>
  </div>
}

function Loading() { return <div className="cb-settings-loading" role="status"><LoaderCircle size={20} className="cb-settings-spin" />Loading…</div> }
function formatDate(value: string) {
  const date = new Date(value)
  return Number.isNaN(date.getTime()) ? 'Time not specified' : date.toLocaleString('en-GB', { day: 'numeric', month: 'short', hour: '2-digit', minute: '2-digit' })
}
function formatBytes(value: number) { return value < 1024 ? `${value} B` : value < 1024 * 1024 ? `${(value / 1024).toFixed(1)} KB` : `${(value / (1024 * 1024)).toFixed(1)} MB` }
