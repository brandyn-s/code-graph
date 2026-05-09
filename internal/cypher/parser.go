package cypher

import (
	"fmt"
	"strconv"
	"strings"
)

// Parser converts a token stream into an AST.
type Parser struct {
	tokens []Token
	pos    int
}

// Parse tokenizes and parses a Cypher query string into an AST.
func Parse(input string) (*Query, error) {
	tokens, err := Lex(input)
	if err != nil {
		return nil, fmt.Errorf("lex: %w", err)
	}
	p := &Parser{tokens: tokens}
	return p.parseQuery()
}

func (p *Parser) peek() Token {
	if p.pos >= len(p.tokens) {
		return Token{Type: TokEOF}
	}
	return p.tokens[p.pos]
}

func (p *Parser) advance() Token {
	t := p.peek()
	p.pos++
	return t
}

func (p *Parser) expect(typ TokenType) error {
	t := p.advance()
	if t.Type != typ {
		return fmt.Errorf("expected token %d, got %d (%q) at pos %d", typ, t.Type, t.Value, t.Pos)
	}
	return nil
}

func (p *Parser) parseQuery() (*Query, error) {
	q := &Query{}

	// Reject write keywords at parse-time. code-graph implements a
	// read-only Cypher subset; CREATE/DELETE/SET/MERGE/REMOVE are
	// recognized for the sole purpose of producing a clear error.
	if err := p.rejectWriteKeyword(p.peek()); err != nil {
		return nil, err
	}

	// MATCH clause (required)
	if p.peek().Type != TokMatch {
		return nil, fmt.Errorf("expected MATCH at pos %d, got %q", p.peek().Pos, p.peek().Value)
	}
	m, err := p.parseMatch()
	if err != nil {
		return nil, err
	}
	q.Match = m

	// Reject write keyword between MATCH and WHERE/RETURN.
	if err := p.rejectWriteKeyword(p.peek()); err != nil {
		return nil, err
	}
	// Reject WITH clause between MATCH and WHERE/RETURN.
	if err := p.rejectWithClause(p.peek()); err != nil {
		return nil, err
	}

	// WHERE clause (optional)
	if p.peek().Type == TokWhere {
		w, err := p.parseWhere()
		if err != nil {
			return nil, err
		}
		q.Where = w
	}

	// Reject write keyword / WITH clause between WHERE and RETURN.
	if err := p.rejectWriteKeyword(p.peek()); err != nil {
		return nil, err
	}
	if err := p.rejectWithClause(p.peek()); err != nil {
		return nil, err
	}

	// RETURN clause (optional but common)
	if p.peek().Type == TokReturn {
		r, err := p.parseReturn()
		if err != nil {
			return nil, err
		}
		q.Return = r
	}

	// Reject trailing tokens. Without this, queries like
	// `MATCH (n) DELETE n` silently parse (the trailing DELETE n is
	// dropped). Trailing-token rejection makes the read-only-subset
	// claim accurate at parse time.
	if p.peek().Type != TokEOF {
		if err := p.rejectWriteKeyword(p.peek()); err != nil {
			return nil, err
		}
		if err := p.rejectWithClause(p.peek()); err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("unexpected trailing token %q at pos %d", p.peek().Value, p.peek().Pos)
	}

	return q, nil
}

// rejectWithClause emits a clear error when the parser encounters a
// `WITH` token where it would begin a clause (i.e. NOT inside `STARTS
// WITH` / `ENDS WITH`, which are consumed inside parseCondition before
// parseQuery sees them at clause boundaries).
//
// Background: code-graph's Cypher subset does not implement the
// `WITH` intermediate-projection clause. Bare `MATCH ... WITH ...
// RETURN ...` queries previously fell through to a generic
// "unexpected trailing token" error message. PSM 2026-05-07 baseline
// reported confusion when callers wrote aggregation queries (`WITH
// b.name AS callee, COUNT(*) AS calls ...`) — the generic message
// did not surface the actual cause (no aggregation-via-WITH support)
// or the recommended workarounds. This rejection is the B2 error-
// fast path: name the gap, point at the workarounds. Full WITH /
// aggregation support remains a separate workstream.
func (p *Parser) rejectWithClause(t Token) error {
	if t.Type != TokWith {
		return nil
	}
	return fmt.Errorf(
		"WITH clause not supported in code-graph's Cypher subset (pos %d). "+
			"Aggregation via `WITH ... COUNT(*)` is not implemented. "+
			"Workarounds: (a) use `RETURN COUNT(*)` directly when the entire "+
			"MATCH should be counted; (b) use `search_graph` with `min_degree`/"+
			"`max_degree` filters for fan-in/fan-out counting; (c) post-process "+
			"raw rows in the caller", t.Pos)
}

