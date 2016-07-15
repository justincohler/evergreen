package model

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"

	"github.com/evergreen-ci/evergreen/util"
	"gopkg.in/yaml.v2"
)

// This file contains all of the infrastructure for turning a YAML project configuration
// into a usable Project struct. A basic overview of the project parsing process is:
//
// First, the YAML bytes are unmarshalled into an intermediary parserProject.
// The parserProject's internal types define custom YAML unmarshal hooks, allowing
// users to do things like offer a single definition where we expect a list, e.g.
//   `tags: "single_tag"` instead of the more verbose `tags: ["single_tag"]`
// or refer to task by a single selector. Custom YAML handling allows us to
// add other useful features like detecting fatal errors and reporting them
// through the YAML parser's error code, which supplies helpful line number information
// that we would lose during validation against already-parsed data. In the future,
// custom YAML hooks will allow us to add even more helpful features, like alerting users
// when they use fields that aren't actually defined.
//
// Once the intermediary project is created, we crawl it to evaluate tag selectors
// and (TODO) matrix definitions. This step recursively crawls variants, tasks, their
// dependencies, and so on, to replace selectors with the tasks they reference and return
// a populated Project type.
//
// Code outside of this file should never have to consider selectors or parser* types
// when handling project code.

// parserProject serves as an intermediary struct for parsing project
// configuration YAML. It implements the Unmarshaler interface
// to allow for flexible handling.
type parserProject struct {
	Enabled         bool                       `yaml:"enabled"`
	Stepback        bool                       `yaml:"stepback"`
	DisableCleanup  bool                       `yaml:"disable_cleanup"`
	BatchTime       int                        `yaml:"batchtime"`
	Owner           string                     `yaml:"owner"`
	Repo            string                     `yaml:"repo"`
	RemotePath      string                     `yaml:"remote_path"`
	RepoKind        string                     `yaml:"repokind"`
	Branch          string                     `yaml:"branch"`
	Identifier      string                     `yaml:"identifier"`
	DisplayName     string                     `yaml:"display_name"`
	CommandType     string                     `yaml:"command_type"`
	Ignore          parserStringSlice          `yaml:"ignore"`
	Pre             *YAMLCommandSet            `yaml:"pre"`
	Post            *YAMLCommandSet            `yaml:"post"`
	Timeout         *YAMLCommandSet            `yaml:"timeout"`
	CallbackTimeout int                        `yaml:"callback_timeout_secs"`
	Modules         []Module                   `yaml:"modules"`
	BuildVariants   []parserBV                 `yaml:"buildvariants"`
	Functions       map[string]*YAMLCommandSet `yaml:"functions"`
	Tasks           []parserTask               `yaml:"tasks"`
	ExecTimeoutSecs int                        `yaml:"exec_timeout_secs"`

	// Matrix code
	Axes     []matrixAxis `yaml:"axes"`
	Matrixes []matrix     `yaml:"matrixes"`
}

// parserTask represents an intermediary state of task definitions.
type parserTask struct {
	Name            string              `yaml:"name"`
	Priority        int64               `yaml:"priority"`
	ExecTimeoutSecs int                 `yaml:"exec_timeout_secs"`
	DisableCleanup  bool                `yaml:"disable_cleanup"`
	DependsOn       parserDependencies  `yaml:"depends_on"`
	Requires        TaskSelectors       `yaml:"requires"`
	Commands        []PluginCommandConf `yaml:"commands"`
	Tags            parserStringSlice   `yaml:"tags"`
	Stepback        *bool               `yaml:"stepback"`
}

// helper methods for task tag evaluations
func (pt *parserTask) name() string   { return pt.Name }
func (pt *parserTask) tags() []string { return pt.Tags }

// parserDependency represents the intermediary state for referencing dependencies.
type parserDependency struct {
	TaskSelector
	Status        string `yaml:"status"`
	PatchOptional bool   `yaml:"patch_optional"`
}

// parserDependencies is a type defined for unmarshalling both a single
// dependency or multiple dependencies into a slice.
type parserDependencies []parserDependency

