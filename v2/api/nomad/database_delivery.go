package nomad

import (
	"errors"
	"fmt"
	"maps"
	"sort"
	"strconv"
	"strings"

	nomadapi "github.com/hashicorp/nomad/api"

	"norn/v2/api/model"
)

// Named-database runtime delivery. Connection values never appear in a job
// specification. Norn writes them to the Nomad Variable at
// nomad/jobs/<jobID>, which Nomad's implicit workload-identity policy lets
// only that job's tasks (and a periodic job's children) read. The submitted
// job contains templates that render the value inside the allocation's
// secrets directory: as an environment variable for runtime.env (the
// connection URL itself) and as a 0400 file whose path is placed in
// runtime.fileEnv. A changed rendered value restarts the task.
//
// The variable holds two kinds of items:
//
//   - current items (norn_db_url_<name>) plus the current fence
//     (norn_delivery_catalog_revision): the promoted material, read by
//     function invocations, cron resubmissions and rollbacks;
//   - staged items (norn_rev<R>_db_url_<name>): material resolved against
//     catalog revision R, read only by the job version a deploy submitted
//     for R.
//
// A deploy stages R, submits a job version whose templates read R's items,
// and promotes R to current only after rollout readiness. Staging never
// changes an item a running allocation renders, so preparing candidate
// material cannot cut current allocations over; and no write replaces
// newer material with older.

const (
	databaseSecretsDir   = "norn-databases"
	databaseItemPrefix   = "norn_db_url_"
	componentItemPrefix  = "norn_db_component_"
	targetItemPrefix     = "norn_db_target_"
	deliveryRevisionItem = "norn_delivery_catalog_revision"
)

// DatabaseVariablePath is the Nomad Variable a job's templates read.
func DatabaseVariablePath(jobID string) string { return "nomad/jobs/" + jobID }

// DatabaseItemKey is the current item for one logical database. Logical
// names are [a-z][a-z0-9-]*, so the mapping is injective.
func DatabaseItemKey(name string) string {
	return databaseItemPrefix + strings.ReplaceAll(name, "-", "_")
}

// DatabaseComponentItemKey names one separately delivered connection value.
func DatabaseComponentItemKey(name, component string) string {
	return componentItemPrefix + component + "_" + strings.ReplaceAll(name, "-", "_")
}

// DatabaseTargetItemKey holds the non-secret target identity (JSON) the
// connection value belongs to, so every reader of a revision can revalidate
// which target it would write.
func DatabaseTargetItemKey(name string) string {
	return targetItemPrefix + strings.ReplaceAll(name, "-", "_")
}

// DatabaseRevisionItemKey is the staged connection item for a logical
// database at a catalog revision. Its prefix never matches a current item.
func DatabaseRevisionItemKey(name string, revision int64) string {
	return revisionPrefix(revision) + strings.TrimPrefix(DatabaseItemKey(name), "norn_")
}

// DatabaseRevisionTargetKey is the staged target identity item.
func DatabaseRevisionTargetKey(name string, revision int64) string {
	return revisionPrefix(revision) + strings.TrimPrefix(DatabaseTargetItemKey(name), "norn_")
}

func revisionPrefix(revision int64) string { return fmt.Sprintf("norn_rev%d_", revision) }

// stagedKey maps a current item key to its revision-qualified key.
func stagedKey(key string, revision int64) string {
	return revisionPrefix(revision) + strings.TrimPrefix(key, "norn_")
}

// DatabaseVariableItems converts logical name -> connection URL into
// current-keyed items.
func DatabaseVariableItems(urls map[string]string) map[string]string {
	items := make(map[string]string, len(urls))
	for name, value := range urls {
		items[DatabaseItemKey(name)] = value
	}
	return items
}

// DatabaseDeliveryJobIDs are the jobs whose variables a deploy must write:
// the service job (also the source copied for function invocations) and
// every scheduled process's periodic job.
func DatabaseDeliveryJobIDs(spec *model.InfraSpec) []string {
	ids := []string{spec.App}
	for _, name := range sortedProcessNames(spec) {
		if spec.Processes[name].Schedule != "" {
			ids = append(ids, spec.App+"-"+name)
		}
	}
	return ids
}

