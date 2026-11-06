import type { Panel } from '../../../entity/dashboards';
import type { ConditionNode, QueryNode } from '../../../entity/query';

// Substitutes ${key} tokens with each variable's selected value right before a panel query is
// sent; the backend stores the tokens untouched (dashboards_models.Variable).
const VARIABLE_TOKEN = /\$\{(\w+)\}/g;

function substitute(value: string, values: Record<string, string>): string {
  return value.replace(VARIABLE_TOKEN, (token, key: string) =>
    Object.prototype.hasOwnProperty.call(values, key) ? values[key] : token,
  );
}

function substituteConditionValue(
  value: ConditionNode['value'],
  values: Record<string, string>,
): ConditionNode['value'] {
  if (typeof value === 'string') return substitute(value, values);
  if (Array.isArray(value)) return value.map((v) => substitute(v, values));
  return value;
}

function substituteQueryNode(node: QueryNode, values: Record<string, string>): QueryNode {
  if (node.type === 'condition' && node.condition) {
    return {
      ...node,
      condition: {
        ...node.condition,
        value: substituteConditionValue(node.condition.value, values),
      },
    };
  }
  if (node.type === 'logical' && node.logic) {
    return {
      ...node,
      logic: {
        ...node.logic,
        children: node.logic.children.map((child) => substituteQueryNode(child, values)),
      },
    };
  }
  return node;
}

export function applyVariablesToPanel(panel: Panel, values: Record<string, string>): Panel {
  let result = panel;

  if (result.promQuery) {
    // An unset variable means "all" in PromQL, like Grafana's All option: use it as =~"${key}".
    const promQuery = substitute(result.promQuery, values).replace(VARIABLE_TOKEN, '.*');
    result = { ...result, promQuery };
  }

  if (Object.keys(values).length === 0) return result;

  if (result.query) {
    result = { ...result, query: substituteQueryNode(result.query, values) };
  }
  if (result.serviceName) {
    result = { ...result, serviceName: substitute(result.serviceName, values) };
  }
  if (result.operationName) {
    result = { ...result, operationName: substitute(result.operationName, values) };
  }

  return result;
}
