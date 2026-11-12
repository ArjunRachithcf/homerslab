// A primary for one test: the real binary started twice, as the agent and as the manager, in the test's own
// directory with its own HOME, and no container engine anywhere (DOCKER_HOST points at nowhere).
import { test as base } from '@playwright/test';
import { type ChildProcess, execFile, spawn } from 'node:child_process';
import { mkdtemp, rm } from 'node:fs/promises';
import http from 'node:http';
import net from 'node:net';
import { tmpdir } from 'node:os';
import path from 'node:path';
import { DatabaseSync } from 'node:sqlite';
import { promisify } from 'node:util';
import { bin, sysroot } from './paths';

const run = promisify(execFile);

export type Link = { url: string; localUrl: string; expiresAt: string };

export class Primary {
  private agent?: ChildProcess;
  private manager?: ChildProcess;
  private edge?: http.Server;
  private readonly logs: string[] = [];
  private constructor(readonly dir: string, private readonly env: NodeJS.ProcessEnv) {}

  static async start(): Promise<Primary> {
    const dir = await mkdtemp(path.join(tmpdir(), 'homerslab-flow-'));
    const env = { PATH: process.env.PATH, HOME: dir, DOCKER_HOST: 'unix:///nonexistent', CONTAINER_HOST: 'unix:///nonexistent' };
    const p = new Primary(dir, env);
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
  static readonly adminName = 'admin.ferreira.family';

  /**
   * Where the browser opens Admin. The edge isn't part of these tests, so a small server stands in for it: it passes
   * the browser's requests on to Admin's socket as the edge does, at Admin's name over HTTPS (Host, and the page's
   * own Origin), and Admin's answers back.
   */
  async adminURL(): Promise<string> {
    if (!this.edge) {
      const edge = http.createServer((req, res) => {
        const own = `http://${req.headers.host ?? ''}`;
        const headers = { ...req.headers, host: Primary.adminName };
        if (headers.origin === own) headers.origin = `https://${Primary.adminName}`;
        const up = http.request({ socketPath: this.at('admin.sock'), path: req.url, method: req.method, headers }, (r) => {
          res.writeHead(r.statusCode ?? 502, r.headers);
          r.pipe(res);
        });
        up.on('error', () => res.writeHead(502).end());
        req.pipe(up);
      });
      await new Promise<void>((resolve) => edge.listen(0, '127.0.0.1', resolve));
      this.edge = edge;
    }
    const { port } = this.edge.address() as net.AddressInfo;
    return `http://127.0.0.1:${port}/store`;
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
        headers: { 'Content-Type': 'application/json', ...(token ? { Authorization: `Bearer ${token}` } : {}) },
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

export const test = base.extend<{ primary: Primary }>({
  // eslint-disable-next-line no-empty-pattern -- Playwright reads a fixture's dependencies from this pattern
  primary: async ({}, use) => {
    const p = await Primary.start();
    await use(p);
    await p.stop();
  },
});
export { expect } from '@playwright/test';
