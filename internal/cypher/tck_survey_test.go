package cypher

// openCypher TCK conformance survey. Runs the vendored TCK scenarios (see
// tck/README.md for provenance) against the real engine and compares every
// scenario's verdict to the pinned baseline in tck/baseline.tsv.
//
// The test fails when ANY verdict changes — in either direction. A
// previously-passing scenario failing is a regression; a previously-failing
// scenario passing means the baseline (and possibly CONFORMANCE.md's known
// deviations) understates conformance and must be updated:
//
//	CBM_UPDATE_TCK_BASELINE=1 go test ./internal/cypher/ -run TestTCKSurvey
//
// This corpus found three silent-wrong-results bugs on its first run
// (LIMIT 0 returning rows, LIMIT 1.7 accepted, n.id returning the SQLite
// rowid instead of the user property) that both the hand-built conformance
// corpus and targeted regression sweeps had missed.

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/brandyn-s/code-graph/internal/store"
)

const (
	tckFeatureRoot  = "tck/features"
	tckBaselinePath = "tck/baseline.tsv"
)

// ---------- Gherkin scenario model ----------

type tckScenario struct {
	feature     string
	name        string
	isOutline   bool
	setups      []string // CREATE statements from "having executed" blocks
	query       string
	expectErr   bool
	ordered     bool
	expectCols  []string
	expectRows  [][]string // raw cell strings
	expectEmpty bool
	hasResult   bool
}

func (sc tckScenario) key() string { return sc.feature + " :: " + sc.name }

func parseFeatureFile(path string) []tckScenario {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	lines := strings.Split(string(data), "\n")
	var scenarios []tckScenario
	var cur *tckScenario
	i := 0
	readDocstring := func() string {
		// expects lines[i] to be the opening """
		i++
		var sb strings.Builder
		for i < len(lines) && !strings.Contains(lines[i], `"""`) {
			sb.WriteString(strings.TrimSpace(lines[i]))
			sb.WriteString(" ")
			i++
		}
		i++ // closing """
		return strings.TrimSpace(sb.String())
	}
	readTable := func() (cols []string, rows [][]string) {
		for i < len(lines) {
			l := strings.TrimSpace(lines[i])
			if !strings.HasPrefix(l, "|") {
				break
			}
			cells := splitTableRow(l)
			if cols == nil {
				cols = cells
			} else {
				rows = append(rows, cells)
			}
			i++
		}
		return
	}
	for i < len(lines) {
		l := strings.TrimSpace(lines[i])
		switch {
		case strings.HasPrefix(l, "Scenario Outline:"):
			if cur != nil {
				scenarios = append(scenarios, *cur)
			}
			cur = &tckScenario{feature: filepath.Base(path), name: strings.TrimSpace(strings.TrimPrefix(l, "Scenario Outline:")), isOutline: true}
			i++
		case strings.HasPrefix(l, "Scenario:"):
			if cur != nil {
				scenarios = append(scenarios, *cur)
			}
			cur = &tckScenario{feature: filepath.Base(path), name: strings.TrimSpace(strings.TrimPrefix(l, "Scenario:"))}
			i++
		case cur != nil && (strings.HasPrefix(l, "And having executed:") || strings.HasPrefix(l, "Given having executed:")):
			i++
			cur.setups = append(cur.setups, readDocstring())
		case cur != nil && strings.HasPrefix(l, "When executing query:"):
			i++
			cur.query = readDocstring()
		case cur != nil && strings.HasPrefix(l, "Then the result should be empty"):
			cur.expectEmpty = true
			cur.hasResult = true
			i++
		case cur != nil && strings.HasPrefix(l, "Then the result should be, in any order:"):
			cur.ordered = false
			cur.hasResult = true
			i++
			cur.expectCols, cur.expectRows = readTable()
		case cur != nil && strings.HasPrefix(l, "Then the result should be, in order:"):
			cur.ordered = true
			cur.hasResult = true
			i++
			cur.expectCols, cur.expectRows = readTable()
		case cur != nil && (strings.Contains(l, "should be raised at compile time") || strings.Contains(l, "should be raised at runtime")):
			cur.expectErr = true
			i++
		default:
			i++
		}
	}
	if cur != nil {
		scenarios = append(scenarios, *cur)
	}
	return scenarios
}

