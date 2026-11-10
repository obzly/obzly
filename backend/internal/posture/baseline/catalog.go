// Package baseline is Obzly's own posture checks, used where a cloud's native findings are off
// or out of reach (docs/CLOUD_COST_POSTURE.md "Obzly baseline"). Each check is a CEL expression
// over one resource's facts, which the probe gathers read-only. The probe evaluates them and
// sends only findings; the Site reads the same catalog for titles and remediation.
package baseline

import (
	"embed"
	"fmt"
	"io/fs"
	"maps"
	"path"
	"slices"
	"sort"
	"strings"
	"sync"

	"cel.dev/cel-go/cel"
	"gopkg.in/yaml.v3"

	"obzly/internal/synthetics/spec"
)

//go:embed checks
var files embed.FS

// Check is one baseline check, as written in checks/<provider>/<name>.yaml.
type Check struct {
	ID       string `yaml:"id"       json:"id"`
	Provider string `yaml:"provider" json:"provider"`
	// Resource is the fact type the check reads, e.g. aws_s3_bucket.
	Resource string `yaml:"resource" json:"resource"`
	Title    string `yaml:"title"    json:"title"`
	Severity string `yaml:"severity" json:"severity"`
	// Control is the finding's identity: the native control it stands in for (Security Hub's
	// S3.8), so a finding keeps its history when its source changes.
	Control   string             `yaml:"control"   json:"control"`
	Standards []spec.StandardRef `yaml:"standards" json:"standards"`
	// Pass is true when the resource complies; Applies, when set, is false when it doesn't apply.
	Pass        string      `yaml:"pass"        json:"-"`
	Applies     string      `yaml:"applies"     json:"-"`
	Evidence    []string    `yaml:"evidence"    json:"-"`
	Remediation Remediation `yaml:"remediation" json:"remediation"`
	Fixtures    struct {
		Pass []map[string]any `yaml:"pass"`
		Fail []map[string]any `yaml:"fail"`
	} `yaml:"fixtures"    json:"-"`

	pass    cel.Program
	applies cel.Program
}

// Remediation is how to fix a failed check; {name}, {region} and {resourceGroup} are filled in
// from the finding.
type Remediation struct {
	CLI     string `yaml:"cli"     json:"cli"`
	IaC     string `yaml:"iac"     json:"iac"`
	Console string `yaml:"console" json:"console"`
}

// Fact is what the probe read about one resource, in the field names the checks use.
type Fact struct {
	Type    string
	Key     string
	Account string
	Region  string
	Values  map[string]any
}

// Result is one check on one fact.
type Result struct {
	Check  *Check
	Fact   Fact
	Passed bool
}

var (
	loadOnce sync.Once
	catalog  []*Check
	byID     map[string]*Check
	errLoad  error

	severities = []string{spec.SeverityCritical, spec.SeverityHigh, spec.SeverityMedium, spec.SeverityLow,
		spec.SeverityInformational}
)

// Checks is the whole catalog, by ID.
func Checks() ([]*Check, error) {
	loadOnce.Do(func() { catalog, byID, errLoad = load(files) })
	return catalog, errLoad
}

// Lookup finds a check by ID; nil when there's none (a check removed since a finding was made).
func Lookup(id string) *Check {
	if _, err := Checks(); err != nil {
		return nil
	}
	return byID[id]
}

// ForProvider is the provider's checks.
func ForProvider(provider string) []*Check {
	checks, _ := Checks()
	var out []*Check
	for _, check := range checks {
		if check.Provider == provider {
			out = append(out, check)
		}
	}
	return out
}

