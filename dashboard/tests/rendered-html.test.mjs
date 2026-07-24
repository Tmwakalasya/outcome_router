import assert from "node:assert/strict";
import { access, readFile } from "node:fs/promises";
import test from "node:test";

const root = new URL("../", import.meta.url);

async function render() {
  const workerUrl = new URL("../dist/server/index.js", import.meta.url);
  workerUrl.searchParams.set("test", `${process.pid}-${Date.now()}`);
  const { default: worker } = await import(workerUrl.href);
  return worker.fetch(
    new Request("http://localhost/", { headers: { accept: "text/html" } }),
    { ASSETS: { fetch: async () => new Response("Not found", { status: 404 }) } },
    { waitUntil() {}, passThroughOnException() {} },
  );
}

test("server-renders the Outcome Router audit dashboard", async () => {
  const response = await render();
  assert.equal(response.status, 200);
  const html = await response.text();
  assert.match(html, /<title>Outcome Router — Production Routing Audit<\/title>/i);
  assert.match(html, /Quality is holding/);
  assert.match(html, /Verified gross savings/);
  assert.match(html, /Cost-quality frontier/);
  assert.match(html, /Decision ledger/);
  assert.doesNotMatch(html, /codex-preview|Your site is taking shape|react-loading-skeleton/i);
});

test("starter preview is removed and metadata is product-specific", async () => {
  await assert.rejects(access(new URL("app/_sites-preview", root)));
  const [page, layout, packageJson] = await Promise.all([
    readFile(new URL("app/page.tsx", root), "utf8"),
    readFile(new URL("app/layout.tsx", root), "utf8"),
    readFile(new URL("package.json", root), "utf8"),
  ]);
  assert.match(page, /\/v1\/audit\/summary/);
  assert.match(page, /Outcome<span>Router/);
  assert.match(layout, /Production Routing Audit/);
  assert.doesNotMatch(packageJson, /react-loading-skeleton/);
});