// rejectWriteKeyword returns a clear error if the token is a write
// keyword (CREATE / DELETE / SET / MERGE / REMOVE). code-graph's
// Cypher subset is read-only by design.
func (p *Parser) rejectWriteKeyword(t Token) error {
	switch t.Type {
	case TokCreate:
		return fmt.Errorf("CREATE not supported in read-only Cypher subset (pos %d)", t.Pos)
	case TokDelete:
		return fmt.Errorf("DELETE not supported in read-only Cypher subset (pos %d)", t.Pos)
	case TokSet:
		return fmt.Errorf("SET not supported in read-only Cypher subset (pos %d)", t.Pos)
	case TokMerge:
		return fmt.Errorf("MERGE not supported in read-only Cypher subset (pos %d)", t.Pos)
	case TokRemove:
		return fmt.Errorf("REMOVE not supported in read-only Cypher subset (pos %d)", t.Pos)
	}
	return nil
}

func (p *Parser) parseMatch() (*MatchClause, error) {
	if err := p.expect(TokMatch); err != nil {
		return nil, err
	}
	pat, err := p.parsePattern()
	if err != nil {
		return nil, fmt.Errorf("match pattern: %w", err)
	}
	return &MatchClause{Pattern: pat}, nil
}

func (p *Parser) parsePattern() (*Pattern, error) {
	pat := &Pattern{}

	// First element must be a node
	node, err := p.parseNodePattern()
	if err != nil {
		return nil, err
	}
	pat.Elements = append(pat.Elements, node)

	// Parse alternating rel-node pairs
	for p.isRelStart() {
		rel, nextNode, err := p.parseRelAndNode()
		if err != nil {
			return nil, err
		}
		pat.Elements = append(pat.Elements, rel, nextNode)
	}

	return pat, nil
}

// isRelStart checks whether the next tokens begin a relationship pattern.
// Patterns: -[...]-> or <-[...]- or -[...]-
func (p *Parser) isRelStart() bool {
	t := p.peek()
	return t.Type == TokDash || t.Type == TokLT
}

func (p *Parser) parseRelAndNode() (*RelPattern, *NodePattern, error) {
	rel := &RelPattern{MinHops: 1, MaxHops: 1}

	// Determine direction by looking at leading token
	// Possibilities:
	//   -[...]->(node)   outbound
	//   <-[...]-(node)   inbound
	//   -[...]-(node)    any

	leadingArrow := false
	if p.peek().Type == TokLT {
		leadingArrow = true
		p.advance() // consume <
	}

	// Expect dash
	if err := p.expect(TokDash); err != nil {
		return nil, nil, fmt.Errorf("expected '-' in relationship: %w", err)
	}

	// Optional bracket section [...]
	if p.peek().Type == TokLBracket {
		if err := p.parseRelBracket(rel); err != nil {
			return nil, nil, err
		}
	}

	// Expect dash
	if err := p.expect(TokDash); err != nil {
		return nil, nil, fmt.Errorf("expected '-' after relationship: %w", err)
	}

	// Check trailing arrow
	trailingArrow := false
	if p.peek().Type == TokGT {
		trailingArrow = true
		p.advance() // consume >
	}

	// Determine direction
	switch {
	case !leadingArrow && trailingArrow:
		rel.Direction = "outbound"
	case leadingArrow && !trailingArrow:
		rel.Direction = "inbound"
	default:
		rel.Direction = "any"
	}

	// Parse the next node
	node, err := p.parseNodePattern()
	if err != nil {
		return nil, nil, err
	}

	return rel, node, nil
}

func (p *Parser) parseRelBracket(rel *RelPattern) error {
	p.advance() // consume [

	// Optional variable name
	if p.peek().Type == TokIdent {
		rel.Variable = p.advance().Value
	}

	// Optional :TYPE or :TYPE1|TYPE2
	if p.peek().Type == TokColon {
		p.advance() // consume :
		types, err := p.parseRelTypes()
		if err != nil {
			return err
		}
		rel.Types = types
	}

	// Optional *min..max for variable-length
	if p.peek().Type == TokStar {
		p.advance() // consume *
		if err := p.parseHopRange(rel); err != nil {
			return err
		}
	}

	// Expect ]
	if err := p.expect(TokRBracket); err != nil {
		return fmt.Errorf("expected ']' to close relationship: %w", err)
	}

	return nil
}

