import type { ChannelConfig, ChannelKind } from '../../../entity/alerting';

export const KINDS: { value: ChannelKind; label: string }[] = [
  { value: 'slack', label: 'Slack' },
  { value: 'webhook', label: 'Webhook' },
  { value: 'pagerduty', label: 'PagerDuty' },
  { value: 'email', label: 'Email' },
];

export interface Draft {
  id?: string;
  name: string;
  kind: ChannelKind;
  url: string;
  authorization: string;
  routingKey: string;
  to: string;
}

export const emptyDraft: Draft = {
  name: '',
  kind: 'slack',
  url: '',
  authorization: '',
  routingKey: '',
  to: '',
};

// configOf is the draft's secret settings; null when editing and nothing
// secret was entered (the stored config is kept).
export const configOf = (draft: Draft): ChannelConfig | null => {
  switch (draft.kind) {
    case 'slack':
      return draft.url ? { url: draft.url } : null;
    case 'webhook':
      return draft.url
        ? {
            url: draft.url,
            headers: draft.authorization ? { Authorization: draft.authorization } : undefined,
          }
        : null;
    case 'pagerduty':
      return draft.routingKey ? { routingKey: draft.routingKey } : null;
    case 'email': {
      const to = draft.to
        .split(',')
        .map((address) => address.trim())
        .filter(Boolean);
      return to.length > 0 ? { to } : null;
    }
  }
};
