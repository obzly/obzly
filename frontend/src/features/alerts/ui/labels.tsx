import { Chip } from './bits';

// LabelTags renders an alert's labels, alertname first.
export const LabelTags = ({
  labels,
  hide = [],
}: {
  labels: Record<string, string>;
  /** Label names to leave out where they are already shown (e.g. alertname). */
  hide?: string[];
}) => (
  <div className="flex flex-wrap gap-1">
    {Object.entries(labels)
      .filter(([key]) => !hide.includes(key))
      .sort(([a], [b]) => (a === 'alertname' ? -1 : b === 'alertname' ? 1 : a.localeCompare(b)))
      .map(([key, value]) => (
        <Chip key={key} mono>
          {key}={value}
        </Chip>
      ))}
  </div>
);
