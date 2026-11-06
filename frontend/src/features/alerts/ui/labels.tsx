import { Tag } from 'antd';

// LabelTags renders an alert's labels, alertname first.
export const LabelTags = ({ labels }: { labels: Record<string, string> }) => (
  <div className="flex flex-wrap gap-1">
    {Object.entries(labels)
      .sort(([a], [b]) => (a === 'alertname' ? -1 : b === 'alertname' ? 1 : a.localeCompare(b)))
      .map(([key, value]) => (
        <Tag key={key} className="m-0 font-mono text-xs">
          {key}={value}
        </Tag>
      ))}
  </div>
);
