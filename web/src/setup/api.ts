// First-time setup's side of the manager's API, on the setup listener. The session is a cookie the claim sets.

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
  const resp = await fetch(path, {
    method,
    credentials: 'same-origin',
    headers: body === undefined ? {} : { 'Content-Type': 'application/json' },
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

export const claim = (key: string) => call<undefined>('POST', '/api/v1/setup/claim', { key });
export const getSetup = () => call<Setup>('GET', '/api/v1/setup');
export const getPrimary = () => call<Facts>('GET', '/api/v1/nodes/primary');
export const answer = (step: string, body: Record<string, string>) => call<Setup>('PUT', `/api/v1/setup/steps/${encodeURIComponent(step)}`, body);
