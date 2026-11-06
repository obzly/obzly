import type {
  Definition,
  ExtractSource,
  PromQLSeries,
  RunResult,
  Step,
  StepResult,
} from '../../../entity/synthetics';

// Mirrors spec.TemplatePattern and the built-ins in backend
// internal/synthetics/spec.
const TEMPLATE = /\{\{\s*(\$?[A-Za-z_][A-Za-z0-9_]*)\s*\}\}/g;
const PLACEHOLDER = /\$\{env:[A-Za-z_][A-Za-z0-9_]*\}/;
export const BUILTINS = ['$uuid', '$timestamp', '$isoTimestamp', '$randomInt'];
export const MAX_STEPS = 10;

export const EXTRACT_LABEL: Record<ExtractSource, string> = {
  json: 'JSON path',
  header: 'Header',
  status: 'Status code',
  regex: 'Regex (group 1)',
  body: 'Whole body',
};

export interface VariableRow {
  name: string;
  /** "Variable: …" or "Step 1 · JSON $.accessToken". */
  source: string;
  /** 1-based steps that use it. */
  usedIn: number[];
  secret: boolean;
  /** 1-based step that extracts it; undefined for monitor variables. */
  step?: number;
}

export interface UndefinedUse {
  /** 1-based. */
  step: number;
  name: string;
}

export const emptyStep = (n: number): Step => ({
  name: `Step ${n}`,
  request: { method: 'GET', url: 'https://', followRedirects: true },
  assertions: [{ kind: 'status', op: 'equals', value: '200', severity: 'fail' }],
  extract: [],
});

/** The {{name}}s a step uses (URL, header values, body, assertion values), each once. */
export const usedVariables = (step: Step): string[] => {
  const texts = [
    step.request.url,
    step.request.body ?? '',
    ...Object.values(step.request.headers ?? {}),
    ...(step.assertions ?? []).map((assertion) => assertion.value ?? ''),
  ];
  return [
    ...new Set(texts.flatMap((text) => [...text.matchAll(TEMPLATE)].map((match) => match[1]))),
  ];
};

/** What each variable is, where it comes from and where it's used, plus uses nothing defines yet. */
export const variableReport = (
  definition: Definition,
): { rows: VariableRow[]; undefinedUses: UndefinedUse[] } => {
  const steps = definition.steps ?? [];
  const rows: VariableRow[] = Object.entries(definition.variables ?? {})
    .filter(([name]) => name.trim())
    .map(([name, value]) => ({
      name,
      source: `Variable: ${value}`,
      usedIn: [],
      secret: PLACEHOLDER.test(value),
    }));
  const defined = new Set(rows.map((row) => row.name));
  const undefinedUses: UndefinedUse[] = [];
  steps.forEach((step, index) => {
    for (const name of usedVariables(step)) {
      if (BUILTINS.includes(name)) continue;
      if (!defined.has(name)) {
        undefinedUses.push({ step: index + 1, name });
        continue;
      }
      // The latest definition before this step is the one it reads.
      const row = [...rows].reverse().find((item) => item.name === name);
      if (row && !row.usedIn.includes(index + 1)) row.usedIn.push(index + 1);
    }
    for (const extract of step.extract ?? []) {
      if (!extract.name.trim()) continue;
      defined.add(extract.name);
      rows.push({
        name: extract.name,
        source: `Step ${index + 1} · ${EXTRACT_LABEL[extract.from]}${extract.path ? ` ${extract.path}` : ''}`,
        usedIn: [],
        secret: !!extract.secret,
        step: index + 1,
      });
    }
  });
  return { rows, undefinedUses };
};

export const usageText = (usedIn: number[]): string =>
  usedIn.length === 0
    ? 'Not used yet'
    : `Used in step${usedIn.length > 1 ? 's' : ''} ${usedIn.join(', ')}`;

/** One line for a collapsed step: "POST https://… · 2 assertions · extracts token". */
export const stepSummary = (step: Step): string => {
  const parts = [`${step.request.method} ${step.request.url}`];
  const assertions = step.assertions?.length ?? 0;
  if (assertions) parts.push(`${assertions} assertion${assertions > 1 ? 's' : ''}`);
  const names = (step.extract ?? []).map((extract) => extract.name).filter(Boolean);
  if (names.length) parts.push(`extracts ${names.join(', ')}`);
  return parts.join(' · ');
};

