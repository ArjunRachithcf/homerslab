// Admin: the Store (the catalog, an app's page) and the install plan, a page of its own whose address carries the
// plan's id. Scripted from the Admin prototype (prototypes/assets/admin-catalog.js); the data comes from the manager.
import { useCallback, useEffect, useState, type MouseEvent, type ReactNode } from 'react';
import { flushSync } from 'react-dom';
import { icon } from '../design';
import { rich, t, type Key } from '../i18n/t';
import { getApp, getCatalog, getPlan, makePlan, Refused, type Plan, type StoreApp, type Warning } from './api';
import { and, disk, lower, memory } from './format';

type Route = { page: 'store' } | { page: 'app'; id: string } | { page: 'plan'; id: string } | { page: 'none' };

function route(path: string): Route {
  let m: RegExpExecArray | null;
  if (path === '/' || path === '/store') return { page: 'store' };
  if ((m = /^\/store\/apps\/([\w-]+)$/.exec(path))) return { page: 'app', id: m[1] ?? '' };
  if ((m = /^\/plans\/(p-[0-9a-f]+)$/.exec(path))) return { page: 'plan', id: m[1] ?? '' };
  return { page: 'none' };
}

const Icon = ({ name, className = 'icon' }: { name: string; className?: string }) => (
  <span aria-hidden="true" style={{ display: 'contents' }} dangerouslySetInnerHTML={{ __html: icon(name, className) }} />
);

type Go = (path: string, type?: 'push' | 'pop') => void;

/** A link inside Admin: it moves without reloading the page. */
function Link({ to, go, className, children, current }: { to: string; go: Go; className?: string; children: ReactNode; current?: boolean }) {
  const click = (e: MouseEvent) => {
    if (e.metaKey || e.ctrlKey || e.shiftKey || e.button !== 0) return;
    e.preventDefault();
    go(to);
  };
  return <a href={to} className={className} onClick={click} aria-current={current ? 'page' : undefined}>{children}</a>;
}

export function App() {
  const [path, setPath] = useState(location.pathname);
  const go = useCallback<Go>((to, type = 'push') => {
    if (to !== location.pathname) history.pushState(null, '', to);
    window.HL.run(() => flushSync(() => setPath(to)), { type }).then(() => {
      document.getElementById('main')?.focus({ preventScroll: true });
      window.scrollTo(0, 0);
    });
  }, []);
  useEffect(() => {
    const onPop = () => window.HL.run(() => flushSync(() => setPath(location.pathname)), { type: 'pop' });
    window.addEventListener('popstate', onPop);
    return () => window.removeEventListener('popstate', onPop);
  }, []);

  const r = route(path);
  return (
    <>
      <a className="sr-only" href="#main">{t('admin.skip')}</a>
      <div className="a-shell">
        <aside className="sidebar glass a-side" aria-label={t('admin.nav')}>
          <div className="brand" role="img" aria-label={t('brand')} dangerouslySetInnerHTML={{ __html: window.LOCKUP }} />
          <nav className="nav" aria-label={t('admin.nav')}>
            <Link to="/store" go={go} className="nav-item" current={r.page !== 'none'}>
              <Icon name="store" /><span>{t('admin.nav.store')}</span>
            </Link>
          </nav>
          <p className="a-where">{t('admin.where')}</p>
        </aside>
        <div className="a-canvas">
          <main className="a-main" id="main" tabIndex={-1}>
            <div className="a-page">
              {r.page === 'store' && <StorePage go={go} />}
              {r.page === 'app' && <AppScreen key={r.id} id={r.id} go={go} />}
              {r.page === 'plan' && <PlanScreen key={r.id} id={r.id} go={go} />}
              {r.page === 'none' && <Missing heading="admin.notFound.title" lead="admin.notFound.lead" />}
            </div>
          </main>
        </div>
      </div>
    </>
  );
}

