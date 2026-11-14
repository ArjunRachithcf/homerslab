// Home for one worker of the flow tests (#21): the real binary as Home's process, on its socket, reading a projection
// this file writes as the manager would; a stand-in for the edge, over HTTPS at the household's names (*.ferreira.family,
// a certificate of its own that Chromium is told to trust); a stand-in for the household account's sign-in, an OIDC
// issuer that signs in whoever the test says; and the household's apps, as pages of their own. No container engine
// anywhere, its own HOME, nothing of the manager's or the agent's.
import { test as base } from '@playwright/test';
import { type ChildProcess, execFile, spawn } from 'node:child_process';
import { createHash, generateKeyPairSync, randomBytes, sign, X509Certificate, type KeyObject } from 'node:crypto';
import { mkdtemp, readFile, rename, rm, writeFile } from 'node:fs/promises';
import http from 'node:http';
import https from 'node:https';
import net from 'node:net';
import { tmpdir } from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { promisify } from 'node:util';
import { bin } from './paths';

const catalog = path.join(path.dirname(fileURLToPath(import.meta.url)), '..', '..', 'catalogs', 'official');

const run = promisify(execFile);

export const domain = 'ferreira.family';
export type Person = { username: string; name: string; groups: string[] };
export const ines: Person = { username: 'ines', name: 'Inês Ferreira', groups: ['homerslab-admins'] };
export const rui: Person = { username: 'rui', name: 'Rui Ferreira', groups: ['homerslab-adults'] };
export const tomas: Person = { username: 'tomas', name: 'Tomás Ferreira', groups: ['homerslab-kids'] };

export type App = { id: string; name: string; purpose: string; category: string; host: string; audience: string[]; health: string; icon?: string };

/** The Ferreira household's apps: for three audiences, in three states. */
export const apps: App[] = [
  { id: 'bentopdf', name: 'BentoPDF', purpose: 'PDF tools', category: 'documents', host: 'pdf', audience: ['admin', 'adult'], health: 'healthy', icon: 'bentopdf' },
  { id: 'immich', name: 'Immich', purpose: 'Photos and videos', category: 'photos', host: 'photos', audience: ['admin', 'adult'], health: 'healthy', icon: 'immich' },
  { id: 'donetick', name: 'Donetick', purpose: 'Chores', category: 'home', host: 'chores', audience: ['admin', 'adult', 'kid'], health: 'unhealthy' },
  { id: 'termix', name: 'Termix', purpose: 'A terminal in the browser', category: 'tools', host: 'term', audience: ['admin'], health: 'starting' },
];

const clientID = 'home-client';
const secret = randomBytes(24).toString('base64url');

export class HomeWorld {
  private home?: ChildProcess;
  private edge?: https.Server;
  private issuer?: http.Server;
  private readonly logs: string[] = [];
  private readonly codes = new Map<string, Record<string, unknown>>();
  private readonly key: { privateKey: KeyObject; publicKey: KeyObject } = generateKeyPairSync('rsa', { modulusLength: 2048 });
  /** Who the sign-in signs in next. */
  person: Person = rui;
  port = 0;
  spki = '';
  private issuerPort = 0;

  private constructor(readonly dir: string) {}

  static async start(): Promise<HomeWorld> {
    const w = new HomeWorld(await mkdtemp(path.join(tmpdir(), 'homerslab-home-')));
    const tls = await w.certificate();
    w.issuer = http.createServer((req, res) => w.serveIssuer(req, res));
    w.issuerPort = await listen(w.issuer);
    w.edge = https.createServer({ key: tls.key, cert: tls.cert }, (req, res) => w.serveEdge(req, res));
    w.port = await listen(w.edge);
    await writeFile(w.at('secret'), secret, { mode: 0o600 });
    await w.write(apps);
    w.home = spawn(bin, ['home', '--socket', w.at('home.sock'), '--projection', w.at('home.json'), '--client-secret', w.at('secret')], {
      env: { PATH: process.env.PATH, HOME: w.dir, DOCKER_HOST: 'unix:///nonexistent', CONTAINER_HOST: 'unix:///nonexistent' },
      stdio: ['ignore', 'ignore', 'pipe'],
    });
    w.home.stderr?.on('data', (b: Buffer) => {
      w.logs.push(String(b));
      if (process.env.FLOW_LOGS) process.stderr.write(`home: ${b}`);
    });
    await waitForSocket(w.at('home.sock'));
    return w;
  }

