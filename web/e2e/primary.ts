// A primary for one test: the real binary started twice, as the agent and as the manager, in the test's own
// directory with its own HOME, and no container engine anywhere (DOCKER_HOST points at nowhere).
import { test as base } from '@playwright/test';
import { type ChildProcess, execFile, spawn } from 'node:child_process';
import { mkdtemp, rm } from 'node:fs/promises';
import net from 'node:net';
import { tmpdir } from 'node:os';
import path from 'node:path';
import { promisify } from 'node:util';

const run = promisify(execFile);
const here = path.dirname(new URL(import.meta.url).pathname);
const bin = process.env.HOMERSLAB_BIN ?? path.join(here, '..', '..', 'bin', 'homerslab');
const sysroot = path.join(here, '..', '..', 'testdata', 'primary');

export type Link = { url: string; localUrl: string; expiresAt: string };

export class Primary {
  private agent?: ChildProcess;
  private manager?: ChildProcess;
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
    child.stderr?.on('data', (b: Buffer) => process.env.FLOW_LOGS && process.stderr.write(`${args[0]}: ${b}`));
    return child;
  }

  async startManager(): Promise<void> {
    this.manager = this.spawn('manager', '--db', this.at('manager.db'), '--agent-socket', this.at('agent.sock'),
      '--control-socket', this.at('manager.sock'), '--listen', '127.0.0.1:0');
    await waitForSocket(this.at('manager.sock'));
  }

  // An interruption: the manager stops, as after a power cut, and starts again on the same database.
  async restartManager(): Promise<void> {
    await stop(this.manager);
    await this.startManager();
  }

  // What the installer does: ask the manager, through the CLI, for a one-time link.
  async newLink(): Promise<Link> {
    const { stdout } = await run(bin, ['setup', 'link', '--json', '--control-socket', this.at('manager.sock')], { env: this.env });
    return JSON.parse(stdout) as Link;
  }

  async stop(): Promise<void> {
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