/** load fetches a page's data once, and says when the manager didn't answer. */
function useLoad<T>(load: () => Promise<T>): { data?: T; error?: Refused | Error } {
  const [state, setState] = useState<{ data?: T; error?: Refused | Error }>({});
  useEffect(() => {
    let live = true;
    load().then((data) => live && setState({ data }), (error: Error) => live && setState({ error }));
    return () => {
      live = false;
    };
  }, [load]);
  return state;
}

function Problem() {
  return (
    <div className="empty" role="alert">
      <Icon name="alert" />
      <p className="empty-title">{t('admin.error.title')}</p>
      <p className="empty-text">{t('admin.error.lead')}</p>
      <button type="button" className="btn" onClick={() => location.reload()}>{t('admin.error.retry')}</button>
    </div>
  );
}

function Missing({ heading, lead }: { heading: Key; lead: Key }) {
  useTitle(t(heading));
  return (
    <div className="page-head"><div><h1>{t(heading)}</h1><p className="page-sub">{t(lead)}</p></div></div>
  );
}

const Loading = () => <p className="a-loading" role="status">{t('admin.loading')}</p>;

function useTitle(title: string | undefined) {
  useEffect(() => {
    if (title) document.title = t('admin.title', { title });
  }, [title]);
}

const tierWords = (tier: string) => t(tier === 'verified' ? 'store.tier.verified' : 'store.tier.community');
const Tier = ({ tier }: { tier: string }) => (
  <span className="tier"><Icon name={tier === 'verified' ? 'shield' : 'people'} />{tierWords(tier)}</span>
);
const AppIcon = ({ src, className = '' }: { src: string; className?: string }) => <img className={`app-icon ${className}`} src={src} alt="" />;

/** Get: makes the app's install plan, and opens it. */
function GetButton({ app, go, primary }: { app: { id: string; name: string }; go: Go; primary?: boolean }) {
  const [state, setState] = useState<{ busy?: boolean; why?: string }>({});
  const get = async () => {
    setState({ busy: true });
    try {
      go(`/plans/${(await makePlan(app.id)).id}`);
    } catch (e) {
      const code = e instanceof Refused ? e.code : 'unknown';
      setState({ why: code === 'household-not-set-up' ? t('store.notSetUp') : t('store.cantPlan', { why: code }) });
    }
  };
  return (
    <>
      <button type="button" className={`store-get ${primary ? 'is-primary' : ''}`} aria-label={t('store.getLabel', { app: app.name })}
        aria-busy={state.busy} disabled={state.busy} onClick={get}>
        {state.busy ? t('store.planning') : t('store.get')}
      </button>
      {state.why && <p className="notice notice-warn a-get-why" role="alert">{state.why}</p>}
    </>
  );
}

function StorePage({ go }: { go: Go }) {
  const { data, error } = useLoad(getCatalog);
  useTitle(t('store.title'));
  if (error) return <Problem />;
  if (!data) return <Loading />;
  return (
    <>
      <div className="store-head">
        <div className="store-head-row"><h1>{t('store.title')}</h1></div>
        <p className="page-sub">{t('store.sub', { catalog: data.catalog.name, publisher: data.catalog.publisher })}</p>
      </div>
      {data.categories.map((k) => <Shelf key={k.id} id={k.id} name={k.name} apps={data.apps.filter((a) => a.category === k.id)} go={go} />)}
    </>
  );
}

function Shelf({ id, name, apps, go }: { id: string; name: string; apps: StoreApp[]; go: Go }) {
  if (!apps.length) return null;
  return (
    <section className="store-shelf" aria-labelledby={`sh-${id}`}>
      <div className="store-shelf-head"><div><h2 id={`sh-${id}`}>{name}</h2></div></div>
      <div className="card store-grid">
        {apps.map((a) => (
          <div className="store-item" key={a.id}>
            <Link to={`/store/apps/${a.id}`} go={go} className="store-item-link">
              <AppIcon src={a.icon} className="app-icon-store" />
              <span className="store-item-text"><b>{a.name}</b><span>{a.purpose}</span></span>
            </Link>
            <GetButton app={a} go={go} />
          </div>
        ))}
      </div>
    </section>
  );
}

