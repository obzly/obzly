// "Nice" bucket widths (ms) for an auto bucketMs: the smallest one that keeps
// the bucket count under targetBuckets.
const NICE_INTERVALS_MS = [
  1_000,
  5_000,
  10_000,
  15_000,
  30_000, // seconds
  60_000,
  2 * 60_000,
  5 * 60_000,
  10 * 60_000,
  15 * 60_000,
  30 * 60_000, // minutes
  3_600_000,
  2 * 3_600_000,
  3 * 3_600_000,
  6 * 3_600_000,
  12 * 3_600_000, // hours
  86_400_000,
  2 * 86_400_000,
  7 * 86_400_000,
  30 * 86_400_000, // days
];

export function computeAutoBucketMs(fromMs: number, toMs: number, targetBuckets = 60): number {
  const rangeMs = Math.max(toMs - fromMs, 1);
  const raw = rangeMs / targetBuckets;

  for (const interval of NICE_INTERVALS_MS) {
    if (interval >= raw) {
      return interval;
    }
  }

  return NICE_INTERVALS_MS[NICE_INTERVALS_MS.length - 1];
}