func splitTableRow(l string) []string {
	l = strings.Trim(l, "|")
	parts := strings.Split(l, "|")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		out = append(out, strings.TrimSpace(p))
	}
	return out
}

// ---------- out-of-scope screen ----------

var (
	reParam        = regexp.MustCompile(`\$\w+`)
	rePathAssign   = regexp.MustCompile(`\b\w+\s*=\s*\(`)
	reMultiPattern = regexp.MustCompile(`\)\s*,\s*\(`)
	rePropRHS      = regexp.MustCompile(`(=|<>|<=|>=|<|>)\s*[a-zA-Z_]\w*\.`)
)

// tckOutOfScope reports whether the query uses a feature outside the
// documented read-only subset (the scenario is then excluded from the
// pass/fail conformance signal rather than counted as a failure).
func tckOutOfScope(q string) (bool, string) {
	u := strings.ToUpper(q)
	// Mask the supported multi-word forms whose components collide with
	// unsupported keywords (STARTS WITH vs WITH, IS NOT NULL vs NOT).
	u = strings.ReplaceAll(u, "STARTS WITH", "STARTSWITH_OK")
	u = strings.ReplaceAll(u, "ENDS WITH", "ENDSWITH_OK")
	u = strings.ReplaceAll(u, "IS NOT NULL", "ISNOTNULL_OK")
	if !strings.HasPrefix(strings.TrimSpace(u), "MATCH") {
		return true, "no MATCH clause (bare RETURN expressions unsupported)"
	}
	for _, kw := range []string{"OPTIONAL", "UNION", "UNWIND", "MERGE", "CALL", "FOREACH", "SKIP", "CREATE", "DELETE", "SET", "REMOVE", "EXISTS", "CASE", "WITH", "NOT", "XOR"} {
		if regexp.MustCompile(`\b` + kw + `\b`).MatchString(u) {
			return true, "keyword:" + kw
		}
	}
	if strings.Count(u, "MATCH") > 1 {
		return true, "multiple MATCH clauses"
	}
	if reParam.MatchString(q) {
		return true, "parameters"
	}
	if rePathAssign.MatchString(q) {
		return true, "path variable assignment"
	}
	if reMultiPattern.MatchString(q) {
		return true, "comma-separated patterns"
	}
	if rePropRHS.MatchString(q) {
		return true, "non-literal comparison RHS"
	}
	if strings.Contains(q, "`") {
		return true, "backtick identifiers"
	}
	// Function-call screen via lexing: an Ident immediately followed by
	// '(' is a function call in Cypher (node patterns are '(' THEN ident).
	toks, err := Lex(q)
	if err != nil {
		return true, "unlexable: " + err.Error()
	}
	for j := 0; j+1 < len(toks); j++ {
		if toks[j].Type == TokIdent && toks[j+1].Type == TokLParen {
			name := strings.ToLower(toks[j].Value)
			if name != "count" && name != "labels" {
				return true, "function:" + name
			}
		}
	}
	return false, ""
}

// ---------- CREATE interpreter ----------

type tckGraph struct {
	s       *store.Store
	counter int
}

