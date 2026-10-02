import { useEffect, useRef, useState, type ReactNode } from 'react'
import { createPortal } from 'react-dom'
import { LoaderCircle, X } from 'lucide-react'
import ReactMarkdown from 'react-markdown'
import remarkGfm from 'remark-gfm'
import type { Bot, Capabilities } from '../../lib/types'
import { avatarStyles } from '../../lib/avatars'
import Avatar from '../../components/Avatar'
import './settings.css'

export const avatarColors = avatarStyles

export interface BotDraft {
  name: string
  role: string
  avatar: string
  chief: boolean
  backend: string
  model: string
  effort: string
  telegramEnabled: boolean
  telegramTokenEnv: string
  telegramAllowedUserIds: string
}

export function draftFromBot(bot?: Bot | null, capabilities?: Capabilities | null): BotDraft {
  const backend = bot?.backend ?? (capabilities?.backends.codex?.available !== false ? 'codex' : 'pi')
  const model = bot?.model ?? capabilities?.models.find((entry) => entry.backend === backend)?.id ?? ''
  const efforts = capabilities?.models.find((entry) => entry.id === model && entry.backend === backend)?.efforts ?? []
  return {
    name: bot?.name ?? '', role: bot?.role ?? '', avatar: bot?.avatar ?? 'lavender',
    chief: bot?.chief ?? false, backend, model,
    effort: bot?.effort ?? (efforts.includes('max') ? 'max' : efforts[0] ?? ''),
    telegramEnabled: bot?.telegram?.enabled ?? false,
    telegramTokenEnv: bot?.telegram?.tokenEnv ?? '',
    telegramAllowedUserIds: bot?.telegram?.allowedUserIds?.join(', ') ?? '',
  }
}

export function botPayload(draft: BotDraft): Partial<Bot> {
  return {
    name: draft.name.trim(), role: draft.role.trim(), avatar: draft.avatar,
    chief: draft.chief, backend: draft.backend, model: draft.model, effort: draft.effort,
    telegram: {
      enabled: draft.telegramEnabled,
      tokenEnv: draft.telegramTokenEnv.trim(),
      allowedUserIds: draft.telegramAllowedUserIds.split(/[\s,;]+/).filter(Boolean),
    },
  }
}

export function errorMessage(error: unknown): string {
  return error instanceof Error ? error.message : 'Could not complete the action. Please try again.'
}

export function ModalShell({ title, subtitle, onClose, children, footer, drawer = false }: {
  title: string; subtitle?: string; onClose: () => void; children: ReactNode;
  footer?: ReactNode; drawer?: boolean;
}) {
  const panel = useRef<HTMLDivElement>(null)
  const close = useRef(onClose)
  close.current = onClose
  useEffect(() => {
    const previousFocus = document.activeElement instanceof HTMLElement ? document.activeElement : null
    const previousOverflow = document.body.style.overflow
    document.body.style.overflow = 'hidden'
    panel.current?.focus()
    const onKey = (event: KeyboardEvent) => {
      if (event.key === 'Escape') { event.preventDefault(); close.current(); return }
      if (event.key !== 'Tab' || !panel.current) return
      const items = Array.from(panel.current.querySelectorAll<HTMLElement>(
        'button:not(:disabled), [href], input:not(:disabled), select:not(:disabled), textarea:not(:disabled), [tabindex="0"]',
      )).filter((element) => element.getClientRects().length > 0)
      const first = items[0]
      const last = items[items.length - 1]
      if (!first) { event.preventDefault(); return }
      if (event.shiftKey && (document.activeElement === first || document.activeElement === panel.current)) {
        event.preventDefault(); last.focus()
      } else if (!event.shiftKey && (document.activeElement === last || document.activeElement === panel.current)) {
        event.preventDefault(); first.focus()
      }
    }
    document.addEventListener('keydown', onKey)
    return () => {
      document.removeEventListener('keydown', onKey)
      document.body.style.overflow = previousOverflow
      if (previousFocus?.isConnected) previousFocus.focus()
    }
  }, [])
  return createPortal(
    <div className={`cb-settings-overlay ${drawer ? 'cb-settings-overlay--drawer' : ''}`}
      onClick={(event) => { if (event.target === event.currentTarget) onClose() }}>
      <div className={`cb-settings-panel ${drawer ? 'cb-settings-panel--drawer' : ''}`}
        ref={panel} tabIndex={-1} role="dialog" aria-modal="true" aria-label={title}>
        <header className="cb-settings-header">
          <div><h2>{title}</h2>{subtitle && <p>{subtitle}</p>}</div>
          <button type="button" className="cb-settings-icon-button" onClick={onClose} aria-label={`Close ${title}`}><X size={20} /></button>
        </header>
        {children}
        {footer && <footer className="cb-settings-footer">{footer}</footer>}
      </div>
    </div>, document.body,
  )
}