export const moveStep = (steps: Step[], index: number, by: -1 | 1): Step[] => {
  const target = index + by;
  if (target < 0 || target >= steps.length) return steps;
  const next = [...steps];
  [next[index], next[target]] = [next[target], next[index]];
  return next;
};

/** A copy placed right after the original; extracted names stay, so later steps keep working. */
export const duplicateStep = (steps: Step[], index: number): Step[] => {
  if (steps.length >= MAX_STEPS) return steps;
  const copy: Step = { ...structuredClone(steps[index]), name: `${steps[index].name} (copy)` };
  return [...steps.slice(0, index + 1), copy, ...steps.slice(index + 1)];
};

/** Drops the blank header and variable rows the editor keeps while someone types. */
export const cleanMultiDefinition = (definition: Definition): Definition => {
  if (definition.type !== 'multi') return definition;
  const withoutBlank = (record?: Record<string, string>) => {
    const entries = Object.entries(record ?? {}).filter(([name]) => name.trim());
    return entries.length ? Object.fromEntries(entries) : undefined;
  };
  return {
    ...definition,
    variables: withoutBlank(definition.variables),
    steps: (definition.steps ?? []).map((step) => ({
      ...step,
      request: { ...step.request, headers: withoutBlank(step.request.headers) },
      extract: (step.extract ?? []).filter((extract) => extract.name.trim()),
    })),
  };
};

/** The step that failed a run, if a multi-step one did. */
export const failedStep = (run: RunResult): StepResult | undefined =>
  run.steps?.find((step) => step.status === 'down');

/** "failed at Create order (409)", or "" when no step failed. */
export const failedStepNote = (run: RunResult): string => {
  const step = failedStep(run);
  if (!step) return '';
  const why = step.httpStatus ? String(step.httpStatus) : step.error ? shorten(step.error) : '';
  return `failed at ${step.name}${why ? ` (${why})` : ''}`;
};

export interface StepRow {
  /** 1-based. */
  index: number;
  name: string;
  /** Share of runs the step passed, 0..1. */
  success?: number;
  p50Ms?: number;
  p95Ms?: number;
  /** This step's share of the summed p95s, 0..1. */
  share?: number;
}

/**
 * One row per step, from instant series labelled step_index and step: the
 * monitor's steps now, plus any older index still in the range. A renamed
 * step keeps its index and its current name.
 */
export const stepRows = (
  steps: Step[] | undefined,
  success: PromQLSeries[],
  p50: PromQLSeries[],
  p95: PromQLSeries[],
): StepRow[] => {
  const value = (series: PromQLSeries[], index: number) => {
    const values = series
      .filter((item) => Number(item.labels.step_index) === index)
      .map((item) => item.points[item.points.length - 1]?.value)
      .filter((item): item is number => item !== undefined);
    return values.length ? Math.max(...values) : undefined;
  };
  const indexes = new Set<number>([
    ...(steps ?? []).map((_, i) => i + 1),
    ...success.map((item) => Number(item.labels.step_index)),
  ]);
  const rows = [...indexes]
    .filter((index) => Number.isInteger(index) && index > 0)
    .sort((a, b) => a - b)
    .map((index) => {
      const p50Seconds = value(p50, index);
      const p95Seconds = value(p95, index);
      return {
        index,
        name:
          steps?.[index - 1]?.name ??
          success.find((item) => Number(item.labels.step_index) === index)?.labels.step ??
          `Step ${index}`,
        success: value(success, index),
        p50Ms: p50Seconds === undefined ? undefined : p50Seconds * 1000,
        p95Ms: p95Seconds === undefined ? undefined : p95Seconds * 1000,
      };
    });
  const total = rows.reduce((sum, row) => sum + (row.p95Ms ?? 0), 0);
  return rows.map((row) => ({
    ...row,
    share: total > 0 && row.p95Ms !== undefined ? row.p95Ms / total : undefined,
  }));
};

const shorten = (text: string) => (text.length > 40 ? `${text.slice(0, 39)}…` : text);
