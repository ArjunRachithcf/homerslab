// A primary for one test: the real binary started twice, as the agent and as the manager, in the test's own
// directory with its own HOME, and no container engine anywhere (DOCKER_HOST points at nowhere).
import { test as base } from '@playwright/test';
import { type ChildProcess, execFile, spawn } from 'node:child_process';
import { createHash, randomBytes } from 'node:crypto';
import { mkdtemp, rm } from 'node:fs/promises';
import http from 'node:http';
import https from 'node:https';
import net from 'node:net';
import { tmpdir } from 'node:os';
import path from 'node:path';
import { DatabaseSync } from 'node:sqlite';
import { promisify } from 'node:util';
import { edgeArgs, edgeCertificate, type EdgeTLS } from './edge';
import { bin, sysroot } from './paths';

const run = promisify(execFile);

export type Link = { url: string; localUrl: string; expiresAt: string };
/** A request the edge's stand-in passed on or answered: whether it carried Admin's session cookie, and its answer. */
export type Seen = { host: string; method: string; path: string; session: boolean; status: number };

// Admin's cookies, as Admin sets them (internal/manager/adminaccount.go): __Host- prefixed, so Chromium takes them
// only over HTTPS, Secure, at Path=/ and with no Domain.
const sessionCookie = '__Host-homerslab-session';
const signInCookie = '__Host-homerslab-sign-in';
const sessionLife = 12 * 3600;

function hostCookie(name: string, value: string, maxAge: number): string {
  return `${name}=${value}; Path=/; Max-Age=${maxAge}; HttpOnly; Secure; SameSite=Lax`;
}

function cookiesOf(req: http.IncomingMessage): Record<string, string> {
  const out: Record<string, string> = {};
  for (const part of (req.headers.cookie ?? '').split(';')) {
    const i = part.indexOf('=');
    if (i > 0) out[part.slice(0, i).trim()] = part.slice(i + 1).trim();
  }
  return out;
}

export class Primary {
  private agent?: ChildProcess;
  private manager?: ChildProcess;
  private edge?: http.Server;
  private readonly logs: string[] = [];
  private constructor(readonly dir: string, private readonly env: NodeJS.ProcessEnv, private readonly tls: EdgeTLS) {}

  static async start(tls: EdgeTLS): Promise<Primary> {
    const dir = await mkdtemp(path.join(tmpdir(), 'homeward-flow-'));
    const env = { PATH: process.env.PATH, HOME: dir, DOCKER_HOST: 'unix:///nonexistent', CONTAINER_HOST: 'unix:///nonexistent' };
    const p = new Primary(dir, env, tls);
    p.agent = p.spawn('agent', '--socket', p.at('agent.sock'), '--socket-group', String(process.getgid?.() ?? 0), '--sysroot', sysroot,
      '--state-dir', p.at('agent-state'));
    await waitForSocket(p.at('agent.sock'));
    await p.startManager();
    return p;
  }

  at(name: string): string {
    return path.join(this.dir, name);
  }

  private spawn(...args: string[]): ChildProcess {
    const child = spawn(bin, args, { env: this.env, stdio: ['ignore', 'ignore', 'pipe'] });
    child.stderr?.on('data', (b: Buffer) => {
      this.logs.push(`${args[0]}: ${b}`);
      if (process.env.FLOW_LOGS) process.stderr.write(`${args[0]}: ${b}`);
    });
    return child;
  }

  async startManager(): Promise<void> {
    this.manager = this.spawn('manager', '--db', this.at('manager.db'), '--agent-socket', this.at('agent.sock'),
      '--control-socket', this.at('manager.sock'), '--listen', '127.0.0.1:0', '--admin-socket', this.at('admin.sock'));
    await waitForSocket(this.at('manager.sock'));
    await this.waitForManager();
  }

  // The control socket listens before the manager has opened its database: wait until the manager answers on it.
  private async waitForManager(): Promise<void> {
    for (let i = 0; i < 200; i++) {
      const status = await new Promise<number>((resolve) => {
        http.get({ socketPath: this.at('manager.sock'), path: '/api/v1/setup' }, (r) => { r.resume(); resolve(r.statusCode ?? 0); })
          .on('error', () => resolve(0));
      });
      if (status === 200) return;
      await new Promise((r) => setTimeout(r, 25));
    }
    throw new Error('the manager never answered on its control socket');
  }

  // An interruption: the manager stops, as after a power cut, and starts again on the same database.
  async restartManager(): Promise<void> {
    await this.stopManager();
    await this.startManager();
  }

  // The agent stops: the manager carries on without the machine's facts.
  async stopAgent(): Promise<void> {
    await stop(this.agent);
    this.agent = undefined;
  }

  async stopManager(): Promise<void> {
    await stop(this.manager);
    this.manager = undefined;
  }

  // What the installer does: ask the manager, through the CLI, for a one-time link.
  async newLink(): Promise<Link> {
    const { stdout } = await run(bin, ['setup', 'link', '--json', '--control-socket', this.at('manager.sock')], { env: this.env });
    return JSON.parse(stdout) as Link;
  }

