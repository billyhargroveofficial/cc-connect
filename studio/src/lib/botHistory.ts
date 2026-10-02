import type { Event } from './types';

export interface BotHistoryEntry {
  title: string;
  content: string;
}

const instructionsUpdated = {
  title: 'Instructions or skills updated',
  content: 'Instructions or skills were updated. A new Codex session was created with the conversation history carried over.',
};
const sessionRecovered = {
  title: 'Session recovered',
  content: "The Pi session has not been saved yet, or its file is missing. A new session was created while preserving the bot's visible history.",
};
const historyNotices = new Map<string, BotHistoryEntry>([
  [instructionsUpdated.content, instructionsUpdated],
  ['Настройки инструкций или навыков обновлены. Создана новая сессия Codex с переносом истории разговора.', instructionsUpdated],
  [sessionRecovered.content, sessionRecovered],
  ['Сессия Pi ещё не была записана или её файл отсутствует. Создана новая сессия с сохранением видимой истории бота.', sessionRecovered],
]);
const silentNotices = new Set(['Session connected.', 'Session history saved.']);

/** Only product lifecycle receipts belong in the bot island. Exact matches
 * keep provider errors, permission notices and identical owner text visible. */
export function botHistoryEntry(event: Event): BotHistoryEntry | null {
  if (event.type !== 'system' || typeof event.data.content !== 'string') return null;
  return historyNotices.get(event.data.content.trim()) || null;
}

export function isSilentSessionNotice(event: Event): boolean {
  return event.type === 'system' && typeof event.data.content === 'string'
    && (silentNotices.has(event.data.content.trim()) || botHistoryEntry(event) !== null);
}