//nolint:gocognit // straight-line token-walking interpreter
func (g *tckGraph) interpretCreate(stmt string) error {
	toks, err := Lex(stmt)
	if err != nil {
		return fmt.Errorf("lex: %w", err)
	}
	p := 0
	peek := func() Token {
		if p >= len(toks) {
			return Token{Type: TokEOF}
		}
		return toks[p]
	}
	advance := func() Token { t := peek(); p++; return t }

	vars := map[string]int64{} // per-statement variable bindings

	parseValue := func() (any, error) {
		t := advance()
		switch t.Type {
		case TokString:
			return t.Value, nil
		case TokNumber:
			if strings.Contains(t.Value, ".") {
				f, _ := strconv.ParseFloat(t.Value, 64)
				return f, nil
			}
			n, _ := strconv.Atoi(t.Value)
			return n, nil
		case TokDash:
			vt := advance()
			if vt.Type != TokNumber {
				return nil, fmt.Errorf("bad negative literal")
			}
			if strings.Contains(vt.Value, ".") {
				f, _ := strconv.ParseFloat(vt.Value, 64)
				return -f, nil
			}
			n, _ := strconv.Atoi(vt.Value)
			return -n, nil
		case TokIdent:
			switch strings.ToLower(t.Value) {
			case "true":
				return true, nil
			case "false":
				return false, nil
			}
			return nil, fmt.Errorf("unsupported value ident %q", t.Value)
		case TokNull:
			return nil, nil
		default:
			return nil, fmt.Errorf("unsupported value token %q", t.Value)
		}
	}
	parseProps := func() (map[string]any, error) {
		props := map[string]any{}
		if peek().Type != TokLBrace {
			return props, nil
		}
		advance() // {
		for peek().Type != TokRBrace {
			if len(props) > 0 {
				if peek().Type != TokComma {
					return nil, fmt.Errorf("expected comma in props")
				}
				advance()
			}
			keyTok := advance()
			if keyTok.Type != TokIdent && keyTok.Type != TokString {
				return nil, fmt.Errorf("bad prop key %q", keyTok.Value)
			}
			if peek().Type != TokColon {
				return nil, fmt.Errorf("expected colon")
			}
			advance()
			if peek().Type == TokLBracket {
				advance()
				var list []any
				for peek().Type != TokRBracket {
					if len(list) > 0 {
						if peek().Type != TokComma {
							return nil, fmt.Errorf("expected comma in list")
						}
						advance()
					}
					v, err := parseValue()
					if err != nil {
						return nil, err
					}
					list = append(list, v)
				}
				advance() // ]
				props[keyTok.Value] = list
				continue
			}
			v, err := parseValue()
			if err != nil {
				return nil, err
			}
			props[keyTok.Value] = v
		}
		advance() // }
		return props, nil
	}
	parseNode := func() (int64, error) {
		if peek().Type != TokLParen {
			return 0, fmt.Errorf("expected ( got %q", peek().Value)
		}
		advance()
		varName := ""
		if peek().Type == TokIdent {
			varName = advance().Value
		}
		var labels []string
		for peek().Type == TokColon {
			advance()
			lt := advance()
			if lt.Type != TokIdent {
				return 0, fmt.Errorf("bad label")
			}
			labels = append(labels, lt.Value)
		}
		props, err := parseProps()
		if err != nil {
			return 0, err
		}
		if peek().Type != TokRParen {
			return 0, fmt.Errorf("expected ) got %q", peek().Value)
		}
		advance()
		// Re-reference of an existing variable binds the same node.
		if varName != "" {
			if id, ok := vars[varName]; ok {
				return id, nil
			}
		}
		if len(labels) > 1 {
			return 0, fmt.Errorf("multi-label node (unsupported by store)")
		}
		label := ""
		if len(labels) == 1 {
			label = labels[0]
		}
		name := ""
		if nv, ok := props["name"].(string); ok {
			name = nv
		}
		g.counter++
		id, err := g.s.UpsertNode(&store.Node{
			Project: "tck", Label: label, Name: name,
			QualifiedName: fmt.Sprintf("tck.n%d", g.counter),
			Properties:    props,
		})
		if err != nil {
			return 0, err
		}
		if varName != "" {
			vars[varName] = id
		}
		return id, nil
	}

	// CREATE pattern[, pattern]*
	if peek().Type != TokCreate {
		return fmt.Errorf("expected CREATE, got %q", peek().Value)
	}
	advance()
	for {
		left, err := parseNode()
		if err != nil {
			return err
		}
		for peek().Type == TokDash || peek().Type == TokLT {
			reversed := false
			if peek().Type == TokLT {
				reversed = true
				advance()
			}
			if peek().Type != TokDash {
				return fmt.Errorf("expected dash")
			}
			advance()
			if peek().Type != TokLBracket {
				return fmt.Errorf("expected [ in CREATE rel")
			}
			advance()
			if peek().Type == TokIdent { // rel variable
				advance()
			}
			if peek().Type != TokColon {
				return fmt.Errorf("expected : in rel")
			}
			advance()
			relType := advance()
			if relType.Type != TokIdent {
				return fmt.Errorf("bad rel type")
			}
			relProps, err := parseProps()
			if err != nil {
				return err
			}
			if peek().Type != TokRBracket {
				return fmt.Errorf("expected ]")
			}
			advance()
			if peek().Type != TokDash {
				return fmt.Errorf("expected dash after ]")
			}
			advance()
			if peek().Type == TokGT {
				advance()
				if reversed {
					return fmt.Errorf("bidirectional CREATE rel")
				}
			} else if !reversed {
				return fmt.Errorf("undirected CREATE rel")
			}
			right, err := parseNode()
			if err != nil {
				return err
			}
			src, tgt := left, right
			if reversed {
				src, tgt = right, left
			}
			if _, err := g.s.InsertEdge(&store.Edge{
				Project: "tck", SourceID: src, TargetID: tgt,
				Type: relType.Value, Properties: relProps,
			}); err != nil {
				return err
			}
			left = right
		}
		if peek().Type == TokComma {
			advance()
			continue
		}
		break
	}
	if peek().Type != TokEOF {
		return fmt.Errorf("trailing tokens in CREATE: %q", peek().Value)
	}
	return nil
}

