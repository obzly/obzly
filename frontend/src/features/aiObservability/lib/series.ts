/** Longest series drawn as a sparkline; longer ones keep their newest buckets. */
const MAX_POINTS = 400;

/**
 * Sums a value per time bucket across every group, filling empty buckets with 0
 * between the first and last bucket that has data, so sparklines keep their shape.
 */
export const bucketTotals = <T extends { bucket: number }>(
  points: T[],
  value: (point: T) => number,
  bucketSeconds: number,
): { bucket: number; value: number }[] => {
  if (points.length === 0) return [];
  const totals = new Map<number, number>();
  for (const point of points) {
    totals.set(point.bucket, (totals.get(point.bucket) ?? 0) + value(point));
  }
  const buckets = [...totals.keys()].sort((a, b) => a - b);
  const step = bucketSeconds * 1000;
  if (step <= 0) return buckets.map((bucket) => ({ bucket, value: totals.get(bucket) ?? 0 }));
  const first = Math.max(buckets[0], buckets[buckets.length - 1] - (MAX_POINTS - 1) * step);
  const series: { bucket: number; value: number }[] = [];
  for (let bucket = first; bucket <= buckets[buckets.length - 1]; bucket += step) {
    series.push({ bucket, value: totals.get(bucket) ?? 0 });
  }
  return series;
};

/** The bucket with the highest value, or null when nothing is above zero. */
export const peakBucket = (
  series: { bucket: number; value: number }[],
): { bucket: number; value: number } | null =>
  series.reduce<{ bucket: number; value: number } | null>(
    (peak, point) => (point.value > 0 && (!peak || point.value > peak.value) ? point : peak),
    null,
  );

/** "hour", "5 minutes", "day" for a chart subtitle. */
export const bucketLabel = (bucketSeconds: number): string => {
  const units: [number, string][] = [
    [86400, 'day'],
    [3600, 'hour'],
    [60, 'minute'],
  ];
  for (const [size, name] of units) {
    if (bucketSeconds >= size && bucketSeconds % size === 0) {
      const count = bucketSeconds / size;
      return count === 1 ? name : `${count} ${name}s`;
    }
  }
  return `${bucketSeconds} seconds`;
};