func (p *Parser) parseRelTypes() ([]string, error) {
	var types []string
	t := p.advance()
	if t.Type != TokIdent {
		return nil, fmt.Errorf("expected relationship type name, got %q at pos %d", t.Value, t.Pos)
	}
	types = append(types, t.Value)

	// Handle TYPE1|TYPE2
	for p.peek().Type == TokPipe {
		p.advance() // consume |
		t = p.advance()
		if t.Type != TokIdent {
			return nil, fmt.Errorf("expected relationship type after '|', got %q at pos %d", t.Value, t.Pos)
		}
		types = append(types, t.Value)
	}
	return types, nil
}

func (p *Parser) parseHopRange(rel *RelPattern) error {
	// Possibilities after *:
	//   *1..3   min=1, max=3
	//   *..3    min=1, max=3
	//   *1..    min=1, max=0 (unbounded)
	//   *3      min=1, max=3 (shorthand)
	//   (empty) min=1, max=0 (unbounded)

	switch p.peek().Type {
	case TokNumber:
		n, _ := strconv.Atoi(p.advance().Value)
		if p.peek().Type == TokDotDot {
			// *N..M or *N..
			rel.MinHops = n
			p.advance() // consume ..
			if p.peek().Type == TokNumber {
				m, _ := strconv.Atoi(p.advance().Value)
				rel.MaxHops = m
			} else {
				rel.MaxHops = 0 // unbounded
			}
		} else {
			// *N (shorthand for *1..N)
			rel.MinHops = 1
			rel.MaxHops = n
		}
	case TokDotDot:
		// *..M
		p.advance() // consume ..
		rel.MinHops = 1
		if p.peek().Type == TokNumber {
			m, _ := strconv.Atoi(p.advance().Value)
			rel.MaxHops = m
		} else {
			rel.MaxHops = 0
		}
	default:
		// Just * with no range: unbounded
		rel.MinHops = 1
		rel.MaxHops = 0
	}

	return nil
}

func (p *Parser) parseNodePattern() (*NodePattern, error) {
	if err := p.expect(TokLParen); err != nil {
		return nil, fmt.Errorf("expected '(' for node pattern: %w", err)
	}

	node := &NodePattern{}

	// Optional variable name
	if p.peek().Type == TokIdent {
		node.Variable = p.advance().Value
	}

	// Optional :Label
	if p.peek().Type == TokColon {
		p.advance() // consume :
		t := p.advance()
		if t.Type != TokIdent {
			return nil, fmt.Errorf("expected label name after ':', got %q at pos %d", t.Value, t.Pos)
		}
		node.Label = t.Value
	}

	// Optional {key: "val", ...}
	if p.peek().Type == TokLBrace {
		props, err := p.parseInlineProps()
		if err != nil {
			return nil, err
		}
		node.Props = props
	}

	if err := p.expect(TokRParen); err != nil {
		return nil, fmt.Errorf("expected ')' to close node pattern: %w", err)
	}

	return node, nil
}

func (p *Parser) parseInlineProps() (map[string]string, error) {
	p.advance() // consume {
	props := make(map[string]string)

	for p.peek().Type != TokRBrace {
		if len(props) > 0 {
			if err := p.expect(TokComma); err != nil {
				return nil, fmt.Errorf("expected ',' between properties: %w", err)
			}
		}

		// key
		keyTok := p.advance()
		if keyTok.Type != TokIdent {
			return nil, fmt.Errorf("expected property key, got %q at pos %d", keyTok.Value, keyTok.Pos)
		}

		// :
		if err := p.expect(TokColon); err != nil {
			return nil, fmt.Errorf("expected ':' after property key: %w", err)
		}

		// value (string)
		valTok := p.advance()
		if valTok.Type != TokString {
			return nil, fmt.Errorf("expected string value for property %q, got %q at pos %d", keyTok.Value, valTok.Value, valTok.Pos)
		}

		props[keyTok.Value] = valTok.Value
	}

	p.advance() // consume }
	return props, nil
}

