import type { Bot, CatalogBot, NodeInfo } from "./types";

export type HostCategory = "server" | "mac";
export type HostFilters = Record<HostCategory, boolean>;

export function catalogBotKey(nodeId: string, botId: string) {
  return JSON.stringify([nodeId, botId]);
}

export function hostCategory(node: NodeInfo): HostCategory {
  if (node.local) return "server";
  const platform = node.platform || node.os || "";
  if (/^(darwin|macos|mac)$/i.test(platform)) return "mac";
  // A newly paired Mac has a name before it reports its platform.
  return !platform && /\bmac(?:book|mini|studio|pro)?\b/i.test(`${node.name} ${node.hostname || ""}`)
    ? "mac" : "server";
}

export function hostLabel(node: NodeInfo) {
  return node.local ? "Server" : node.name;
}

export function catalogRows(nodes: NodeInfo[], catalogs: Record<string, Bot[]>): CatalogBot[] {
  return nodes.flatMap(node => (catalogs[node.id] || [])
    .filter(bot => bot.status !== "archived")
    .map(bot => ({ key: catalogBotKey(node.id, bot.id), bot, node })));
}
