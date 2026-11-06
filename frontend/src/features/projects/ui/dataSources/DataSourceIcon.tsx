import { useState } from 'react';

const FALLBACK_ICON = '/icons/menu/data-sources-gray.svg';

interface Props {
  sourceId: string;
  size?: number;
  className?: string;
}

/**
 * A data source's logo (a subset of simple-icons, MIT, used only to identify
 * the technology), or the generic glyph when no suitable logo exists or the
 * closest one belongs to a different product (e.g. Redis for Valkey).
 */
export function DataSourceIcon({ sourceId, size = 28, className }: Props) {
  const [failed, setFailed] = useState(false);

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
