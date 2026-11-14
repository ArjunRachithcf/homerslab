// Home: the apps a person may open, each with whether it is working (story 29), scripted from the Home prototype
// (prototypes/assets/home.js) on the design system. The data comes from Home's own API, which reads only the projection
// the manager keeps; Home can't change anything of the household's.
import { useCallback, useEffect, useRef, useState, type ReactNode } from 'react';
import { icon } from '../design';
import { t, type Key } from '../i18n/t';
import { getHome, SignedOut, signOut, type Health, type HomeView, type Role, type Tile } from './api';

/** How often Home asks again, for the apps' health, while it is on the screen. */
const REFRESH_MS = 30_000;

const Icon = ({ name, className = 'icon' }: { name: string; className?: string }) => (
  <span aria-hidden="true" style={{ display: 'contents' }} dangerouslySetInnerHTML={{ __html: icon(name, className) }} />
);

// The panels, in reading order; each catalog category belongs to one (prototypes/assets/home.js), the rest to the last.
const GROUPS: { id: string; title: Key; cats: string[] }[] = [
  { id: 'watch', title: 'home.group.watch', cats: ['films', 'music', 'books'] },
  { id: 'files', title: 'home.group.files', cats: ['photos', 'documents', 'knowledge'] },
  { id: 'home', title: 'home.group.home', cats: ['home', 'money', 'passwords', 'assistants'] },
  { id: 'other', title: 'home.group.other', cats: [] },
];

type State = { kind: 'ok' | 'starting' | 'down' | 'unknown'; dot: string; words: string; kidWords: string };

function stateOf(h: Health): State {
  switch (h) {
    case 'healthy':
      return { kind: 'ok', dot: 'green', words: t('home.state.healthy'), kidWords: t('home.state.healthy') };
    case 'starting':
      return { kind: 'starting', dot: 'orange', words: t('home.state.starting'), kidWords: t('home.state.starting') };
    case 'unhealthy':
    case 'stopped':
      return { kind: 'down', dot: 'red', words: t('home.state.down'), kidWords: t('home.state.kidDown') };
    default:
      return { kind: 'unknown', dot: '', words: t('home.state.unknown'), kidWords: t('home.state.unknown') };
  }
}

// The design system's state classes for a row or tile: down is red, starting orange (its "updating").
const stateClass = (s: State) => (s.kind === 'down' ? 'is-down' : s.kind === 'starting' ? 'is-updating' : '');

const greeting = (now: Date) => {
  const h = now.getHours();
  return t(h < 5 ? 'home.greet.evening' : h < 12 ? 'home.greet.morning' : h < 18 ? 'home.greet.afternoon' : 'home.greet.evening');
};
const firstName = (name: string) => name.split(/\s+/)[0] ?? name;
/** An avatar's hue, from the username: the same person keeps their colour. */
const hue = (s: string) => [...s].reduce((h, c) => (h * 31 + c.charCodeAt(0)) % 360, 7);
const roleKey: Record<Role, Key> = { admin: 'home.me.role.admin', adult: 'home.me.role.adult', kid: 'home.me.role.kid' };
/** The household's name inside a sentence: "The Ferreira household" → "the Ferreira household". */
const inSentence = (household: string) => household.replace(/^The /, 'the ');

const Avatar = ({ who, className = '' }: { who: HomeView['person']; className?: string }) => (
  <span className={`avatar ${className}`} style={{ ['--h' as string]: hue(who.username) }} aria-hidden="true">{firstName(who.name).charAt(0)}</span>
);

const AppIcon = ({ app }: { app: Tile }) =>
  app.icon ? <img className="app-icon" src={app.icon} alt="" decoding="async" />
    : <span className="app-icon is-mark" aria-hidden="true" dangerouslySetInnerHTML={{ __html: window.MARK }} />;

