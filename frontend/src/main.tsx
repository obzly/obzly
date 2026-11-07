import dayjs from 'dayjs';
import relativeTime from 'dayjs/plugin/relativeTime';
import timezone from 'dayjs/plugin/timezone';
import utc from 'dayjs/plugin/utc';
import { StrictMode } from 'react';
import { createRoot } from 'react-dom/client';

import './index.css';

import App from './App.tsx';
import { migrateStoredQueryNames } from './features/query/lib/nameMigration';

dayjs.extend(utc);
dayjs.extend(timezone);
dayjs.extend(relativeTime);

migrateStoredQueryNames();

const render = () =>
  createRoot(document.getElementById('root')!).render(
    <StrictMode>
      <App />
    </StrictMode>,
  );

// The live demo answers API calls in the browser; production builds drop this.
if (__OBZLY_DEMO__) {
  import('./demo/install').then(render);
} else {
  render();
}
