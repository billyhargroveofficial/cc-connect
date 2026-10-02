import type { Event } from "./types";
import { mergeEvents } from "./events";

export const emptyJournal: Event[] = Object.freeze([]) as unknown as Event[];

type Listener = () => void;

/**
 * Mutable journal ownership lives outside React. Consumers subscribe to the
 * exact projection they render, so a token for one bot cannot wake the app
 * shell, another conversation, or the roster.
 */
export class EventJournal {
  private events: Record<string, Event[]> = {};
  private rosterEvents: Record<string, Event[]> = {};
  private messageEvents: Record<string, Event[]> = {};
  private botListeners = new Map<string, Set<Listener>>();
  private rosterListeners = new Set<Listener>();

  all() { return this.events; }
  allRoster() { return this.rosterEvents; }
  forBot(id: string): Event[] { return this.events[id] || emptyJournal; }
  messagesFor(id: string): Event[] { return this.messageEvents[id] || emptyJournal; }

  subscribe(id: string, listener: Listener) {
    let listeners = this.botListeners.get(id);
    if (!listeners) this.botListeners.set(id, listeners = new Set());
    listeners.add(listener);
    return () => {
      listeners?.delete(listener);
      if (!listeners?.size) this.botListeners.delete(id);
    };
  }

  subscribeRoster(listener: Listener) {
    this.rosterListeners.add(listener);
    return () => { this.rosterListeners.delete(listener); };
  }

  merge(incoming: Event[], fixedBotId?: string) {
    if (!incoming.length) return;
    const groups = new Map<string, Event[]>();
    for (const event of incoming) {
      const id = fixedBotId || event.botId;
      if (!id) continue;
      const group = groups.get(id);
      if (group) group.push(event); else groups.set(id, [event]);
    }
    const changedBots: string[] = [];
    let rosterChanged = false;
    for (const [id, group] of groups) {
      const merged = mergeEvents(this.events[id] || [], group);
      if (merged !== this.events[id]) {
        this.events = { ...this.events, [id]: merged };
        changedBots.push(id);
      }
      const previews = group.filter(event => event.type === "message" || event.type === "system");
      if (previews.length) {
        const preview = mergeEvents(this.rosterEvents[id] || [], previews);
        if (preview !== this.rosterEvents[id]) {
          this.rosterEvents = { ...this.rosterEvents, [id]: preview };
          rosterChanged = true;
        }
      }
      const messages = group.filter(event => event.type === "message");
      if (messages.length) {
        const mergedMessages = mergeEvents(this.messageEvents[id] || [], messages);
        if (mergedMessages !== this.messageEvents[id]) this.messageEvents = { ...this.messageEvents, [id]: mergedMessages };
      }
    }
    for (const id of changedBots) for (const listener of this.botListeners.get(id) || []) listener();
    if (rosterChanged) for (const listener of this.rosterListeners) listener();
  }

  clear() {
    const changedBots = Object.keys(this.events);
    const rosterChanged = Object.keys(this.rosterEvents).length > 0;
    if (!changedBots.length && !rosterChanged) return;
    this.events = {};
    this.rosterEvents = {};
    this.messageEvents = {};
    for (const id of changedBots) for (const listener of this.botListeners.get(id) || []) listener();
    if (rosterChanged) for (const listener of this.rosterListeners) listener();
  }
}
