// First-time setup's page. It claims the one-time link from the address's fragment, then shows the step setup is at,
// from the manager's state: one question per screen, in plain words. What is answered is kept in the manager's
// database, so setup resumes where it stopped.
import { useCallback, useEffect, useRef, useState } from 'react';
import { flushSync } from 'react-dom';
import { rich, t, type Key } from '../i18n/t';
import { answer, claim, dropLink, getPrimary, getSetup, goodNonce, heldLink, holdLink, keepSession, newNonce, Refused, Unreachable, type Facts, type Link, type Reason, type Setup } from './api';
import { Assistant, Field, Icon, stepTitle, type Screen } from './Assistant';
import { diskSize, memory } from './format';

// An error says the manager didn't answer, unless it did: then `title` is the neutral one and `says` holds what it said
// (it answered, so the screen never claims it didn't, #109). Full: the disk under the
// manager's database is full, so nothing can be kept until there is room.
type Phase = { kind: 'loading' } | { kind: 'refused' } | { kind: 'error'; says?: Key; title?: Key } | { kind: 'full' }
  | { kind: 'ready'; setup: Setup; facts: Facts | null };

const BUILT = new Set(['welcome', 'you', 'household', 'signin', 'network', 'domain']);
const validMail = (m: string) => /^[^@\s]+@[^@\s]+\.[^@\s]+$/.test(m.trim());