// JobIDs are every Nomad job ID Norn registers for an app's processes: the
// service job and one periodic job per scheduled process. Function
// invocations use unique "<app>-<process>-<ms>" IDs.
func JobIDs(spec *model.InfraSpec) []string {
	ids := []string{spec.App}
	for _, name := range sortedProcessNames(spec) {
		if spec.Processes[name].Schedule != "" {
			ids = append(ids, spec.App+"-"+name)
		}
	}
	return ids
}

// JobRegistered reports whether a job ID is registered in a region (a
// stopped-but-registered job counts: it can be restarted).
func (c *Client) JobRegistered(region, jobID string) (bool, error) {
	_, _, err := c.api.Jobs().Info(jobID, &nomadapi.QueryOptions{Region: region})
	if err == nil {
		return true, nil
	}
	if strings.Contains(err.Error(), "404") {
		return false, nil
	}
	return false, fmt.Errorf("inspect job %s: %w", jobID, err)
}

// HasRuntimeDatabases reports whether any declared database is delivered
// to processes.
func HasRuntimeDatabases(spec *model.InfraSpec) bool {
	for _, requirement := range spec.Databases {
		if requirement.Runtime != nil {
			return true
		}
	}
	return false
}

// addDatabaseTemplates adds the private delivery templates to a task. It is
// a pure function of the spec and revision: no connection value is
// involved. Templates always read one explicit, immutable staged revision;
// nothing ever reads the mutable current items. Revision 0 names items that
// are never written (revisions start at 1), so a caller that fails to supply
// a revision gets a task that fails closed at render (error_on_missing_key)
// rather than a silently mutable fallback.
func addDatabaseTemplates(spec *model.InfraSpec, jobID string, revision int64, task *nomadapi.Task) {
	if !HasRuntimeDatabases(spec) {
		return
	}
	path := DatabaseVariablePath(jobID)
	var envLines []string
	for _, requirement := range spec.Databases {
		if requirement.Runtime == nil {
			continue
		}
		key := DatabaseRevisionItemKey(requirement.Name, revision)
		if requirement.Runtime.Env != "" {
			envLines = append(envLines, fmt.Sprintf("%s={{ .%s }}", requirement.Runtime.Env, key))
		}
		if requirement.Runtime.FileEnv != "" {
			destination := "secrets/" + databaseSecretsDir + "/" + requirement.Name + ".url"
			task.Templates = append(task.Templates, databaseTemplate(fmt.Sprintf("{{ with nomadVar %q }}{{ .%s }}{{ end }}", path, key), destination, false))
			if task.Env == nil {
				task.Env = map[string]string{}
			}
			task.Env[requirement.Runtime.FileEnv] = "${NOMAD_SECRETS_DIR}/" + databaseSecretsDir + "/" + requirement.Name + ".url"
		}
		if components := requirement.Runtime.Components; components != nil {
			for _, field := range []struct{ component, env string }{
				{"host", components.Host}, {"user", components.User}, {"password", components.Password}, {"name", components.Name},
			} {
				// Component passwords are raw values, unlike percent-encoded
				// URLs. Nomad's env parser needs JSON quoting for characters
				// such as spaces, quotes and backslashes.
				envLines = append(envLines, fmt.Sprintf("%s={{ .%s | toJSON }}", field.env, stagedKey(DatabaseComponentItemKey(requirement.Name, field.component), revision)))
			}
		}
	}
	if len(envLines) > 0 {
		data := fmt.Sprintf("{{ with nomadVar %q }}\n%s\n{{ end }}\n", path, strings.Join(envLines, "\n"))
		task.Templates = append(task.Templates, databaseTemplate(data, "secrets/"+databaseSecretsDir+".env", true))
	}
}

func databaseTemplate(data, destination string, env bool) *nomadapi.Template {
	return &nomadapi.Template{
		EmbeddedTmpl:  strPtr(data),
		DestPath:      strPtr(destination),
		Perms:         strPtr("0400"),
		ChangeMode:    strPtr("restart"),
		Envvars:       boolPtr(env),
		ErrMissingKey: boolPtr(true),
	}
}

// ErrDatabaseVariableConflict reports that a delivery variable holds other
// values (or changed concurrently) and was not replaced.
var ErrDatabaseVariableConflict = errors.New("database delivery variable holds other values")

