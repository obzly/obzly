package logs_core

import (
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
)

// ParseSearchQuery is the Go port of the logs page search bar parser
// (frontend/src/features/query/lib/searchQueryParser.ts); keep the two in
// step. Grammar: NOT > AND > OR, parentheses, implicit AND between terms.
//
//	field:value   contains ("*" = exists)    field=value   equals
//	field!=value  not equals                 field>=, <=, >, <   ranges
//	-term / NOT term                         bare word = message contains
//
// Values may be quoted ("like this"). Returns nil for an empty query.
func ParseSearchQuery(input string) (*QueryNode, error) {
	trimmed := strings.TrimSpace(input)
	if trimmed == "" {
		return nil, nil
	}

	tokens, err := tokenizeSearch(trimmed)
	if err != nil {
		return nil, err
	}
	if len(tokens) == 0 {
		return nil, nil
	}

	p := &searchParser{tokens: tokens}
	node, err := p.parseOr()
	if err != nil {
		return nil, err
	}
	if p.pos < len(p.tokens) {
		return nil, fmt.Errorf("unexpected %q - check parentheses", p.tokens[p.pos].text)
	}
	return &node, nil
}

type searchTokenType int

const (
	tokenAnd searchTokenType = iota
	tokenOr
	tokenNot
	tokenLParen
	tokenRParen
	tokenTerm
)

type searchToken struct {
	kind searchTokenType
	text string
}

var fieldOpPattern = regexp.MustCompile(`(?s)^([a-zA-Z_][a-zA-Z0-9_.]*)(!=|>=|<=|:|=|>|<)(.*)$`)

func tokenizeSearch(input string) ([]searchToken, error) {
	var tokens []searchToken
	runes := []rune(input)
	i := 0

	for i < len(runes) {
		c := runes[i]
		if c == ' ' || c == '\t' || c == '\n' {
			i++
			continue
		}
		if c == '(' {
			tokens = append(tokens, searchToken{tokenLParen, "("})
			i++
			continue
		}
		if c == ')' {
			tokens = append(tokens, searchToken{tokenRParen, ")"})
			i++
			continue
		}

		var raw strings.Builder
		for i < len(runes) && runes[i] != ' ' && runes[i] != '\t' && runes[i] != '\n' {
			if (runes[i] == '(' || runes[i] == ')') && raw.Len() > 0 {
				break
			}
			if runes[i] == '"' {
				quoteStart := i
				i++
				for i < len(runes) && runes[i] != '"' {
					if runes[i] == '\\' && i+1 < len(runes) {
						i++
					}
					i++
				}
				if i >= len(runes) {
					return nil, fmt.Errorf("unterminated quote starting at position %d", quoteStart)
				}
				i++
				raw.WriteString(string(runes[quoteStart:i]))
				continue
			}
			raw.WriteRune(runes[i])
			i++
		}

		text := raw.String()
		if text == "" {
			i++
			continue
		}
		switch strings.ToUpper(text) {
		case "AND":
			tokens = append(tokens, searchToken{tokenAnd, text})
		case "OR":
			tokens = append(tokens, searchToken{tokenOr, text})
		case "NOT":
			tokens = append(tokens, searchToken{tokenNot, text})
		default:
			tokens = append(tokens, searchToken{tokenTerm, text})
		}
	}
	return tokens, nil
}

type searchParser struct {
	tokens []searchToken
	pos    int
}

func (p *searchParser) peek() *searchToken {
	if p.pos < len(p.tokens) {
		return &p.tokens[p.pos]
	}
	return nil
}

func (p *searchParser) parseOr() (QueryNode, error) {
	first, err := p.parseAnd()
	if err != nil {
		return QueryNode{}, err
	}
	children := []QueryNode{first}
	for t := p.peek(); t != nil && t.kind == tokenOr; t = p.peek() {
		p.pos++
		next, err := p.parseAnd()
		if err != nil {
			return QueryNode{}, err
		}
		children = append(children, next)
	}
	return combineSearch(LogicalOperatorOr, children), nil
}

func (p *searchParser) parseAnd() (QueryNode, error) {
	first, err := p.parseUnary()
	if err != nil {
		return QueryNode{}, err
	}
	children := []QueryNode{first}
	for {
		t := p.peek()
		if t == nil || t.kind == tokenOr || t.kind == tokenRParen {
			break
		}
		if t.kind == tokenAnd {
			p.pos++
		}
		next, err := p.parseUnary()
		if err != nil {
			return QueryNode{}, err
		}
		children = append(children, next)
	}
	return combineSearch(LogicalOperatorAnd, children), nil
}

