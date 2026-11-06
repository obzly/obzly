import type { JSX } from 'react';

export const MobilePlaceholderComponent = (): JSX.Element => {
  return (
    <div className="flex flex-col items-center gap-6 px-5 pt-10 text-center text-base">
      <img src="/logo.svg" alt="Obzly" className="h-[120px] w-[120px]" />
      <p>Obzly is built for larger screens.</p>
      <p className="text-gray-600 dark:text-gray-300">
        Searching logs, reading traces and building dashboards need more room than a phone has. Open
        Obzly on a laptop or desktop to continue.
      </p>
    </div>
  );
};