// UnmarshalYAML reads YAML into an array of parserDependency. It will
// successfully unmarshal arrays of dependency entries or single dependency entry.
func (pds *parserDependencies) UnmarshalYAML(unmarshal func(interface{}) error) error {
	// first check if we are handling a single dep that is not in an array.
	pd := parserDependency{}
	if err := unmarshal(&pd); err == nil {
		*pds = parserDependencies([]parserDependency{pd})
		return nil
	}
	var slice []parserDependency
	if err := unmarshal(&slice); err != nil {
		return err
	}
	*pds = parserDependencies(slice)
	return nil
}

// UnmarshalYAML reads YAML into a parserDependency. A single selector string
// will be also be accepted.
func (pd *parserDependency) UnmarshalYAML(unmarshal func(interface{}) error) error {
	if err := unmarshal(&pd.TaskSelector); err != nil {
		return err
	}
	otherFields := struct {
		Status        string `yaml:"status"`
		PatchOptional bool   `yaml:"patch_optional"`
	}{}
	// ignore any errors here; if we're using a single-string selector, this is expected to fail
	unmarshal(&otherFields)
	// TODO validate status
	pd.Status = otherFields.Status
	pd.PatchOptional = otherFields.PatchOptional
	return nil
}

// TaskSelector handles the selection of specific task/variant combinations
// in the context of dependencies and requirements fields.
type TaskSelector struct {
	Name    string `yaml:"name"`
	Variant string `yaml:"variant"`
}

// TaskSelectors is a helper type for parsing arrays of TaskSelector.
type TaskSelectors []TaskSelector

// UnmarshalYAML reads YAML into an array of TaskSelector. It will
// successfully unmarshal arrays of dependency selectors or a single selector.
func (tss *TaskSelectors) UnmarshalYAML(unmarshal func(interface{}) error) error {
	// first, attempt to unmarshal a single selector
	var single TaskSelector
	if err := unmarshal(&single); err == nil {
		*tss = TaskSelectors([]TaskSelector{single})
		return nil
	}
	var slice []TaskSelector
	if err := unmarshal(&slice); err != nil {
		return err
	}
	*tss = TaskSelectors(slice)
	return nil
}

// UnmarshalYAML allows tasks to be referenced as single selector strings.
// This works by first attempting to unmarshal the YAML into a string
// and then falling back to the TaskSelector struct.
func (ts *TaskSelector) UnmarshalYAML(unmarshal func(interface{}) error) error {
	// first, attempt to unmarshal just a selector string
	var onlySelector string
	if err := unmarshal(&onlySelector); err == nil {
		if onlySelector != "" {
			ts.Name = onlySelector
			return nil
		}
	}
	// we define a new type so that we can grab the yaml struct tags without the struct methods,
	// preventing infinite recursion on the UnmarshalYAML() method.
	type copyType TaskSelector
	var tsc copyType
	if err := unmarshal(&tsc); err != nil {
		return err
	}
	if tsc.Name == "" {
		return fmt.Errorf("task selector must have a name")
	}
	*ts = TaskSelector(tsc)
	return nil
}

// parserBV is a helper type storing intermediary variant definitions.
type parserBV struct {
	Name        string            `yaml:"name"`
	DisplayName string            `yaml:"display_name"`
	Expansions  map[string]string `yaml:"expansions"`
	Tags        parserStringSlice `yaml:"tags"`
	Modules     parserStringSlice `yaml:"modules"`
	Disabled    bool              `yaml:"disabled"`
	Push        bool              `yaml:"push"`
	BatchTime   *int              `yaml:"batchtime"`
	Stepback    *bool             `yaml:"stepback"`
	RunOn       parserStringSlice `yaml:"run_on"`
	Tasks       parserBVTasks     `yaml:"tasks"`
}

// helper methods for variant tag evaluations
func (pbv *parserBV) name() string   { return pbv.Name }
func (pbv *parserBV) tags() []string { return pbv.Tags }