// ErrStaleDatabaseDelivery reports a delivery resolved against an older
// catalog revision than the one already promoted for the job.
var ErrStaleDatabaseDelivery = errors.New("database delivery is older than the promoted delivery")

// PutDatabaseVariable creates a job's delivery variable, or confirms an
// identical one. It never replaces different values: reading the latest
// index confers no authority to repoint a running job. Errors never include
// item values.
func (c *Client) PutDatabaseVariable(region, jobID string, items map[string]string) error {
	current, err := c.peekDatabaseVariable(region, jobID, items)
	if err != nil {
		return err
	}
	if current != nil {
		if maps.Equal(map[string]string(current.Items), items) {
			return nil
		}
		return fmt.Errorf("%w for %s", ErrDatabaseVariableConflict, jobID)
	}
	return c.writeDatabaseVariable(region, &nomadapi.Variable{Path: DatabaseVariablePath(jobID), Items: maps.Clone(items)}, items)
}

// DeliverDatabaseVariable stages current-keyed items resolved against
// catalogRevision under that revision's staged keys. If the job has no
// variable yet (nothing can be rendering it), the values are also
// published as current. Otherwise current items are untouched until
// PromoteDatabaseVariable. A revision older than the promoted one is stale;
// restaging a revision must carry identical values. Writes are
// check-and-set against the version compared, so a concurrent writer is a
// conflict, never an overwrite; an unchanged variable is not rewritten.
func (c *Client) DeliverDatabaseVariable(region, jobID string, items map[string]string, catalogRevision int64) error {
	if catalogRevision < 1 {
		return fmt.Errorf("database delivery for %s has no catalog revision", jobID)
	}
	staged := map[string]string{}
	for key, value := range items {
		if !strings.HasPrefix(key, databaseItemPrefix) && !strings.HasPrefix(key, componentItemPrefix) && !strings.HasPrefix(key, targetItemPrefix) {
			return fmt.Errorf("database delivery for %s has an unexpected item", jobID)
		}
		staged[stagedKey(key, catalogRevision)] = value
	}
	current, err := c.peekDatabaseVariable(region, jobID, items)
	if err != nil {
		return err
	}
	path := DatabaseVariablePath(jobID)
	if current == nil {
		initial := maps.Clone(items)
		maps.Copy(initial, staged)
		initial[deliveryRevisionItem] = strconv.FormatInt(catalogRevision, 10)
		return c.writeDatabaseVariable(region, &nomadapi.Variable{Path: path, Items: initial}, items)
	}
	promoted, err := deliveryFence(current, jobID)
	if err != nil {
		return err
	}
	if promoted > catalogRevision {
		return fmt.Errorf("%w for %s (promoted revision %d, this delivery %d)", ErrStaleDatabaseDelivery, jobID, promoted, catalogRevision)
	}
	next := maps.Clone(map[string]string(current.Items))
	changed := false
	for key, value := range staged {
		if existing, ok := next[key]; ok && existing != value {
			return fmt.Errorf("%w for %s: revision %d was already staged with other values; activate a new catalog revision to rotate", ErrDatabaseVariableConflict, jobID, catalogRevision)
		} else if !ok {
			next[key] = value
			changed = true
		}
	}
	if !changed {
		return nil
	}
	return c.writeDatabaseVariable(region, &nomadapi.Variable{Path: path, Items: next, ModifyIndex: current.ModifyIndex}, items)
}