  // The CLI on this machine, against this primary's manager.
  async cli(...args: string[]): Promise<string> {
    const { stdout } = await run(bin, [...args, '--control-socket', this.at('manager.sock')], { env: this.env });
    return stdout;
  }

  // Admin's name: the household's domain, as the domain step leaves it (withDomain stands in for that step).
  static readonly domain = 'ferreira.family';
  static readonly adminName = `admin.${Primary.domain}`;

  /** What reached the edge's stand-in, and how it was answered: for the tests that look at what the browser sent. */
  readonly seen: Seen[] = [];
  private readonly attempts = new Set<string>();
  private readonly codes = new Map<string, string>();

  /** An address at one of the household's names, through the edge's stand-in. */
  url(host: string, p = '/'): string {
    if (!this.edge) throw new Error('the edge’s stand-in starts with adminURL()');
    const { port } = this.edge.address() as net.AddressInfo;
    return `https://${host}.${Primary.domain}:${port}${p}`;
  }

  /**
   * Where the browser opens Admin. The edge isn't part of these tests, so a small server stands in for it: over HTTPS
   * at the household's names, it passes the browser's requests for Admin's name on to Admin's socket, as they came,
   * and Admin's answers back. Admin's own sign-in can't run here (its client's secret is sealed by the agent as root,
   * and the sign-in's server sits at its fixed address), so the stand-in also stands in for the sign-in: Admin's
   * /auth/sign-in and /auth/callback, and the household account's authorize, which signs Inês in. The session cookie
   * is the browser's own, from that sign-in, as Admin sets it; the stand-in never adds one.
   */
  async adminURL(): Promise<string> {
    if (!this.edge) {
      const edge = https.createServer({ key: this.tls.key, cert: this.tls.cert }, (req, res) => this.serveEdge(req, res));
      await new Promise<void>((resolve) => edge.listen(0, '127.0.0.1', resolve));
      this.edge = edge;
    }
    return this.url('admin', '/store');
  }

  private serveEdge(req: http.IncomingMessage, res: http.ServerResponse): void {
    const host = (req.headers.host ?? '').replace(/:\d+$/, '');
    const u = new URL(req.url ?? '/', 'https://edge');
    const cookies = cookiesOf(req);
    res.on('finish', () => this.seen.push({ host, method: req.method ?? '', path: u.pathname,
      session: sessionCookie in cookies, status: res.statusCode }));
    if (host === Primary.adminName && u.pathname === '/auth/sign-in') {
      const state = randomBytes(16).toString('base64url');
      this.attempts.add(state);
      const back = this.url('admin', '/auth/callback');
      res.writeHead(303, { 'Set-Cookie': hostCookie(signInCookie, state, 600),
        Location: this.url('auth', `/application/o/authorize/?state=${state}&redirect_uri=${encodeURIComponent(back)}`) }).end();
    } else if (host === Primary.adminName && u.pathname === '/auth/callback') {
      const state = u.searchParams.get('state') ?? '';
      const who = this.codes.get(u.searchParams.get('code') ?? '');
      this.codes.delete(u.searchParams.get('code') ?? '');
      if (!this.attempts.delete(state) || cookies[signInCookie] !== state || !who) {
        res.writeHead(403).end();
        return;
      }
      res.writeHead(303, { 'Set-Cookie': [hostCookie(signInCookie, '', -1), hostCookie(sessionCookie, this.adminSession(who), sessionLife)],
        Location: '/' }).end(); // Admin's start; Admin's own sends an admin to /account/ until setup's last step is done
    } else if (host === Primary.adminName) {
      const up = http.request({ socketPath: this.at('admin.sock'), path: req.url, method: req.method, headers: req.headers }, (r) => {
        res.writeHead(r.statusCode ?? 502, r.headers);
        r.pipe(res);
      });
      up.on('error', () => (res.headersSent ? res.destroy() : res.writeHead(502).end()));
      req.pipe(up);
    } else if (host === `auth.${Primary.domain}` && u.pathname === '/application/o/authorize/') {
      const back = u.searchParams.get('redirect_uri') ?? '';
      if (back !== this.url('admin', '/auth/callback')) {
        res.writeHead(400).end('not Admin’s client');
        return;
      }
      const code = randomBytes(16).toString('base64url');
      this.codes.set(code, 'ines');
      res.writeHead(302, { Location: `${back}?code=${code}&state=${encodeURIComponent(u.searchParams.get('state') ?? '')}` }).end();
    } else if (host === `pdf.${Primary.domain}`) {
      // One of the household's apps: another origin, on the same site as Admin.
      res.writeHead(200, { 'Content-Type': 'text/html; charset=utf-8' }).end('<!doctype html><html lang="en"><title>BentoPDF</title><h1>BentoPDF</h1>');
    } else {
      res.writeHead(421).end();
    }
  }