export function Notice({ error, success }: { error?: string; success?: string }) {
  if (!error && !success) return null
  return <div className={`cb-settings-notice ${error ? 'cb-settings-notice--error' : ''}`}
    role={error ? 'alert' : 'status'}>{error || success}</div>
}

export function SaveButton({ busy, children = 'Save', disabled = false }: {
  busy: boolean; children?: ReactNode; disabled?: boolean;
}) {
  return <button type="submit" className="cb-settings-button cb-settings-button--primary" disabled={busy || disabled}>
    {busy && <LoaderCircle size={16} className="cb-settings-spin" />}{children}
  </button>
}

export function BotFields({ value, onChange, capabilities, running = false }: {
  value: BotDraft; onChange: (value: BotDraft) => void;
  capabilities: Capabilities | null; running?: boolean;
}) {
  const update = <K extends keyof BotDraft>(key: K, next: BotDraft[K]) => onChange({ ...value, [key]: next })
  return <>
    <div className="cb-settings-avatar-palette" role="group" aria-label="Avatar style">
      {avatarColors.map((avatar) => <button key={avatar.id} type="button" title={avatar.name}
        aria-label={avatar.name} aria-pressed={value.avatar === avatar.id}
        className={`cb-settings-avatar-option ${value.avatar === avatar.id ? 'is-selected' : ''}`}
        onClick={() => update('avatar', avatar.id)}>
        <Avatar avatar={avatar.id} identity={`avatar-style-${avatar.id}`} size={40} />
      </button>)}
    </div>
    <label className="cb-settings-field">Name
      <input value={value.name} onChange={(event) => update('name', event.target.value)} required maxLength={80} placeholder="What is your bot's name?" autoComplete="off" />
    </label>
    <label className="cb-settings-field">Role
      <textarea value={value.role} onChange={(event) => update('role', event.target.value)} rows={3}
        maxLength={4000} placeholder="What it helps with and the results it is responsible for" />
    </label>
    <label className="cb-settings-toggle-row">
      <span><strong>Lead bot</strong><small>Coordinates the other bots and stays first in the list.</small></span>
      <input type="checkbox" className="cb-settings-switch" checked={value.chief} onChange={(event) => update('chief', event.target.checked)} />
    </label>
    <section className="cb-settings-section">
      <h3>Default model</h3>
      <RuntimeFields backend={value.backend} model={value.model} effort={value.effort}
        capabilities={capabilities} disabled={running}
        onChange={(next) => onChange({ ...value, ...next })} />
      {running && <p className="cb-settings-hint">You can change the model after the current response finishes.</p>}
    </section>
    <section className="cb-settings-section">
      <label className="cb-settings-toggle-row">
        <span><strong>Telegram</strong><small>One conversation shared by the app and your Telegram bot.</small></span>
        <input type="checkbox" className="cb-settings-switch" checked={value.telegramEnabled}
          onChange={(event) => update('telegramEnabled', event.target.checked)} />
      </label>
      {value.telegramEnabled && <div className="cb-settings-nested-fields">
        <label className="cb-settings-field">Token environment variable
          <input value={value.telegramTokenEnv} onChange={(event) => update('telegramTokenEnv', event.target.value)}
            placeholder="TELEGRAM_BOT_TOKEN" required pattern="[A-Za-z_][A-Za-z0-9_]*" autoComplete="off" spellCheck={false} />
          <small>Set the BotFather token in the server environment. Only the variable name is stored here.</small>
        </label>
        <label className="cb-settings-field">Allowed user IDs
          <input value={value.telegramAllowedUserIds} onChange={(event) => update('telegramAllowedUserIds', event.target.value)}
            placeholder="123456789" required pattern="[0-9 ,;\s]+" inputMode="numeric" autoComplete="off" />
          <small>Comma-separated numeric Telegram IDs. The bot only responds to these users.</small>
        </label>
      </div>}
    </section>
  </>
}