func (p *searchParser) parseUnary() (QueryNode, error) {
	t := p.peek()
	if t != nil && t.kind == tokenNot {
		p.pos++
		inner, err := p.parseUnary()
		if err != nil {
			return QueryNode{}, err
		}
		return notNode(inner), nil
	}
	if t != nil && t.kind == tokenTerm && strings.HasPrefix(t.text, "-") && len(t.text) > 1 {
		p.pos++
		inner, err := parseSearchTerm(t.text[1:])
		if err != nil {
			return QueryNode{}, err
		}
		return notNode(inner), nil
	}
	return p.parsePrimary()
}

func (p *searchParser) parsePrimary() (QueryNode, error) {
	t := p.peek()
	if t == nil {
		return QueryNode{}, fmt.Errorf(`expected a term or "("`)
	}
	switch t.kind {
	case tokenLParen:
		p.pos++
		node, err := p.parseOr()
		if err != nil {
			return QueryNode{}, err
		}
		if closing := p.peek(); closing == nil || closing.kind != tokenRParen {
			return QueryNode{}, fmt.Errorf(`missing closing ")"`)
		}
		p.pos++
		return node, nil
	case tokenTerm:
		p.pos++
		return parseSearchTerm(t.text)
	default:
		return QueryNode{}, fmt.Errorf("unexpected %q", t.text)
	}
}

func parseSearchTerm(raw string) (QueryNode, error) {
	match := fieldOpPattern.FindStringSubmatch(raw)
	if match == nil {
		return searchCondition("message", ConditionOperatorContains, dequoteSearch(raw)), nil
	}

	field, op, value := match[1], match[2], dequoteSearch(match[3])
	switch op {
	case ":":
		if field == "timestamp" {
			return QueryNode{}, fmt.Errorf(
				`timestamp can't be matched with ":" - use a range like timestamp>=2026-09-23T06:40:00Z`,
			)
		}
		if value == "*" {
			return searchCondition(field, ConditionOperatorExists, nil), nil
		}
		return searchCondition(field, ConditionOperatorContains, value), nil
	case "=":
		return searchCondition(field, ConditionOperatorEquals, value), nil
	case "!=":
		return searchCondition(field, ConditionOperatorNotEquals, value), nil
	case ">":
		return searchCondition(field, ConditionOperatorGreaterThan, coerceSearchNumber(value)), nil
	case ">=":
		return searchCondition(field, ConditionOperatorGreaterOrEqual, coerceSearchNumber(value)), nil
	case "<":
		return searchCondition(field, ConditionOperatorLessThan, coerceSearchNumber(value)), nil
	case "<=":
		return searchCondition(field, ConditionOperatorLessOrEqual, coerceSearchNumber(value)), nil
	}
	return searchCondition("message", ConditionOperatorContains, dequoteSearch(raw)), nil
}

func combineSearch(op LogicalOperator, children []QueryNode) QueryNode {
	if len(children) == 1 {
		return children[0]
	}
	return QueryNode{Type: QueryNodeTypeLogical, Logic: &LogicalNode{Operator: op, Children: children}}
}

func notNode(inner QueryNode) QueryNode {
	return QueryNode{
		Type:  QueryNodeTypeLogical,
		Logic: &LogicalNode{Operator: LogicalOperatorNot, Children: []QueryNode{inner}},
	}
}

func searchCondition(field string, op ConditionOperator, value any) QueryNode {
	return QueryNode{Type: QueryNodeTypeCondition, Condition: &ConditionNode{Field: field, Operator: op, Value: value}}
}

func dequoteSearch(value string) string {
	if len(value) >= 2 && strings.HasPrefix(value, `"`) && strings.HasSuffix(value, `"`) {
		inner := value[1 : len(value)-1]
		return strings.ReplaceAll(strings.ReplaceAll(inner, `\"`, `"`), `\\`, `\`)
	}
	return value
}

func coerceSearchNumber(value string) any {
	if strings.TrimSpace(value) == "" {
		return value
	}
	n, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
	if err != nil || math.IsInf(n, 0) || math.IsNaN(n) {
		return value
	}
	return n
}
