import type { CloudProvider } from '../../../entity/cloud';

/** The AWS and Azure marks, drawn small beside a connection's name. */
export const ProviderLogo = ({
  provider,
  size = 22,
}: {
  provider: CloudProvider;
  size?: number;
}) =>
  provider === 'aws' ? (
    <svg
      viewBox="0 0 44 26"
      width={(size * 44) / 26}
      height={size}
      role="img"
      aria-label="AWS"
      className="shrink-0 text-[#232f3e] dark:text-white"
    >
      <text
        x="22"
        y="14"
        textAnchor="middle"
        fontFamily="Arial, Helvetica, sans-serif"
        fontWeight="700"
        fontSize="14"
        letterSpacing="-.3"
        fill="currentColor"
      >
        aws
      </text>
      <path
        d="M7 18.2c8.6 5 21.4 5 30 0"
        fill="none"
        stroke="#FF9900"
        strokeWidth="2.4"
        strokeLinecap="round"
      />
      <path
        d="M33.4 16.6l4.2.9-1.3 4"
        fill="none"
        stroke="#FF9900"
        strokeWidth="2.2"
        strokeLinecap="round"
        strokeLinejoin="round"
      />
    </svg>
  ) : (
    <svg
      viewBox="0 0 24 24"
      width={size}
      height={size}
      role="img"
      aria-label="Microsoft Azure"
      className="shrink-0"
    >
      <path
        d="M8.4 2.6h5.3L8.2 19.9a1 1 0 0 1-.95.7H3a.8.8 0 0 1-.76-1.06L7.45 3.3a.8.8 0 0 1 .95-.7z"
        fill="#0F5BAA"
      />
      <path d="M16.3 14.2H8a.38.38 0 0 0-.26.66l5.3 4.95c.16.15.36.23.57.23h4.7z" fill="#0078D4" />
      <path
        d="M15.65 3.3a.8.8 0 0 0-.76-.7H9a.8.8 0 0 1 .76.7l5.2 16.24A.8.8 0 0 1 14.2 20.6h5.9a.8.8 0 0 0 .76-1.06z"
        fill="#2C9DE8"
      />
    </svg>
  );
