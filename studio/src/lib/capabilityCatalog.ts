import type { Capabilities } from "./types";

// Discovery can succeed for one harness while another temporarily times out.
// Retain only that harness's known model metadata; its new unavailable status
// still disables mutations until discovery recovers. A successful empty
// catalog remains authoritative and never gains invented models.
export function retainCapabilityCatalog(previous: Capabilities | null | undefined, next: Capabilities): Capabilities {
  if (!previous) return next;
  const retained = previous.models.filter(model => next.backends[model.backend]?.available === false &&
    !next.models.some(choice => choice.backend === model.backend && choice.id === model.id));
  return retained.length ? { ...next, models: [...next.models, ...retained] } : next;
}

export function hasTransientDiscoveryFailure(capabilities: Capabilities): boolean {
  return Object.values(capabilities.backends).some(backend => !backend.available &&
    /deadline|timeout|timed out|temporar|connection|disconnected|context canceled|socket|\beof\b/i.test(backend.reason || ""));
}
