// Recommendations, commitments and the planner, across Sites. Amounts stay in their own currency;
// an unknown saving (no resource-level cost yet) is never counted as zero.
import type {
  AdviceSection,
  AdviceSectionName,
  AdviceState,
  Commitment,
  CommitmentCoverage,
  CommitmentType,
  CommitmentsResponse,
  CostRecommendation,
  Plan,
  PlannerResponse,
  PurchaseOption,
  RecommendationKind,
  RecommendationStatus,
  RecommendationsResponse,
} from '../../../entity/cost';
import type { CostSite, SiteOutcome } from './merge';

export type SiteRecommendation = CostRecommendation & CostSite;
export type SiteCommitment = Commitment & CostSite;
export type SitePlan = Plan & CostSite;
export type SiteSection = AdviceSection & CostSite;

export const KIND_LABEL: Record<RecommendationKind, string> = {
  rightsizing: 'Rightsize',
  idle: 'Idle',
  modernize: 'Modernize',
  storage: 'Storage',
};

export const STATUS_LABEL: Record<RecommendationStatus, string> = {
  open: 'Open',
  planned: 'Planned',
  done: 'Done',
  dismissed: 'Dismissed',
};

export const SOURCE_LABEL: Record<string, string> = {
  compute_optimizer: 'Compute Optimizer',
  advisor: 'Azure Advisor',
  obzly: 'Obzly check',
};

export const BASIS_LABEL: Record<string, string> = {
  provider: "The provider's estimate",
  resource_cost: "From the resource's own cost over the last 30 days",
  list_price: 'From the list price',
};

export const TYPE_LABEL: Record<CommitmentType, string> = {
  compute_sp: 'Compute Savings Plan',
  ec2_instance_sp: 'EC2 Instance Savings Plan',
  ec2_ri: 'EC2 Reserved Instances',
  rds_ri: 'RDS Reserved Instances',
  azure_sp: 'Azure savings plan',
  azure_reservation: 'Azure reservations',
};

export const TYPE_SHORT: Record<CommitmentType, string> = {
  compute_sp: 'Compute SP',
  ec2_instance_sp: 'EC2 Instance SP',
  ec2_ri: 'EC2 RIs',
  rds_ri: 'RDS RIs',
  azure_sp: 'Savings plan',
  azure_reservation: 'Reservations',
};

export const TYPE_FLEXIBILITY: Record<CommitmentType, string> = {
  compute_sp:
    'Any EC2 instance family, size, region, OS or tenancy, plus Fargate and Lambda: the most flexible, a little less discount.',
  ec2_instance_sp:
    'One instance family in one region, any size, OS or tenancy: more discount, less flexibility.',
  ec2_ri: 'One instance type family in one region; size-flexible on Linux. Can be resold.',
  rds_ri: 'One engine, family and region; size-flexible. No 3-year term without upfront.',
  azure_sp: 'Any VM size or region, plus App Service and Container Instances.',
  azure_reservation: 'One VM series in one region, with instance size flexibility.',
};

export const PAYMENT_LABEL: Record<PurchaseOption['payment'], string> = {
  no_upfront: 'No upfront',
  partial_upfront: 'Partial upfront',
  all_upfront: 'All upfront',
  monthly: 'Monthly',
  upfront: 'Upfront',
};

export const TERM_LABEL: Record<PurchaseOption['term'], string> = {
  '1y': '1 year',
  '3y': '3 years',
};

export const SECTION_LABEL: Record<AdviceSectionName, string> = {
  rightsizing: 'Rightsizing',
  checks: 'Obzly checks',
  commitments: 'Commitments',
  planner: 'Purchase recommendations',
};

export const STATE_LABEL: Record<AdviceState, string> = {
  ok: 'OK',
  not_enabled: 'Not enabled',
  needs_permission: 'Needs permission',
  error: 'Error',
};

export const PAYMENTS: Record<string, PurchaseOption['payment'][]> = {
  aws: ['no_upfront', 'partial_upfront', 'all_upfront'],
  azure: ['monthly', 'upfront'],
};

export const TERMS: PurchaseOption['term'][] = ['1y', '3y'];

const succeeded = <T>(outcomes: SiteOutcome<T>[]) =>
  outcomes.flatMap((outcome) =>
    outcome.value === undefined ? [] : [{ site: outcome.site, value: outcome.value }],
  );

export const mergeRecommendations = (outcomes: SiteOutcome<RecommendationsResponse>[]) => ({
  items: succeeded(outcomes).flatMap(({ site, value }) =>
    value.items.map((item): SiteRecommendation => ({ ...item, ...site })),
  ),
  sections: succeeded(outcomes).flatMap(({ site, value }) =>
    value.sections.map((section): SiteSection => ({ ...section, ...site })),
  ),
});

