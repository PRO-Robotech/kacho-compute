#!/usr/bin/env node
/*
 * run-incremental.js — прогон newman-сьюты kacho-compute ПО ОДНОМУ КЕЙСУ за раз.
 *
 * Зачем: kacho-compute (как и YC) quota-guarded — пачка кейсов разом создаёт сотни
 * ресурсов одновременно и упирается в quota cap. Здесь: один кейс → результат →
 * (если упал — зачистка тест-папок) → следующий. Низкий resource-footprint в любой момент.
 * Один newman-процесс (library API) — без per-case process startup, поэтому быстро.
 *
 * Кейсы самоочищаются (cleanup-step в конце), но не все полностью (некоторые `*-CR-CRUD-*`
 * оставляют pre-disk / pre-image) — поэтому periodic-cleanup каждые N кейсов + final cleanup
 * + initial cleanup (стереть накопленный мусор от прошлых прогонов).
 *
 * Usage:
 *   tests/newman/scripts/run-incremental.sh                 # все кейсы, с нуля
 *   tests/newman/scripts/run-incremental.sh --resume        # продолжить (пропустить уже сделанные)
 *   tests/newman/scripts/run-incremental.sh --service disk  # только один ресурс
 *   tests/newman/scripts/run-incremental.sh --cleanup-only  # только зачистить тест-папки и выйти
 *   ENV=environments/yc.postman_environment.json ... .sh    # другой env-файл
 *   CLEANUP_EVERY=25  DELAY_REQUEST=30  ... .sh             # тюнинг
 *
 * Outputs (tests/newman/out/incremental/):
 *   progress.tsv  — case-id \t PASS|FAIL \t assertions \t failed \t requests \t ms
 *   failed/<id>.json — newman summary упавшего кейса (для разбора)
 *   summary.txt   — итоговая сводка
 */
'use strict';
const fs = require('fs');
const path = require('path');
const newman = require('newman');

const ROOT = path.resolve(__dirname, '..');
const ENV_FILE = path.join(ROOT, process.env.ENV || 'environments/local.postman_environment.json');
const COLLECTIONS_DIR = path.join(ROOT, 'collections');
const OUT = path.join(ROOT, 'out/incremental');
const PROGRESS = path.join(OUT, 'progress.tsv');
const SUMMARY = path.join(OUT, 'summary.txt');
const CLEANUP_EVERY = parseInt(process.env.CLEANUP_EVERY || '25', 10);
const DELAY_REQUEST = parseInt(process.env.DELAY_REQUEST || '30', 10);
const ALL_RESOURCES_DEFAULT = ['disk', 'image', 'snapshot', 'instance', 'disk-type', 'zone', 'operation'];
const ALL_RESOURCES = process.env.SERVICES ? process.env.SERVICES.split(/[\s,]+/).filter(Boolean) : ALL_RESOURCES_DEFAULT;

const args = process.argv.slice(2);
const RESUME = args.includes('--resume');
const CLEANUP_ONLY = args.includes('--cleanup-only');
const FAILED_ONLY = args.includes('--failed'); // прогнать только кейсы, помеченные FAIL в текущем progress.tsv (точечный re-run после фикса)
const svcIdx = args.indexOf('--service');
const ONLY_RESOURCE = svcIdx >= 0 ? args[svcIdx + 1] : null;
const casesIdx = args.indexOf('--cases');
// явный список case-id'ов: --cases C1,C2,... или env CASES="C1 C2 ..." (приоритет над --service/SERVICES)
let ONLY_CASES = null;
{
  const raw = (casesIdx >= 0 ? args[casesIdx + 1] : '') || process.env.CASES || '';
  const ids = raw.split(/[\s,]+/).filter(Boolean);
  if (ids.length) ONLY_CASES = new Set(ids);
}

fs.mkdirSync(path.join(OUT, 'failed'), { recursive: true });

// --- env ---
const env = JSON.parse(fs.readFileSync(ENV_FILE, 'utf8'));
const ev = (k) => { const v = env.values.find(x => x.key === k); return v ? v.value : undefined; };
const BASE = (ev('baseUrl') || '').replace(/\/$/, '');
const TEST_FOLDERS = env.values.filter(x => /^existingFolder/.test(x.key)).map(x => x.value).filter(Boolean);
if (!BASE) { console.error('нет baseUrl в env'); process.exit(2); }
if (!TEST_FOLDERS.length) { console.error('нет existingFolder* в env'); process.exit(2); }

