// A notification's "Open in Obzly" link: /?site=…&project=…&tab=alerts&rule=…
// or &tab=incidents&session=… (built by the backend, alerting/templates.go).

export interface NotificationLink {
  siteId?: string;
  projectId: string;
  tab: 'alerts' | 'incidents';
  ruleId?: string;
  sessionId?: string;
}

const ID = /^[A-Za-z0-9._-]{1,128}$/;

/** The link in search, or undefined when it isn't one. */
export const parseNotificationLink = (search: string): NotificationLink | undefined => {
  const params = new URLSearchParams(search);
  const projectId = params.get('project') ?? '';
  const tab = params.get('tab');
  if (!ID.test(projectId) || (tab !== 'alerts' && tab !== 'incidents')) return undefined;
  const optional = (name: string) => {
    const value = params.get(name);
    return value && ID.test(value) ? value : undefined;
  };
  return {
    projectId,
    tab,
    siteId: optional('site'),
    ruleId: tab === 'alerts' ? optional('rule') : undefined,
    sessionId: tab === 'incidents' ? optional('session') : undefined,
  };
};

/** search without the link's parameters, so a reload doesn't open it again. */
export const withoutNotificationLink = (search: string): string => {
  const params = new URLSearchParams(search);
  for (const name of ['site', 'project', 'tab', 'rule', 'session']) params.delete(name);
  const rest = params.toString();
  return rest ? `?${rest}` : '';
};