  at(name: string): string {
    return path.join(this.dir, name);
  }

  /** An address at one of the household's names, through the edge's stand-in. */
  url(host: string, p = '/'): string {
    return `https://${host}.${domain}:${this.port}${p}`;
  }

  /** Chromium's flags: the household's names lead to the edge's stand-in, whose certificate it trusts. */
  args(): string[] {
    return [`--host-resolver-rules=MAP *.${domain} 127.0.0.1`, `--ignore-certificate-errors-spki-list=${this.spki}`];
  }

  /** The edge's certificate for the household's names, and its key: Chromium trusts it by its public key's hash. */
  private async certificate(): Promise<{ key: Buffer; cert: Buffer }> {
    const [key, cert] = [this.at('tls.key'), this.at('tls.crt')];
    await run('openssl', ['req', '-x509', '-newkey', 'ec', '-pkeyopt', 'ec_paramgen_curve:P-256', '-nodes', '-keyout', key, '-out', cert,
      '-days', '1', '-subj', `/CN=*.${domain}`, '-addext', `subjectAltName=DNS:*.${domain}`]);
    const [k, c] = await Promise.all([readFile(key), readFile(cert)]);
    this.spki = createHash('sha256').update(new X509Certificate(c).publicKey.export({ type: 'spki', format: 'der' })).digest('base64');
    return { key: k, cert: c };
  }

  /** The projection, as the manager writes it: whole, by a rename. */
  async write(list: App[]): Promise<void> {
    const icon = async (id?: string) => id
      ? { type: 'image/svg+xml', data: (await readFile(path.join(catalog, 'apps', id, 'icon.svg'))).toString('base64') }
      : undefined;
    const projection = {
      household: 'The Ferreira household', home: this.url('home', '').replace(/\/$/, ''), admin: this.url('admin', '').replace(/\/$/, ''),
      signIn: { issuer: this.issuerURL(), clientId: clientID, upstream: `http://127.0.0.1:${this.issuerPort}` },
      apps: await Promise.all(list.map(async (a) => ({ id: a.id, name: a.name, purpose: a.purpose, category: a.category,
        url: this.url(a.host, '').replace(/\/$/, ''), audience: a.audience, health: a.health, icon: await icon(a.icon) }))),
      madeAt: new Date().toISOString(),
    };
    await writeFile(this.at('home.json.new'), JSON.stringify(projection), { mode: 0o640 });
    await rename(this.at('home.json.new'), this.at('home.json'));
  }

  private issuerURL(): string {
    return this.url('auth', '/application/o/homerslab-home/');
  }

  /** The edge's stand-in: Home's name to Home's socket, as the edge proxies it; the sign-in's; the apps'. */
  private serveEdge(req: http.IncomingMessage, res: http.ServerResponse): void {
    const host = (req.headers.host ?? '').replace(/:\d+$/, '');
    if (host === `home.${domain}`) {
      const up = http.request({ socketPath: this.at('home.sock'), path: req.url, method: req.method, headers: req.headers }, (r) => {
        res.writeHead(r.statusCode ?? 502, r.headers);
        r.pipe(res);
      });
      up.on('error', () => res.writeHead(502).end());
      req.pipe(up);
      return;
    }
    if (host === `auth.${domain}`) {
      this.serveIssuer(req, res);
      return;
    }
    const app = apps.find((a) => host === `${a.host}.${domain}`);
    if (!app) {
      res.writeHead(421).end();
      return;
    }
    res.writeHead(200, { 'Content-Type': 'text/html; charset=utf-8' });
    res.end(`<!doctype html><html lang="en"><title>${app.name}</title><h1>${app.name}</h1>`);
  }

