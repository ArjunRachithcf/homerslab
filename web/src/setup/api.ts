// First-time setup's side of the manager's API, on the setup listener. Claiming the link gives a session token, which
// this tab keeps (sessionStorage) and sends as a bearer token: not a cookie, which every port of this host would get.
// Until the manager has answered the claim, the tab also holds the link's key, so a claim the network lost can be sent
// again; the key never goes anywhere but the claim's body.

const TOKEN = 'homerslab.setup.session';
const KEY = 'homerslab.setup.link';
const memory = new Map<string, string>(); // when the browser refuses storage, and the newest value

function read(name: string): string | null {
  try {
    return memory.get(name) ?? sessionStorage.getItem(name); // what this page just kept wins over an older stored value
  } catch {
    return memory.get(name) ?? null;
  }
}

function write(name: string, value: string | null) {
  if (value === null) memory.delete(name);
  else memory.set(name, value);
  try {
    if (value === null) sessionStorage.removeItem(name);
    else sessionStorage.setItem(name, value);
  } catch {
    // memory holds it for this page's life
  }
}

const token = () => read(TOKEN);

/** The link's key, held by this tab until the manager has answered its claim. */
export const heldKey = () => read(KEY);
export const holdKey = (key: string) => write(KEY, key);
export const dropKey = () => write(KEY, null);

export type Half = 'lan' | 'mesh';
export type StepState = { id: string; half: Half; state: 'todo' | 'done' };
export type Setup = { half: Half; current: string; steps: StepState[]; answers: Record<string, string> };

export type Facts = {
  hostname: string;
  distro: { id: string; name: string; version: string; pretty: string };
  architecture: string;
  memoryBytes: number;
  disks: { name: string; model: string; sizeBytes: number; rotational: boolean; removable: boolean }[];
  tpm: { present: boolean; version?: string };
  gpus: { vendor: string; model: string; pciId: string }[];
};

/** The request got no answer: the network dropped it, or the manager isn't listening. */
export class Unreachable extends Error {
  constructor() {
    super('unreachable');
  }
}

/** The manager said no, with its reason. */
export class Refused extends Error {
  constructor(readonly status: number, readonly code: string, readonly fields: string[] = []) {
    super(code);
  }
}

async function call<T>(method: string, path: string, body?: unknown): Promise<T> {
  const headers: Record<string, string> = {};
  if (body !== undefined) headers['Content-Type'] = 'application/json';
  const t = token();
  if (t) headers.Authorization = `Bearer ${t}`;
  let resp: Response;
  try {
    resp = await fetch(path, {
      method,
      credentials: 'omit',
      headers,
      body: body === undefined ? undefined : JSON.stringify(body),
    });
  } catch {
    throw new Unreachable();
  }
  if (resp.status === 204) return undefined as T;
  const data: unknown = await resp.json().catch(() => ({}));
  if (!resp.ok) {
    const e = data as { error?: string; fields?: string[] };
    throw new Refused(resp.status, e.error ?? 'unknown', e.fields ?? []);
  }
  return data as T;
}

export async function claim(key: string): Promise<void> {
  write(TOKEN, (await call<{ token: string }>('POST', '/api/v1/setup/claim', { key })).token);
}
export const getSetup = () => call<Setup>('GET', '/api/v1/setup');
export const getPrimary = () => call<Facts>('GET', '/api/v1/nodes/primary');
export const answer = (step: string, body: Record<string, string>) => call<Setup>('PUT', `/api/v1/setup/steps/${encodeURIComponent(step)}`, body);
