import { useEffect, useState, type FormEvent } from 'react'
import { Archive, ArrowLeft, Check, Clock3, FileText, FolderOpen, LoaderCircle, Plus, RefreshCw, Search, Shield, Wrench } from 'lucide-react'
import type { Bot, Capabilities, Maintenance, MaintenanceRun, Skill } from '../../lib/types'
import { api } from '../../lib/api'
import { isWorking,telegramLabel } from '../../lib/events'
import { BotFields, botPayload, draftFromBot, errorMessage, MarkdownEditor, ModalShell, Notice, RuntimeFields, SaveButton } from './shared'

interface SettingsDrawerProps {
  bot: Bot | null
  bots?: Bot[]
  capabilities: Capabilities | null
  onClose: () => void
  onBotChange: (bot: Bot) => void
  onArchive: (id: string) => void
  initialTab?: string
  global?: boolean
}

export function SettingsDrawer({ bot, bots = [], capabilities, onClose, onBotChange, onArchive, initialTab, global = false }: SettingsDrawerProps) {
  const tabs = global
    ? [{ id: 'instructions', title: 'Instructions' }, { id: 'skills', title: 'Skills' }, { id: 'maintenance', title: 'Maintenance' }]
    : [{ id: 'profile', title: 'Profile' }, { id: 'instructions', title: 'Instructions' }, { id: 'skills', title: 'Skills' }]
  const [tab, setTab] = useState(() => tabs.some((entry) => entry.id === initialTab) ? initialTab! : tabs[0].id)
  const scopeId = global ? undefined : bot?.id
  if (!global && !bot) return null
  return <ModalShell title={global ? 'Shared settings' : bot!.name}
    subtitle={global ? 'Instructions and skills for all bots.' : "Your bot's workspace."} onClose={onClose} drawer>
    <nav className="cb-settings-tabs" role="tablist" aria-label="Settings sections">
      {tabs.map((entry) => <button key={entry.id} type="button" role="tab" aria-selected={tab === entry.id}
        aria-controls={`cb-settings-tab-${entry.id}`} id={`cb-settings-tab-button-${entry.id}`}
        className={tab === entry.id ? 'is-active' : ''} onClick={() => setTab(entry.id)}>{entry.title}</button>)}
    </nav>
    <div className="cb-settings-tab-content" role="tabpanel" id={`cb-settings-tab-${tab}`}
      aria-labelledby={`cb-settings-tab-button-${tab}`}>
      {tab === 'profile' && bot && <ProfilePane key={bot.id} bot={bot} capabilities={capabilities} onBotChange={onBotChange}
        onArchive={(id) => { onArchive(id); onClose() }} />}
      {tab === 'instructions' && <InstructionsPane key={scopeId ?? 'shared'} id={scopeId} />}
      {tab === 'skills' && <SkillsPane key={scopeId ?? 'shared'} bot={global ? null : bot} onBotChange={onBotChange} />}
      {tab === 'maintenance' && <MaintenancePane capabilities={capabilities} bots={bots} />}
    </div>
  </ModalShell>
}