function AppScreen({ id, go }: { id: string; go: Go }) {
  const load = useCallback(() => getApp(id), [id]);
  const { data, error } = useLoad(load);
  useTitle(data?.app.name);
  if (error instanceof Refused && error.status === 404) return <Missing heading="admin.notFound.title" lead="admin.notFound.lead" />;
  if (error) return <Problem />;
  if (!data) return <Loading />;
  const a = data.app;
  const kept = a.data.filter((d) => d.backup !== false).map((d) => lower(d.what));
  const r = a.resources;
  let needs = t('store.needs.value', { memory: memory(r.memoryMiB * 2 ** 20), disk: disk(r.diskGiB * 2 ** 30) });
  if (r.grows) needs += t('store.needs.grows', { what: r.grows });
  needs += r.gpu.need === 'none' ? t('store.needs.gpu.none') : t(r.gpu.need === 'required' ? 'store.needs.gpu.required' : 'store.needs.gpu.optional', { what: r.gpu.for ?? '' });
  const wired: [string, string][] = [
    [t('store.signIn'), t(`store.signIn.${a.signIn[0]?.method ?? 'guard'}` as Key)],
    [t('store.backups'), kept.length ? t('store.backups.some', { what: and(kept) }) : t('store.backups.none')],
    [t('store.exposure'), t(a.names.public ? 'store.exposure.public' : 'store.exposure.mesh')],
  ];
  const info: [string, string][] = [
    [t('store.catalog'), t('store.catalog.value', { catalog: data.catalog.name, publisher: data.catalog.publisher })],
    [t('store.tier'), tierWords(a.tier)],
    [t('store.version'), a.versions.app],
    [t('store.needs'), needs],
    [t('store.extras'), a.extras.length ? a.extras.map((x) => x.why).join('; ') : t('store.extras.none')],
    [t('store.kids'), a.audience.kidView ? t('store.kids.view', { view: a.audience.kidView }) : t('store.kids.none')],
  ];
  return (
    <>
      <Link to="/store" go={go} className="topbar-back a-back"><Icon name="chevronBack" />{t('admin.back.store')}</Link>
      <div className="store-app-head">
        <AppIcon src={data.icon} className="app-icon-xl" />
        <div className="store-app-names">
          <h1>{a.name}</h1>
          <p className="store-app-purpose">{a.purpose}</p>
          <div className="store-app-badges"><Tier tier={a.tier} /></div>
          <div className="store-app-act"><GetButton app={a} go={go} primary /></div>
        </div>
      </div>
      <section className="store-sec" aria-label={t('store.shots')}>
        <div className="store-shots" tabIndex={0} role="group" aria-label={t('store.shotsScroll')}>
          {data.screenshots.map((s) => (
            <figure className="store-shot" key={s.url}>
              <img className="store-shot-img" src={s.url} alt={s.caption} loading="lazy" />
              <figcaption>{s.caption}</figcaption>
            </figure>
          ))}
        </div>
      </section>
      <p className="store-about">{a.description}</p>
      <Facts id="st-wired" title={t('store.wired')} rows={wired} />
      <Facts id="st-info" title={t('store.info')} rows={info} two />
    </>
  );
}

function Facts({ id, title, rows, two }: { id: string; title: string; rows: [string, string][]; two?: boolean }) {
  return (
    <section className="store-sec" aria-labelledby={id}>
      <h2 id={id}>{title}</h2>
      <div className="card store-card">
        <dl className={`store-dl ${two ? 'store-dl-2' : ''}`}>
          {rows.map(([k, v]) => <div key={k}><dt>{k}</dt><dd>{v}</dd></div>)}
        </dl>
      </div>
    </section>
  );
}