// parserBVTask is a helper type storing intermediary variant task configurations.
type parserBVTask struct {
	Name            string             `yaml:"name"`
	Patchable       *bool              `yaml:"patchable"`
	Priority        int64              `yaml:"priority"`
	DependsOn       parserDependencies `yaml:"depends_on"`
	Requires        TaskSelectors      `yaml:"requires"`
	ExecTimeoutSecs int                `yaml:"exec_timeout_secs"`
	Stepback        *bool              `yaml:"stepback"`
	Distros         parserStringSlice  `yaml:"distros"`
	RunOn           parserStringSlice  `yaml:"run_on"` // Alias for "Distros" TODO: deprecate Distros
}

// UnmarshalYAML allows the YAML parser to read both a single selector string or
// a fully defined parserBVTask.
func (pbvt *parserBVTask) UnmarshalYAML(unmarshal func(interface{}) error) error {
	// first, attempt to unmarshal just a selector string
	var onlySelector string
	if err := unmarshal(&onlySelector); err == nil {
		if onlySelector != "" {
			pbvt.Name = onlySelector
			return nil
		}
	}
	// we define a new type so that we can grab the YAML struct tags without the struct methods,
	// preventing infinite recursion on the UnmarshalYAML() method.
	type copyType parserBVTask
	var copy copyType
	if err := unmarshal(&copy); err != nil {
		return err
	}
	if copy.Name == "" {
		return fmt.Errorf("task selector must have a name")
	}
	// logic for aliasing the "run_on" field to "distros"
	if len(copy.RunOn) > 0 {
		if len(copy.Distros) > 0 {
			return fmt.Errorf("cannot use both 'run_on' and 'distros' fields")
		}
		copy.Distros, copy.RunOn = copy.RunOn, nil
	}
	*pbvt = parserBVTask(copy)
	return nil
}

// parserBVTasks is a helper type for handling arrays of parserBVTask.
type parserBVTasks []parserBVTask

// UnmarshalYAML allows the YAML parser to read both a single parserBVTask or
// an array of them into a slice.
func (pbvts *parserBVTasks) UnmarshalYAML(unmarshal func(interface{}) error) error {
	// first, attempt to unmarshal just a selector string
	var single parserBVTask
	if err := unmarshal(&single); err == nil {
		*pbvts = parserBVTasks([]parserBVTask{single})
		return nil
	}
	var slice []parserBVTask
	if err := unmarshal(&slice); err != nil {
		return err
	}
	*pbvts = parserBVTasks(slice)
	return nil
}

// parserStringSlice is YAML helper type that accepts both an array of strings
// or single string value during unmarshalling.
type parserStringSlice []string

// UnmarshalYAML allows the YAML parser to read both a single string or
// an array of them into a slice.
func (pss *parserStringSlice) UnmarshalYAML(unmarshal func(interface{}) error) error {
	var single string
	if err := unmarshal(&single); err == nil {
		*pss = []string{single}
		return nil
	}
	var slice []string
	if err := unmarshal(&slice); err != nil {
		return err
	}
	*pss = slice
	return nil
}

// LoadProjectInto loads the raw data from the config file into project
// and sets the project's identifier field to identifier. Tags are evaluateed.
func LoadProjectInto(data []byte, identifier string, project *Project) error {
	p, errs := projectFromYAML(data) // ignore warnings, for now (TODO)
	if len(errs) > 0 {
		// create a human-readable error list
		buf := bytes.Buffer{}
		for _, e := range errs {
			if len(errs) > 1 {
				buf.WriteString("\n\t") //only newline if we have multiple errs
			}
			buf.WriteString(e.Error())
		}
		return fmt.Errorf("error loading project yaml: %v", buf.String())
	}
	*project = *p
	project.Identifier = identifier
	return nil
}

// projectFromYAML reads and evaluates project YAML, returning a project and warnings and
// errors encountered during parsing or evaluation.
func projectFromYAML(yml []byte) (*Project, []error) {
	pp, errs := createIntermediateProject(yml)
	if len(errs) > 0 {
		return nil, errs
	}
	p, errs := translateProject(pp)
	return p, errs
}