/** Panels after the first get spans so that panels in one row end level (12-column grid), as the prototype lays them. */
function layout<T>(groups: { items: T[] }[]): { span: number; cols: number }[] {
  const out: { span: number; cols: number }[] = [];
  const rows = (n: number, cols: number) => Math.ceil(n / cols);
  const [first, ...rest] = groups;
  if (first) out.push({ span: 12, cols: Math.min(3, first.items.length) });
  for (let i = 0; i < rest.length; i += 2) {
    const a = rest[i], b = rest[i + 1];
    if (!a) break;
    if (!b) {
      out.push({ span: 12, cols: Math.min(3, a.items.length) });
      break;
    }
    const tries = [[8, 2, 4, 1], [9, 3, 3, 1], [6, 1, 6, 1], [6, 2, 6, 2], [4, 1, 8, 2], [3, 1, 9, 3]] as const;
    const fit = tries.find(([, ca, , cb]) => rows(a.items.length, ca) === rows(b.items.length, cb) && a.items.length >= ca && b.items.length >= cb);
    if (fit) out.push({ span: fit[0], cols: fit[1] }, { span: fit[2], cols: fit[3] });
    else out.push({ span: 12, cols: Math.min(3, a.items.length) }, { span: 12, cols: Math.min(3, b.items.length) });
  }
  return out;
}

export function App() {
  const [view, setView] = useState<HomeView>();
  const [failed, setFailed] = useState(false);
  const [signedOut, setSignedOut] = useState(false);
  const [attempt, setAttempt] = useState(0);
  useEffect(() => {
    let live = true;
    const load = () => getHome().then((v) => {
      if (!live) return;
      setView(v);
      setFailed(false);
    }, (e: unknown) => {
      if (!live) return;
      if (e instanceof SignedOut) location.assign('/auth/sign-in');
      else setFailed(true);
    });
    void load();
    const every = setInterval(() => {
      if (document.visibilityState === 'visible') void load();
    }, REFRESH_MS);
    return () => {
      live = false;
      clearInterval(every);
    };
  }, [attempt]);
  const retry = useCallback(() => setAttempt((n) => n + 1), []);
  useEffect(() => {
    document.title = t('home.title');
  }, []);

  if (signedOut) return <SignedOutScreen />;
  if (!view) {
    return (
      <main className="page" id="home" tabIndex={-1}>
        {failed ? <Problem retry={retry} /> : <p className="h-loading" role="status">{t('home.loading')}</p>}
      </main>
    );
  }
  return (
    <>
      <a className="sr-only" href="#home">{t('home.skip')}</a>
      <Bar view={view} onSignedOut={() => setSignedOut(true)} />
      <Body view={view} />
    </>
  );
}

function Problem({ retry }: { retry: () => void }) {
  return (
    <div className="empty h-problem" role="alert">
      <Icon name="alert" />
      <p className="empty-title">{t('home.error.title')}</p>
      <p className="empty-text">{t('home.error.lead')}</p>
      <button type="button" className="btn" onClick={retry}>{t('home.error.retry')}</button>
    </div>
  );
}

function SignedOutScreen() {
  useEffect(() => document.getElementById('home')?.focus(), []);
  return (
    <main className="page" id="home" tabIndex={-1}>
      <section className="h-hello" aria-labelledby="h-title">
        <h1 id="h-title">{t('home.signedOut.title')}</h1>
        <p className="h-sub">{t('home.signedOut.lead')}</p>
      </section>
      <a className="btn btn-primary" href="/auth/sign-in">{t('home.signedOut.again')}</a>
    </main>
  );
}

