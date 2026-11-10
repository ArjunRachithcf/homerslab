// First-time setup's side of the manager's API, on the setup listener. Claiming the link gives a session token, which
// this tab keeps (sessionStorage) and sends as a bearer token: not a cookie, which every port of this host would get.

const TOKEN = 'homerslab.setup.session';
let memory: string | null = null; // when the browser refuses storage

function token(): string | null {
  try {
    return sessionStorage.getItem(TOKEN) ?? memory;
  } catch {
    return memory;
  }
}

function keep(t: string) {
  memory = t;
  try {
    sessionStorage.setItem(TOKEN, t);
  } catch {
    // memory holds it for this page's life
  }
}

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
  const resp = await fetch(path, {
    method,
    credentials: 'omit',
    headers,
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  if (resp.status === 204) return undefined as T;
  const data: unknown = await resp.json().catch(() => ({}));
  if (!resp.ok) {
    const e = data as { error?: string; fields?: string[] };
    throw new Refused(resp.status, e.error ?? 'unknown', e.fields ?? []);
  }
  return data as T;
}

export async function claim(key: string): Promise<void> {
  keep((await call<{ token: string }>('POST', '/api/v1/setup/claim', { key })).token);
}
export const getSetup = () => call<Setup>('GET', '/api/v1/setup');
export const getPrimary = () => call<Facts>('GET', '/api/v1/nodes/primary');
export const answer = (step: string, body: Record<string, string>) => call<Setup>('PUT', `/api/v1/setup/steps/${encodeURIComponent(step)}`, body);