// PromoteDatabaseVariable cuts the current items over to a staged revision
// after rollout readiness, and prunes staged revisions older than the
// previously promoted one (which is kept for allocations still draining).
func (c *Client) PromoteDatabaseVariable(region, jobID string, catalogRevision int64) error {
	current, _, err := c.api.Variables().Peek(DatabaseVariablePath(jobID), &nomadapi.QueryOptions{Region: region})
	if err != nil {
		return fmt.Errorf("read database delivery variable for %s failed", jobID)
	}
	if current == nil {
		return fmt.Errorf("%w for %s: nothing was staged", ErrDatabaseVariableConflict, jobID)
	}
	promoted, err := deliveryFence(current, jobID)
	if err != nil {
		return err
	}
	switch {
	case promoted > catalogRevision:
		return fmt.Errorf("%w for %s (promoted revision %d, this promotion %d)", ErrStaleDatabaseDelivery, jobID, promoted, catalogRevision)
	case promoted == catalogRevision:
		return nil
	}
	prefix := revisionPrefix(catalogRevision)
	next := map[string]string{deliveryRevisionItem: strconv.FormatInt(catalogRevision, 10)}
	stagedCount := 0
	for key, value := range current.Items {
		revision, isStaged := stagedRevision(key)
		switch {
		case isStaged && revision == catalogRevision:
			next["norn_"+strings.TrimPrefix(key, prefix)] = value
			next[key] = value
			stagedCount++
		case isStaged:
			if revision >= promoted {
				next[key] = value
			}
		case strings.HasPrefix(key, databaseItemPrefix), strings.HasPrefix(key, componentItemPrefix), strings.HasPrefix(key, targetItemPrefix), key == deliveryRevisionItem:
			// replaced by the promoted revision
		default:
			next[key] = value
		}
	}
	if stagedCount == 0 {
		return fmt.Errorf("%w for %s: revision %d was not staged", ErrDatabaseVariableConflict, jobID, catalogRevision)
	}
	return c.writeDatabaseVariable(region, &nomadapi.Variable{Path: DatabaseVariablePath(jobID), Items: next, ModifyIndex: current.ModifyIndex}, current.Items)
}

// PromotedDatabaseRevision reports the catalog revision whose material is
// current for a job (0 when nothing was delivered).
func (c *Client) PromotedDatabaseRevision(region, jobID string) (int64, error) {
	current, _, err := c.api.Variables().Peek(DatabaseVariablePath(jobID), &nomadapi.QueryOptions{Region: region})
	if err != nil {
		return 0, fmt.Errorf("read database delivery variable for %s failed", jobID)
	}
	if current == nil {
		return 0, nil
	}
	return deliveryFence(current, jobID)
}

func deliveryFence(variable *nomadapi.Variable, jobID string) (int64, error) {
	raw, ok := variable.Items[deliveryRevisionItem]
	if !ok {
		return 0, nil
	}
	revision, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || revision < 1 {
		return 0, fmt.Errorf("%w for %s: unreadable delivery fence", ErrDatabaseVariableConflict, jobID)
	}
	return revision, nil
}

func stagedRevision(key string) (int64, bool) {
	if !strings.HasPrefix(key, "norn_rev") {
		return 0, false
	}
	rest := strings.TrimPrefix(key, "norn_rev")
	digits := rest[:strings.IndexByte(rest+"_", '_')]
	revision, err := strconv.ParseInt(digits, 10, 64)
	return revision, err == nil && revision > 0
}

// DatabaseRevision is one staged revision's material for a job: connection
// values and target identities by logical database name.
type DatabaseRevision struct {
	Revision   int64
	Promoted   int64
	URLs       map[string]string
	Components map[string]string
	Targets    map[string]string
}

// ReadDatabaseRevision returns a revision's staged items for a job. Readers
// (rollback, cron resubmission, function invocations) revalidate its target
// identities before referencing it.
func (c *Client) ReadDatabaseRevision(region, jobID string, revision int64) (DatabaseRevision, error) {
	current, _, err := c.api.Variables().Peek(DatabaseVariablePath(jobID), &nomadapi.QueryOptions{Region: region})
	if err != nil {
		return DatabaseRevision{}, fmt.Errorf("read database delivery variable for %s failed", jobID)
	}
	if current == nil {
		return DatabaseRevision{}, fmt.Errorf("%w for %s: nothing was delivered", ErrDatabaseVariableConflict, jobID)
	}
	promoted, err := deliveryFence(current, jobID)
	if err != nil {
		return DatabaseRevision{}, err
	}
	out := DatabaseRevision{Revision: revision, Promoted: promoted, URLs: map[string]string{}, Components: map[string]string{}, Targets: map[string]string{}}
	prefix := revisionPrefix(revision)
	for key, value := range current.Items {
		if staged, ok := stagedRevision(key); !ok || staged != revision {
			continue
		}
		rest := strings.TrimPrefix(key, prefix)
		switch {
		case strings.HasPrefix(rest, "db_url_"):
			out.URLs[strings.TrimPrefix(rest, "db_url_")] = value
		case strings.HasPrefix(rest, "db_target_"):
			out.Targets[strings.TrimPrefix(rest, "db_target_")] = value
		case strings.HasPrefix(rest, "db_component_"):
			out.Components["norn_"+rest] = value
		}
	}
	return out, nil
}