// ---------- expected-value parsing + comparison ----------

type tckNodeLit struct {
	labels []string
	props  map[string]string // raw cell strings
}
type tckRelLit struct{ typ string }
type tckIncomparable struct{ why string }

var (
	reNodeLit = regexp.MustCompile(`^\(\s*((?::\w+)*)\s*(\{.*\})?\s*\)$`)
	reRelLit  = regexp.MustCompile(`^\[:(\w+)\s*(\{.*\})?\]$`)
)

func parseExpectedCell(cell string) any {
	cell = strings.TrimSpace(cell)
	switch {
	case cell == "null":
		return nil
	case cell == "true":
		return true
	case cell == "false":
		return false
	case strings.HasPrefix(cell, "'") && strings.HasSuffix(cell, "'"):
		return strings.Trim(cell, "'")
	case strings.HasPrefix(cell, "<"):
		return tckIncomparable{"path literal"}
	case strings.Contains(cell, ")-[") || strings.Contains(cell, ")<-["):
		return tckIncomparable{"path expression"}
	}
	if n, err := strconv.Atoi(cell); err == nil {
		return n
	}
	if f, err := strconv.ParseFloat(cell, 64); err == nil {
		return f
	}
	if m := reNodeLit.FindStringSubmatch(cell); m != nil {
		var labels []string
		for _, l := range strings.Split(m[1], ":") {
			if l != "" {
				labels = append(labels, l)
			}
		}
		props := map[string]string{}
		if m[2] != "" {
			inner := strings.Trim(m[2], "{}")
			for _, kv := range strings.Split(inner, ",") {
				parts := strings.SplitN(kv, ":", 2)
				if len(parts) == 2 {
					props[strings.TrimSpace(parts[0])] = strings.TrimSpace(parts[1])
				}
			}
		}
		return tckNodeLit{labels: labels, props: props}
	}
	if m := reRelLit.FindStringSubmatch(cell); m != nil {
		return tckRelLit{typ: m[1]}
	}
	if strings.HasPrefix(cell, "[") && strings.HasSuffix(cell, "]") {
		inner := strings.Trim(cell, "[]")
		var list []any
		if strings.TrimSpace(inner) != "" {
			for _, item := range strings.Split(inner, ",") {
				list = append(list, parseExpectedCell(strings.TrimSpace(item)))
			}
		}
		return list
	}
	return tckIncomparable{"unparsed cell: " + cell}
}

