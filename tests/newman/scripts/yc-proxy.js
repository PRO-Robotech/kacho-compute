#!/usr/bin/env node
/*
 * yc-proxy.js — локальный reverse-proxy для прогона newman-сьюты kacho-compute против РЕАЛЬНОГО Yandex Cloud.
 *
 * Зачем: коллекции бьют единый `{{baseUrl}}` (наш api-gateway отдаёт под одним хостом и /compute, и
 * /operations, без auth — dev-mode). У YC: compute, операции, resource-manager — РАЗНЫЕ хосты, и нужен
 * `Authorization: Bearer <IAM>` на каждом запросе. Прокси: `baseUrl=http://localhost:18081` →
 *   /compute/v1/*       → https://compute.api.cloud.yandex.net
 *   /operations/*       → https://operation.api.cloud.yandex.net
 *   /resource-manager/* → https://resource-manager.api.cloud.yandex.net
 *   /vpc/v1/*           → https://vpc.api.cloud.yandex.net   (на случай если кейсу нужны peer-ресурсы)
 * + подставляет Bearer-токен (через `yc iam create-token`, кешируется, рефрешится).
 *
 * Usage:  node tests/newman/scripts/yc-proxy.js [--port 18081]
 *         (нужен сконфигурированный `yc` CLI; токен берётся `yc iam create-token`)
 */
'use strict';
const http = require('http');
const { execSync } = require('child_process');

const PORT = (() => { const i = process.argv.indexOf('--port'); return i >= 0 ? parseInt(process.argv[i + 1], 10) : 18081; })();
const ROUTES = [
  ['/compute/v1/',       'https://compute.api.cloud.yandex.net'],
  ['/operations/',       'https://operation.api.cloud.yandex.net'],
  ['/resource-manager/', 'https://resource-manager.api.cloud.yandex.net'],
  ['/vpc/v1/',           'https://vpc.api.cloud.yandex.net'],
];
const TOKEN_TTL_MS = 10 * 60 * 1000; // YC IAM-токен живёт ~12ч; рефрешим раз в 10 мин с запасом

let tokenCache = { value: null, ts: 0 };
function iamToken() {
  if (tokenCache.value && Date.now() - tokenCache.ts < TOKEN_TTL_MS) return tokenCache.value;
  const t = execSync('yc iam create-token', { encoding: 'utf8' }).trim();
  tokenCache = { value: t, ts: Date.now() };
  return t;
}

function pickUpstream(path) {
  for (const [prefix, host] of ROUTES) if (path.startsWith(prefix)) return host;
  return null;
}

const server = http.createServer((req, res) => {
  const u = new URL(req.url, 'http://x');
  const upstreamHost = pickUpstream(u.pathname);
  if (!upstreamHost) { res.writeHead(404, { 'content-type': 'application/json' }); res.end(JSON.stringify({ message: `yc-proxy: no route for ${u.pathname}` })); return; }

  const chunks = [];
  req.on('data', c => chunks.push(c));
  req.on('end', async () => {
    const body = Buffer.concat(chunks);
    let token;
    try { token = iamToken(); } catch (e) { res.writeHead(502, { 'content-type': 'application/json' }); res.end(JSON.stringify({ message: 'yc-proxy: cannot get IAM token: ' + e.message })); return; }
    const headers = { 'authorization': 'Bearer ' + token };
    if (req.headers['content-type']) headers['content-type'] = req.headers['content-type'];
    if (req.headers['accept']) headers['accept'] = req.headers['accept'];
    const init = { method: req.method, headers };
    if (body.length && req.method !== 'GET' && req.method !== 'HEAD') init.body = body;
    try {
      const r = await fetch(upstreamHost + u.pathname + u.search, init);
      const buf = Buffer.from(await r.arrayBuffer());
      const ct = r.headers.get('content-type') || 'application/json';
      res.writeHead(r.status, { 'content-type': ct });
      res.end(buf);
    } catch (e) {
      res.writeHead(502, { 'content-type': 'application/json' });
      res.end(JSON.stringify({ message: 'yc-proxy: upstream error: ' + e.message }));
    }
  });
});

server.listen(PORT, '127.0.0.1', () => {
  try { iamToken(); } catch (e) { console.error('WARN: предварительный yc iam create-token не удался:', e.message); }
  console.log(`yc-proxy слушает http://127.0.0.1:${PORT}  →  ${ROUTES.map(r => r[0] + '* → ' + r[1]).join('  |  ')}`);
});
process.on('SIGINT', () => { server.close(() => process.exit(0)); });
process.on('SIGTERM', () => { server.close(() => process.exit(0)); });