/** The bar: the household's word, which gives way to the greeting once it scrolls under the bar; the person's menu. */
function Bar({ view, onSignedOut }: { view: HomeView; onSignedOut: () => void }) {
  const bar = useRef<HTMLElement>(null);
  const button = useRef<HTMLButtonElement>(null);
  const menu = useRef<HTMLDivElement>(null);
  const [open, setOpen] = useState(false);
  const [place, setPlace] = useState<{ top: number; right: number }>();
  useEffect(() => {
    const el = bar.current;
    if (!el) return;
    const onScroll = () => el.classList.toggle('is-scrolled', scrollY > 4);
    addEventListener('scroll', onScroll, { passive: true });
    onScroll();
    const title = document.getElementById('h-title');
    const seen = title && 'IntersectionObserver' in window
      ? new IntersectionObserver((es) => el.classList.toggle('is-titled', !es[0]?.isIntersecting), { rootMargin: `-${el.offsetHeight || 52}px 0px 0px 0px` })
      : undefined;
    if (title) seen?.observe(title);
    return () => {
      removeEventListener('scroll', onScroll);
      seen?.disconnect();
    };
  }, []);
  useEffect(() => {
    if (!open) return;
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') {
        setOpen(false);
        button.current?.focus();
      }
    };
    const onDown = (e: PointerEvent) => {
      if (!menu.current?.contains(e.target as Node) && !button.current?.contains(e.target as Node)) setOpen(false);
    };
    addEventListener('keydown', onKey);
    addEventListener('pointerdown', onDown);
    const first = setTimeout(() => menu.current?.querySelector<HTMLElement>('button, a')?.focus({ preventScroll: true }), 40);
    return () => {
      removeEventListener('keydown', onKey);
      removeEventListener('pointerdown', onDown);
      clearTimeout(first);
    };
  }, [open]);
  const toggle = () => {
    const r = button.current?.getBoundingClientRect();
    if (r) setPlace({ top: r.bottom + 8, right: Math.max(16, document.documentElement.clientWidth - r.right) });
    setOpen((o) => !o);
  };
  const out = async () => {
    try {
      await signOut();
    } finally {
      onSignedOut();
    }
  };
  const p = view.person;
  return (
    <>
      <header className="topbar" ref={bar}>
        <div className="page">
          <div className="topbar-inner">
            <a className="brand h-brand" href="/" aria-label={t('home.brand', { household: view.household })}>
              <span aria-hidden="true" style={{ display: 'contents' }} dangerouslySetInnerHTML={{ __html: window.MARK }} />
              <span className="names">
                <span className="word">{view.household}</span>
                <span className="mini" aria-hidden="true">{t('home.hello', { greeting: greeting(new Date()), name: firstName(p.name) }).replace(/\.$/, '')}</span>
              </span>
            </a>
            <div className="topbar-end">
              <button type="button" className="h-me" ref={button} aria-haspopup="dialog" aria-expanded={open}
                aria-controls={open ? 'me-menu' : undefined} onClick={toggle}>
                <Avatar who={p} /><span className="h-me-name">{p.name}</span>
              </button>
            </div>
          </div>
        </div>
      </header>
      <div className={`menu ${open ? 'is-open' : ''}`} id="me-menu" role="dialog" aria-label={t('home.me.menu')} ref={menu}
        style={place ? { top: place.top, right: place.right, left: 'auto' } : undefined}>
        <div className="menu-who">
          <Avatar who={p} className="avatar-lg" />
          <span><b>{p.name}</b><small>{t(roleKey[p.role], { household: inSentence(view.household) })}</small></span>
        </div>
        <div className="menu-sep" />
        <button type="button" className="menu-item" onClick={() => void out()}>
          <Icon name="signout" /><span>{t('home.me.signOut')}<small>{t('home.me.signOut.sub')}</small></span>
        </button>
      </div>
    </>
  );
}

