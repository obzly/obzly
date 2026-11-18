import { type JSX, useState } from 'react';

const FALLBACK_ICON = '/icons/menu/data-sources-gray.svg';

type GenericIcon =
  | 'server'
  | 'globe'
  | 'database'
  | 'memory'
  | 'logLines'
  | 'chart'
  | 'heartbeat'
  | 'network'
  | 'spark'
  | 'loadBalancer';

// Sources with no suitable logo (or whose closest logo is another product's,
// e.g. Redis for Valkey) get a generic glyph for what they are.
const GENERIC_ICON: Record<string, GenericIcon> = {
  windows: 'server',
  iis: 'globe',
  sqlserver: 'database',
  valkey: 'database',
  riak: 'database',
  memcached: 'memory',
  syslog: 'logLines',
  statsd: 'chart',
  'http-check': 'heartbeat',
  snmp: 'network',
  zookeeper: 'network',
  'obi-network': 'network',
  'claude-code': 'spark',
  'llm-app-otel': 'spark',
  haproxy: 'loadBalancer',
};

const GLYPH: Record<GenericIcon, JSX.Element> = {
  server: (
    <>
      <rect x="3" y="4" width="18" height="7" rx="2" />
      <rect x="3" y="13" width="18" height="7" rx="2" />
      <path d="M7 7.5h.01M7 16.5h.01M11 7.5h6M11 16.5h6" />
    </>
  ),
  globe: (
    <>
      <circle cx="12" cy="12" r="9" />
      <path d="M3 12h18M12 3c2.5 2.7 3.8 5.7 3.8 9s-1.3 6.3-3.8 9c-2.5-2.7-3.8-5.7-3.8-9S9.5 5.7 12 3z" />
    </>
  ),
  database: (
    <>
      <ellipse cx="12" cy="5.5" rx="8" ry="2.5" />
      <path d="M4 5.5v13c0 1.4 3.6 2.5 8 2.5s8-1.1 8-2.5v-13M4 12c0 1.4 3.6 2.5 8 2.5s8-1.1 8-2.5" />
    </>
  ),
  memory: (
    <>
      <rect x="2.5" y="6" width="19" height="10" rx="1.5" />
      <path d="M6.5 9.5v3M10.5 9.5v3M13.5 9.5v3M17.5 9.5v3M5 16v3M9 16v3M15 16v3M19 16v3" />
    </>
  ),
  logLines: <path d="M4 6h16M4 10h11M4 14h16M4 18h9" />,
  chart: <path d="M4 4v16h16M8 16v-4M12 16V8M16 16v-6" />,
  heartbeat: <path d="M3 12h4l2-5 4 10 2-5h6" />,
  network: (
    <>
      <circle cx="12" cy="5" r="2.2" />
      <circle cx="5" cy="19" r="2.2" />
      <circle cx="19" cy="19" r="2.2" />
      <path d="M12 7.2V12M12 12l-5.4 5.4M12 12l5.4 5.4" />
    </>
  ),
  spark: (
    <>
      <path d="M11 3c.6 4.2 2.8 6.4 7 7-4.2.6-6.4 2.8-7 7-.6-4.2-2.8-6.4-7-7 4.2-.6 6.4-2.8 7-7z" />
      <path d="M18.5 15c.3 1.6 1 2.3 2.5 2.5-1.5.2-2.2.9-2.5 2.5-.3-1.6-1-2.3-2.5-2.5 1.5-.2 2.2-.9 2.5-2.5z" />
    </>
  ),
  loadBalancer: (
    <>
      <rect x="8" y="3" width="8" height="5" rx="1.5" />
      <rect x="2.5" y="16" width="5.5" height="5" rx="1.5" />
      <rect x="9.25" y="16" width="5.5" height="5" rx="1.5" />
      <rect x="16" y="16" width="5.5" height="5" rx="1.5" />
      <path d="M12 8v8M5.25 16v-1.5a2 2 0 0 1 2-2h9.5a2 2 0 0 1 2 2V16" />
    </>
  ),
};

interface Props {
  sourceId: string;
  size?: number;
  className?: string;
}

/**
 * A data source's logo (a subset of simple-icons, MIT, used only to identify
 * the technology), or a generic glyph for what the source is when no suitable
 * logo exists.
 */
export function DataSourceIcon({ sourceId, size = 28, className }: Props) {
  const [failed, setFailed] = useState(false);
  const generic = GENERIC_ICON[sourceId];

  if (generic) {
    return (
      <svg
        viewBox="0 0 24 24"
        width={size}
        height={size}
        fill="none"
        stroke="currentColor"
        strokeWidth={1.7}
        strokeLinecap="round"
        strokeLinejoin="round"
        aria-hidden="true"
        className={`text-gray-500 dark:text-gray-300 ${className ?? ''}`}
      >
        {GLYPH[generic]}
      </svg>
    );
  }

  return (
    <img
      src={failed ? FALLBACK_ICON : `/icons/datasources/${sourceId}.svg`}
      onError={() => setFailed(true)}
      alt=""
      width={size}
      height={size}
      className={className}
    />
  );
}
