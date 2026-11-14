// Home's side of Home's own API: what the person's Home shows, from the projection the manager keeps. Nothing is kept
// in the browser but Home's session cookie, which the browser sends itself.

/** An app's health, as the agent last found it. */
export type Health = 'healthy' | 'starting' | 'unhealthy' | 'stopped' | 'unknown';
export type Role = 'admin' | 'adult' | 'kid';
export type Tile = { id: string; name: string; purpose: string; category: string; url: string; health: Health; icon?: string };
export type HomeView = { household: string; person: { username: string; name: string; role: Role }; admin?: string; apps: Tile[] };

/** Signed out: the page goes to the sign-in. */
export class SignedOut extends Error {}

async function call<T>(method: string, path: string): Promise<T | undefined> {
  const resp = await fetch(path, {
    method,
    credentials: 'same-origin',
    headers: method === 'GET' ? {} : { 'Content-Type': 'application/json' },
    body: method === 'GET' ? undefined : '{}',
  });
  if (resp.status === 401) throw new SignedOut();
  if (!resp.ok) throw new Error(`${method} ${path}: ${resp.status}`);
  return resp.status === 204 ? undefined : ((await resp.json()) as T);
}

export const getHome = async () => (await call<HomeView>('GET', '/api/v1/home')) as HomeView;
export const signOut = () => call<undefined>('POST', '/auth/sign-out');