export const mergeCommitments = (outcomes: SiteOutcome<CommitmentsResponse>[]) => ({
  commitments: succeeded(outcomes).flatMap(({ site, value }) =>
    value.commitments.map((item): SiteCommitment => ({ ...item, ...site })),
  ),
  coverage: succeeded(outcomes).flatMap(({ site, value }) =>
    value.coverage.map((item): CommitmentCoverage & CostSite => ({ ...item, ...site })),
  ),
});

export const mergePlans = (outcomes: SiteOutcome<PlannerResponse>[]): SitePlan[] =>
  succeeded(outcomes).flatMap(({ site, value }) =>
    value.plans.map((plan): SitePlan => ({ ...plan, ...site })),
  );

/** Is it still to do: open or planned. */
export const isActive = (item: CostRecommendation) =>
  item.status === 'open' || item.status === 'planned';

export interface CurrencyTotal {
  currency: string;
  amount: number;
  count: number;
}

/** Sums a value per currency; items without a known amount are counted apart. */
export const totalsByCurrency = <T>(
  items: T[],
  amount: (item: T) => number | null | undefined,
  currency: (item: T) => string,
): { totals: CurrencyTotal[]; unknown: number } => {
  const byCurrency = new Map<string, CurrencyTotal>();
  let unknown = 0;
  for (const item of items) {
    const value = amount(item);
    const code = currency(item);
    if (value === null || value === undefined || !code) {
      unknown += 1;
      continue;
    }
    const total = byCurrency.get(code) ?? { currency: code, amount: 0, count: 0 };
    total.amount += value;
    total.count += 1;
    byCurrency.set(code, total);
  }
  return {
    totals: [...byCurrency.values()].sort((a, b) => b.amount - a.amount),
    unknown,
  };
};

/** The parts of each connection's last refresh that didn't work, with why. */
export const sectionProblems = (sections: SiteSection[]) =>
  sections.filter((section) => section.state !== 'ok');

/** The option at a term and payment, if the provider answered for it. */
export const optionAt = (plan: Plan, term: PurchaseOption['term'], payment: string) =>
  plan.options.find((option) => option.term === term && option.payment === payment);

/** The option to show first: the lowest-risk one that saves anything, else the first. */
export const defaultOption = (plan: Plan): PurchaseOption | undefined =>
  plan.options.find((option) => option.labels.includes('lowest_risk')) ?? plan.options[0];

/** The console steps to buy an option; Obzly never buys. */
export const howToBuy = (plan: Plan, option: PurchaseOption): string[] => {
  const term = TERM_LABEL[option.term];
  const payment = PAYMENT_LABEL[option.payment];
  switch (plan.type) {
    case 'compute_sp':
    case 'ec2_instance_sp':
      return [
        `Sign in to the AWS console as the management account (${plan.connectionName}).`,
        'Open Billing and Cost Management → Savings Plans → Purchase Savings Plans.',
        `Choose ${TYPE_LABEL[plan.type]}, ${term}, ${payment}.`,
        `Enter the hourly commitment: ${hourly(plan, option)}.`,
        'Review the estimated savings against this page, then add to cart and submit.',
      ];
    case 'ec2_ri':
    case 'rds_ri':
      return [
        `Sign in to the AWS console as the account the usage runs in (${plan.connectionName}).`,
        plan.type === 'ec2_ri'
          ? 'Open EC2 → Reserved Instances → Purchase Reserved Instances.'
          : 'Open RDS → Reserved instances → Purchase reserved DB instance.',
        `For each line below, choose the type, region and count, ${term}, ${payment}.`,
        'Check the offering matches the platform (OS, engine, deployment) before you buy.',
      ];
    case 'azure_sp':
      return [
        'In the Azure portal, open Savings plans → Add.',
        `Choose the billing scope for ${plan.connectionName}, ${term}, billed ${payment.toLowerCase()}.`,
        `Enter the hourly commitment: ${hourly(plan, option)}.`,
        'Review and purchase; a billing account owner or reservation purchaser must approve.',
      ];
    case 'azure_reservation':
      return [
        'In the Azure portal, open Reservations → Add → Virtual machine.',
        `Choose the scope for ${plan.connectionName}, ${term}, billed ${payment.toLowerCase()}.`,
        'For each line below, pick the size and region and enter the quantity.',
        'Turn on instance size flexibility, then review and purchase.',
      ];
  }
  return [];
};

// The hourly amount to buy, after rightsizing first when it applies.
const hourly = (plan: Plan, option: PurchaseOption) =>
  option.hourlyCommitment > 0
    ? `${option.hourlyCommitment.toFixed(option.hourlyCommitment < 1 ? 4 : 2)} ${plan.currency || 'USD'}`
    : (option.commitment ?? 'as recommended');

/** "pays back in month 4", "saves from month 1". */
export const paybackText = (option: PurchaseOption) =>
  option.upfront > 0 && option.breakEvenMonths > 0
    ? `pays back in month ${Math.ceil(option.breakEvenMonths)}`
    : 'saves from month 1';