  /** The household account's sign-in, as Home's client sees it: discovery, authorize, token and keys. */
  private serveIssuer(req: http.IncomingMessage, res: http.ServerResponse): void {
    const u = new URL(req.url ?? '/', 'http://x');
    const base = '/application/o/homerslab-home/';
    const json = (status: number, body: unknown) => res.writeHead(status, { 'Content-Type': 'application/json' }).end(JSON.stringify(body));
    if (u.pathname === `${base}.well-known/openid-configuration`) {
      json(200, { issuer: this.issuerURL(), authorization_endpoint: this.url('auth', '/application/o/authorize/'),
        token_endpoint: this.url('auth', '/application/o/token/'), jwks_uri: this.url('auth', `${base}jwks/`) });
    } else if (u.pathname === `${base}jwks/`) {
      json(200, { keys: [{ ...this.key.publicKey.export({ format: 'jwk' }), kid: 'k1', alg: 'RS256', use: 'sig' }] });
    } else if (u.pathname === '/application/o/authorize/') {
      const q = u.searchParams;
      if (q.get('client_id') !== clientID || q.get('redirect_uri') !== this.url('home', '/auth/callback') || q.get('code_challenge_method') !== 'S256') {
        res.writeHead(400).end('not Home’s client');
        return;
      }
      const code = randomBytes(16).toString('base64url');
      const now = Math.floor(Date.now() / 1000);
      const p = this.person;
      this.codes.set(code, { iss: this.issuerURL(), aud: clientID, sub: `hashed-${p.username}`, preferred_username: p.username, name: p.name,
        groups: p.groups, nonce: q.get('nonce'), iat: now, exp: now + 300, auth_time: now, _challenge: q.get('code_challenge') });
      res.writeHead(302, { Location: `${q.get('redirect_uri')}?code=${code}&state=${encodeURIComponent(q.get('state') ?? '')}` }).end();
    } else if (u.pathname === '/application/o/token/' && req.method === 'POST') {
      let body = '';
      req.on('data', (b: Buffer) => (body += b));
      req.on('end', () => {
        const f = new URLSearchParams(body);
        const [id, pw] = Buffer.from((req.headers.authorization ?? '').replace(/^Basic /, ''), 'base64').toString().split(':');
        const claims = this.codes.get(f.get('code') ?? '');
        this.codes.delete(f.get('code') ?? '');
        const challenge = createHash('sha256').update(f.get('code_verifier') ?? '').digest('base64url');
        if (id !== clientID || decodeURIComponent(pw ?? '') !== secret || !claims || claims._challenge !== challenge) {
          json(400, { error: 'invalid_grant' });
          return;
        }
        delete claims._challenge;
        json(200, { id_token: this.sign(claims) });
      });
    } else {
      res.writeHead(404).end();
    }
  }

  private sign(claims: Record<string, unknown>): string {
    const b64 = (v: unknown) => Buffer.from(JSON.stringify(v)).toString('base64url');
    const input = `${b64({ alg: 'RS256', kid: 'k1', typ: 'JWT' })}.${b64(claims)}`;
    return `${input}.${sign('sha256', Buffer.from(input), this.key.privateKey).toString('base64url')}`;
  }

  /** What Home has logged so far. */
  log(): string {
    return this.logs.join('');
  }

  async stop(): Promise<void> {
    this.edge?.close();
    this.issuer?.close();
    if (this.home && this.home.exitCode === null) {
      const exited = new Promise((resolve) => this.home?.once('exit', resolve));
      this.home.kill('SIGTERM');
      await exited;
    }
    await rm(this.dir, { recursive: true, force: true });
  }
}

function listen(s: net.Server): Promise<number> {
  return new Promise((resolve) => s.listen(0, '127.0.0.1', () => resolve((s.address() as net.AddressInfo).port)));
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

/**
 * The tests' fixtures: one Home world per worker, the browser launched to reach it, and each test's projection back to
 * the household's apps and Rui as the next to sign in.
 */
export const test = base.extend<{ home: HomeWorld }, { world: HomeWorld }>({
  world: [async ({}, use) => { // eslint-disable-line no-empty-pattern -- Playwright reads a fixture's dependencies from this pattern
    const w = await HomeWorld.start();
    await use(w);
    await w.stop();
  }, { scope: 'worker' }],
  launchOptions: [async ({ world }, use) => use({ args: world.args() }), { scope: 'worker' }],
  home: async ({ world }, use) => {
    world.person = rui;
    await world.write(apps);
    await use(world);
  },
});
export { expect } from '@playwright/test';
