package logs_core

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func Test_ParseSearchQuery_BuildsExpectedTree(t *testing.T) {
	cond := func(field string, op ConditionOperator, value any) string {
		b, _ := json.Marshal(searchCondition(field, op, value))
		return string(b)
	}

	cases := []struct {
		input string
		want  string
	}{
		{`timeout`, cond("message", ConditionOperatorContains, "timeout")},
		{`level:error`, cond("level", ConditionOperatorContains, "error")},
		{`level=ERROR`, cond("level", ConditionOperatorEquals, "ERROR")},
		{`service!=api`, cond("service", ConditionOperatorNotEquals, "api")},
		{`trace_id:*`, cond("trace_id", ConditionOperatorExists, nil)},
		{`duration_ms>=250`, cond("duration_ms", ConditionOperatorGreaterOrEqual, 250.0)},
		{`status<500`, cond("status", ConditionOperatorLessThan, 500.0)},
		{`user.name=abc`, cond("user.name", ConditionOperatorEquals, "abc")},
		{
			`message:"connection reset (by peer)"`,
			cond("message", ConditionOperatorContains, "connection reset (by peer)"),
		},
		{`"say \"hi\""`, cond("message", ConditionOperatorContains, `say "hi"`)},
	}
	for _, c := range cases {
		node, err := ParseSearchQuery(c.input)
		require.NoError(t, err, c.input)
		got, _ := json.Marshal(node)
		assert.JSONEq(t, c.want, string(got), c.input)
	}
}

func Test_ParseSearchQuery_OperatorsAndPrecedence(t *testing.T) {
	node, err := ParseSearchQuery(`level:error service:api OR NOT (status>=500 AND -healthcheck)`)
	require.NoError(t, err)

	require.Equal(t, QueryNodeTypeLogical, node.Type)
	assert.Equal(t, LogicalOperatorOr, node.Logic.Operator)
	require.Len(t, node.Logic.Children, 2)

	implicitAnd := node.Logic.Children[0]
	assert.Equal(t, LogicalOperatorAnd, implicitAnd.Logic.Operator)
	assert.Len(t, implicitAnd.Logic.Children, 2)

	negated := node.Logic.Children[1]
	assert.Equal(t, LogicalOperatorNot, negated.Logic.Operator)
	inner := negated.Logic.Children[0]
	assert.Equal(t, LogicalOperatorAnd, inner.Logic.Operator)
	assert.Equal(t, LogicalOperatorNot, inner.Logic.Children[1].Logic.Operator, "-term is NOT term")

	lower, err := ParseSearchQuery(`a and b or c`)
	require.NoError(t, err)
	assert.Equal(t, LogicalOperatorOr, lower.Logic.Operator, "keywords are case-insensitive")
}

func Test_ParseSearchQuery_EmptyAndInvalidInput(t *testing.T) {
	node, err := ParseSearchQuery("   ")
	require.NoError(t, err)
	assert.Nil(t, node)

	for _, input := range []string{`"unterminated`, `(level:error`, `level:error)`, `timestamp:2026`, `a AND`} {
		_, err := ParseSearchQuery(input)
		assert.Error(t, err, input)
	}
}
