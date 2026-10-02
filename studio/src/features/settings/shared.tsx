import { useEffect, useId, useRef, useState, type ReactNode } from 'react'
import { createPortal } from 'react-dom'
import { LoaderCircle, X } from 'lucide-react'
import ReactMarkdown from 'react-markdown'
import remarkGfm from 'remark-gfm'
import type { Bot, Capabilities } from '../../lib/types'
import { avatarStyles } from '../../lib/avatars'
import Avatar from '../../components/Avatar'
import { AnimatePresence, LayoutGroup, m, useIsPresent, useReducedMotion, backdropMotion, modalMotion, controlMotion, motionSpring, motionTransition } from '../../lib/motion'
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

let modalBodyLocks = 0
let unlockedBodyOverflow = ''

function lockModalBody() {
  if (modalBodyLocks === 0) unlockedBodyOverflow = document.body.style.overflow
  modalBodyLocks += 1
  document.body.style.overflow = 'hidden'
  return () => {
    modalBodyLocks = Math.max(0, modalBodyLocks - 1)
    if (modalBodyLocks === 0) document.body.style.overflow = unlockedBodyOverflow
  }
}

export function ModalShell({ title, subtitle, onClose, children, footer, drawer = false, wide = false }: {
  title: string; subtitle?: string; onClose: () => void; children: ReactNode;
  footer?: ReactNode; drawer?: boolean; wide?: boolean;
}) {
  const panel = useRef<HTMLDivElement>(null)
  const present = useIsPresent()
  const returnTarget = useRef(typeof HTMLElement !== 'undefined' && document.activeElement instanceof HTMLElement ? document.activeElement : null)
  const returnFrame = useRef<number | null>(null)
  const close = useRef(onClose)
  close.current = onClose
  useEffect(() => {
    if (!present) return
    if (returnFrame.current !== null) {
      window.cancelAnimationFrame(returnFrame.current)
      returnFrame.current = null
    }
    const previousFocus = returnTarget.current
    const element = panel.current
    const unlockBody = lockModalBody()
    panel.current?.focus()
    const onKey = (event: KeyboardEvent) => {
      if (event.key === 'Escape') { event.preventDefault(); close.current(); return }
      if (event.key !== 'Tab' || !panel.current) return
      const items = Array.from(panel.current.querySelectorAll<HTMLElement>(
        'button:not(:disabled), [href], input:not(:disabled), select:not(:disabled), textarea:not(:disabled), [tabindex="0"]',
      )).filter((element) => element.tabIndex >= 0 && !element.closest('[inert]') && element.getClientRects().length > 0)
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
      unlockBody()
      returnFrame.current = window.requestAnimationFrame(() => {
        returnFrame.current = null
        if (!previousFocus?.isConnected || previousFocus.closest('[inert]')) return
        // An exit applies inert before effect cleanup and can clear focus to
        // BODY. Wait for that commit while preserving a newly opened dialog.
        const otherModal = Array.from(document.querySelectorAll<HTMLElement>('[role="dialog"][aria-modal="true"]'))
          .some((dialog) => dialog !== element && !dialog.closest('[inert]') && dialog.getClientRects().length > 0 && !dialog.contains(previousFocus))
        const focused = document.activeElement
        if (!otherModal && (element?.contains(focused) || focused === document.body || !focused)) previousFocus.focus({ preventScroll: true })
      })
    }
  }, [present])
  return createPortal(
    <m.div className={`cb-settings-overlay ${drawer ? 'cb-settings-overlay--drawer' : ''}`}
      variants={backdropMotion} initial="hidden" animate="visible" exit="exit" inert={!present}
      onClick={(event) => { if (event.target === event.currentTarget) onClose() }}>
      <m.div className={`cb-settings-panel${drawer ? ' cb-settings-panel--drawer' : ''}${wide ? ' cb-settings-panel--wide' : ''}`}
        variants={modalMotion} initial="hidden" animate="visible" exit="exit"
        ref={panel} tabIndex={-1} role="dialog" aria-modal="true" aria-label={title}>
        <header className="cb-settings-header">
          <div><h2>{title}</h2>{subtitle && <p>{subtitle}</p>}</div>
          <m.button {...controlMotion} type="button" className="cb-settings-icon-button" onClick={onClose} aria-label={`Close ${title}`}><X size={20} /></m.button>
        </header>
        {children}
        {footer && <footer className="cb-settings-footer">{footer}</footer>}
      </m.div>
    </m.div>, document.body,
  )
}

export function Notice({ error, success }: { error?: string; success?: string }) {
  const reduced = useReducedMotion()
  return <AnimatePresence initial={false}>
    {(error || success) && <m.div key={error ? 'error' : 'success'} className="cb-settings-notice-wrap"
      initial={{ height: 0, opacity: 0 }} animate={{ height: 'auto', opacity: 1 }} exit={{ height: 0, opacity: 0 }}
      transition={reduced ? { duration: 0 } : motionTransition.disclosure}>
      <div className={`cb-settings-notice ${error ? 'cb-settings-notice--error' : ''}`}
        role={error ? 'alert' : 'status'}>{error || success}</div>
    </m.div>}
  </AnimatePresence>
}

