import assert from 'node:assert/strict';
import test from 'node:test';
import { botHistoryEntry, isSilentSessionNotice } from './botHistory.ts';

const updated = 'Instructions or skills were updated. A new Codex session was created with the conversation history carried over.';
const recovered = "The Pi session has not been saved yet, or its file is missing. A new session was created while preserving the bot's visible history.";
const event = content => ({ seq: 1, botId: 'bot', type: 'system', time: '', data: { content } });

test('session configuration and recovery receipts move into bot history without mutating raw payloads', () => {
  for (const [content, title, canonical] of [
    [updated, 'Instructions or skills updated', updated],
    ['Настройки инструкций или навыков обновлены. Создана новая сессия Codex с переносом истории разговора.', 'Instructions or skills updated', updated],
    [recovered, 'Session recovered', recovered],
    ['Сессия Pi ещё не была записана или её файл отсутствует. Создана новая сессия с сохранением видимой истории бота.', 'Session recovered', recovered],
  ]) {
    const receipt = event(content);
    const original = structuredClone(receipt);
    assert.deepEqual(botHistoryEntry(receipt), { title, content: canonical });
    assert.equal(isSilentSessionNotice(receipt), true);
    assert.deepEqual(receipt, original, 'history presentation does not rewrite the journal');
  }
});

test('routine session persistence receipts stay silent without adding history entries', () => {
  for (const content of ['Session connected.', 'Session history saved.']) {
    const receipt = event(content);
    assert.equal(botHistoryEntry(receipt), null);
    assert.equal(isSilentSessionNotice(receipt), true);
  }
});

test('history classification never hides meaningful notices, permission prompts or owner text', () => {
  for (const receipt of [
    event('Connection lost; retry required.'),
    event(`${updated} Failed to restore the history.`),
    event('The saved goal stays paused until the first response of the new session.'),
    { ...event(updated), type: 'permission' },
    { ...event(updated), type: 'message', data: { role: 'user', content: updated } },
    { ...event(recovered), type: 'native' },
    event(null),
  ]) {
    assert.equal(botHistoryEntry(receipt), null);
    assert.equal(isSilentSessionNotice(receipt), false);
  }
});