// compareTCKValue returns (match, incomparableReason).
func compareTCKValue(expected any, actual any) (bool, string) {
	switch e := expected.(type) {
	case nil:
		return actual == nil, ""
	case bool:
		b, ok := actual.(bool)
		return ok && b == e, ""
	case string:
		s, ok := actual.(string)
		return ok && s == e, ""
	case int:
		f, ok := toFloat(actual)
		return ok && f == float64(e), ""
	case float64:
		f, ok := toFloat(actual)
		return ok && f == e, ""
	case tckNodeLit:
		if len(e.labels) > 1 {
			return false, "multi-label expected node"
		}
		m, ok := actual.(map[string]any)
		if !ok {
			return false, ""
		}
		wantLabel := ""
		if len(e.labels) == 1 {
			wantLabel = e.labels[0]
		}
		if m["label"] != wantLabel {
			return false, ""
		}
		for k, raw := range e.props {
			// Whole-node rows expose only the canonical code-graph fields;
			// the TCK graph stores the `name` prop into Node.Name, so name
			// is the one user property comparable on whole-node returns.
			if k != "name" {
				return false, "expected node prop beyond name: " + k
			}
			want := parseExpectedCell(raw)
			ws, ok := want.(string)
			if !ok {
				return false, "non-string name prop"
			}
			if m["name"] != ws {
				return false, ""
			}
		}
		if len(e.props) == 0 {
			// (:A) must not match a node created with a name prop.
			if m["name"] != "" {
				return false, ""
			}
		}
		return true, ""
	case tckRelLit:
		m, ok := actual.(map[string]any)
		if !ok {
			return false, ""
		}
		return m["type"] == e.typ, ""
	case []any:
		var actualList []any
		switch a := actual.(type) {
		case []string:
			for _, s := range a {
				actualList = append(actualList, s)
			}
		case []any:
			actualList = a
		default:
			return false, ""
		}
		if len(actualList) != len(e) {
			return false, ""
		}
		for i := range e {
			ok, why := compareTCKValue(e[i], actualList[i])
			if why != "" {
				return false, why
			}
			if !ok {
				return false, ""
			}
		}
		return true, ""
	case tckIncomparable:
		return false, e.why
	}
	return false, fmt.Sprintf("unhandled expected type %T", expected)
}

// ---------- the survey ----------

