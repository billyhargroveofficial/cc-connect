import { useEffect, useRef, useState, type ReactNode } from 'react'
import { createPortal } from 'react-dom'
import { LoaderCircle, X } from 'lucide-react'
import ReactMarkdown from 'react-markdown'
import remarkGfm from 'remark-gfm'
import type { Bot, Capabilities } from '../../lib/types'
import './settings.css'

export const avatarColors = [
  { id: 'lavender', color: '#b9a0ed', name: 'Лавандовый' },
  { id: 'mint', color: '#9acbb3', name: 'Мятный' },
  { id: 'peach', color: '#dfaf8e', name: 'Персиковый' },
  { id: 'blue', color: '#94b7df', name: 'Голубой' },
  { id: 'rose', color: '#d89aae', name: 'Розовый' },
  { id: 'amber', color: '#d8be7c', name: 'Золотой' },
]

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
  return error instanceof Error ? error.message : 'Не удалось выполнить действие. Попробуйте ещё раз.'
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
          <button type="button" className="cb-settings-icon-button" onClick={onClose} aria-label="Закрыть настройки"><X size={20} /></button>
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

export function SaveButton({ busy, children = 'Сохранить', disabled = false }: {
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
    <div className="cb-settings-avatar-palette" role="group" aria-label="Цвет аватара">
      {avatarColors.map((avatar) => <button key={avatar.id} type="button" title={avatar.name}
        aria-label={avatar.name} aria-pressed={value.avatar === avatar.id}
        className={`cb-settings-avatar-option ${value.avatar === avatar.id ? 'is-selected' : ''}`}
        onClick={() => update('avatar', avatar.id)} style={{ background: avatar.color }}>
        <span className="cb-settings-avatar-eyes"><i /><i /></span>
      </button>)}
    </div>
    <label className="cb-settings-field">Имя
      <input value={value.name} onChange={(event) => update('name', event.target.value)} required maxLength={80} placeholder="Как зовут бота?" autoComplete="off" />
    </label>
    <label className="cb-settings-field">Роль
      <textarea value={value.role} onChange={(event) => update('role', event.target.value)} rows={3}
        maxLength={4000} placeholder="В чём он помогает и за какой результат отвечает" />
    </label>
    <label className="cb-settings-toggle-row">
      <span><strong>Главный бот</strong><small>Координирует остальных и остаётся первым в списке.</small></span>
      <input type="checkbox" className="cb-settings-switch" checked={value.chief} onChange={(event) => update('chief', event.target.checked)} />
    </label>
    <section className="cb-settings-section">
      <h3>Модель по умолчанию</h3>
      <RuntimeFields backend={value.backend} model={value.model} effort={value.effort}
        capabilities={capabilities} disabled={running}
        onChange={(next) => onChange({ ...value, ...next })} />
      {running && <p className="cb-settings-hint">Модель можно изменить после завершения текущего ответа.</p>}
    </section>
    <section className="cb-settings-section">
      <label className="cb-settings-toggle-row">
        <span><strong>Telegram</strong><small>Общий разговор в приложении и личном Telegram-боте.</small></span>
        <input type="checkbox" className="cb-settings-switch" checked={value.telegramEnabled}
          onChange={(event) => update('telegramEnabled', event.target.checked)} />
      </label>
      {value.telegramEnabled && <div className="cb-settings-nested-fields">
        <label className="cb-settings-field">Переменная окружения с токеном
          <input value={value.telegramTokenEnv} onChange={(event) => update('telegramTokenEnv', event.target.value)}
            placeholder="TELEGRAM_BOT_TOKEN" required pattern="[A-Za-z_][A-Za-z0-9_]*" autoComplete="off" spellCheck={false} />
          <small>Токен BotFather должен быть задан в окружении сервера. Здесь хранится только имя переменной.</small>
        </label>
        <label className="cb-settings-field">Разрешённые ID пользователей
          <input value={value.telegramAllowedUserIds} onChange={(event) => update('telegramAllowedUserIds', event.target.value)}
            placeholder="123456789" required pattern="[0-9 ,;\s]+" inputMode="numeric" autoComplete="off" />
          <small>Числовые Telegram ID через запятую. Бот отвечает только этим пользователям.</small>
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
      <label className="cb-settings-field">Исполнитель
        <select value={backend} disabled={disabled} onChange={(event) => {
          const next = event.target.value
          selectModel(next, capabilities?.models.find((entry) => entry.backend === next)?.id ?? '')
        }}>
          <option value="codex">Codex</option><option value="pi">Pi · DeepSeek</option>
        </select>
      </label>
      <label className="cb-settings-field">Модель
        <select value={model} disabled={disabled || models.length === 0} onChange={(event) => selectModel(backend, event.target.value)}>
          {!models.some((entry) => entry.id === model) && <option value={model}>{model || (capabilities ? 'Нет доступных моделей' : 'Загружаем модели…')}</option>}
          {models.map((entry) => <option key={entry.id} value={entry.id}>{entry.name || entry.id}</option>)}
        </select>
      </label>
    </div>
    {efforts.length > 0 && <label className="cb-settings-field">Уровень рассуждения
      <select value={effort} disabled={disabled} onChange={(event) => onChange({ backend, model, effort: event.target.value })}>
        {!efforts.includes(effort) && <option value={effort} disabled>{effort ? `${effort} · не подтверждён` : 'Выберите уровень'}</option>}
        {efforts.map((entry) => <option key={entry} value={entry}>{entry === 'ultra' ? 'Ultra' : entry}</option>)}
      </select>
    </label>}
    {availability?.available === false && <p className="cb-settings-hint">{availability.reason || 'Исполнитель пока не подключён.'}</p>}
  </>
}

export function MarkdownEditor({ content, onChange, readOnly = false, label = 'Содержимое', placeholder }: {
  content: string; onChange: (content: string) => void; readOnly?: boolean; label?: string; placeholder?: string;
}) {
  const [preview, setPreview] = useState(false)
  return <div className="cb-settings-editor">
    <div className="cb-settings-editor-toolbar">
      <span>{label}</span>
      <div className="cb-settings-segment" aria-label="Режим редактора">
        <button type="button" onClick={() => setPreview(false)} aria-pressed={!preview}>{readOnly ? 'Текст' : 'Редактор'}</button>
        <button type="button" onClick={() => setPreview(true)} aria-pressed={preview}>Просмотр</button>
      </div>
    </div>
    {preview
      ? <div className="cb-settings-markdown"><ReactMarkdown remarkPlugins={[remarkGfm]}>{content || '*Пока пусто*'}</ReactMarkdown></div>
      : <textarea aria-label={label} value={content} onChange={(event) => onChange(event.target.value)} readOnly={readOnly}
        placeholder={placeholder} rows={18} spellCheck={false} className="cb-settings-source" />}
  </div>
}
