// Posture actions across Sites: each Site ranks its own; scores add up by controls, not by
// averaging percentages.
import type {
  ActionKind,
  ActionStatus,
  ActionsResponse,
  PostureAction,
  SimulatedScore,
} from '../../../entity/posture';
import type { CostSite, SiteOutcome } from '../../cost';

export type SiteAction = PostureAction & CostSite;

export type ActionFilter = 'todo' | 'plan' | 'done' | 'accepted_risk' | 'dismissed' | 'all';

export const KIND_LABEL: Record<ActionKind, string> = {
  coverage: 'Coverage gap',
  fix: 'Fix',
  guardrail: 'Guardrail',
};

export const STATUS_LABEL: Record<ActionStatus, string> = {
  open: 'Open',
  done: 'Done',
  accepted_risk: 'Accepted risk',
};

export const mergeActions = (outcomes: SiteOutcome<ActionsResponse>[]) => {
  const actions: SiteAction[] = [];
  const scores = new Map<
    string,
    SimulatedScore & { passedNow: number; passedPlan: number; passedAll: number }
  >();
  for (const outcome of outcomes) {
    if (!outcome.value) continue;
    actions.push(...outcome.value.actions.map((action) => ({ ...action, ...outcome.site })));
    for (const score of outcome.value.standards) {
      const merged = scores.get(score.standard) ?? {
        ...score,
        evaluated: 0,
        passedNow: 0,
        passedPlan: 0,
        passedAll: 0,
      };
      merged.evaluated += score.evaluated;
      merged.passedNow += (score.current * score.evaluated) / 100;
      merged.passedPlan += (score.withPlan * score.evaluated) / 100;
      merged.passedAll += (score.withAll * score.evaluated) / 100;
      scores.set(score.standard, merged);
    }
  }
  const percent = (passed: number, total: number) =>
    total ? Math.round((passed * 1000) / total) / 10 : 0;
  return {
    actions: actions.sort((a, b) => b.rank - a.rank),
    standards: [...scores.values()].map(
      ({ passedNow, passedPlan, passedAll, ...score }): SimulatedScore => ({
        ...score,
        current: percent(passedNow, score.evaluated),
        withPlan: percent(passedPlan, score.evaluated),
        withAll: percent(passedAll, score.evaluated),
      }),
    ),
  };
};

export const matchesFilter = (action: PostureAction, filter: ActionFilter) => {
  switch (filter) {
    case 'todo':
      return action.status === 'open' && !action.dismissed;
    case 'plan':
      return action.planned && !action.dismissed;
    case 'done':
      return action.status === 'done';
    case 'accepted_risk':
      return action.status === 'accepted_risk';
    case 'dismissed':
      return action.dismissed;
  }
  return true;
};

export const actionId = (action: SiteAction) =>
  `${action.siteId}|${action.projectId}|${action.key}`;