function Body({ view }: { view: HomeView }) {
  const p = view.person, kid = p.role === 'kid';
  const apps = view.apps.map((a) => ({ a, st: stateOf(a.health) }));
  const trouble = apps.find((x) => x.st.kind === 'down') ?? apps.find((x) => x.st.kind === 'starting');
  const fact = kid ? t('home.fact.kid')
    : !apps.length ? t('home.fact.none')
      : trouble ? t(trouble.st.kind === 'down' ? 'home.fact.down' : 'home.fact.starting', { app: trouble.a.name })
        : apps.length === 1 ? t('home.fact.one') : t('home.fact.all');
  const domain = location.hostname.replace(/^home\./, '');
  const sub = kid || !apps.length ? '' : trouble ? (trouble.st.kind === 'starting' ? t('home.sub.starting') : apps.length > 1 ? t('home.sub.rest') : '')
    : t('home.sub.domain', { domain });
  const hello = kid ? t('home.hello.kid', { name: firstName(p.name) }) : t('home.hello', { greeting: greeting(new Date()), name: firstName(p.name) });

  let body: ReactNode;
  if (!apps.length) {
    body = (
      <section className="panel glass h-none" aria-labelledby="h-none">
        <div className="empty"><p className="empty-title" id="h-none">{t('home.empty.title')}</p><p className="empty-text">{t('home.empty.lead')}</p></div>
      </section>
    );
  } else if (kid) {
    body = (
      <section aria-label={t('home.apps')}>
        <div className="kid-tiles">
          {apps.map(({ a, st }) => (
            <a key={a.id} className={`kid-tile glass glass-react ${stateClass(st)}`} href={a.url}>
              <AppIcon app={a} />
              <span className="kid-tile-name">{a.name}</span>
              <span className="kid-tile-for">{a.purpose}</span>
              <span className="kid-tile-state">{st.dot && <span className={`dot dot-${st.dot}`} />}{st.kidWords}</span>
            </a>
          ))}
        </div>
      </section>
    );
  } else {
    const groups = GROUPS.map((g, i) => ({ ...g, items: apps.filter((x) => (i === GROUPS.length - 1
      ? !GROUPS.some((o) => o.cats.includes(x.a.category)) : g.cats.includes(x.a.category))) })).filter((g) => g.items.length);
    const spans = layout(groups);
    body = (
      <div className="grid h-panels">
        {groups.map((g, gi) => {
          const { span, cols } = spans[gi] ?? { span: 12, cols: 1 };
          return (
            <section key={g.id} className={`panel glass glass-react span-${span}`} aria-labelledby={`g-${g.id}`}>
              <h2 className="panel-title" id={`g-${g.id}`}>{t(g.title)} <span className="count">{g.items.length}</span></h2>
              <div className={`panel-rows ${cols === 1 ? 'list' : ''}`} style={{ ['--cols' as string]: cols }}>
                {g.items.map(({ a, st }) => (
                  <a key={a.id} className={`app-row ${stateClass(st)}`} href={a.url}>
                    <AppIcon app={a} />
                    <span className="app-row-text"><span className="app-row-name">{a.name}</span><span className="app-row-for">{a.purpose}</span></span>
                    <span className="app-row-end">
                      {st.kind === 'ok' ? <span className="sr-only">{st.words}</span> : <span className="app-row-state">{st.words}</span>}
                      <span className={`dot ${st.dot ? `dot-${st.dot}` : ''}`} aria-hidden="true" />
                      <Icon name="chevronRight" className="icon row-chev" />
                    </span>
                  </a>
                ))}
              </div>
            </section>
          );
        })}
      </div>
    );
  }

  return (
    <main className={`page ${kid ? 'h-kid' : ''}`} id="home" tabIndex={-1}>
      <section className="h-hello" aria-labelledby="h-title">
        <h1 id="h-title">{hello} <span className="fact">{fact}</span></h1>
        {sub && <p className="h-sub">{sub}</p>}
      </section>
      {body}
      {view.admin && (
        <section className="h-sec" aria-labelledby="h-admin">
          <div className="group-head"><h2 id="h-admin">{t('home.admin.head')}</h2></div>
          <div className="group glass lead-sm">
            <a className="row row-link" href={view.admin}>
              <span className="row-lead"><Icon name="settings" className="icon icon-md" /></span>
              <span className="row-main"><span className="row-title">{t('home.admin.title')}</span><span className="row-sub wrap">{t('home.admin.sub')}</span></span>
              <Icon name="chevronRight" className="icon row-chev" />
            </a>
          </div>
        </section>
      )}
      {!kid && (
        <section className="h-sec only-phone" aria-labelledby="h-dev">
          <div className="group-head"><h2 id="h-dev">{t('home.device.head')}</h2></div>
          <div className="group glass lead-sm">
            <div className="row">
              <span className="row-lead"><Icon name="phone" className="icon icon-md" /></span>
              <span className="row-main"><span className="row-title">{t('home.device.title')}</span><span className="row-sub wrap">{t('home.device.sub')}</span></span>
            </div>
          </div>
        </section>
      )}
      <footer className="h-foot"><span>{t('home.foot', { household: view.household })}</span><span>{t('home.foot.icons')}</span></footer>
    </main>
  );
}