func (c *Client) peekDatabaseVariable(region, jobID string, items map[string]string) (*nomadapi.Variable, error) {
	current, _, err := c.api.Variables().Peek(DatabaseVariablePath(jobID), &nomadapi.QueryOptions{Region: region})
	if err != nil {
		return nil, fmt.Errorf("read database delivery variable for %s: %s", jobID, redactItems(err.Error(), items))
	}
	return current, nil
}

// writeDatabaseVariable creates (index 0) or check-and-set updates.
func (c *Client) writeDatabaseVariable(region string, variable *nomadapi.Variable, items map[string]string) error {
	jobID := strings.TrimPrefix(variable.Path, "nomad/jobs/")
	write := &nomadapi.WriteOptions{Region: region}
	var err error
	if variable.ModifyIndex == 0 {
		_, _, err = c.api.Variables().CheckedCreate(variable, write)
	} else {
		_, _, err = c.api.Variables().CheckedUpdate(variable, write)
	}
	var conflict nomadapi.ErrCASConflict
	if errors.As(err, &conflict) {
		return fmt.Errorf("%w for %s: changed concurrently", ErrDatabaseVariableConflict, jobID)
	}
	if err != nil {
		return fmt.Errorf("write database delivery variable for %s: %s", jobID, redactItems(err.Error(), items))
	}
	return nil
}

// CopyDatabaseVariable gives a one-shot job (a function invocation) the
// current delivery values of its app's service job.
// CopyDatabaseVariable gives a one-shot job (a function invocation) its own
// copy of exactly one staged revision, which the caller has read and
// revalidated. The copy is created once (create-only) and never mutated; the
// invocation's templates reference that revision. It returns the items
// written, for exact-ownership cleanup.
func (c *Client) CopyDatabaseVariable(region, toJobID string, revision DatabaseRevision) (map[string]string, error) {
	if revision.Revision < 1 || len(revision.URLs)+len(revision.Components) == 0 {
		return nil, fmt.Errorf("%w for %s: no staged revision to copy", ErrDatabaseVariableConflict, toJobID)
	}
	items := map[string]string{deliveryRevisionItem: strconv.FormatInt(revision.Revision, 10)}
	for name, value := range revision.URLs {
		items[stagedKey(databaseItemPrefix+name, revision.Revision)] = value
	}
	for key, value := range revision.Components {
		if !strings.HasPrefix(key, componentItemPrefix) {
			return nil, fmt.Errorf("%w for %s: invalid database component", ErrDatabaseVariableConflict, toJobID)
		}
		items[stagedKey(key, revision.Revision)] = value
	}
	for name, value := range revision.Targets {
		items[stagedKey(targetItemPrefix+name, revision.Revision)] = value
	}
	if err := c.PutDatabaseVariable(region, toJobID, items); err != nil {
		return nil, err
	}
	return items, nil
}

// DeleteDatabaseVariable removes a one-shot job's delivery variable only if
// it still holds exactly the material this invocation wrote (checked delete
// against the version compared).
func (c *Client) DeleteDatabaseVariable(region, jobID string, owned map[string]string) error {
	current, _, err := c.api.Variables().Peek(DatabaseVariablePath(jobID), &nomadapi.QueryOptions{Region: region})
	if err != nil {
		return fmt.Errorf("read database delivery variable for %s failed", jobID)
	}
	if current == nil {
		return nil
	}
	if !maps.Equal(map[string]string(current.Items), owned) {
		return fmt.Errorf("%w for %s: not the material this invocation owns", ErrDatabaseVariableConflict, jobID)
	}
	if _, err := c.api.Variables().CheckedDelete(DatabaseVariablePath(jobID), current.ModifyIndex, &nomadapi.WriteOptions{Region: region}); err != nil {
		return fmt.Errorf("delete database delivery variable for %s failed", jobID)
	}
	return nil
}

func sortedProcessNames(spec *model.InfraSpec) []string {
	names := make([]string, 0, len(spec.Processes))
	for name := range spec.Processes {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func redactItems(text string, items map[string]string) string {
	for _, value := range items {
		if value != "" {
			text = strings.ReplaceAll(text, value, "[redacted]")
		}
	}
	return text
}
