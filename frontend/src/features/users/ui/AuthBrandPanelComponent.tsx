import type { JSX } from 'react';

// The sign-in page's brand panel; hidden on small screens since it carries
// no information.

const SIGNAL_CHIPS: { icon: string; label: string }[] = [
  { icon: '📝', label: 'Logs' },
  { icon: '🔍', label: 'Traces' },
  { icon: '📊', label: 'Metrics' },
  { icon: '📈', label: 'Dashboards' },
];

// A sample; "35+ integrations" below covers the rest.
const DATA_SOURCES = ['Nginx', 'PostgreSQL', 'Kafka', 'Kubernetes', 'MySQL', 'Redis'];

const PLATFORM_BADGES = ['OTLP-native', 'Federated SSO', 'TOTP MFA', 'Apache 2.0', 'Self-hosted'];

export function AuthBrandPanelComponent(): JSX.Element {
  return (
    <section
      className="relative hidden items-center overflow-hidden p-12 text-white lg:flex"
      style={{
        background: 'linear-gradient(155deg, #071a14 0%, #064e3b 45%, #059669 100%)',
      }}
    >
      <div
        aria-hidden="true"
        className="auth-brand-glow pointer-events-none absolute -inset-[20%]"
        style={{
          background:
            'radial-gradient(38% 32% at 18% 22%, rgba(110, 231, 183, 0.35), transparent 70%), ' +
            'radial-gradient(34% 30% at 82% 78%, rgba(16, 185, 129, 0.32), transparent 70%)',
        }}
      />

      <div className="relative z-10 max-w-[460px]">
        <div className="flex items-center gap-3">
          <img className="h-9 w-9" src="/logo.svg" />
          <span className="text-2xl font-extrabold tracking-tight">Obzly</span>
        </div>

        <h1 className="mt-7 text-[clamp(1.7rem,1.1rem+1.8vw,2.5rem)] leading-[1.15] font-extrabold tracking-tight">
          Observability,
          <br />
          without the operational tax.
        </h1>

        <p className="mt-4 mb-8 text-[0.95rem] leading-relaxed text-white/80">
          Logs, traces, metrics, and dashboards in one self-hosted platform - no cluster to run, no
          per-seat bill, and your data never leaves your own infrastructure.
        </p>

        <div className="flex flex-wrap gap-2">
          {SIGNAL_CHIPS.map((chip, i) => (
            <span
              key={chip.label}
              className="auth-chip inline-flex items-center gap-1.5 rounded-full border border-white/15 bg-white/10 px-3 py-1.5 text-sm font-semibold backdrop-blur-sm"
              style={{ animationDelay: `${i * 0.35}s` }}
            >
              <span aria-hidden="true">{chip.icon}</span> {chip.label}
            </span>
          ))}
        </div>

        <div className="mt-8 border-t border-white/15 pt-6">
          <div className="mb-2 text-xs font-bold tracking-wider text-white/60 uppercase">
            35+ data source integrations
          </div>
          <div className="flex flex-wrap gap-1.5">
            {DATA_SOURCES.map((name) => (
              <span
                key={name}
                className="rounded-md border border-white/15 bg-white/5 px-2.5 py-1 text-xs font-semibold text-white/90"
              >
                {name}
              </span>
            ))}
            <span className="rounded-md border border-white/15 bg-white/5 px-2.5 py-1 text-xs font-semibold text-white/60">
              and more
            </span>
          </div>
        </div>

        <div className="mt-5">
          <div className="flex flex-wrap gap-1.5">
            {PLATFORM_BADGES.map((name) => (
              <span
                key={name}
                className="rounded-md border border-emerald-300/40 bg-emerald-300/10 px-2.5 py-1 text-xs font-bold text-white"
              >
                {name}
              </span>
            ))}
          </div>
        </div>
      </div>
    </section>
  );
}
