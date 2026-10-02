import assert from "node:assert/strict";
import path from "node:path";
import test from "node:test";
import { fileURLToPath } from "node:url";
import { loadConfigFromFile } from "vite";

const root = path.dirname(fileURLToPath(import.meta.url));

test("public Connect Bots host is accepted by dev and preview servers", async () => {
  const loaded = await loadConfigFromFile(
    { command: "serve", mode: "production" },
    path.join(root, "vite.config.ts"),
    root,
  );

  assert.ok(loaded);
  assert.deepEqual(loaded.config.server?.allowedHosts, ["billyhargrove.ru"]);
  assert.deepEqual(loaded.config.preview?.allowedHosts, ["billyhargrove.ru"]);
});