export function SaveButton({ busy, children = 'Save', disabled = false }: {
  busy: boolean; children?: ReactNode; disabled?: boolean;
}) {
  const reduced = useReducedMotion()
  return <m.button {...(!busy && !disabled ? controlMotion : {})} type="submit" className="cb-settings-button cb-settings-button--primary" disabled={busy || disabled}>
    <AnimatePresence initial={false}>{busy && <m.span key="busy" className="cb-settings-button-spinner"
      initial={{ width: 0, opacity: 0 }} animate={{ width: 16, opacity: 1 }} exit={{ width: 0, opacity: 0 }} transition={reduced ? { duration: 0 } : motionTransition.quick}>
      <LoaderCircle size={16} className="cb-settings-spin" />
    </m.span>}</AnimatePresence>{children}
  </m.button>
}

export function BotFields({ value, onChange, capabilities, running = false }: {
  value: BotDraft; onChange: (value: BotDraft) => void;
  capabilities: Capabilities | null; running?: boolean;
}) {
  const paletteId = useId()
  const update = <K extends keyof BotDraft>(key: K, next: BotDraft[K]) => onChange({ ...value, [key]: next })
  return <>
    <LayoutGroup id={paletteId}><div className="cb-settings-avatar-palette" role="group" aria-label="Avatar style">
      {avatarColors.map((avatar) => <m.button key={avatar.id} type="button" title={avatar.name}
        whileHover={{ y: -2 }} whileTap={{ scale: 0.94 }} transition={motionSpring.control}
        aria-label={avatar.name} aria-pressed={value.avatar === avatar.id}
        className={`cb-settings-avatar-option ${value.avatar === avatar.id ? 'is-selected' : ''}`}
        onClick={() => update('avatar', avatar.id)}>
        {value.avatar === avatar.id && <m.span className="cb-settings-avatar-ring" layoutId="avatar-selection" transition={motionSpring.control} />}
        <Avatar avatar={avatar.id} identity={`avatar-style-${avatar.id}`} size={40} />
      </m.button>)}
    </div></LayoutGroup>
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
      <AnimatePresence initial={false}>{value.telegramEnabled && <TelegramFields key="telegram" value={value} onChange={onChange} />}</AnimatePresence>
    </section>
  </>
}

function TelegramFields({ value, onChange }: { value: BotDraft; onChange: (value: BotDraft) => void }) {
  const present = useIsPresent()
  const reduced = useReducedMotion()
  return <m.div className="cb-settings-disclosure" inert={!present}
    initial={{ height: 0, opacity: 0 }} animate={{ height: 'auto', opacity: 1 }} exit={{ height: 0, opacity: 0 }}
    transition={reduced ? { duration: 0 } : motionTransition.disclosure}>
    <div className="cb-settings-nested-fields">
      <label className="cb-settings-field">Token environment variable
        <input value={value.telegramTokenEnv} onChange={(event) => onChange({ ...value, telegramTokenEnv: event.target.value })}
          placeholder="TELEGRAM_BOT_TOKEN" required disabled={!present} pattern="[A-Za-z_][A-Za-z0-9_]*" autoComplete="off" spellCheck={false} />
        <small>Set the BotFather token in the server environment. Only the variable name is stored here.</small>
      </label>
      <label className="cb-settings-field">Allowed user IDs
        <input value={value.telegramAllowedUserIds} onChange={(event) => onChange({ ...value, telegramAllowedUserIds: event.target.value })}
          placeholder="123456789" required disabled={!present} pattern="[0-9 ,;\s]+" inputMode="numeric" autoComplete="off" />
        <small>Comma-separated numeric Telegram IDs. The bot only responds to these users.</small>
      </label>
    </div>
  </m.div>
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
        <m.button {...controlMotion} type="button" onClick={() => setPreview(false)} aria-pressed={!preview}>{readOnly ? 'Source' : 'Editor'}</m.button>
        <m.button {...controlMotion} type="button" onClick={() => setPreview(true)} aria-pressed={preview}>Preview</m.button>
      </div>
    </div>
    <AnimatePresence initial={false} mode="wait"><EditorSurface key={preview ? 'preview' : 'source'}>{preview
      ? <div className="cb-settings-markdown"><ReactMarkdown remarkPlugins={[remarkGfm]}>{content || '*Nothing here yet*'}</ReactMarkdown></div>
      : <textarea aria-label={label} value={content} onChange={(event) => onChange(event.target.value)} readOnly={readOnly}
        placeholder={placeholder} rows={18} spellCheck={false} className="cb-settings-source" />}</EditorSurface></AnimatePresence>
  </div>
}

function EditorSurface({ children }: { children: ReactNode }) {
  const present = useIsPresent()
  return <m.div inert={!present} initial={{ opacity: 0 }} animate={{ opacity: 1 }} exit={{ opacity: 0 }}
    transition={motionTransition.quick}>{children}</m.div>
}