func TestTCKSurvey(t *testing.T) {
	if _, err := os.Stat(tckFeatureRoot); err != nil {
		t.Fatalf("vendored TCK fixtures missing at %s: %v", tckFeatureRoot, err)
	}
	var files []string
	err := filepath.Walk(tckFeatureRoot, func(path string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() && strings.HasSuffix(path, ".feature") {
			files = append(files, path)
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(files)

	verdicts := map[string]string{} // scenario key -> verdict
	details := map[string]string{}
	counts := map[string]int{}
	for _, f := range files {
		for _, sc := range parseFeatureFile(f) {
			verdict, detail := runTCKScenario(t, sc)
			key := sc.key()
			if _, dup := verdicts[key]; dup {
				// Disambiguate rare duplicate scenario names.
				key += " (2)"
			}
			verdicts[key] = verdict
			details[key] = detail
			counts[verdict]++
		}
	}

	keys := make([]string, 0, len(verdicts))
	for k := range verdicts {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	if os.Getenv("CBM_UPDATE_TCK_BASELINE") != "" {
		var sb strings.Builder
		for _, k := range keys {
			fmt.Fprintf(&sb, "%s\t%s\n", k, verdicts[k])
		}
		if err := os.WriteFile(tckBaselinePath, []byte(sb.String()), 0o644); err != nil {
			t.Fatalf("write baseline: %v", err)
		}
		t.Logf("baseline regenerated: %s (%d scenarios)", tckBaselinePath, len(keys))
	}

	// Scorecard (always logged for visibility).
	ck := make([]string, 0, len(counts))
	for k := range counts {
		ck = append(ck, k)
	}
	sort.Strings(ck)
	t.Logf("=== TCK survey scorecard ===")
	for _, k := range ck {
		t.Logf("%-16s %d", k, counts[k])
	}

	// Compare against the pinned baseline.
	baseRaw, err := os.ReadFile(tckBaselinePath)
	if err != nil {
		t.Fatalf("baseline missing (%v) — generate with CBM_UPDATE_TCK_BASELINE=1", err)
	}
	baseline := map[string]string{}
	for _, line := range strings.Split(string(baseRaw), "\n") {
		// Git may check the fixture out with CRLF on Windows. Normalize the
		// line ending so the pinned verdict is identical on every platform.
		line = strings.TrimSuffix(line, "\r")
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "\t", 2)
		if len(parts) == 2 {
			baseline[parts[0]] = parts[1]
		}
	}

	for _, k := range keys {
		want, known := baseline[k]
		if !known {
			t.Errorf("scenario not in baseline (new fixture?): %s = %s — regenerate with CBM_UPDATE_TCK_BASELINE=1", k, verdicts[k])
			continue
		}
		if verdicts[k] != want {
			t.Errorf("verdict drift: %s\n  baseline: %s  now: %s\n  detail: %s\n  (intentional change? regenerate with CBM_UPDATE_TCK_BASELINE=1 and update CONFORMANCE.md)",
				k, want, verdicts[k], details[k])
		}
	}
	for k := range baseline {
		if _, ok := verdicts[k]; !ok {
			t.Errorf("baseline scenario no longer present: %s (fixture removed? regenerate baseline)", k)
		}
	}
}

func runTCKScenario(t *testing.T, sc tckScenario) (string, string) {
	if sc.isOutline {
		return "SKIP_OUTLINE", ""
	}
	if sc.query == "" {
		return "SKIP_NOQUERY", ""
	}
	if oos, why := tckOutOfScope(sc.query); oos {
		return "OUT_OF_SCOPE", why
	}
	if !sc.hasResult && !sc.expectErr {
		return "SKIP_NOEXPECT", ""
	}

	s, err := store.OpenMemory()
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer s.Close()
	if err := s.UpsertProject("tck", "/tmp/tck"); err != nil {
		t.Fatalf("project: %v", err)
	}
	g := &tckGraph{s: s}
	for _, setup := range sc.setups {
		if err := g.interpretCreate(setup); err != nil {
			return "SKIP_SETUP", err.Error()
		}
	}

	exec := &Executor{Store: s}
	res, err := exec.Execute(sc.query)
	if sc.expectErr {
		if err != nil {
			return "PASS_ERROR", ""
		}
		return "FAIL_ERROR", "expected an error, query succeeded"
	}
	if err != nil {
		return "FAIL", "engine error: " + err.Error()
	}

	if sc.expectEmpty {
		if len(res.Rows) == 0 {
			return "PASS", ""
		}
		return "FAIL", fmt.Sprintf("expected empty, got %d rows", len(res.Rows))
	}

	// Map expected columns to result columns (case-insensitive: the engine
	// canonicalizes COUNT(*) etc.).
	colMap := make(map[int]string, len(sc.expectCols))
	for i, ec := range sc.expectCols {
		found := ""
		for _, rc := range res.Columns {
			if strings.EqualFold(rc, ec) {
				found = rc
				break
			}
		}
		if found == "" {
			return "FAIL", fmt.Sprintf("expected column %q not in result columns %v", ec, res.Columns)
		}
		colMap[i] = found
	}

	if len(res.Rows) != len(sc.expectRows) {
		return "FAIL", fmt.Sprintf("row count: got %d want %d", len(res.Rows), len(sc.expectRows))
	}

	type expRow []any
	expRows := make([]expRow, len(sc.expectRows))
	for i, raw := range sc.expectRows {
		er := make(expRow, len(raw))
		for j, cell := range raw {
			v := parseExpectedCell(cell)
			if inc, bad := v.(tckIncomparable); bad {
				return "SKIP_COMPARE", inc.why
			}
			er[j] = v
		}
		expRows[i] = er
	}

	matchRow := func(er expRow, row map[string]any) (bool, string) {
		for j, ev := range er {
			ok, why := compareTCKValue(ev, row[colMap[j]])
			if why != "" {
				return false, why
			}
			if !ok {
				return false, ""
			}
		}
		return true, ""
	}

	if sc.ordered {
		for i, er := range expRows {
			ok, why := matchRow(er, res.Rows[i])
			if why != "" {
				return "SKIP_COMPARE", why
			}
			if !ok {
				return "FAIL", fmt.Sprintf("ordered row %d mismatch: got %v", i, res.Rows[i])
			}
		}
		return "PASS", ""
	}

	// any-order: greedy bipartite match
	used := make([]bool, len(res.Rows))
	for _, er := range expRows {
		matched := false
		for ri, row := range res.Rows {
			if used[ri] {
				continue
			}
			ok, why := matchRow(er, row)
			if why != "" {
				return "SKIP_COMPARE", why
			}
			if ok {
				used[ri] = true
				matched = true
				break
			}
		}
		if !matched {
			return "FAIL", fmt.Sprintf("no row matches expected %v", er)
		}
	}
	return "PASS", ""
}