func (p *Parser) parseWhere() (*WhereClause, error) {
	p.advance() // consume WHERE
	w := &WhereClause{Operator: "AND"}

	cond, err := p.parseCondition()
	if err != nil {
		return nil, err
	}
	w.Conditions = append(w.Conditions, cond)

	for p.peek().Type == TokAnd || p.peek().Type == TokOr {
		op := p.advance()
		if op.Type == TokOr {
			w.Operator = "OR"
		}
		cond, err := p.parseCondition()
		if err != nil {
			return nil, err
		}
		w.Conditions = append(w.Conditions, cond)
	}

	return w, nil
}

func (p *Parser) parseCondition() (Condition, error) {
	c := Condition{}

	// variable.property
	varTok := p.advance()
	if varTok.Type != TokIdent {
		return c, fmt.Errorf("expected variable name in condition, got %q at pos %d", varTok.Value, varTok.Pos)
	}
	c.Variable = varTok.Value

	if err := p.expect(TokDot); err != nil {
		return c, fmt.Errorf("expected '.' after variable in condition: %w", err)
	}

	propTok := p.advance()
	if propTok.Type != TokIdent {
		return c, fmt.Errorf("expected property name in condition, got %q at pos %d", propTok.Value, propTok.Pos)
	}
	c.Property = propTok.Value

	// Operator
	op := p.peek()
	switch op.Type {
	case TokEQ:
		c.Operator = "="
		p.advance()
	case TokRegex:
		c.Operator = "=~"
		p.advance()
	case TokGT:
		c.Operator = ">"
		p.advance()
	case TokLT:
		c.Operator = "<"
		p.advance()
	case TokGTE:
		c.Operator = ">="
		p.advance()
	case TokLTE:
		c.Operator = "<="
		p.advance()
	case TokContains:
		c.Operator = "CONTAINS"
		p.advance()
	case TokStarts:
		// STARTS WITH
		p.advance() // consume STARTS
		if p.peek().Type != TokWith {
			return c, fmt.Errorf("expected WITH after STARTS at pos %d", p.peek().Pos)
		}
		p.advance() // consume WITH
		c.Operator = "STARTS WITH"
	case TokEnds:
		// ENDS WITH (parallel to STARTS WITH)
		p.advance() // consume ENDS
		if p.peek().Type != TokWith {
			return c, fmt.Errorf("expected WITH after ENDS at pos %d", p.peek().Pos)
		}
		p.advance() // consume WITH
		c.Operator = "ENDS WITH"
	case TokIs:
		// IS NULL or IS NOT NULL — these conditions take no value.
		p.advance() // consume IS
		if p.peek().Type == TokNot {
			p.advance() // consume NOT
			if p.peek().Type != TokNull {
				return c, fmt.Errorf("expected NULL after IS NOT at pos %d, got %q", p.peek().Pos, p.peek().Value)
			}
			p.advance() // consume NULL
			c.Operator = "IS NOT NULL"
		} else {
			if p.peek().Type != TokNull {
				return c, fmt.Errorf("expected NULL or NOT NULL after IS at pos %d, got %q", p.peek().Pos, p.peek().Value)
			}
			p.advance() // consume NULL
			c.Operator = "IS NULL"
		}
		// IS NULL / IS NOT NULL take no value; return early.
		return c, nil
	case TokIn:
		// IN [list] — value list of strings or numbers.
		p.advance() // consume IN
		if err := p.expect(TokLBracket); err != nil {
			return c, fmt.Errorf("expected '[' after IN: %w", err)
		}
		c.Operator = "IN"
		// Empty list is a parse error — `WHERE x IN []` would always be false
		// and is almost certainly user error. Reject it explicitly.
		if p.peek().Type == TokRBracket {
			return c, fmt.Errorf("empty list after IN at pos %d", p.peek().Pos)
		}
		for {
			vt := p.advance()
			switch vt.Type {
			case TokString, TokNumber:
				c.Values = append(c.Values, vt.Value)
			default:
				return c, fmt.Errorf("expected string or number in IN list, got %q at pos %d", vt.Value, vt.Pos)
			}
			if p.peek().Type == TokComma {
				p.advance() // consume ,
				continue
			}
			break
		}
		if err := p.expect(TokRBracket); err != nil {
			return c, fmt.Errorf("expected ']' to close IN list: %w", err)
		}
		// IN takes a list, not a scalar value; return early.
		return c, nil
	default:
		return c, fmt.Errorf("expected comparison operator, got %q at pos %d", op.Value, op.Pos)
	}

	// Value (string or number)
	valTok := p.advance()
	switch valTok.Type {
	case TokString:
		c.Value = valTok.Value
	case TokNumber:
		c.Value = valTok.Value
	default:
		return c, fmt.Errorf("expected value in condition, got %q at pos %d", valTok.Value, valTok.Pos)
	}

	return c, nil
}