// createIntermediateProject marshals the supplied YAML into our
// intermediate project representation (i.e. before selectors or
// matrix logic has been evaluated).
func createIntermediateProject(yml []byte) (*parserProject, []error) {
	p := &parserProject{}
	err := yaml.Unmarshal(yml, p)
	if err != nil {
		return nil, []error{err}
	}
	return p, nil
}

// translateProject converts our intermediate project representation into
// the Project type that Evergreen actually uses. Errors are added to
// pp.errors and pp.warnings and must be checked separately.
func translateProject(pp *parserProject) (*Project, []error) {
	// Transfer top level fields
	proj := &Project{
		Enabled:         pp.Enabled,
		Stepback:        pp.Stepback,
		DisableCleanup:  pp.DisableCleanup,
		BatchTime:       pp.BatchTime,
		Owner:           pp.Owner,
		Repo:            pp.Repo,
		RemotePath:      pp.RemotePath,
		RepoKind:        pp.RepoKind,
		Branch:          pp.Branch,
		Identifier:      pp.Identifier,
		DisplayName:     pp.DisplayName,
		CommandType:     pp.CommandType,
		Ignore:          pp.Ignore,
		Pre:             pp.Pre,
		Post:            pp.Post,
		Timeout:         pp.Timeout,
		CallbackTimeout: pp.CallbackTimeout,
		Modules:         pp.Modules,
		Functions:       pp.Functions,
		ExecTimeoutSecs: pp.ExecTimeoutSecs,
	}
	tse := NewParserTaskSelectorEvaluator(pp.Tasks)
	vse := NewVariantSelectorEvaluator(pp.BuildVariants)
	var evalErrs, errs []error
	proj.Tasks, errs = evaluateTasks(tse, vse, pp.Tasks)
	evalErrs = append(evalErrs, errs...)
	proj.BuildVariants, errs = evaluateBuildVariants(tse, vse, pp.BuildVariants)
	evalErrs = append(evalErrs, errs...)
	return proj, evalErrs
}

// evaluateTasks translates intermediate tasks into true ProjectTask types,
// evaluating any selectors in the DependsOn or Requires fields.
func evaluateTasks(tse *taskSelectorEvaluator, vse *variantSelectorEvaluator,
	pts []parserTask) ([]ProjectTask, []error) {
	tasks := []ProjectTask{}
	var evalErrs, errs []error
	for _, pt := range pts {
		t := ProjectTask{
			Name:            pt.Name,
			Priority:        pt.Priority,
			ExecTimeoutSecs: pt.ExecTimeoutSecs,
			DisableCleanup:  pt.DisableCleanup,
			Commands:        pt.Commands,
			Tags:            pt.Tags,
			Stepback:        pt.Stepback,
		}
		t.DependsOn, errs = evaluateDependsOn(tse, vse, pt.DependsOn)
		evalErrs = append(evalErrs, errs...)
		t.Requires, errs = evaluateRequires(tse, vse, pt.Requires)
		evalErrs = append(evalErrs, errs...)
		tasks = append(tasks, t)
	}
	return tasks, evalErrs
}

// evaluateBuildsVariants translates intermediate tasks into true BuildVariant types,
// evaluating any selectors in the Tasks fields.
func evaluateBuildVariants(tse *taskSelectorEvaluator, vse *variantSelectorEvaluator,
	pbvs []parserBV) ([]BuildVariant, []error) {
	bvs := []BuildVariant{}
	var evalErrs, errs []error
	for _, pbv := range pbvs {
		bv := BuildVariant{
			DisplayName: pbv.DisplayName,
			Name:        pbv.Name,
			Expansions:  pbv.Expansions,
			Modules:     pbv.Modules,
			Disabled:    pbv.Disabled,
			Push:        pbv.Push,
			BatchTime:   pbv.BatchTime,
			Stepback:    pbv.Stepback,
			RunOn:       pbv.RunOn,
			Tags:        pbv.Tags,
		}
		bv.Tasks, errs = evaluateBVTasks(tse, vse, pbv.Tasks)
		evalErrs = append(evalErrs, errs...)
		bvs = append(bvs, bv)
	}
	return bvs, errs
}