  // An Admin session, as Admin opens one after a sign-in with the household account: only its token's hash is kept.
  private adminSession(username: string): string {
    const token = randomBytes(32).toString('base64url');
    const db = new DatabaseSync(this.at('manager.db'));
    try {
      db.exec('PRAGMA busy_timeout = 5000');
      db.prepare(`INSERT INTO admin_session (token_hash, username, created_at, expires_at) VALUES (?, ?, ?, ?)`)
        .run(createHash('sha256').update(token).digest(), username, Date.now() - 60_000, Date.now() + sessionLife * 1000);
    } finally {
      db.close();
    }
    return token;
  }

  // How many install plans the manager has made.
  plans(): number {
    const db = new DatabaseSync(this.at('manager.db'));
    try {
      db.exec('PRAGMA busy_timeout = 5000');
      return (db.prepare('SELECT count(*) AS n FROM plan').get() as { n: number }).n;
    } finally {
      db.close();
    }
  }

  // Stands in for setup's domain step, which setup reaches only after the sign-in step (#17): its answer, as the
  // step keeps it in the manager's database.
  withDomain(): void {
    const db = new DatabaseSync(this.at('manager.db'));
    try {
      db.exec('PRAGMA busy_timeout = 5000');
      db.prepare(`INSERT INTO setup_answer (key, value) VALUES ('domain', 'ferreira.family')
        ON CONFLICT (key) DO UPDATE SET value = excluded.value`).run();
    } finally {
      db.close();
    }
  }

  // How many setup sessions the manager's database keeps open.
  openSessions(): number {
    const db = new DatabaseSync(this.at('manager.db'));
    try {
      db.exec('PRAGMA busy_timeout = 5000');
      const row = db.prepare('SELECT count(*) AS n FROM setup_session WHERE revoked_at IS NULL').get() as { n: number };
      return row.n;
    } finally {
      db.close();
    }
  }

  // First-time setup's built steps, answered through the setup page's API: the Ferreira household, Inês its admin.
  async setUp(names: { you?: string; household?: string } = {}): Promise<void> {
    const link = new URL((await this.newLink()).localUrl);
    const key = new URLSearchParams(link.hash.slice(1)).get('k');
    const api = async (method: string, path: string, body: unknown, token?: string) => {
      const resp = await fetch(new URL(path, link), {
        method, body: JSON.stringify(body),
        // A step answered for the first time is at revision 0.
        headers: { 'Content-Type': 'application/json', ...(method === 'PUT' ? { 'If-Match': '"0"' } : {}), ...(token ? { Authorization: `Bearer ${token}` } : {}) },
      });
      if (!resp.ok) throw new Error(`${method} ${path}: ${resp.status} ${await resp.text()}`);
      return resp.json() as Promise<{ token?: string }>;
    };
    const { token } = await api('POST', '/api/v1/setup/claim', { key, nonce: 'a-tab-of-its-own-0123456789' });
    await api('PUT', '/api/v1/setup/steps/welcome', {}, token);
    await api('PUT', '/api/v1/setup/steps/you', { name: names.you ?? 'Inês Ferreira', mail: 'ines@ferreira.family' }, token);
    await api('PUT', '/api/v1/setup/steps/household', { name: names.household ?? 'The Ferreira household' }, token);
    this.withDomain();
  }

  // What the agent and the manager have logged so far.
  log(): string {
    return this.logs.join('');
  }

  async stop(): Promise<void> {
    this.edge?.close();
    await Promise.all([stop(this.manager), stop(this.agent)]);
    await rm(this.dir, { recursive: true, force: true });
  }
}

async function stop(child?: ChildProcess): Promise<void> {
  if (!child || child.exitCode !== null) return;
  const exited = new Promise((resolve) => child.once('exit', resolve));
  child.kill('SIGTERM');
  await exited;
}

async function waitForSocket(sock: string): Promise<void> {
  for (let i = 0; i < 200; i++) {
    const ok = await new Promise<boolean>((resolve) => {
      const c = net.connect(sock, () => { c.end(); resolve(true); });
      c.on('error', () => resolve(false));
    });
    if (ok) return;
    await new Promise((r) => setTimeout(r, 25));
  }
  throw new Error(`nothing listens on ${sock}`);
}

export const test = base.extend<{ primary: Primary }, { edgeTLS: EdgeTLS }>({
  // eslint-disable-next-line no-empty-pattern -- Playwright reads a fixture's dependencies from this pattern
  edgeTLS: [async ({}, use) => use(await edgeCertificate(Primary.domain)), { scope: 'worker' }],
  // The household's names lead to the edge's stand-in, whose certificate Chromium trusts.
  launchOptions: [async ({ edgeTLS }, use) => use({ args: edgeArgs(Primary.domain, edgeTLS) }), { scope: 'worker' }],
  primary: async ({ edgeTLS }, use) => {
    const p = await Primary.start(edgeTLS);
    await use(p);
    await p.stop();
  },
});
export { expect } from '@playwright/test';