func (p *Parser) parseReturn() (*ReturnClause, error) {
	p.advance() // consume RETURN
	r := &ReturnClause{OrderDir: "ASC"}

	// Optional DISTINCT
	if p.peek().Type == TokDistinct {
		r.Distinct = true
		p.advance()
	}

	// Parse return items
	item, err := p.parseReturnItem()
	if err != nil {
		return nil, err
	}
	r.Items = append(r.Items, item)

	for p.peek().Type == TokComma {
		p.advance() // consume ,
		item, err := p.parseReturnItem()
		if err != nil {
			return nil, err
		}
		r.Items = append(r.Items, item)
	}

	// Optional ORDER BY
	if p.peek().Type == TokOrder {
		orderBy, orderDir, err := p.parseOrderBy()
		if err != nil {
			return nil, err
		}
		r.OrderBy = orderBy
		r.OrderDir = orderDir
	}

	// Optional LIMIT
	if p.peek().Type == TokLimit {
		p.advance() // consume LIMIT
		numTok := p.advance()
		if numTok.Type != TokNumber {
			return nil, fmt.Errorf("expected number after LIMIT, got %q", numTok.Value)
		}
		n, _ := strconv.Atoi(numTok.Value)
		r.Limit = n
	}

	return r, nil
}

func (p *Parser) parseReturnItem() (ReturnItem, error) {
	item := ReturnItem{}

	// Check for COUNT(variable)
	if p.peek().Type == TokCount {
		return p.parseCountItem()
	}

	// variable or variable.property or func_name(variable)
	varTok := p.advance()
	if varTok.Type != TokIdent {
		return item, fmt.Errorf("expected variable in RETURN item, got %q at pos %d", varTok.Value, varTok.Pos)
	}

	// Phase B2 (Plan 8-Phase Arc, 2026-05-09): function-call form.
	// `labels(node)` is the openCypher standard form for getting a node's
	// label set. code-graph nodes have one label each, so the result is
	// always a single-element array.
	if p.peek().Type == TokLParen {
		return p.parseFunctionCallItem(varTok)
	}

	item.Variable = varTok.Value

	if p.peek().Type == TokDot {
		p.advance() // consume .
		propTok := p.advance()
		if propTok.Type != TokIdent {
			return item, fmt.Errorf("expected property after '.', got %q", propTok.Value)
		}
		item.Property = propTok.Value
	}

	// Optional AS alias
	if p.peek().Type == TokAs {
		p.advance() // consume AS
		aliasTok := p.advance()
		if aliasTok.Type != TokIdent {
			return item, fmt.Errorf("expected alias after AS, got %q", aliasTok.Value)
		}
		item.Alias = aliasTok.Value
	}

	return item, nil
}

// parseFunctionCallItem parses a built-in function call in a RETURN item.
// Currently supports: labels(variable). Extending the set later means adding
// more cases here and matching executor logic in resolveItemValue.
//
// The funcTok has already been consumed; we expect '(' next.
func (p *Parser) parseFunctionCallItem(funcTok Token) (ReturnItem, error) {
	item := ReturnItem{}
	funcName := strings.ToUpper(funcTok.Value)
	switch funcName {
	case "LABELS":
		// labels(node) — fall through.
	default:
		return item, fmt.Errorf(
			"unknown function %q at pos %d (supported: labels)",
			funcTok.Value, funcTok.Pos)
	}
	item.Func = funcName
	if err := p.expect(TokLParen); err != nil {
		return item, fmt.Errorf("expected '(' after %s: %w", funcName, err)
	}
	argTok := p.advance()
	if argTok.Type != TokIdent {
		return item, fmt.Errorf(
			"expected variable in %s(), got %q at pos %d",
			funcName, argTok.Value, argTok.Pos)
	}
	item.Variable = argTok.Value
	if err := p.expect(TokRParen); err != nil {
		return item, fmt.Errorf("expected ')' after %s argument: %w", funcName, err)
	}
	// Optional AS alias
	if p.peek().Type == TokAs {
		p.advance()
		aliasTok := p.advance()
		if aliasTok.Type != TokIdent {
			return item, fmt.Errorf("expected alias after AS, got %q", aliasTok.Value)
		}
		item.Alias = aliasTok.Value
	}
	return item, nil
}

