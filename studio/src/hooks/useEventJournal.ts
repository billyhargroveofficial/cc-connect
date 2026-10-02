import { useCallback, useSyncExternalStore } from "react";
import { EventJournal, emptyJournal } from "../lib/eventJournal";

export function useBotEvents(journal: EventJournal, botId: string) {
  const subscribe = useCallback(
    (listener: () => void) => botId ? journal.subscribe(botId, listener) : () => undefined,
    [journal, botId],
  );
  const snapshot = useCallback(
    () => botId ? journal.forBot(botId) : emptyJournal,
    [journal, botId],
  );
  return useSyncExternalStore(subscribe, snapshot, snapshot);
}

export function useRosterEvents(journal: EventJournal) {
  const subscribe = useCallback((listener: () => void) => journal.subscribeRoster(listener), [journal]);
  const snapshot = useCallback(() => journal.allRoster(), [journal]);
  return useSyncExternalStore(subscribe, snapshot, snapshot);
}
