// The setup assistant's card, from the design system: progress dots in two halves, a calm illustration, one title,
// one sentence, the question, "More options", and one primary button.
import type { FormEvent, ReactNode } from 'react';
import { icon } from '../design';
import { t, type Key } from '../i18n/t';
import type { StepState } from './api';

export type Screen = {
  illo: string;
  title: string;
  lead?: ReactNode;
  body?: ReactNode;
  moreLabel?: string;
  more?: ReactNode;
  primary?: { label: string; ok?: boolean; why?: string; busy?: boolean };
};

type Props = Screen & {
  steps: StepState[];
  now: string;
  illoFacts: { name?: string; mail?: string };
  onBack?: () => void;
  onPrimary?: () => void;
};

const Html = ({ html, className }: { html: string; className?: string }) => (
  <span className={className} dangerouslySetInnerHTML={{ __html: html }} />
);

export function Assistant(p: Props) {
  const i = p.steps.findIndex((s) => s.id === p.now);
  const submit = (e: FormEvent) => {
    e.preventDefault();
    if (p.primary && p.primary.ok !== false && !p.primary.busy) p.onPrimary?.();
  };
  const illo = window.ILLO[p.illo]?.(p.illoFacts) ?? '';
  const off = p.primary?.ok === false;
  return (
    <form className="assistant glass-thick" aria-labelledby="as-title" onSubmit={submit} noValidate>
      {p.onBack && (
        <button type="button" className="topbar-back as-back" onClick={p.onBack}>
          <Html html={icon('chevronBack')} />
          {t('setup.back')}
        </button>
      )}
      <div className="dots" aria-hidden="true">
        {(['lan', 'mesh'] as const).map((half) => (
          <span className="dots-half" key={half}>
            {p.steps.filter((s) => s.half === half).map((s) => (
              <i key={s.id} className={`dot-step${s.id === p.now ? ' is-now' : s.state === 'done' ? ' is-done' : ''}`} />
            ))}
          </span>
        ))}
      </div>
      <div dangerouslySetInnerHTML={{ __html: window.HL.still() ? illo : illo.replace('class="illo ', 'class="illo is-in ') }} />
      <div>
        <h1 className="as-title" id="as-title" tabIndex={-1}>
          {i >= 0 && <span className="sr-only">{t('setup.stepOf', { n: i + 1, total: p.steps.length })}</span>}
          {p.title}
        </h1>
        {p.lead && <p className="as-lead">{p.lead}</p>}
      </div>
      {p.body && <div className="as-body">{p.body}</div>}
      {p.more && (
        <details className="more">
          <summary>
            {p.moreLabel ?? t('setup.more')}
            <Html html={icon('chevronDown')} />
          </summary>
          <div className="more-body">{p.more}</div>
        </details>
      )}
      {p.primary && (
        <div className="as-actions">
          <button type="submit" className="btn btn-primary" disabled={off} aria-busy={p.primary.busy || undefined}
            aria-describedby={off && p.primary.why ? 'as-why' : undefined}>
            {p.primary.busy && <span className="spinner" aria-hidden="true" />}
            {p.primary.label}
          </button>
          {off && p.primary.why && <p className="as-why" id="as-why">{p.primary.why}</p>}
        </div>
      )}
    </form>
  );
}

/** A field of the design system's forms. */
export function Field(p: {
  id: string; label: string; value: string; onChange: (v: string) => void;
  type?: string; autoComplete?: string; hint?: string; error?: string;
}) {
  const describedBy = [p.hint && `h-${p.id}`, p.error && `e-${p.id}`].filter(Boolean).join(' ') || undefined;
  return (
    <div className="field">
      <label className="field-label" htmlFor={`f-${p.id}`}>{p.label}</label>
      <input id={`f-${p.id}`} className="input" type={p.type ?? 'text'} value={p.value} autoComplete={p.autoComplete ?? 'off'}
        spellCheck={false} aria-invalid={p.error ? true : undefined} aria-describedby={describedBy}
        onChange={(e) => p.onChange(e.target.value)} />
      {p.hint && <span className="hint" id={`h-${p.id}`}>{p.hint}</span>}
      {p.error && <span className="error-text" id={`e-${p.id}`}>{p.error}</span>}
    </div>
  );
}

export const Icon = ({ name, cls }: { name: string; cls?: string }) => <Html html={icon(name, cls)} />;

export const stepTitle = (id: string): string => t(`step.${id}.title` as Key);
