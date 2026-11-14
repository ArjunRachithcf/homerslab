// First-time setup's last step, in Admin: the first admin has signed in with their passkey over the mesh, and setup
// finishes once they have two ways in: a second passkey, or another admin with one (#87). The second passkey's link
// comes from this machine's terminal, never from a page.
import { useCallback, useEffect, useState } from 'react';
import { t } from '../../i18n/t';
import { Assistant, Icon } from '../../setup/Assistant';

type AccountSetup = {
  current: string; passkeys?: number; twoWaysIn?: boolean; command: string; household: string;
  steps: { id: string; revision: number }[];
};

async function getAccount(): Promise<AccountSetup> {
  const r = await fetch('/api/v1/account/setup', { credentials: 'same-origin', headers: { Accept: 'application/json' } });
  if (r.status === 401) {
    location.assign('/auth/sign-in');
    throw new Error('signed-out');
  }
  if (!r.ok) throw new Error(String(r.status));
  return (await r.json()) as AccountSetup;
}

export function Account() {
  const [s, setS] = useState<AccountSetup | null>(null);
  const [busy, setBusy] = useState(false);
  const [failed, setFailed] = useState(false);
  const [done, setDone] = useState(false);

  const load = useCallback(() => {
    getAccount().then(setS).catch(() => setFailed(true));
  }, []);
  useEffect(() => {
    load();
    if (done) return; // setup is finished: nothing more to wait for
    const timer = window.setInterval(load, 3000);
    return () => window.clearInterval(timer);
  }, [load, done]);
  const title = t(done ? 'account.done.title' : 'account.title');
  useEffect(() => {
    document.title = t('setup.title', { title });
  }, [title]);

  const finish = async () => {
    setBusy(true);
    setFailed(false);
    try {
      // From the revision of the step this page last read, as every answer is (#110).
      const revision = s?.steps.find((x) => x.id === 'done')?.revision ?? 0;
      const r = await fetch('/api/v1/account/setup/steps/done', {
        method: 'PUT', credentials: 'same-origin', body: '{}',
        headers: { 'Content-Type': 'application/json', 'If-Match': `"${revision}"` },
      });
      if (r.status === 409) load(); // the step changed since: read it again
      if (r.ok) setDone(true);
      else setFailed(true);
    } catch {
      setFailed(true);
    } finally {
      setBusy(false);
    }
  };

  let screen: React.ReactNode;
  if (done) {
    screen = <Assistant steps={[]} now="" illoFacts={{}} illo="done" title={t('account.done.title')} lead={t('account.done.lead')} />;
  } else {
    const two = !!s?.twoWaysIn;
    screen = (
      <Assistant steps={[]} now="" illoFacts={{}} illo="passkey" title={t('account.title')} lead={t('account.lead')}
        body={<>
          <div className="as-check"><Icon name="check" /><span>{t('account.passkeys', { count: String(s?.passkeys ?? 1) })}</span></div>
          {!two && <>
            <p>{t('account.command')}</p>
            <div className="cmd"><code>{t('account.commandLine')}</code></div>
            <p className="hint as-waiting" role="status"><span className="spinner" aria-hidden="true" />{t('account.waiting')}</p>
          </>}
          {failed && <div className="notice notice-warn" role="alert"><Icon name="alert" /><span className="notice-body">{t('account.failed')}</span></div>}
        </>}
        primary={{ label: t('account.finish'), ok: two, why: t('account.why'), busy }} onPrimary={() => void finish()} />
    );
  }
  return (
    <div className="as-page">
      <a className="sr-only" href="#s-main">{t('setup.skip')}</a>
      <header className="as-top">
        <span className="brand">
          <span className="brand-l" dangerouslySetInnerHTML={{ __html: window.LOCKUP }} />
          <span className="brand-m" role="img" aria-label={t('brand')} dangerouslySetInnerHTML={{ __html: window.MARK }} />
        </span>
      </header>
      <main className="as-main vt-screen" id="s-main" tabIndex={-1}>{screen}</main>
    </div>
  );
}