export function RuntimeFields({ backend, model, effort, capabilities, disabled = false, excludedEfforts = [], onChange }: {
  backend: string; model: string; effort: string; capabilities: Capabilities | null;
  disabled?: boolean; excludedEfforts?: string[]; onChange: (value: { backend: string; model: string; effort: string }) => void;
}) {
  const models = capabilities?.models.filter((entry) => entry.backend === backend) ?? []
  const efforts = (models.find((entry) => entry.id === model)?.efforts ?? []).filter((entry) => !excludedEfforts.includes(entry))
  const availability = capabilities?.backends[backend]
  const selectModel = (nextBackend: string, nextModel: string) => {
    const allowed = (capabilities?.models.find((entry) => entry.backend === nextBackend && entry.id === nextModel)?.efforts ?? []).filter((entry) => !excludedEfforts.includes(entry))
    onChange({ backend: nextBackend, model: nextModel, effort: allowed.includes(effort) ? effort : allowed.includes('max') ? 'max' : allowed[0] ?? '' })
  }
  return <>
    <div className="cb-settings-runtime-fields">
      <label className="cb-settings-field">Harness
        <select value={backend} disabled={disabled} onChange={(event) => {
          const next = event.target.value
          selectModel(next, capabilities?.models.find((entry) => entry.backend === next)?.id ?? '')
        }}>
          <option value="codex">Codex</option><option value="pi">Pi · DeepSeek</option>
        </select>
      </label>
      <label className="cb-settings-field">Model
        <select value={model} disabled={disabled || models.length === 0} onChange={(event) => selectModel(backend, event.target.value)}>
          {!models.some((entry) => entry.id === model) && <option value={model}>{model || (capabilities ? 'No models available' : 'Loading models…')}</option>}
          {models.map((entry) => <option key={entry.id} value={entry.id}>{entry.name || entry.id}</option>)}
        </select>
      </label>
    </div>
    {efforts.length > 0 && <label className="cb-settings-field">Reasoning effort
      <select value={effort} disabled={disabled} onChange={(event) => onChange({ backend, model, effort: event.target.value })}>
        {!efforts.includes(effort) && <option value={effort} disabled>{effort ? `${effort} · unconfirmed` : 'Select effort'}</option>}
        {efforts.map((entry) => <option key={entry} value={entry}>{entry === 'ultra' ? 'Ultra' : entry}</option>)}
      </select>
    </label>}
    {availability?.available === false && <p className="cb-settings-hint">{availability.reason || 'This harness is not connected yet.'}</p>}
  </>
}

export function MarkdownEditor({ content, onChange, readOnly = false, label = 'Content', placeholder }: {
  content: string; onChange: (content: string) => void; readOnly?: boolean; label?: string; placeholder?: string;
}) {
  const [preview, setPreview] = useState(false)
  return <div className="cb-settings-editor">
    <div className="cb-settings-editor-toolbar">
      <span>{label}</span>
      <div className="cb-settings-segment" aria-label="Editor mode">
        <button type="button" onClick={() => setPreview(false)} aria-pressed={!preview}>{readOnly ? 'Source' : 'Editor'}</button>
        <button type="button" onClick={() => setPreview(true)} aria-pressed={preview}>Preview</button>
      </div>
    </div>
    {preview
      ? <div className="cb-settings-markdown"><ReactMarkdown remarkPlugins={[remarkGfm]}>{content || '*Nothing here yet*'}</ReactMarkdown></div>
      : <textarea aria-label={label} value={content} onChange={(event) => onChange(event.target.value)} readOnly={readOnly}
        placeholder={placeholder} rows={18} spellCheck={false} className="cb-settings-source" />}
  </div>
}