// evaluateBVTasks translates intermediate tasks into true BuildVariantTask types,
// evaluating any selectors referencing tasks, and further evaluating any selectors
// in the DependsOn or Requires fields of those tasks.
func evaluateBVTasks(tse *taskSelectorEvaluator, vse *variantSelectorEvaluator,
	pbvts []parserBVTask) ([]BuildVariantTask, []error) {
	var evalErrs, errs []error
	ts := []BuildVariantTask{}
	tasksByName := map[string]BuildVariantTask{}
	for _, pt := range pbvts {
		names, err := tse.evalSelector(ParseSelector(pt.Name))
		if err != nil {
			evalErrs = append(evalErrs, err)
			continue
		}
		// create new task definitions--duplicates must have the same status requirements
		for _, name := range names {
			// create a new task by copying the task that selected it,
			// so we can preserve the "Variant" and "Status" field.
			t := BuildVariantTask{
				Name:            name,
				Patchable:       pt.Patchable,
				Priority:        pt.Priority,
				ExecTimeoutSecs: pt.ExecTimeoutSecs,
				Stepback:        pt.Stepback,
				Distros:         pt.Distros,
			}
			t.DependsOn, errs = evaluateDependsOn(tse, vse, pt.DependsOn)
			evalErrs = append(evalErrs, errs...)
			t.Requires, errs = evaluateRequires(tse, vse, pt.Requires)
			evalErrs = append(evalErrs, errs...)

			// add the new task if it doesn't already exists (we must avoid conflicting status fields)
			if old, ok := tasksByName[t.Name]; !ok {
				ts = append(ts, t)
				tasksByName[t.Name] = t
			} else {
				// it's already in the new list, so we check to make sure the status definitions match.
				if !reflect.DeepEqual(t, old) {
					evalErrs = append(evalErrs, fmt.Errorf(
						"conflicting definitions of build variant tasks '%v': %v != %v", name, t, old))
					continue
				}
			}
		}
	}
	return ts, evalErrs
}

// evaluateDependsOn expands any selectors in a dependency definition.
func evaluateDependsOn(tse *taskSelectorEvaluator, vse *variantSelectorEvaluator,
	deps []parserDependency) ([]TaskDependency, []error) {
	var evalErrs []error
	var err error
	newDeps := []TaskDependency{}
	newDepsByNameAndVariant := map[TVPair]TaskDependency{}
	for _, d := range deps {
		names := []string{""}
		if d.Name == AllDependencies {
			// * is a special case for dependencies, so don't eval it
			names = []string{AllDependencies}
		} else {
			names, err = tse.evalSelector(ParseSelector(d.Name))
			if err != nil {
				evalErrs = append(evalErrs, err)
				continue
			}
		}
		// we default to handle the empty variant, but expand the list of variants
		// if the variant field is set.
		variants := []string{""}
		if d.Variant != "" {
			variants, err = vse.evalSelector(ParseSelector(d.Variant))
			if err != nil {
				evalErrs = append(evalErrs, err)
				continue
			}
		}
		// create new dependency definitions--duplicates must have the same status requirements
		for _, name := range names {
			for _, variant := range variants {
				// create a newDep by copying the dep that selected it,
				// so we can preserve the "Status" and "PatchOptional" field.
				newDep := TaskDependency{
					Name:          name,
					Variant:       variant,
					Status:        d.Status,
					PatchOptional: d.PatchOptional,
				}
				// add the new dep if it doesn't already exists (we must avoid conflicting status fields)
				if oldDep, ok := newDepsByNameAndVariant[TVPair{newDep.Variant, newDep.Name}]; !ok {
					newDeps = append(newDeps, newDep)
					newDepsByNameAndVariant[TVPair{newDep.Variant, newDep.Name}] = newDep
				} else {
					// it's already in the new list, so we check to make sure the status definitions match.
					if !reflect.DeepEqual(newDep, oldDep) {
						evalErrs = append(evalErrs, fmt.Errorf(
							"conflicting definitions of dependency '%v': %v != %v", name, newDep, oldDep))
						continue
					}
				}
			}
		}
	}
	return newDeps, evalErrs
}