// Evaluate runs every check of the provider on the facts of its resource type. A check that
// doesn't apply to a fact gives no result; one that can't be evaluated (a missing fact field)
// counts as failed with its error in the evidence, never as a pass.
func Evaluate(provider string, facts []Fact) []Result {
	byType := map[string][]Fact{}
	for _, fact := range facts {
		byType[fact.Type] = append(byType[fact.Type], fact)
	}
	var results []Result
	for _, check := range ForProvider(provider) {
		for _, fact := range byType[check.Resource] {
			applies, passed, err := check.evaluate(fact.Values)
			if !applies {
				continue
			}
			if err != nil {
				fact.Values = maps.Clone(fact.Values)
				fact.Values["evaluation_error"] = err.Error()
			}
			results = append(results, Result{Check: check, Fact: fact, Passed: passed && err == nil})
		}
	}
	return results
}

// EvidenceOf is the check's evidence fields of a fact, as text.
func (c *Check) EvidenceOf(values map[string]any) map[string]string {
	evidence := map[string]string{}
	for _, field := range append(slices.Clone(c.Evidence), "evaluation_error") {
		if value, ok := values[field]; ok {
			evidence[field] = fmt.Sprint(value)
		}
	}
	return evidence
}

func (c *Check) evaluate(values map[string]any) (applies, passed bool, err error) {
	input := map[string]any{"r": values}
	if c.applies != nil {
		out, _, err := c.applies.Eval(input)
		if err != nil {
			return true, false, err
		}
		if ok, _ := out.Value().(bool); !ok {
			return false, false, nil
		}
	}
	out, _, err := c.pass.Eval(input)
	if err != nil {
		return true, false, err
	}
	ok, isBool := out.Value().(bool)
	if !isBool {
		return true, false, fmt.Errorf("check %s returned %T, not a bool", c.ID, out.Value())
	}
	return true, ok, nil
}

func load(fsys fs.FS) ([]*Check, map[string]*Check, error) {
	env, err := cel.NewEnv(cel.Variable("r", cel.MapType(cel.StringType, cel.DynType)))
	if err != nil {
		return nil, nil, err
	}
	var checks []*Check
	index := map[string]*Check{}
	err = fs.WalkDir(fsys, "checks", func(name string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() || path.Ext(name) != ".yaml" {
			return err
		}
		data, err := fs.ReadFile(fsys, name)
		if err != nil {
			return err
		}
		var check Check
		if err := yaml.Unmarshal(data, &check); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		if err := check.compile(env); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		if index[check.ID] != nil {
			return fmt.Errorf("%s: check %s is defined twice", name, check.ID)
		}
		index[check.ID] = &check
		checks = append(checks, &check)
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	sort.Slice(checks, func(i, j int) bool { return checks[i].ID < checks[j].ID })
	return checks, index, nil
}

func (c *Check) compile(env *cel.Env) error {
	switch {
	case c.ID == "" || c.Resource == "" || c.Title == "" || c.Control == "":
		return fmt.Errorf("a check needs id, resource, title and control")
	case c.Provider != spec.ProviderAWS && c.Provider != spec.ProviderAzure:
		return fmt.Errorf("check %s: provider must be aws or azure", c.ID)
	case !slices.Contains(severities, c.Severity):
		return fmt.Errorf("check %s: severity must be one of %s", c.ID, strings.Join(severities, ", "))
	}
	for _, ref := range c.Standards {
		if spec.PostureStandardName[ref.Standard] == "" || ref.Control == "" {
			return fmt.Errorf("check %s: unknown standard %q", c.ID, ref.Standard)
		}
	}
	var err error
	if c.pass, err = program(env, c.Pass); err != nil {
		return fmt.Errorf("check %s pass: %w", c.ID, err)
	}
	if c.Applies != "" {
		if c.applies, err = program(env, c.Applies); err != nil {
			return fmt.Errorf("check %s applies: %w", c.ID, err)
		}
	}
	return nil
}

func program(env *cel.Env, expression string) (cel.Program, error) {
	ast, issues := env.Compile(expression)
	if issues != nil && issues.Err() != nil {
		return nil, issues.Err()
	}
	if !ast.OutputType().IsExactType(cel.BoolType) && !ast.OutputType().IsExactType(cel.DynType) {
		return nil, fmt.Errorf("returns %s, not a bool", ast.OutputType())
	}
	return env.Program(ast)
}
