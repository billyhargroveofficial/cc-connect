/** Connect Bots' thin local tool bridge. No global Pi configuration is changed. */
import { Type } from "@earendil-works/pi-ai";
import type { ExtensionAPI } from "@earendil-works/pi-coding-agent";

export default function (pi: ExtensionAPI) {
  const origin = process.env.CONNECT_BOTS_API_URL;
  const token = process.env.CONNECT_BOTS_INTERNAL_TOKEN;
  const botId = process.env.CONNECT_BOTS_BOT_ID;
  if (!origin || !token || !botId) return;

  const tools = [
    { name: "bots_list", label: "Боты", description: "List your persistent Connect Bots and their current state.", parameters: Type.Object({}) },
    { name: "bots_status", label: "Состояние бота", description: "Read a bot's state and most recent result.", parameters: Type.Object({ botId: Type.String() }) },
    { name: "bots_send", label: "Поручить боту", description: "Ask another persistent bot to do bounded work and wait for its result. Do not recursively delegate back to a busy caller.", parameters: Type.Object({ botId: Type.String(), message: Type.String() }) },
	{ name: "bots_publish_files", label: "Отправить файлы", description: "Send prepared files, pictures, documents or archives from your own workspace as downloadable conversation attachments. Original files are preserved.", parameters: Type.Object({ paths: Type.Array(Type.String(), { minItems: 1, maxItems: 10 }), caption: Type.Optional(Type.String()) }) },
    { name: "bots_create", label: "Создать бота", description: "Create a persistent specialist. Only the chief may use this tool.", parameters: Type.Object({ name: Type.String(), role: Type.String(), backend: Type.Optional(Type.String()), model: Type.Optional(Type.String()) }) },
  ];

  for (const tool of tools) {
    pi.registerTool({
      ...tool,
      async execute(callId, args, signal) {
        const timeout = AbortSignal.timeout(180_000);
        const combined = signal ? AbortSignal.any([signal, timeout]) : timeout;
        const response = await fetch(new URL("/api/studio/internal/tools", origin), {
          method: "POST",
          headers: { "Content-Type": "application/json", Authorization: `Bearer ${token}` },
          body: JSON.stringify({ botId, callId, name: tool.name, arguments: args }),
          signal: combined,
        });
        if (!response.ok) throw new Error(`Connect Bots tool failed (${response.status})`);
        const result = await response.json() as { success: boolean; contentItems: Array<{type: string; text?: string}> };
        return {
          content: result.contentItems.map(item => ({ type: "text" as const, text: item.text ?? JSON.stringify(item) })),
          details: { botId, tool: tool.name, success: result.success },
          isError: !result.success,
        };
      },
    });
  }
}