// parseCountItem parses a COUNT(variable | * | DISTINCT variable) [AS alias] expression.
// COUNT(*) is the openCypher standard form for counting all rows;
// COUNT(var) counts non-null bindings of a specific variable;
// COUNT(DISTINCT var) counts unique values of var across bindings.
// The executor treats COUNT(*) and COUNT(var) as row-count aggregations
// and COUNT(DISTINCT var) as a set-cardinality aggregation.
func (p *Parser) parseCountItem() (ReturnItem, error) {
	item := ReturnItem{}
	p.advance() // consume COUNT
	item.Func = "COUNT"
	if err := p.expect(TokLParen); err != nil {
		return item, fmt.Errorf("expected '(' after COUNT: %w", err)
	}
	// COUNT(DISTINCT var) — consume DISTINCT and continue to the variable.
	// Phase B1 (Plan 8-Phase Arc, 2026-05-09): standard openCypher form.
	if p.peek().Type == TokDistinct {
		p.advance() // consume DISTINCT
		item.Distinct = true
	}
	varTok := p.advance()
	switch varTok.Type {
	case TokIdent:
		item.Variable = varTok.Value
		// Optional .property — COUNT(DISTINCT n.name) counts unique
		// values of the named property rather than unique bindings.
		if p.peek().Type == TokDot {
			p.advance() // consume .
			propTok := p.advance()
			if propTok.Type != TokIdent {
				return item, fmt.Errorf("expected property after '.' in COUNT(), got %q at pos %d",
					propTok.Value, propTok.Pos)
			}
			item.Property = propTok.Value
		}
	case TokStar:
		// COUNT(*) — represented internally with Variable="*".
		// Executor short-circuits to the binding count without
		// requiring a specific variable to be non-null.
		if item.Distinct {
			return item, fmt.Errorf("COUNT(DISTINCT *) is not valid — use COUNT(*) or COUNT(DISTINCT var) at pos %d",
				varTok.Pos)
		}
		item.Variable = "*"
	default:
		return item, fmt.Errorf("expected variable or '*' in COUNT(), got %q at pos %d", varTok.Value, varTok.Pos)
	}
	if err := p.expect(TokRParen); err != nil {
		return item, fmt.Errorf("expected ')' after COUNT argument: %w", err)
	}

	// Optional AS alias
	if p.peek().Type == TokAs {
		p.advance() // consume AS
		aliasTok := p.advance()
		if aliasTok.Type != TokIdent {
			return item, fmt.Errorf("expected alias after AS, got %q", aliasTok.Value)
		}
		item.Alias = aliasTok.Value
	}

	return item, nil
}

// parseOrderBy parses ORDER BY <field|COUNT(var)> [ASC|DESC].
func (p *Parser) parseOrderBy() (orderBy, orderDir string, err error) {
	p.advance() // consume ORDER
	if err := p.expect(TokBy); err != nil {
		return "", "", fmt.Errorf("expected BY after ORDER: %w", err)
	}

	orderField, err := p.parseOrderField()
	if err != nil {
		return "", "", err
	}

	// Optional ASC/DESC
	dir := ""
	if p.peek().Type == TokAsc {
		dir = "ASC"
		p.advance()
	} else if p.peek().Type == TokDesc {
		dir = "DESC"
		p.advance()
	}
	return orderField, dir, nil
}

// parseOrderField parses the field expression in ORDER BY: either COUNT(var) or var[.prop].
func (p *Parser) parseOrderField() (string, error) {
	if p.peek().Type == TokCount {
		p.advance() // consume COUNT
		if err := p.expect(TokLParen); err != nil {
			return "", fmt.Errorf("expected '(' after COUNT in ORDER BY: %w", err)
		}
		varTok := p.advance()
		if varTok.Type != TokIdent {
			return "", fmt.Errorf("expected variable in COUNT(), got %q", varTok.Value)
		}
		if err := p.expect(TokRParen); err != nil {
			return "", fmt.Errorf("expected ')' after COUNT variable in ORDER BY: %w", err)
		}
		return "COUNT(" + varTok.Value + ")", nil
	}

	orderTok := p.advance()
	if orderTok.Type != TokIdent {
		return "", fmt.Errorf("expected field name for ORDER BY, got %q", orderTok.Value)
	}
	field := orderTok.Value
	if p.peek().Type == TokDot {
		p.advance() // consume .
		propTok := p.advance()
		field += "." + propTok.Value
	}
	return field, nil
}