function ProfilePane({ bot, capabilities, onBotChange, onArchive }: {
  bot: Bot; capabilities: Capabilities | null; onBotChange: (bot: Bot) => void; onArchive: (id: string) => void;
}) {
  const [draft, setDraft] = useState(() => draftFromBot(bot, capabilities))
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [success, setSuccess] = useState('')
  const [confirmArchive, setConfirmArchive] = useState(false)
  async function save(event: FormEvent) {
    event.preventDefault()
    if (busy) return
    setBusy(true); setError(''); setSuccess('')
    try {
      const updated = await api.updateBot(bot.id, botPayload(draft))
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
      <BotFields value={draft} onChange={(next) => { setDraft(next); setSuccess('') }} capabilities={capabilities} running={isWorking(bot.status)} />
      {bot.telegram?.enabled && <div className={`cb-settings-notice cb-settings-connection-status ${bot.telegram.status === 'error' ? 'cb-settings-notice--error' : ''}`}
        role={bot.telegram.status === 'error' ? 'alert' : 'status'}>
        <span>{telegramLabel(bot.telegram)}</span>{bot.telegram.error && <p>{bot.telegram.error}</p>}
      </div>}
      <div className="cb-settings-location"><FolderOpen size={16} /><span>{bot.workDir}</span></div>
      <section className="cb-settings-section cb-settings-archive-section">
        {confirmArchive ? <>
          <p>The bot will leave the list. Its history and workspace files will be kept.</p>
          <div className="cb-settings-inline-actions">
            <button type="button" className="cb-settings-button" onClick={() => setConfirmArchive(false)} disabled={busy}>Cancel</button>
            <button type="button" className="cb-settings-button cb-settings-button--danger" onClick={archive} disabled={busy}>
              {busy ? <LoaderCircle size={15} className="cb-settings-spin" /> : <Archive size={15} />}Archive
            </button>
          </div>
        </> : <button type="button" className="cb-settings-text-button" onClick={() => setConfirmArchive(true)} disabled={busy}>
          <Archive size={15} />Archive bot
        </button>}
      </section>
      <Notice error={error} success={success} />
    </div>
    <footer className="cb-settings-footer"><span /><SaveButton busy={busy} disabled={!draft.name.trim()} /></footer>
  </form>
}

function InstructionsPane({ id }: { id?: string }) {
  const [content, setContent] = useState('')
  const [saved, setSaved] = useState('')
  const [path, setPath] = useState('')
  const [loading, setLoading] = useState(true)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [success, setSuccess] = useState('')
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
      const result = await api.saveInstructions(id, content)
      setContent(result.content); setSaved(result.content); setPath(result.path); setSuccess('Instructions saved.')
    } catch (cause) { setError(errorMessage(cause)) }
    finally { setBusy(false) }
  }
  return <form onSubmit={save} className="cb-settings-form">
    <div className="cb-settings-scroll">
      <div className="cb-settings-intro"><FileText size={20} />
        <p>{id ? 'AGENTS.md defines persistent rules for this bot. Shared instructions and user skills are also available.'
          : 'This AGENTS.md adds shared rules to all bots. It is stored in a separate app folder.'}</p>
      </div>
      {loading ? <Loading /> : !error || path ? <MarkdownEditor content={content} onChange={(next) => { setContent(next); setSuccess('') }} readOnly={busy} label="AGENTS.md"
        placeholder="Describe the role, habits, and working rules…" /> : null}
      {path && <div className="cb-settings-location"><FolderOpen size={15} /><span>{path}</span></div>}
      <Notice error={error} success={success} />
    </div>
    <footer className="cb-settings-footer"><span className="cb-settings-hint">Changes apply to future messages.</span>
      <SaveButton busy={busy} disabled={loading || !path || content === saved} /></footer>
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
  if (creating || editing) return <SkillEditor skill={editing} botId={id} onBack={() => { setCreating(false); setEditing(null) }} onSaved={edited} />
  const filtered = skills.filter((skill) => `${skill.name} ${skill.description}`.toLocaleLowerCase().includes(query.toLocaleLowerCase()))
  const changed = bot && [...disabled].sort().join('\n') !== [...(bot.disabledSkills ?? [])].sort().join('\n')
  return <form onSubmit={save} className="cb-settings-form">
    <div className="cb-settings-scroll">
      <div className="cb-settings-intro"><Shield size={20} /><p>{bot
        ? 'Choose the skills available to this bot. Shared and user skills are inherited; you can also disable them here.'
        : 'Shared skills are available to every bot. Built-in and user skills are also listed here.'}</p></div>
      <div className="cb-settings-skill-toolbar">
        <label className="cb-settings-search"><Search size={16} /><input aria-label="Search skills" placeholder="Find a skill" value={query} onChange={(event) => setQuery(event.target.value)} /></label>
        <button type="button" className="cb-settings-button" onClick={() => setCreating(true)}><Plus size={16} />Add</button>
      </div>
      {loading ? <Loading /> : <div className="cb-settings-skill-list">
        {filtered.map((skill) => <div className="cb-settings-skill-row" key={skill.id}>
          <button className="cb-settings-skill-info" type="button" onClick={() => setEditing(skill)}>
            <span><strong>{skill.name}</strong><em>{scopeNames[skill.scope] ?? skill.scope}</em></span>
            <p>{skill.description || 'No description provided.'}</p>
            {!skill.editable && <small>Read-only</small>}
          </button>
          {bot && <label className="cb-settings-switch-target"><input type="checkbox" className="cb-settings-switch" aria-label={`Enable skill ${skill.name}`}
            checked={!disabled.includes(skill.id)} disabled={busy}
            onChange={(event) => { setDisabled((previous) => event.target.checked ? previous.filter((entry) => entry !== skill.id) : [...new Set([...previous, skill.id])]); setSuccess('') }} /></label>}
        </div>)}
        {filtered.length === 0 && <p className="cb-settings-empty">{query ? 'No skills match this name.' : 'No skills yet. Add your first one.'}</p>}
      </div>}
      <Notice error={error} success={success} />
    </div>
    {bot && <footer className="cb-settings-footer"><span className="cb-settings-hint">This selection only applies to this bot.</span>
      <SaveButton busy={busy} disabled={loading || !changed} /></footer>}
  </form>
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
      if (skill) await api.saveSkillContent(skill.path, content)
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
      <button type="button" className="cb-settings-text-button cb-settings-back" onClick={onBack}><ArrowLeft size={16} />All skills</button>
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
            <button type="button" className="cb-settings-button" onClick={run} disabled={running || Boolean(value?.running)}>
              {running || value?.running ? <LoaderCircle size={15} className="cb-settings-spin" /> : <RefreshCw size={15} />}Run
            </button>
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
  const failed = report.status === 'failed' || report.status === 'error' || report.status === 'interrupted' || Boolean(report.error)
  const inProgress = report.status === 'running'
  const deferred = report.status === 'deferred'
  const removedFiles = typeof report.removedFiles === 'number' ? report.removedFiles : undefined
  const bytesRemoved = typeof report.bytesRemoved === 'number' ? report.bytesRemoved : undefined
  const inventory = Array.isArray(report.inventory) ? report.inventory.filter((entry): entry is string => typeof entry === 'string') : []
  return <div className={`cb-settings-report ${failed ? 'is-failed' : ''}`}>
    <button type="button" className="cb-settings-report-header" aria-expanded={expanded} onClick={onToggle}>
      {inProgress ? <LoaderCircle size={15} className="cb-settings-spin" /> : failed ? <Wrench size={15} /> : deferred ? <Clock3 size={15} /> : <Check size={15} />}
      <span><strong>{botName ?? report.botName ?? (report.botId ? 'Archived bot' : 'All bots')}</strong><small>{formatDate(report.startedAt ?? report.time ?? '')}</small></span>
      <em>{inProgress ? 'Checking' : failed ? 'Error' : deferred ? 'After response' : 'Done'}</em>
    </button>
    {expanded && <div className="cb-settings-report-details">
      {report.summary && <p>{report.summary}</p>}
      {removedFiles !== undefined && <p>Temporary files removed: {removedFiles}{bytesRemoved !== undefined ? ` · ${formatBytes(bytesRemoved)}` : ''}</p>}
      {inventory.length > 0 && <ul>{inventory.map((file) => <li key={file}>{file}</li>)}</ul>}
      {report.error && <p className="cb-settings-report-error">{report.error}</p>}
    </div>}
  </div>
}

function Loading() { return <div className="cb-settings-loading" role="status"><LoaderCircle size={20} className="cb-settings-spin" />Loading…</div> }
function formatDate(value: string) {
  const date = new Date(value)
  return Number.isNaN(date.getTime()) ? 'Time not specified' : date.toLocaleString('en-GB', { day: 'numeric', month: 'short', hour: '2-digit', minute: '2-digit' })
}
function formatBytes(value: number) { return value < 1024 ? `${value} B` : value < 1024 * 1024 ? `${(value / 1024).toFixed(1)} KB` : `${(value / (1024 * 1024)).toFixed(1)} MB` }
