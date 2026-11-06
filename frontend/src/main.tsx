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

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <App />
  </StrictMode>,
);