function PlanScreen({ id, go }: { id: string; go: Go }) {
  const load = useCallback(() => getPlan(id), [id]);
  const { data: p, error } = useLoad(load);
  useTitle(p ? t('plan.title', { app: p.app.name }) : undefined);
  if (error instanceof Refused && error.status === 404) return <Missing heading="plan.missing.title" lead="plan.missing.lead" />;
  if (error) return <Problem />;
  if (!p) return <Loading />;
  const when = new Date(p.madeAt).toLocaleString(undefined, { dateStyle: 'medium', timeStyle: 'short' });
  return (
    <>
      <Link to={`/store/apps/${p.app.id}`} go={go} className="topbar-back a-back"><Icon name="chevronBack" />{p.app.name}</Link>
      <div className="page-head">
        <div className="a-ident">
          <AppIcon src={`/api/v1/catalog/apps/${p.app.id}/icon`} className="app-icon-lg" />
          <div><h1>{t('plan.title', { app: p.app.name })}</h1><p className="page-sub">{t('plan.sub', { app: p.app.name })}</p></div>
        </div>
      </div>
      <p className="plan-meta">
        {rich('plan.meta', { id: <span className="plan-id">{p.id}</span>, when, app: p.app.name, version: p.app.version, tier: tierWords(p.app.tier), catalog: p.app.catalog, publisher: p.app.publisher })}
      </p>
      <div className="split">
        <div className="plan-sections"><PlanSections p={p} /></div>
        <aside className="rail plan-rail" aria-label={t('plan.creates')}><PlanCard p={p} /></aside>
      </div>
    </>
  );
}

const Q = ({ id, title, sub }: { id: string; title: string; sub?: ReactNode }) => (
  <div className="plan-q"><h2 id={id}>{title}</h2>{sub && <span>{sub}</span>}</div>
);

function Row({ lead, title, sub, trail }: { lead?: string; title: ReactNode; sub?: ReactNode; trail?: ReactNode }) {
  return (
    <div className="row is-top">
      {lead && <span className="row-lead"><Icon name={lead} className="icon icon-md" /></span>}
      <span className="row-main"><span className="row-label wrap">{title}</span>{sub && <span className="row-sub wrap">{sub}</span>}</span>
      {trail}
    </div>
  );
}

const engineName = (e: string) => t(e === 'podman' ? 'engine.podman' : 'engine.docker');

const plural = (n: number, one: Key, many: Key, vars: Record<string, string | number>) => t(n === 1 ? one : many, { n, ...vars });