const sleep = (ms) => new Promise(r => setTimeout(r, ms));

// --- cleanup тест-папок (FK-safe порядок; async-Delete'ы fire-and-forget, несколько проходов) ---
// instances первыми (Disk нельзя удалить пока attached); затем snapshots/images (не блокируют);
// затем disks. Disk.Delete тоже async (Operation) — несколько проходов с паузой дают воркерам
// додавить detach+delete.
// [restPath, listKey]
const KINDS = [
  ['instances', 'instances'],
  ['snapshots', 'snapshots'],
  ['images',    'images'],
  ['disks',     'disks'],
];
async function jget(url) {
  try { const r = await fetch(url); if (!r.ok) return null; return await r.json(); } catch { return null; }
}
async function jdel(url) {
  try { await fetch(url, { method: 'DELETE' }); } catch { /* ignore */ }
}
async function cleanupPass(passes = 3) {
  let removedTotal = 0;
  for (let p = 0; p < passes; p++) {
    let removed = 0;
    for (const fid of TEST_FOLDERS) {
      for (const [restPath, listKey] of KINDS) {
        const j = await jget(`${BASE}/compute/v1/${restPath}?folderId=${encodeURIComponent(fid)}&pageSize=1000`);
        const arr = (j && Array.isArray(j[listKey])) ? j[listKey] : [];
        for (const r of arr) {
          if (!r || !r.id) continue;
          // Disk attached к Instance — Delete вернёт Operation с error; всё равно пробуем,
          // на следующем проходе (после Instance.Delete) пройдёт.
          await jdel(`${BASE}/compute/v1/${restPath}/${encodeURIComponent(r.id)}`);
          removed++;
        }
      }
    }
    removedTotal += removed;
    if (removed === 0) break;
    await sleep(2000);
  }
  return removedTotal;
}
async function remainingCount() {
  let n = 0;
  for (const fid of TEST_FOLDERS) for (const [restPath, listKey] of KINDS) {
    const j = await jget(`${BASE}/compute/v1/${restPath}?folderId=${encodeURIComponent(fid)}&pageSize=1000`);
    const arr = (j && Array.isArray(j[listKey])) ? j[listKey] : [];
    n += arr.length;
  }
  return n;
}

// --- enumerate cases (top-level folders в каждой коллекции) ---
function failedFromProgress() {
  if (!fs.existsSync(PROGRESS)) return new Set();
  const s = new Set();
  for (const line of fs.readFileSync(PROGRESS, 'utf8').split('\n')) {
    const [id, st] = line.split('\t');
    if (id && st === 'FAIL') s.add(id);
  }
  return s;
}
function enumerateCases() {
  // ONLY_CASES имеет приоритет: ищем эти case-id во всех коллекциях.
  const resources = ONLY_CASES ? ALL_RESOURCES : (ONLY_RESOURCE ? [ONLY_RESOURCE] : ALL_RESOURCES);
  const failedSet = FAILED_ONLY ? failedFromProgress() : null;
  const cases = [];
  for (const res of resources) {
    const col = path.join(COLLECTIONS_DIR, `${res}.postman_collection.json`);
    if (!fs.existsSync(col)) { console.error(`[skip] нет коллекции ${res}`); continue; }
    const c = JSON.parse(fs.readFileSync(col, 'utf8'));
    for (const item of (c.item || [])) {
      const name = item.name;
      const id = name.split(' — ')[0].split(' - ')[0].trim() || name;
      if (ONLY_CASES && !ONLY_CASES.has(id)) continue;
      if (failedSet && !failedSet.has(id)) continue;
      cases.push({ res, collection: col, folderName: name, id });
    }
  }
  return cases;
}

// --- run one folder через newman library ---
function runFolder(tc) {
  return new Promise((resolve) => {
    const t0 = Date.now();
    newman.run({
      collection: tc.collection,
      environment: ENV_FILE,
      folder: tc.folderName,
      delayRequest: DELAY_REQUEST,
      reporters: [],
      bail: false,
    }, (err, summary) => {
      const ms = Date.now() - t0;
      const a = (summary && summary.run && summary.run.stats && summary.run.stats.assertions) || { total: 0, failed: 0 };
      const rq = (summary && summary.run && summary.run.stats && summary.run.stats.requests) || { total: 0 };
      const runErrs = (summary && summary.run && summary.run.failures) || [];
      const failed = a.failed + (err ? 1 : 0);
      const status = (err || a.failed > 0 || runErrs.length > 0) ? 'FAIL' : 'PASS';
      resolve({ status, assertions: a.total, failed, requests: rq.total, ms, err: err && err.message, failures: runErrs.map(f => ({ name: f.source && f.source.name, err: f.error && f.error.message })) });
    });
  });
}

