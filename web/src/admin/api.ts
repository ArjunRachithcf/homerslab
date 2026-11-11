// Admin's side of the manager's API, on Admin's own listener. Nothing is kept in the browser: every page asks.

export type Tier = 'verified' | 'community';
export type SignInMethod = 'oidc' | 'ldap' | 'guard';

export type CatalogInfo = { id: string; name: string; publisher: string };
export type Category = { id: string; name: string };
export type StoreApp = {
  id: string; name: string; purpose: string; description: string; category: string; tier: Tier; signIn: SignInMethod; icon: string;
};
export type Catalog = { catalog: CatalogInfo; categories: Category[]; apps: StoreApp[] };

export type Definition = {
  id: string; name: string; purpose: string; description: string; category: string; tier: Tier;
  versions: { app: string };
  signIn: { method: SignInMethod }[];
  names: { name: string; public: boolean };
  audience: { default: string; kidView?: string };
  data: { what: string; backup?: boolean }[];
  resources: { memoryMiB: number; diskGiB: number; grows?: string; gpu: { need: string; for?: string } };
  extras: { id: string; why: string; optional: boolean }[];
};
export type AppPage = { catalog: CatalogInfo; app: Definition; icon: string; screenshots: { url: string; caption: string }[] };

export type Warning = { kind: 'memory' | 'disk' | 'gpu' | 'build'; needsBytes?: number; freeBytes?: number; services?: string[]; unknown?: boolean };
export type Amount = { needsBytes: number; freeBytes: number; unknown?: boolean; grows?: string };
export type Plan = {
  id: string; kind: 'install'; madeAt: string;
  app: { id: string; name: string; purpose: string; version: string; tier: Tier; catalog: string; publisher: string };
  name: { host: string; asked?: string; why?: string; meshes: string[]; exposure: string; publicAllowed: boolean };
  audience: { default: string; people: { name: string; role: string }[]; kidView?: string };
  signIn: { method: SignInMethod; provider: { id: string; name: string }; creates: string };
  backups: { every: string; restoreTest: string; data: { what: string; kind: 'folder' | 'dump' }[]; left: { what: string; why: string }[] };
  placement: { node: string; architecture: string; engine: string; project: string; services: { name: string; image: string; digest?: string }[] };
  extras: { id: string; kind: string; service: string; device?: string; capability?: string; why: string; optional: boolean }[];
  secrets: { name: string; what: string; sealedTo: 'tpm' | 'host' }[];
  resources: { memory: Amount; disk: Amount; gpu: { need: string; for?: string; vendors?: string[]; usable: string[] }; warnings: Warning[] };
  risky: boolean;
};

/** The manager said no, with its reason. */
export class Refused extends Error {
  constructor(readonly status: number, readonly code: string) {
    super(code);
  }
}

async function call<T>(method: string, path: string, body?: unknown): Promise<T> {
  const resp = await fetch(path, {
    method,
    credentials: 'omit',
    headers: body === undefined ? {} : { 'Content-Type': 'application/json' },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  const data: unknown = await resp.json().catch(() => ({}));
  if (!resp.ok) throw new Refused(resp.status, (data as { error?: string }).error ?? 'unknown');
  return data as T;
}

export const getCatalog = () => call<Catalog>('GET', '/api/v1/catalog');
export const getApp = (id: string) => call<AppPage>('GET', `/api/v1/catalog/apps/${encodeURIComponent(id)}`);
export const makePlan = (app: string) => call<Plan>('POST', '/api/v1/plans', { app });
export const getPlan = (id: string) => call<Plan>('GET', `/api/v1/plans/${encodeURIComponent(id)}`);