function PlanSections({ p }: { p: Plan }) {
  const node = p.placement.node;
  const names = p.placement.services.map((s) => s.name);
  const people = p.audience.people.map((x) => x.name);
  const r = p.resources;
  const warn = (w: Warning): string => {
    const v = { node, app: p.app.name };
    switch (w.kind) {
      case 'memory': return t('plan.warn.memory', { ...v, needs: memory(w.needsBytes ?? 0), free: memory(w.freeBytes ?? 0) });
      case 'disk': return w.unknown ? t('plan.warn.diskUnknown', { ...v, needs: disk(w.needsBytes ?? 0) }) : t('plan.warn.disk', { ...v, needs: disk(w.needsBytes ?? 0), free: disk(w.freeBytes ?? 0) });
      case 'gpu': return t('plan.warn.gpu', { ...v, vendors: (r.gpu.vendors ?? []).join(', ') });
      default: return t('plan.warn.build', { ...v, arch: p.placement.architecture, engine: engineName(p.placement.engine), services: (w.services ?? []).join(', ') });
    }
  };
  const short = new Set(r.warnings.map((w) => w.kind));
  const enough = (kind: Warning['kind']) => (
    <span className={`pill ${short.has(kind) ? 'pill-orange' : 'pill-green'}`}>{t(short.has(kind) ? 'plan.short' : 'plan.enough')}</span>
  );
  const amount = (a: Plan['resources']['memory'], fmt: (b: number) => string) =>
    a.unknown ? t('plan.amount.unknown', { needs: fmt(a.needsBytes), node })
      : a.grows ? t('plan.amount.grows', { needs: fmt(a.needsBytes), free: fmt(a.freeBytes), what: a.grows })
        : t('plan.amount', { needs: fmt(a.needsBytes), free: fmt(a.freeBytes) });
  let gpu = t('plan.gpu.none');
  if (r.gpu.need !== 'none') {
    if (r.gpu.usable.length) gpu = t('plan.gpu.uses', { gpus: and(r.gpu.usable), what: r.gpu.for ?? '' });
    else if (r.gpu.need === 'optional') gpu = t('plan.gpu.without', { what: r.gpu.for ?? '' });
    else gpu = t('plan.gpu.missing', { vendors: (r.gpu.vendors ?? []).join(', '), what: r.gpu.for ?? '' });
  }
  const secrets = p.secrets.map((s) => s.what);
  const sealed = p.secrets[0]?.sealedTo === 'host' ? 'host' : 'tpm';
  const signInVars = { app: p.app.name, provider: p.signIn.provider.name, node };
  return (
    <>
      <section aria-labelledby="pq-where">
        <Q id="pq-where" title={t('plan.where')} sub={t('plan.where.sub')} />
        <div className="group">
          <Row lead="nodes" title={<b>{node}</b>} sub={<>{t('plan.where.node', { arch: p.placement.architecture, engine: engineName(p.placement.engine) })}<br />
            {plural(names.length, 'plan.where.containers.one', 'plan.where.containers.many', { names: names.join(', ') })}</>} />
        </div>
      </section>
      <section aria-labelledby="pq-who">
        <Q id="pq-who" title={t('plan.who')} sub={t('plan.who.sub')} />
        <div className="group">
          <Row lead="people" title={`${t(`plan.who.${p.audience.default}` as Key)}: ${people.length ? and(people) : t('plan.who.nobody')}`}
            sub={p.audience.kidView ? t('plan.who.kidView', { view: p.audience.kidView }) : undefined} />
        </div>
      </section>
      <section aria-labelledby="pq-signin">
        <Q id="pq-signin" title={t('plan.signIn')} />
        <div className="group">
          <Row lead={p.signIn.method === 'guard' ? 'shield' : 'user'} title={t(`plan.signIn.${p.signIn.method}` as Key, signInVars)}
            sub={t(`plan.signIn.${p.signIn.method}.sub` as Key, signInVars)} />
        </div>
      </section>
      <section aria-labelledby="pq-name">
        <Q id="pq-name" title={t('plan.name')} />
        <div className="group">
          <Row lead="mesh" title={<span className="host">{p.name.host}</span>}
            sub={<>{p.name.why && <>{t(`plan.name.why.${p.name.why}` as Key, { asked: p.name.asked ?? '' })}<br /></>}
              {t(p.name.publicAllowed ? 'plan.name.public' : 'plan.name.mesh', { meshes: and(p.name.meshes), node })}</>} />
        </div>
      </section>
      <section aria-labelledby="pq-backups">
        <Q id="pq-backups" title={t('plan.backups')} sub={t('plan.backups.sub')} />
        <div className="group lead-sm">
          {p.backups.data.map((d) => <Row key={d.what} lead="check" title={d.what} sub={t(d.kind === 'dump' ? 'plan.backups.dump' : 'plan.backups.folder')} />)}
          {p.backups.left.map((d) => <Row key={d.what} lead="minus" title={d.what} sub={t('plan.backups.left', { why: d.why.startsWith(p.app.name) ? d.why : lower(d.why) })} />)}
          {!p.backups.data.length && !p.backups.left.length && <Row lead="minus" title={t('plan.backups.none')} />}
        </div>
        {p.backups.data.length > 0 && <p className="group-foot">{t('plan.backups.foot')}</p>}
      </section>
      <section aria-labelledby="pq-resources">
        <Q id="pq-resources" title={t('plan.resources')} sub={t('plan.resources.sub', { node })} />
        {r.warnings.length > 0 && (
          <div className="notice notice-warn a-warnings" role="note">
            <Icon name="alert" />
            <span className="notice-body">{r.warnings.map((w) => <b key={w.kind}>{warn(w)}</b>)}{t('plan.warn.lead')}</span>
          </div>
        )}
        <div className="group lead-sm">
          <Row lead="memory" title={t('plan.memory')} sub={t('plan.amount.memory', { needs: memory(r.memory.needsBytes), free: memory(r.memory.freeBytes) })} trail={enough('memory')} />
          <Row lead="disk" title={t('plan.disk')} sub={amount(r.disk, disk)} trail={enough('disk')} />
          <Row lead="gpu" title={t('plan.gpu')} sub={gpu} trail={r.gpu.need === 'none' ? undefined : enough('gpu')} />
        </div>
      </section>
      <section aria-labelledby="pq-extras">
        <Q id="pq-extras" title={t('plan.extras')} sub={p.risky ? <span className="needs-key"><Icon name="key" />{t('plan.extras.key')}</span> : t('plan.extras.sub')} />
        <div className="group">
          {p.extras.length ? p.extras.map((x) => (
            <Row key={x.id} lead="key" title={x.why} sub={t('plan.extras.item', { kind: x.kind, what: x.device ?? x.capability ?? '', service: x.service })}
              trail={<span className="row-value">{t(x.optional ? 'plan.extras.optional' : 'plan.extras.required')}</span>} />
          )) : <Row title={t('plan.extras.none')} />}
        </div>
      </section>
      <section aria-labelledby="pq-secrets">
        <Q id="pq-secrets" title={t('plan.secrets')} />
        <div className="group">
          <Row lead="lock" title={secrets.length
            ? plural(secrets.length, `plan.secrets.${sealed}.one` as Key, `plan.secrets.${sealed}.many` as Key, { node, what: and(secrets) })
            : t('plan.secrets.none')} />
        </div>
      </section>
    </>
  );
}