export function App() {
  const [phase, setPhase] = useState<Phase>({ kind: 'loading' });
  const [view, setView] = useState('welcome');

  const load = useCallback(async (stale: () => boolean) => {
    try {
      const [setup, facts] = await Promise.all([getSetup(), getPrimary().catch(() => null)]);
      if (stale()) return;
      setPhase({ kind: 'ready', setup, facts });
      const asked = location.hash.replace(/^#\//, '');
      setView(reachable(setup, asked) ? asked : 'welcome');
    } catch (e) {
      if (stale()) return;
      setPhase(e instanceof Refused && e.status === 401 ? { kind: 'refused' } : problem(e));
    }
  }, []);

  // The key rides in the fragment, which never reaches a server or a log, and so does this tab's nonce for its claim.
  // Both stay there, and in this page's memory (never in storage), until the manager has answered the claim: a claim
  // the network lost, or whose answer it lost, is sent again with the same nonce, by itself and then from "Try again",
  // and a reload finds both where they were. A link can also arrive in a tab that already shows setup: then only the
  // fragment changes. Each opening supersedes the one before: a late answer to an earlier one changes nothing.
  const opening = useRef(0);
  const open = useCallback(async () => {
    const run = ++opening.current;
    const stale = () => run !== opening.current;
    const link = fromAddress() ?? heldLink();
    if (link) {
      holdLink(link);
      history.replaceState(null, '', `${location.pathname}#${new URLSearchParams({ k: link.key, n: link.nonce })}`);
      const answer = await claimed(link, stale);
      if (stale()) return;
      if (answer.kind === 'unanswered') {
        setPhase(problem(answer.error));
        return;
      }
      if (answer.kind === 'claimed') keepSession(answer.token);
      dropLink();
      history.replaceState(null, '', `${location.pathname}#/welcome`);
      setView('welcome');
    }
    await load(stale);
  }, [load]);

  useEffect(() => {
    const onHash = () => {
      if (new URLSearchParams(location.hash.slice(1)).has('k')) void open();
    };
    void open();
    window.addEventListener('hashchange', onHash);
    return () => window.removeEventListener('hashchange', onHash);
  }, [open]);

  const retry = useCallback(() => {
    setPhase({ kind: 'loading' });
    void open();
  }, [open]);

  const go = useCallback((id: string, type: 'push' | 'pop' | 'fade' = 'push') => {
    window.HL.run(() => flushSync(() => setView(id)), { type }).then(() => {
      document.getElementById('as-title')?.focus({ preventScroll: true });
      window.scrollTo(0, 0);
    });
  }, []);

  useEffect(() => {
    if (phase.kind === 'ready' && location.hash !== `#/${view}`) history.replaceState(null, '', `#/${view}`);
  }, [phase, view]);

  let screen: React.ReactNode = null;
  if (phase.kind === 'refused' || phase.kind === 'error' || phase.kind === 'full') {
    screen = <Problem kind={phase.kind} says={phase.kind === 'error' ? phase.says : undefined}
      title={phase.kind === 'error' ? phase.title : undefined} onRetry={retry} />;
  } else if (phase.kind === 'ready') {
    screen = <StepScreen phase={phase} view={view} go={go} onAnswered={(setup) => setPhase({ ...phase, setup })} />;
  }

  return (
    <div className="as-page">
      <a className="sr-only" href="#s-main">{t('setup.skip')}</a>
      <header className="as-top">
        <span className="brand">
          <span className="brand-l" dangerouslySetInnerHTML={{ __html: window.LOCKUP }} />
          <span className="brand-m" role="img" aria-label={t('brand')} dangerouslySetInnerHTML={{ __html: window.MARK }} />
        </span>
        <span className="as-where">
          <Icon name="home" />
          <span className="host">{location.host}</span>
          <span className="sep long">{t('setup.where.lan')}</span>
        </span>
      </header>
      <main className="as-main vt-screen" id="s-main" tabIndex={-1}>{screen}</main>
      <footer className="as-foot">{phase.kind === 'ready' ? t('setup.foot') : null}</footer>
    </div>
  );
}

/** The link was refused, the manager can't be reached, or the machine's disk is full. */
function Problem({ kind, says, title: said, onRetry }: { kind: 'refused' | 'error' | 'full'; says?: Key; title?: Key; onRetry: () => void }) {
  const refused = kind === 'refused';
  const title = t(refused ? 'setup.refused.title' : kind === 'full' ? 'setup.full.title' : said ?? 'setup.error.title');
  useTitle(title);
  return (
    <Assistant steps={[]} now="" illoFacts={{}} illo="welcome" title={title}
      lead={t(refused ? 'setup.refused.lead' : kind === 'full' ? 'setup.full.lead' : says ?? 'setup.error.lead')}
      body={refused ? <div className="cmd"><code>{t('setup.refused.command')}</code></div> : undefined}
      primary={refused ? undefined : { label: t('setup.error.retry') }} onPrimary={onRetry} />
  );
}

function useTitle(title: string) {
  useEffect(() => {
    document.title = t('setup.title', { title });
  }, [title]);
}

/**
 * The link in the address's fragment, with the nonce this tab claims it with: the one the fragment carries (a reload
 * while the claim was out), the one this page holds for the same key, or a new one.
 */
function fromAddress(): Link | null {
  const f = new URLSearchParams(location.hash.slice(1));
  const key = f.get('k');
  if (!key) return null;
  const nonce = f.get('n');
  const held = heldLink();
  return { key, nonce: goodNonce(nonce) ? nonce : held?.key === key ? held.nonce : newNonce() };
}

// A claim that got no answer is sent again after each of these pauses, then left to "Try again".
const CLAIM_RETRIES = [1000, 2000, 4000];

type Claim = { kind: 'claimed'; token: string } | { kind: 'refused' } | { kind: 'unanswered'; error: unknown };

/**
 * Claims the link: the manager takes the key, or refuses it for good (used, ended or expired). A refused key may be
 * this tab's own, opened again: its session carries on, and load() says if not. Any other answer, or none, leaves the
 * key to be tried again.
 */
async function claimed(link: Link, superseded: () => boolean): Promise<Claim> {
  for (let i = 0; ; i++) {
    try {
      return { kind: 'claimed', token: await claim(link) };
    } catch (e) {
      if (e instanceof Refused && e.code === 'link-refused') return { kind: 'refused' };
      if (!(e instanceof Unreachable) || i >= CLAIM_RETRIES.length || superseded()) return { kind: 'unanswered', error: e };
    }
    await new Promise((r) => setTimeout(r, CLAIM_RETRIES[i]));
    if (superseded()) return { kind: 'unanswered', error: new Unreachable() };
  }
}

/** The manager refused this browser, not its link or answer: it isn't on this machine or its LAN, or not on its page. */
const fromElsewhere = (e: unknown) => e instanceof Refused && (e.status === 403 || e.status === 421);

/** The disk under the manager's database is full (#70). */
const diskFull = (e: unknown) => e instanceof Refused && e.code === 'disk-full';

/** Any answer at all is neutral about it: a 5xx says to try again, a refusal from elsewhere says where to open setup. */
const problem = (e: unknown): Phase => {
  if (diskFull(e)) return { kind: 'full' };
  if (!(e instanceof Refused)) return { kind: 'error' }; // no answer
  return { kind: 'error', title: 'setup.error.answeredTitle', says: fromElsewhere(e) ? 'setup.fail.here' : 'setup.error.answered' };
};

/** A step can be shown when every step before it is done. */
function reachable(setup: Setup, id: string): boolean {
  const i = setup.steps.findIndex((s) => s.id === id);
  return i >= 0 && setup.steps.slice(0, i).every((s) => s.state === 'done');
}

type StepsProps = {
  phase: Extract<Phase, { kind: 'ready' }>;
  view: string;
  go: (id: string, type?: 'push' | 'pop' | 'fade') => void;
  onAnswered: (s: Setup) => void;
};

function StepScreen({ phase, view, go, onAnswered }: StepsProps) {
  const { setup, facts } = phase;
  const steps = setup.steps;
  const at = steps.findIndex((s) => s.id === view);
  const prev = at > 0 ? steps[at - 1] : undefined;
  const back = prev && steps[at]?.half === 'lan' ? () => go(prev.id, 'pop') : undefined;
  const next = () => {
    const following = steps[at + 1];
    if (following) go(following.id);
  };
  const v = useStep(view, setup, facts, next, onAnswered, () => go(setup.current));
  useTitle(v.title);
  return <Assistant key={view} steps={steps} now={view} illoFacts={v.illoFacts ?? {}} onBack={back} onPrimary={v.onPrimary} {...v} />;
}

type StepView = Screen & { onPrimary?: () => void; illoFacts?: { name?: string; mail?: string } };

function useStep(id: string, setup: Setup, facts: Facts | null, next: () => void, onAnswered: (s: Setup) => void, resume: () => void): StepView {
  const a = setup.answers;
  const [name, setName] = useState(a.name ?? '');
  const [mail, setMail] = useState(a.mail ?? '');
  const surname = name.trim().split(/\s+/).pop() ?? '';
  const [household, setHousehold] = useState(a.household ?? '');
  const [touched, setTouched] = useState(!!a.household);
  const [bad, setBad] = useState<Record<string, Reason>>({});
  const [busy, setBusy] = useState(false);
  const [failed, setFailed] = useState<{ step: string; why: Failure } | null>(null);
  const householdValue = touched || !surname ? household : defaultHousehold(surname);
  const [plane, setPlane] = useState<'cloud' | 'self-hosted'>(a.meshControlPlane === 'self-hosted' ? 'self-hosted' : 'cloud');
  const [url, setUrl] = useState(a.meshControlPlane === 'self-hosted' ? (a.meshUrl ?? '') : '');
  const [domain, setDomain] = useState(a.domain ?? (a.mail ?? '').split('@')[1] ?? '');
  const [provider, setProvider] = useState<'infomaniak' | 'rfc2136'>(a.dnsProvider === 'rfc2136' ? 'rfc2136' : 'infomaniak');
  const [server, setServer] = useState(a.dnsServer ?? '');
  const [keyName, setKeyName] = useState(a.dnsKeyName ?? '');
  const network = setup.steps.find((s) => s.id === 'network');
  const step = setup.steps.find((s) => s.id === id);
  // A step that waits for a secret, which arrives in this machine's terminal (#64).
  const waiting = (id === 'network' || id === 'domain') && step?.state === 'waiting' && !!step.secret;
  const confirming = id === 'network' && network?.state === 'waiting' && network.confirm === 'mesh-policies';
  const atMesh = id === 'join' || id === 'passkey' || id === 'done';
  const [moved, setMoved] = useState(false);
  const latest = useRef({ onAnswered, next });
  useEffect(() => {
    latest.current = { onAnswered, next };
  });
  useEffect(() => {
    // The token arrives in this machine's terminal, never through this page: wait for the step to turn done.
    if (!waiting) return;
    const timer = window.setInterval(() => {
      void getSetup().then((s) => {
        latest.current.onAnswered(s);
        if (s.steps.find((x) => x.id === id)?.state === 'done') latest.current.next();
      }).catch(() => undefined);
    }, 2000);
    return () => window.clearInterval(timer);
  }, [waiting, id]);

  useEffect(() => {
    // The admin's first sign-in to Admin, over the mesh, closes this page's listener for good: it answers 410.
    if (!atMesh) return;
    const timer = window.setInterval(() => {
      void getSetup().then((s) => latest.current.onAnswered(s)).catch((e: unknown) => {
        if (e instanceof Refused && e.status === 410) setMoved(true);
      });
    }, 3000);
    return () => window.clearInterval(timer);
  }, [atMesh]);

  const send = async (body: Record<string, string | boolean>) => {
    setBusy(true);
    setFailed(null); // a failure that happens again is a new notice, read out again
    try {
      const s = await answer(id, body);
      onAnswered(s);
      // The manager keeps what it was given in its normal form (spaces collapsed, full-width letters made plain, and
      // so on): show that.
      if (id === 'you') {
        setName(s.answers.name ?? name);
        setMail(s.answers.mail ?? mail);
      } else if (id === 'household' && s.answers.household !== undefined) {
        setHousehold(s.answers.household);
        setTouched(true);
      }
      setBad({});
      if (s.steps.find((x) => x.id === id)?.state === 'done') next(); // a step waiting for a secret stays

    } catch (e) {
      if (e instanceof Refused && e.code === 'invalid') setBad(e.reasons);
      else if (e instanceof Refused && e.status === 401) location.reload();
      else setFailed({ step: id, why: failure(e) });
    } finally {
      setBusy(false);
    }
  };
  const notice = failed?.step === id ? <FailNotice why={failed.why} /> : undefined;

  switch (id) {
    case 'welcome': {
      const resumed = setup.steps.some((s) => s.state === 'done');
      const left = setup.steps.filter((s) => s.state === 'todo' && s.id !== 'done');
      const host = facts?.hostname || location.hostname;
      return {
        illo: 'welcome',
        title: t(resumed ? 'step.welcome.backTitle' : 'step.welcome.title'),
        lead: resumed ? t('step.welcome.backLead', { step: stepTitle(setup.current) }) : rich('step.welcome.lead', { host: <b>{host}</b> }),
        body: resumed ? <Left steps={left.map((s) => s.id)} /> : undefined,
        moreLabel: t('step.welcome.about'),
        more: <><About facts={facts} /><Needs /></>,
        notice,
        primary: { label: t(resumed ? 'step.welcome.resume' : 'step.welcome.start'), busy },
        onPrimary: resumed ? resume : () => send({}),
      };
    }
    case 'you': {
      const ok = !!name.trim() && validMail(mail);
      return {
        illo: 'you', title: t('step.you.title'), lead: t('step.you.lead'), illoFacts: { name, mail },
        body: <>
          <Field id="name" label={t('step.you.name')} value={name} onChange={setName} autoComplete="name"
            error={refusal(bad.name, { 'too-long': 'step.you.nameTooLong', 'hidden-characters': 'step.you.nameHidden' }, 'step.you.nameInvalid')} />
          <Field id="mail" label={t('step.you.mail')} value={mail} onChange={setMail} type="email" autoComplete="email"
            hint={t('step.you.mailHint')}
            error={refusal(bad.mail, { 'too-long': 'step.you.mailTooLong', 'hidden-characters': 'step.you.mailHidden' }, 'step.you.mailInvalid')} />
        </>,
        notice,
        primary: { label: t('setup.continue'), ok, why: t('step.you.why'), busy },
        onPrimary: () => send({ name, mail }),
      };
    }
    case 'household':
      return {
        illo: 'household', title: t('step.household.title'), lead: t('step.household.lead'), illoFacts: { name: householdValue },
        body: <Field id="household" label={t('step.household.name')} value={householdValue}
          onChange={(v) => { setTouched(true); setHousehold(v); }}
          error={refusal(bad.name, { 'too-long': 'step.household.tooLong', 'hidden-characters': 'step.household.hidden' }, 'step.household.invalid')} />,
        notice,
        primary: { label: t('setup.continue'), ok: !!householdValue.trim(), why: t('step.household.why'), busy },
        onPrimary: () => send({ name: householdValue }),
      };
    case 'network': {
      const own = plane === 'self-hosted';
      const choices: ['cloud' | 'self-hosted', Key, Key][] = [
        ['cloud', 'step.network.cloud', 'step.network.cloudSub'], ['self-hosted', 'step.network.own', 'step.network.ownSub'],
      ];
      const answerNow: Record<string, string> = own ? { mesh: 'netbird', controlPlane: plane, url: url.trim() } : { mesh: 'netbird', controlPlane: plane };
      if (confirming) {
        const names = (a.meshConflicts ?? '').split('\n').filter(Boolean);
        return {
          illo: 'network', title: t('step.network.conflictsTitle'), lead: t('step.network.conflictsLead'),
          body: <ul className="group" aria-label={t('step.network.conflictsList')}>
            {names.map((n) => (
              <li className="row" key={n}><span className="row-main"><span className="row-label wrap">{n}</span></span></li>
            ))}
          </ul>,
          more: <p className="hint">{t('step.network.conflictsKept')}</p>,
          primary: { label: t('step.network.switchOff'), busy },
          onPrimary: () => send({ ...answerNow, switchOff: true }),
        };
      }
      if (waiting) {
        return {
          illo: 'network', title: t('step.network.waitTitle'), lead: t('step.network.waitLead'),
          body: <>
            <div className="cmd"><code>{network?.command ?? 'sudo homerslab setup secret mesh'}</code></div>
            <p className="hint as-waiting" role="status"><span className="spinner" aria-hidden="true" />{t('step.network.waiting')}</p>
          </>,
          moreLabel: t('step.network.help'),
          more: <>
            <ol className="group help-steps">
              {(['step.network.help1', 'step.network.help2', 'step.network.help3'] as Key[]).map((k) => <li key={k}><span>{t(k)}</span></li>)}
            </ol>
            <p className="subhead">{t('step.network.helpWhy')}</p>
          </>,
          primary: { label: t('setup.continue'), ok: false, why: t('step.network.waitWhy') },
        };
      }
      return {
        illo: 'network', title: t('step.network.title'), lead: t('step.network.lead'),
        body: <>
          <div className="group" role="radiogroup" aria-label={t('step.network.where')}>
            {choices.map(([v, label, sub]) => (
              <label className="row row-choice" key={v}>
                <input type="radio" name="plane" value={v} checked={plane === v} onChange={() => setPlane(v)} />
                <span className="row-main"><span className="row-label">{t(label)}</span><span className="row-sub wrap">{t(sub)}</span></span>
                <span className="row-trail"><Icon name="check2" cls="icon checkmark" /></span>
              </label>
            ))}
          </div>
          {own && <Field id="url" label={t('step.network.url')} value={url} onChange={setUrl} type="url" hint={t('step.network.urlHint')}
            error={bad.url ? t('step.network.urlInvalid') : undefined} />}
          {network?.state === 'done' && <div className="as-check"><Icon name="check" /><span>{t('step.network.done')}</span></div>}
        </>,
        more: <p className="hint">{t('step.network.later')}</p>,
        primary: { label: t('setup.continue'), ok: !own || /^https:\/\/\S+$/.test(url.trim()), why: t('step.network.why'), busy },
        onPrimary: () => send(answerNow),
      };
    }
    case 'domain': {
      const example = <span className="host">{`photos.${domain.trim() || 'ferreira.family'}`}</span>;
      const lead = rich('step.domain.lead', { example });
      if (waiting) {
        const help: Key[] = provider === 'rfc2136'
          ? ['step.domain.helpRfc21361', 'step.domain.helpRfc21362']
          : ['step.domain.helpInfomaniak1', 'step.domain.helpInfomaniak2', 'step.domain.helpInfomaniak3'];
        return {
          illo: 'domain', title: t('step.domain.waitTitle'), lead: t('step.domain.waitLead'),
          body: <>
            <div className="cmd"><code>{step?.command ?? 'sudo homerslab setup secret dns'}</code></div>
            <p className="hint as-waiting" role="status"><span className="spinner" aria-hidden="true" />{t('step.domain.waiting')}</p>
          </>,
          moreLabel: t('step.domain.help'),
          more: <>
            <ol className="group help-steps">{help.map((k) => <li key={k}><span>{t(k)}</span></li>)}</ol>
            <p className="subhead">{t('step.domain.helpWhy')}</p>
          </>,
          primary: { label: t('setup.continue'), ok: false, why: t('step.domain.waitWhy') },
        };
      }
      const own = provider === 'rfc2136';
      const choices: ['infomaniak' | 'rfc2136', Key, Key][] = [
        ['infomaniak', 'step.domain.infomaniak', 'step.domain.infomaniakSub'], ['rfc2136', 'step.domain.rfc2136', 'step.domain.rfc2136Sub'],
      ];
      const body: Record<string, string> = own
        ? { domain: domain.trim(), provider, server: server.trim(), keyName: keyName.trim() }
        : { domain: domain.trim(), provider };
      const port = a.householdPort && a.householdPort !== '443' ? `:${a.householdPort}` : '';
      return {
        illo: 'domain', title: t('step.domain.title'), lead,
        body: <>
          <Field id="domain" label={t('step.domain.domain')} value={domain} onChange={setDomain} hint={t('step.domain.domainHint')}
            error={bad.domain ? t('step.domain.domainInvalid') : undefined} />
          <div className="group" role="radiogroup" aria-label={t('step.domain.provider')}>
            {choices.map(([v, label, sub]) => (
              <label className="row row-choice" key={v}>
                <input type="radio" name="provider" value={v} checked={provider === v} onChange={() => setProvider(v)} />
                <span className="row-main"><span className="row-label">{t(label)}</span><span className="row-sub wrap">{t(sub)}</span></span>
                <span className="row-trail"><Icon name="check2" cls="icon checkmark" /></span>
              </label>
            ))}
          </div>
          {own && <>
            <Field id="server" label={t('step.domain.server')} value={server} onChange={setServer} hint={t('step.domain.serverHint')}
              error={bad.server ? t('step.domain.serverInvalid') : undefined} />
            <Field id="keyName" label={t('step.domain.keyName')} value={keyName} onChange={setKeyName}
              error={bad.keyName ? t('step.domain.keyNameInvalid') : undefined} />
          </>}
          {step?.state === 'done' && <div className="as-check"><Icon name="check" /><span>
            {t('step.domain.done', { domain: a.domain ?? '', admin: `https://admin.${a.domain ?? ''}${port}` })}</span></div>}
        </>,
        more: <p className="subhead">{t('step.domain.only')}</p>,
        primary: { label: t('setup.continue'), ok: /^[^\s.]+(\.[^\s.]+)+$/.test(domain.trim()) && (!own || (!!server.trim() && !!keyName.trim())),
          why: t('step.domain.why'), busy },
        onPrimary: () => send(body),
      };
    }
    case 'signin': {
      const small = !!facts && facts.memoryBytes < 3.5 * 2 ** 30;
      return {
        illo: 'signin', title: t('step.signin.title'), lead: t('step.signin.lead'),
        body: <>
          <div className="group" role="radiogroup" aria-label={t('step.signin.choose')}>
            <label className="row row-choice">
              <input type="radio" name="provider" value="authentik" checked readOnly />
              <span className="row-main"><span className="row-label">{t('step.signin.authentik')}</span><span className="row-sub wrap">{t('step.signin.authentikSub')}</span></span>
              <span className="row-trail"><Icon name="check2" cls="icon checkmark" /></span>
            </label>
            <label className="row row-choice">
              <input type="radio" name="provider" value="kanidm" disabled />
              <span className="row-main"><span className="row-label">{t('step.signin.kanidm')}</span><span className="row-sub wrap">{t('step.signin.kanidmSub')}</span></span>
              <span className="row-trail"><Icon name="check2" cls="icon checkmark" /></span>
            </label>
          </div>
          {small && <div className="notice notice-warn" role="status"><Icon name="alert" /><span className="notice-body">
            {t('step.signin.small', { memory: memory(facts.memoryBytes) })}</span></div>}
        </>,
        more: <p className="hint">{t('step.signin.later')}</p>,
        notice,
        primary: { label: t('setup.continue'), busy },
        onPrimary: () => send({ provider: 'authentik' }),
      };
    }
    case 'join':
    case 'passkey':
    case 'done':
      if (moved) {
        const admin = a.adminUrl ?? '';
        return {
          illo: 'join', title: t('step.join.movedTitle'), lead: t('step.join.movedLead'),
          body: admin ? <p><a className="btn" href={admin}>{t('step.join.open')}</a> <span className="host">{admin}</span></p> : undefined,
        };
      }
      return {
        illo: 'join', title: t('step.join.title'), lead: t('step.join.lead'),
        body: <>
          <div className="as-check"><Icon name="check" /><span><b>{t('step.join.milestone')}</b> {t('step.join.milestoneSub')}</span></div>
          <p>{t('step.join.command')}</p>
          <div className="cmd"><code>{t('step.join.commandLine')}</code></div>
          <p className="hint as-waiting" role="status"><span className="spinner" aria-hidden="true" />{t('step.join.waiting')}</p>
        </>,
        more: <p className="hint">{t('step.join.once')}</p>,
        primary: { label: t('step.join.continue'), ok: false, why: t('step.join.why') },
      };
    default:
      return {
        illo: id, title: stepTitle(id), lead: t(`step.${id}.lead` as Parameters<typeof t>[0]),
        body: BUILT.has(id) ? undefined : (
          <div className="notice notice-info"><Icon name="info" /><span className="notice-body">{t('setup.error.unbuilt')}</span></div>
        ),
        primary: { label: t('setup.continue'), ok: false, why: t('step.unbuilt.why') },
      };
  }
}

/** What the page says when the manager didn't take an answer; "reload" when setup is elsewhere by now. */
type Failure = { says: Key; reload?: boolean };

function failure(e: unknown): Failure {
  if (e instanceof Unreachable) return { says: 'setup.fail.offline' };
  if (!(e instanceof Refused)) return { says: 'setup.fail.server' };
  if (diskFull(e)) return { says: 'setup.full.answer' };
  if (e.code === 'lan-half-done') return { says: 'setup.fail.lanDone', reload: true };
  if (e.code === 'over-the-mesh') return { says: 'setup.fail.mesh' };
  if (e.status === 409) return { says: 'setup.fail.moved', reload: true }; // earlier-step-open
  if (e.status === 501) return { says: 'setup.error.unbuilt' }; // not-built
  if (fromElsewhere(e)) return { says: 'setup.fail.here' }; // not-from-lan, cross-origin, wrong-host
  if (e.status >= 400 && e.status < 500) return { says: 'setup.fail.refused', reload: true }; // unknown-step, not-json
  return { says: 'setup.fail.server' };
}

/** The longest name the manager keeps, in code points (internal/manager/setup: NameMax). */
const NAME_MAX = 64;

/**
 * The household's name, made from the admin's surname: "The {surname} household", with the surname cut short to fit
 * when it is long, so the page never offers a name the manager refuses. It is cut between grapheme clusters, so an
 * accent, an emoji sequence or a flag stays whole or goes whole; and a tatweel, ZWNJ or ZWJ left at the end goes too.
 */
function defaultHousehold(surname: string): string {
  const whole = t('step.household.default', { surname });
  let room = NAME_MAX - ([...whole].length - [...surname].length);
  if (room >= [...surname].length) return whole;
  let kept = '';
  for (const { segment } of new Intl.Segmenter(undefined, { granularity: 'grapheme' }).segment(surname)) {
    room -= [...segment].length;
    if (room < 0) break;
    kept += segment;
  }
  kept = kept.replace(/[\u0640\u200C\u200D]+$/u, '');
  return kept ? t('step.household.default', { surname: kept }) : '';
}

/** What a field's refusal says: the words for its reason, or the field's own when it is missing or malformed. */
function refusal(reason: Reason | undefined, words: Partial<Record<Reason, Key>>, otherwise: Key): string | undefined {
  return reason && t(words[reason] ?? otherwise);
}

/** A notice that is read out as it appears. */
function FailNotice({ why }: { why: Failure }) {
  return (
    <div className="notice notice-warn" role="alert">
      <Icon name="alert" />
      <span className="notice-body">{t(why.says)}</span>
      {why.reload && (
        <span className="notice-actions">
          <button type="button" className="btn btn-sm" onClick={() => location.reload()}>{t('setup.fail.reload')}</button>
        </span>
      )}
    </div>
  );
}

function Left({ steps }: { steps: string[] }) {
  return (
    <div>
      <p className="group-head" id="as-left">{t('step.welcome.left')}</p>
      <ul className="group lead-sm" aria-labelledby="as-left">
        {steps.map((id) => (
          <li className="row" key={id}>
            <span className="row-lead"><Icon name="clock" cls="icon icon-md" /></span>
            <span className="row-main"><span className="row-label">{stepTitle(id)}</span></span>
          </li>
        ))}
      </ul>
    </div>
  );
}

/** What the admin will need on the way. */
function Needs() {
  const needs: [string, Key][] = [
    ['mesh', 'step.welcome.need.mesh'], ['globe', 'step.welcome.need.domain'], ['mail', 'step.welcome.need.mail'],
    ['printer', 'step.welcome.need.kit'], ['laptop', 'step.welcome.need.device'],
  ];
  return (
    <div>
      <p className="group-head" id="as-needs">{t('step.welcome.needs')}</p>
      <ul className="group lead-sm" aria-labelledby="as-needs">
        {needs.map(([i, k]) => (
          <li className="row" key={k}>
            <span className="row-lead"><Icon name={i} cls="icon icon-md" /></span>
            <span className="row-main"><span className="row-label wrap">{t(k)}</span></span>
          </li>
        ))}
      </ul>
    </div>
  );
}

/** The machine, as its agent reports it. */
function About({ facts }: { facts: Facts | null }) {
  if (!facts) {
    return <div className="notice notice-warn" role="status"><Icon name="alert" /><span className="notice-body">{t('step.welcome.agentSilent')}</span></div>;
  }
  const rows: [string, string][] = [
    [t('fact.machine'), facts.hostname],
    [t('fact.distro'), facts.distro.pretty || `${facts.distro.name} ${facts.distro.version}`],
    [t('fact.architecture'), facts.architecture],
    [t('fact.memory'), memory(facts.memoryBytes)],
    ...facts.disks.map((d): [string, string] => {
      const size = diskSize(d.sizeBytes);
      const detail = d.rotational ? t('fact.spinning', { size }) : d.removable ? t('fact.removable', { size }) : size;
      return [t('fact.disk'), `${d.model || d.name} · ${detail}`];
    }),
    [t('fact.tpm'), facts.tpm.present ? t('fact.tpmVersion', { version: facts.tpm.version ?? '' }).trim() : t('fact.noTPM')],
    ...(facts.gpus.length
      ? facts.gpus.map((g): [string, string] => [t('fact.gpu'), `${g.vendor} ${g.model}`.trim()])
      : [[t('fact.gpu'), t('fact.none')] as [string, string]]),
  ];
  return (
    <ul className="group" aria-label={t('step.welcome.about')}>
      {rows.map(([k, v], i) => (
        <li className="row" key={i}>
          <span className="row-main"><span className="row-sub">{k}</span><span className="row-label wrap">{v}</span></span>
        </li>
      ))}
    </ul>
  );
}
