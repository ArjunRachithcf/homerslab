// First-time setup's page. It claims the one-time link from the address's fragment, then shows the step setup is at,
// from the manager's state: one question per screen, in plain words. What is answered is kept in the manager's
// database, so setup resumes where it stopped.
import { useCallback, useEffect, useState } from 'react';
import { flushSync } from 'react-dom';
import { rich, t, type Key } from '../i18n/t';
import { answer, claim, getPrimary, getSetup, Refused, type Facts, type Setup } from './api';
import { Assistant, Field, Icon, stepTitle, type Screen } from './Assistant';
import { diskSize, memory } from './format';

type Problem = 'refused' | 'error' | 'full';
type Phase = { kind: 'loading' } | { kind: Problem } | { kind: 'ready'; setup: Setup; facts: Facts | null };

/** The disk under the manager's database is full: nothing can be kept until there is room. */
const diskFull = (e: unknown) => e instanceof Refused && e.code === 'disk-full';

const BUILT = new Set(['welcome', 'you', 'household']);
const validMail = (m: string) => /^[^@\s]+@[^@\s]+\.[^@\s]+$/.test(m.trim());

export function App() {
  const [phase, setPhase] = useState<Phase>({ kind: 'loading' });
  const [view, setView] = useState('welcome');

  const load = useCallback(async () => {
    try {
      const [setup, facts] = await Promise.all([getSetup(), getPrimary().catch(() => null)]);
      setPhase({ kind: 'ready', setup, facts });
      const asked = location.hash.replace(/^#\//, '');
      setView(reachable(setup, asked) ? asked : 'welcome');
    } catch (e) {
      setPhase(e instanceof Refused && e.status === 401 ? { kind: 'refused' } : diskFull(e) ? { kind: 'full' } : { kind: 'error' });
    }
  }, []);

  useEffect(() => {
    // The key rides in the fragment, which never reaches a server or a log. It is claimed once, then dropped. A link
    // can also arrive in a tab that already shows setup: then only the fragment changes.
    const open = async () => {
      const key = new URLSearchParams(location.hash.slice(1)).get('k');
      if (key) {
        history.replaceState(null, '', `${location.pathname}#/welcome`);
        setView('welcome');
        try {
          await claim(key);
        } catch (e) {
          // A used link may be this tab's own, opened again: its session carries on, and load() says if not. Another
          // tab has no session: it needs a new link.
          if (!(e instanceof Refused) || diskFull(e)) {
            setPhase({ kind: diskFull(e) ? 'full' : 'error' });
            return;
          }
        }
      }
      await load();
    };
    const onHash = () => {
      if (new URLSearchParams(location.hash.slice(1)).has('k')) void open();
    };
    void open();
    window.addEventListener('hashchange', onHash);
    return () => window.removeEventListener('hashchange', onHash);
  }, [load]);

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
    screen = <ProblemScreen problem={phase.kind} />;
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
function ProblemScreen({ problem }: { problem: Problem }) {
  const refused = problem === 'refused';
  const title = t(`setup.${problem}.title`);
  useTitle(title);
  return (
    <Assistant steps={[]} now="" illoFacts={{}} illo="welcome" title={title} lead={t(`setup.${problem}.lead`)}
      body={refused ? <div className="cmd"><code>{t('setup.refused.command')}</code></div> : undefined}
      primary={refused ? undefined : { label: t('setup.error.retry') }} onPrimary={() => location.reload()} />
  );
}

function useTitle(title: string) {
  useEffect(() => {
    document.title = t('setup.title', { title });
  }, [title]);
}

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
  const [bad, setBad] = useState<string[]>([]);
  const [fullAt, setFullAt] = useState<string | null>(null); // the step whose answer met a full disk
  const [busy, setBusy] = useState(false);
  const householdValue = touched || !surname ? household : t('step.household.default', { surname });

  const send = async (body: Record<string, string>) => {
    setBusy(true);
    try {
      onAnswered(await answer(id, body));
      setBad([]);
      setFullAt(null);
      next();
    } catch (e) {
      setFullAt(diskFull(e) ? id : null);
      if (e instanceof Refused && e.code === 'invalid') setBad(e.fields);
      else if (e instanceof Refused && e.status === 401) location.reload();
    } finally {
      setBusy(false);
    }
  };

  const v = ((): StepView => {
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
              error={bad.includes('name') ? t('step.you.nameInvalid') : undefined} />
            <Field id="mail" label={t('step.you.mail')} value={mail} onChange={setMail} type="email" autoComplete="email"
              hint={t('step.you.mailHint')} error={bad.includes('mail') ? t('step.you.mailInvalid') : undefined} />
          </>,
          primary: { label: t('setup.continue'), ok, why: t('step.you.why'), busy },
          onPrimary: () => send({ name, mail }),
        };
      }
      case 'household':
        return {
          illo: 'household', title: t('step.household.title'), lead: t('step.household.lead'), illoFacts: { name: householdValue },
          body: <Field id="household" label={t('step.household.name')} value={householdValue}
            onChange={(v) => { setTouched(true); setHousehold(v); }}
            error={bad.includes('name') ? t('step.household.invalid') : undefined} />,
          primary: { label: t('setup.continue'), ok: !!householdValue.trim(), why: t('step.household.why'), busy },
          onPrimary: () => send({ name: householdValue }),
        };
      case 'join':
      case 'passkey':
      case 'done':
        return {
          illo: 'join', title: t('step.join.title'), lead: t('step.join.lead'),
          body: <div className="as-check"><Icon name="check" /><span><b>{t('step.join.milestone')}</b> {t('step.join.milestoneSub')}</span></div>,
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
  })();
  return fullAt === id ? { ...v, alert: t('setup.full.answer') } : v;
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
