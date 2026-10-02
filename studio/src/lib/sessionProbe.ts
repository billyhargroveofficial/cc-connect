// EventSource does not expose HTTP status. Check the owner session after a
// bounded reconnect delay, without creating another stream or replaying turns.
export function createSessionProbe({
  check,
  expired,
  schedule = (callback, delay) => setTimeout(callback, delay),
  cancel = (handle) => clearTimeout(handle as ReturnType<typeof setTimeout>),
}: {
  check: () => Promise<{ authenticated?: boolean }>;
  expired: () => void;
  schedule?: (callback: () => void, delay: number) => unknown;
  cancel?: (handle: unknown) => void;
}) {
  let timer: unknown;
  let pending = false;
  let inFlight = false;
  let alive = true;
  let delay = 4000;
  function clear() {
    if (pending) cancel(timer);
    pending = false;
  }
  async function probe() {
    pending = false;
    if (!alive || inFlight) return;
    inFlight = true;
    try {
      const session = await check();
      if (!alive) return;
      delay = 4000;
      if (!session.authenticated) expired();
    } catch {
      // A server outage is not a logout. Keep local drafts and the journal.
      delay = Math.min(delay * 2, 30000);
    } finally {
      inFlight = false;
    }
  }
  return {
    reconnect() {
      if (alive && !pending && !inFlight) {
        pending = true;
        timer = schedule(() => void probe(), delay);
      }
    },
    connected() {
      clear();
      delay = 4000;
    },
    dispose() {
      alive = false;
      clear();
    },
  };
}
