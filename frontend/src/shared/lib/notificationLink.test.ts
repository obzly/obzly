import { describe, expect, it } from 'vitest';

import { parseNotificationLink, withoutNotificationLink } from './notificationLink';

describe('parseNotificationLink', () => {
  it('reads an alert link on a Site', () => {
    expect(parseNotificationLink('?project=p1&rule=r1&site=eu-1&tab=alerts')).toEqual({
      projectId: 'p1',
      tab: 'alerts',
      siteId: 'eu-1',
      ruleId: 'r1',
      sessionId: undefined,
    });
  });

  it('reads an incident link without a Site', () => {
    expect(parseNotificationLink('?project=p1&session=s1&tab=incidents')).toEqual({
      projectId: 'p1',
      tab: 'incidents',
      siteId: undefined,
      ruleId: undefined,
      sessionId: 's1',
    });
  });

  it('ignores anything else', () => {
    expect(parseNotificationLink('')).toBeUndefined();
    expect(parseNotificationLink('?project=p1&tab=users')).toBeUndefined();
    expect(parseNotificationLink('?tab=alerts')).toBeUndefined();
    expect(parseNotificationLink('?project=<script>&tab=alerts')).toBeUndefined();
    expect(parseNotificationLink('?project=p1&tab=alerts&site=a/b')?.siteId).toBeUndefined();
  });
});

describe('withoutNotificationLink', () => {
  it('drops only the link parameters', () => {
    expect(withoutNotificationLink('?project=p1&tab=alerts&rule=r&site=s&keep=1')).toBe('?keep=1');
    expect(withoutNotificationLink('?project=p1&tab=incidents&session=x')).toBe('');
  });
});
