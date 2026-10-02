import { useEffect, useRef, useState, type FormEvent } from 'react'
import { LoaderCircle, Monitor } from 'lucide-react'
import type { Bot, Capabilities, NodeInfo } from '../../lib/types'
import { api } from '../../lib/api'
import { BotFields, botPayload, draftFromBot, errorMessage, ModalShell, Notice, SaveButton } from './shared'

interface NewBotDialogProps {
  capabilities: Capabilities | null
  onClose: () => void
  onCreated: (bot: Bot, nodeId: string) => void
  nodes?: NodeInfo[]
  activeNode?: NodeInfo
  onNodeChange?: (id: string) => void
  loadCapabilities?: (nodeId: string, signal: AbortSignal) => Promise<Capabilities>
  createBot?: (fields: Partial<Bot>, nodeId: string) => Promise<Bot>
}

export function NewBotDialog({ capabilities, onClose, onCreated, nodes, activeNode, onNodeChange, loadCapabilities, createBot }: NewBotDialogProps) {
  const [draft, setDraft] = useState(() => draftFromBot(null, capabilities))
  const [selectedNodeId, setSelectedNodeId] = useState(activeNode?.id ?? 'local')
  const [nodeCapabilities, setNodeCapabilities] = useState(capabilities)
  const [loading, setLoading] = useState(false)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const requestVersion = useRef(0)
  const alive = useRef(true)
  const availableNodes = nodes ?? (activeNode ? [activeNode] : [])
  const selectedNode = availableNodes.find(node => node.id === selectedNodeId)
  const offline = !selectedNode?.online
  useEffect(() => {
    alive.current = true
    return () => { alive.current = false; requestVersion.current++ }
  }, [])
  useEffect(() => { if (activeNode?.id) setSelectedNodeId(activeNode.id) }, [activeNode?.id])
  useEffect(() => { if (!loadCapabilities) setNodeCapabilities(capabilities) }, [capabilities, loadCapabilities])
  useEffect(() => {
    if (!loadCapabilities) return
    const version = ++requestVersion.current
    const controller = new AbortController()
    setLoading(true); setError(''); setNodeCapabilities(null)
    if (offline) { setLoading(false); return () => controller.abort() }
    loadCapabilities(selectedNodeId, controller.signal).then(result => {
      if (controller.signal.aborted || version !== requestVersion.current || !alive.current) return
      setNodeCapabilities(result)
      setDraft(previous => {
        const available = result.models.find(model => model.id === previous.model && model.backend === previous.backend)
        if (available) return { ...previous, effort: available.efforts.includes(previous.effort) ? previous.effort
          : available.efforts.includes('max') ? 'max' : available.efforts[0] ?? '' }
        const defaults = draftFromBot(null, result)
        return { ...previous, backend: defaults.backend, model: defaults.model, effort: defaults.effort }
      })
    }).catch(cause => {
      if (!controller.signal.aborted && version === requestVersion.current && alive.current) setError(errorMessage(cause))
    }).finally(() => {
      if (!controller.signal.aborted && version === requestVersion.current && alive.current) setLoading(false)
    })
    return () => controller.abort()
  }, [selectedNodeId, offline, loadCapabilities])
  useEffect(() => {
    if (draft.model || !nodeCapabilities) return
    const model = nodeCapabilities.models.find((entry) => entry.backend === draft.backend)
    if (model) setDraft((previous) => ({ ...previous, model: model.id, effort: model.efforts.includes('max') ? 'max' : model.efforts[0] ?? '' }))
  }, [nodeCapabilities, draft.backend, draft.model])
  async function submit(event: FormEvent) {
    event.preventDefault()
    if (busy || loading || offline || !draft.name.trim() || (loadCapabilities && !nodeCapabilities)) return
    setBusy(true); setError('')
    try {
      const bot = createBot ? await createBot(botPayload(draft), selectedNodeId) : await api.createBot(botPayload(draft))
      if (!alive.current) return
      onCreated(bot, selectedNodeId)
      onClose()
    } catch (cause) { if (alive.current) setError(errorMessage(cause)) }
    finally { if (alive.current) setBusy(false) }
  }
  return <ModalShell title="New bot" subtitle="Its own role, memory, and workspace." onClose={onClose}>
    <form onSubmit={submit} className="cb-settings-form cb-settings-create-form">
      <div className="cb-settings-scroll"><BotFields value={draft} onChange={setDraft} capabilities={nodeCapabilities} running={busy || loading || offline}
        beforeModel={(nodes !== undefined || activeNode !== undefined) && <section className="cb-settings-section cb-new-bot-host">
          <label className="cb-settings-field"><span><Monitor size={14} />Run on</span>
            <select value={selectedNodeId} disabled={busy} onChange={event => {
              const id = event.target.value
              setSelectedNodeId(id); onNodeChange?.(id)
            }}>
              {!selectedNode && <option value={selectedNodeId} disabled>Host unavailable</option>}
              {availableNodes.map(node =>
                <option key={node.id} value={node.id}>{node.name}{node.online ? '' : ' · Offline'}</option>)}
            </select>
            <small>{!selectedNode ? 'This host is no longer available. Choose another host.' : offline ? 'This host is offline. Reconnect it to create a bot.' : loading ? <><LoaderCircle size={12} className="cb-settings-spin" />Loading available models…</>
              : 'The bot and its files stay on this host.'}</small>
          </label>
        </section>} />
        <Notice error={error} />
      </div>
      <footer className="cb-settings-footer">
        <span className="cb-settings-hint">You can configure instructions and skills after creating the bot.</span>
        <SaveButton busy={busy} disabled={!draft.name.trim() || loading || offline || Boolean(loadCapabilities && !nodeCapabilities)}>Create bot</SaveButton>
      </footer>
    </form>
  </ModalShell>
}