// --- main ---
(async () => {
  if (CLEANUP_ONLY) {
    console.log(`[cleanup-only] зачистка ${TEST_FOLDERS.length} тест-папок на ${BASE} ...`);
    const n = await cleanupPass(5);
    const left = await remainingCount();
    console.log(`[cleanup-only] удалено ~${n}, осталось ${left}`);
    process.exit(left === 0 ? 0 : 1);
  }

  const cases = enumerateCases();
  const done = new Set();
  // --failed / --cases: точечный re-run — не затираем progress.tsv (дополняем).
  const APPEND_PROGRESS = RESUME || FAILED_ONLY || !!ONLY_CASES;
  if (RESUME && fs.existsSync(PROGRESS)) {
    for (const line of fs.readFileSync(PROGRESS, 'utf8').split('\n')) { const id = line.split('\t')[0]; if (id) done.add(id); }
    console.log(`[resume] уже сделано: ${done.size}`);
  } else if (!APPEND_PROGRESS) {
    fs.writeFileSync(PROGRESS, '');
  }

  console.log(`[incremental] ${cases.length} кейсов; env=${path.basename(ENV_FILE)}; base=${BASE}; cleanup каждые ${CLEANUP_EVERY}`);
  process.stdout.write('[initial cleanup] ');
  const ic = await cleanupPass(5);
  console.log(`удалено накопленного мусора ~${ic}, осталось ${await remainingCount()}`);

  let nRun = 0, nPass = 0, nFail = 0, totA = 0, totF = 0, sinceClean = 0;
  const failedCases = [];
  const t0 = Date.now();
  for (const tc of cases) {
    if (done.has(tc.id)) continue;
    const r = await runFolder(tc);
    nRun++; sinceClean++; totA += r.assertions; totF += r.failed;
    if (r.status === 'PASS') { nPass++; }
    else {
      nFail++; failedCases.push(tc.id);
      fs.writeFileSync(path.join(OUT, 'failed', `${tc.id}.json`), JSON.stringify({ case: tc.id, ...r }, null, 2));
      await cleanupPass(4); sinceClean = 0;
    }
    fs.appendFileSync(PROGRESS, `${tc.id}\t${r.status}\t${r.assertions}\t${r.failed}\t${r.requests}\t${r.ms}\n`);
    if (sinceClean >= CLEANUP_EVERY) { await cleanupPass(3); sinceClean = 0; }
    if (nRun % 20 === 0 || r.status === 'FAIL') {
      const el = ((Date.now() - t0) / 1000).toFixed(0);
      console.log(`[${nRun}/${cases.length}] pass=${nPass} fail=${nFail} assertions=${totA} (failed ${totF}) | ${el}s | last: ${tc.id} ${r.status}${r.status === 'FAIL' ? ' :: ' + (r.failures.map(f => f.name + ': ' + f.err).join('; ') || r.err) : ''}`);
    }
  }
  process.stdout.write('[final cleanup] ');
  const fc = await cleanupPass(5); const left = await remainingCount();
  console.log(`удалено ~${fc}, осталось ${left}`);

  const el = ((Date.now() - t0) / 1000).toFixed(0);
  const summary = [
    `===== run-incremental: ${nRun} кейсов за ${el}s =====`,
    `pass=${nPass}  fail=${nFail}  assertions=${totA}  failed-assertions=${totF}`,
    `тест-папки после прогона: осталось ${left} ресурсов (должно быть 0)`,
    failedCases.length ? `FAILED CASES (${failedCases.length}): ${failedCases.join(', ')}` : 'все кейсы зелёные',
    `детали упавших — out/incremental/failed/*.json; полный прогресс — out/incremental/progress.tsv`,
  ].join('\n');
  fs.writeFileSync(SUMMARY, summary + '\n');
  console.log('\n' + summary);
  process.exit(nFail === 0 && left === 0 ? 0 : 1);
})().catch(e => { console.error('FATAL', e); process.exit(2); });