// evaluateRequires expands any selectors in a requirement definition.
func evaluateRequires(tse *taskSelectorEvaluator, vse *variantSelectorEvaluator,
	reqs []TaskSelector) ([]TaskRequirement, []error) {
	var evalErrs []error
	newReqs := []TaskRequirement{}
	newReqsByNameAndVariant := map[TVPair]struct{}{}
	for _, r := range reqs {
		names, err := tse.evalSelector(ParseSelector(r.Name))
		if err != nil {
			evalErrs = append(evalErrs, err)
			continue
		}
		// we default to handle the empty variant, but expand the list of variants
		// if the variant field is set.
		variants := []string{""}
		if r.Variant != "" {
			variants, err = vse.evalSelector(ParseSelector(r.Variant))
			if err != nil {
				evalErrs = append(evalErrs, err)
				continue
			}
		}
		for _, name := range names {
			for _, variant := range variants {
				newReq := TaskRequirement{Name: name, Variant: variant}
				newReq.Name = name
				// add the new req if it doesn't already exists (we must avoid duplicates)
				if _, ok := newReqsByNameAndVariant[TVPair{newReq.Variant, newReq.Name}]; !ok {
					newReqs = append(newReqs, newReq)
					newReqsByNameAndVariant[TVPair{newReq.Variant, newReq.Name}] = struct{}{}
				}
			}
		}
	}
	return newReqs, evalErrs
}

// MATRIX CODE //TODO move me

type matrixAxis struct {
	Id          string      `yaml:"id"`
	DisplayName string      `yaml:"display_name"`
	Values      []axisValue `yaml:"values"`
}

func (ma matrixAxis) find(id string) (axisValue, error) {
	for _, v := range ma.Values {
		if v.Id == id {
			return v, nil
		}
	}
	return axisValue{}, fmt.Errorf("axis '%v' does not contain value '%v'", ma.Id, id)
}

type axisValue struct {
	Id          string            `yaml:"id"`
	DisplayName string            `yaml:"display_name"`
	Variables   map[string]string `yaml:"variables"`
	RunOn       parserStringSlice `yaml:"run_on"`
	Tags        parserStringSlice `yaml:"tags"`
}

// matrixValue represents a "cell" of a matrix
type matrixValue map[string]string

// String returns the matrixValue in simple JSON format
func (mv matrixValue) String() string {
	asJSON, err := json.Marshal(&mv)
	if err != nil {
		return fmt.Sprintf("%#v", mv)
	}
	return string(asJSON)
}

type matrixDefinition map[string]parserStringSlice

// allCells returns every value (cell) within the matrix definition.
// IMPORTANT: this logic assume that all selectors have been evaluated
// and no duplicates exist.
func (mdef matrixDefinition) allCells() []matrixValue {
	// this should never happen, we handle empty defs but just for sanity
	if len(mdef) == 0 {
		return nil
	}
	// You can think of the logic below as traversing an n-dimensional matrix,
	// emulating an n-dimentsional for loop using a set of counters, like an old-school
	// golf counter.  We're doing this iteratively to avoid the overhead and sloppy code
	// required to constantly copy and merge maps that using recursion would require.
	type axisCache struct {
		Id    string
		Vals  []string
		Count int
	}
	axes := []axisCache{}
	for axis, values := range mdef {
		if len(values) == 0 {
			panic(fmt.Sprintf("axis '%v' has empty values list", axis))
		}
		axes = append(axes, axisCache{Id: axis, Vals: values})
	}
	carryOne := false
	cells := []matrixValue{}
	for {
		c := matrixValue{}
		for i := range axes {
			if carryOne {
				carryOne = false
				axes[i].Count = (axes[i].Count + 1) % len(axes[i].Vals)
				if axes[i].Count == 0 { // we overflowed--time to carry the one
					carryOne = true
				}
			}
			// set the current axis/value pair for the new cell
			c[axes[i].Id] = axes[i].Vals[axes[i].Count]
		}
		// if carryOne is still true, that means we've hit all iterations
		if carryOne {
			break
		}
		cells = append(cells, c)
		// add one to the leftmost bucket on the next loop
		carryOne = true
	}
	return cells
}

