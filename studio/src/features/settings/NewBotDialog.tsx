import { useEffect, useState, type FormEvent } from 'react'
import type { Bot, Capabilities } from '../../lib/types'
import { api } from '../../lib/api'
import { BotFields, botPayload, draftFromBot, errorMessage, ModalShell, Notice, SaveButton } from './shared'

interface NewBotDialogProps {
  capabilities: Capabilities | null
  onClose: () => void
  onCreated: (bot: Bot) => void
}

export function NewBotDialog({ capabilities, onClose, onCreated }: NewBotDialogProps) {
  const [draft, setDraft] = useState(() => draftFromBot(null, capabilities))
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  useEffect(() => {
    if (draft.model || !capabilities) return
    const model = capabilities.models.find((entry) => entry.backend === draft.backend)
    if (model) setDraft((previous) => ({ ...previous, model: model.id, effort: model.efforts.includes('max') ? 'max' : model.efforts[0] ?? '' }))
  }, [capabilities, draft.backend, draft.model])
  async function submit(event: FormEvent) {
    event.preventDefault()
    if (busy || !draft.name.trim()) return
    setBusy(true); setError('')
    try {
      const bot = await api.createBot(botPayload(draft))
      onCreated(bot)
      onClose()
    } catch (cause) { setError(errorMessage(cause)) }
    finally { setBusy(false) }
  }
  return <ModalShell title="New bot" subtitle="Its own role, memory, and workspace." onClose={onClose}>
    <form onSubmit={submit} className="cb-settings-form cb-settings-create-form">
      <div className="cb-settings-scroll"><BotFields value={draft} onChange={setDraft} capabilities={capabilities} />
        <Notice error={error} />
      </div>
      <footer className="cb-settings-footer">
        <span className="cb-settings-hint">You can configure instructions and skills after creating the bot.</span>
        <SaveButton busy={busy} disabled={!draft.name.trim()}>Create bot</SaveButton>
      </footer>
    </form>
  </ModalShell>
}