function PlanCard({ p }: { p: Plan }) {
  const vars = { app: p.app.name, provider: p.signIn.provider.name, node: p.placement.node };
  const people = p.audience.people.map((x) => x.name);
  return (
    <div className="card plan-card">
      <h2 className="card-title">{t('plan.creates')}</h2>
      <ul className="lines">
        <li><Icon name={p.signIn.method === 'guard' ? 'shield' : 'user'} /><span>{t(`plan.creates.${p.signIn.method}` as Key, vars)}</span></li>
        <li><Icon name="mesh" /><span>{rich('plan.creates.name', { host: <span className="host">{p.name.host}</span> })}</span></li>
        {p.secrets.length > 0 && <li><Icon name="lock" /><span>{plural(p.secrets.length, 'plan.creates.secrets.one', 'plan.creates.secrets.many', {})}</span></li>}
        {p.backups.data.length > 0 && <li><Icon name="backups" /><span>{t('plan.creates.backups')}</span></li>}
        {people.length > 0 && <li><Icon name="home" /><span>{t('plan.creates.home', { people: and(people) })}</span></li>}
      </ul>
      <button type="button" className="btn btn-primary btn-lg btn-block" disabled aria-describedby="install-why">
        {p.risky && <Icon name="key" />}{t(p.risky ? 'plan.installKey' : 'plan.install')}
      </button>
      <p className="btn-why" id="install-why">{t('plan.install.later')}</p>
      <p className="stepup-bound"><Icon name="lock" /><span>{rich(p.risky ? 'plan.boundKey' : 'plan.bound', { id: <span className="plan-id">{p.id}</span> })}</span></p>
    </div>
  );
}