// TODO outline behavior of this
func (md matrixDefinition) contains(mv matrixValue) bool {
	for k, v := range mv {
		axis, ok := md[k]
		if !ok {
			return false
		}
		if !util.SliceContains(axis, v) {
			return false
		}
	}
	return true
}

type matrixDefinitions []matrixDefinition //TODO

// Contain returns true if any of the definitions contain the given value.
func (mds matrixDefinitions) Contain(v matrixValue) bool {
	for _, m := range mds {
		if m.contains(v) {
			return true
		}
	}
	return false
}

//TODO we'll have to merge this in with parserBV somehow...
type matrix struct {
	Id          string            `yaml:"matrix_name"`
	Spec        matrixDefinition  `yaml:"matrix_spec"`
	Exclude     matrixDefinitions `yaml:"exclude_spec"`
	DisplayName string            `yaml:"display_name"`
	//TODO tasks, rules
}

// helper type for caching the id, tags, and
type matrixDecl struct {
	Id    string
	Value matrixValue
	Tags  []string
}

// helper methods for variant tag evaluations
func (mdecl *matrixDecl) name() string   { return mdecl.Id }
func (mdecl *matrixDecl) tags() []string { return mdecl.Tags }

// TODO axis tag matcher!!
func buildMatrixDeclarations(axes []matrixAxis, matrices []matrix) ([]matrixDecl, []error) {
	var errs []error
	// for each matrix, build out its declarations
	matrixVariantDecls := []matrixDecl{}
	for _, m := range matrices {
		// for each axis value, iterate through possible inputs
		unpruned := m.Spec.allCells()
		pruned := []matrixDecl{}
		for _, cell := range unpruned {
			// create the variant if it isn't excluded
			if !m.Exclude.Contain(cell) {
				decl, err := buildMatrixDeclaration(axes, cell, m.Id)
				if err != nil {
					errs = append(errs,
						fmt.Errorf("%v: error building matrix cell %v: %v", m.Id, cell, err))
					continue
				}
				pruned = append(pruned, decl)
			}
		}
		// safety check to make sure the exclude field is actually working
		if len(m.Exclude) > 0 && len(unpruned) == len(pruned) {
			errs = append(errs, fmt.Errorf("%v: exlude field did not exclude anything"))
		}
		matrixVariantDecls = append(matrixVariantDecls, pruned...)
	}
	return matrixVariantDecls, errs
}

func buildMatrixDeclaration(axes []matrixAxis, mv matrixValue, matrixId string) (matrixDecl, error) {
	decl := matrixDecl{Value: mv}
	tagMap := map[string]struct{}{}
	idBuf := bytes.Buffer{}
	idBuf.WriteString(matrixId)
	idBuf.WriteString("__")
	// we track how many axes we cover, so we know the value is only using real axes
	usedAxes := 0
	// we need to iterate over axis to have a consistent ordering for our names
	for _, a := range axes {
		// skip any axes that aren't used in the variant definitions
		if _, ok := mv[a.Id]; !ok {
			continue
		}
		usedAxes++
		axisVal, err := a.find(mv[a.Id])
		if err != nil {
			return matrixDecl{}, err
		}
		for _, tag := range axisVal.Tags {
			tagMap[tag] = struct{}{}
		}
		idBuf.WriteString(a.Id)
		idBuf.WriteRune('~')
		idBuf.WriteString(axisVal.Id)
		if usedAxes < len(mv) {
			idBuf.WriteRune('_')
		}
	}
	if usedAxes != len(mv) {
		return matrixDecl{}, fmt.Errorf("cell undefined axes", mv)
	}
	decl.Id = idBuf.String()
	for t, _ := range tagMap {
		decl.Tags = append(decl.Tags, t)
	}
	return decl, nil
}
