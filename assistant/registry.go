// Package assistant is the in-console assistant: a Claude-driven agent that
// answers questions about a company's AccessLink data and carries out a small
// set of actions for the signed-in operator.
//
// ---------------------------------------------------------------------------
// THE ONE RULE
// ---------------------------------------------------------------------------
//
// The model has no access of its own. Every tool it can call is a named,
// typed operation that this package turns into a request through the
// console's own HTTP routes, carrying the operator's own session cookie and
// CSRF token (dispatch.go). Authentication, role gates, site grants, tenant
// scoping, validation and the audit trail are therefore the existing ones,
// unchanged, and a tool can do nothing the operator could not do from the
// screens. There is no raw database access, no raw HTTP, no "call endpoint X"
// tool, and no tool returns a raw API body: every result passes through a
// projection that names the fields the model may see.
//
// The registry is MCP-shaped -- name, description, input schema, the
// readOnly/destructive/idempotent annotations -- so it can be exposed over a
// network MCP transport later without changing a tool. Phase 1 exposes it to
// exactly one client: the loop in this package.
package assistant

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"access-terminal-cloud-api/middleware"
	"access-terminal-cloud-api/models"
)

// Param describes one argument of a tool. Deliberately a small subset of JSON
// Schema: strings, integers and booleans, with enums and bounds. It is what
// the model is shown and what arguments are validated against before a tool
// runs -- the model's output is never trusted to match its schema.
type Param struct {
	Name        string
	Type        string // "string" | "integer" | "boolean"
	Description string
	Required    bool
	Enum        []string
	Min, Max    *int
	MaxLen      int
	// Format is a hint carried into the schema ("date" for YYYY-MM-DD).
	Format string
	// Identifier marks a value a tool places in a request PATH: an ID number,
	// a serial, a site or rule id. It is refused if it holds anything that
	// would change which route the path reaches -- a slash above all, which
	// the router decodes even when escaped -- so a tool can only ever reach
	// the route it was written for.
	Identifier bool
}

// Args are validated tool arguments: strings, ints and bools by name.
type Args map[string]any

// String reads a string argument ("" when absent).
func (a Args) String(name string) string {
	v, _ := a[name].(string)
	return v
}

// Int reads an integer argument (0 when absent).
func (a Args) Int(name string) int {
	v, _ := a[name].(int)
	return v
}

// Bool reads a boolean argument (false when absent).
func (a Args) Bool(name string) bool {
	v, _ := a[name].(bool)
	return v
}

// Has reports whether the argument was supplied.
func (a Args) Has(name string) bool {
	_, ok := a[name]
	return ok
}

// ConfirmationPlan is what a consequential tool asks the operator before it
// runs: the wording, and the phrase they must type if the equivalent screen
// demands one.
type ConfirmationPlan struct {
	Consequence    models.AssistantConsequence
	PhraseRequired string
}

// Outcome is the result of running a tool.
type Outcome struct {
	// Result is what the model is told, already projected. JSON-encodable.
	Result any
	// IsError marks a result the model should treat as a failure.
	IsError bool
	// Status is the stored assistant_tool_calls.status value.
	Status string
	// HTTPStatus is the status of the (last) internal request.
	HTTPStatus int
	// Route is the method and path of the (last) internal request.
	Route string
	// RequestID is the id the (last) internal request carried.
	RequestID string
	// Handoff, when set, points the console at the screen that continues.
	Handoff *models.AssistantHandoff
}

// Domains name what a write changes, in the console's own vocabulary: each
// one is a query-key root in web/src/data/keys.ts, and the console drops
// that cache when a tool naming it has run. A write tool must declare at
// least one; a read tool declares none. The registry refuses anything else,
// so a new write cannot leave a screen stale by omission.
const (
	DomainPeople           = "people"
	DomainPermissions      = "permissions"
	DomainSchedules        = "schedules"
	DomainOnboarding       = "onboarding"
	DomainAudit            = "audit"
	DomainTerminals        = "terminals"
	DomainSites            = "sites"
	DomainPendingTerminals = "pending_terminals"
	DomainEvents           = "events"
)

var knownDomains = map[string]bool{
	DomainPeople: true, DomainPermissions: true, DomainSchedules: true, DomainOnboarding: true,
	DomainAudit: true, DomainTerminals: true, DomainSites: true, DomainPendingTerminals: true,
	DomainEvents: true,
}

// Tool is one operation the model may call.
type Tool struct {
	Name        string
	Description string
	Params      []Param

	// MCP annotations.
	ReadOnly    bool
	Destructive bool
	Idempotent  bool

	// Domains is what a successful run changes (see the Domain constants).
	// Required for a write, refused on a read.
	Domains []string

	// MinRole is the lowest role shown this tool. VISIBILITY ONLY: the route
	// the tool dispatches to enforces the real gate, and a tool run by a role
	// below the route's minimum gets the route's 403 like any other caller.
	MinRole string

	// Confirm, when set, is asked before the tool runs. It may read through
	// the router to describe what will happen (a person's name, a terminal's
	// name) and returns the wording the operator approves. Returning nil means
	// no confirmation is needed for these arguments.
	Confirm func(t *Turn, args Args) (*ConfirmationPlan, error)

	// Run performs the tool. It reaches the API only through t.Call.
	Run func(t *Turn, args Args) Outcome

	// MaxDuration bounds one run. Zero means the default.
	MaxDuration time.Duration

	// ParallelSafe tools may run alongside each other within one model round.
	ParallelSafe bool
}

// Definition is the tool as the model sees it. Domains is carried for the
// console, not the model: model.go copies name, description and schema only.
type Definition struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"input_schema"`
	ReadOnly    bool           `json:"read_only"`
	Domains     []string       `json:"domains,omitempty"`
}

// Registry holds the tools, in a fixed order.
type Registry struct {
	tools map[string]*Tool
	order []string
}

// NewRegistry builds an empty registry.
func NewRegistry() *Registry {
	return &Registry{tools: map[string]*Tool{}}
}

// Register adds a tool. It panics on a duplicate or a malformed definition,
// because a registry is assembled once at startup and a bad tool must not
// reach a model.
func (r *Registry) Register(t *Tool) {
	if t.Name == "" || strings.ToLower(t.Name) != t.Name || len(t.Name) > 64 {
		panic(fmt.Sprintf("assistant: tool name %q is not a lower-case identifier of at most 64 characters", t.Name))
	}
	if _, dup := r.tools[t.Name]; dup {
		panic(fmt.Sprintf("assistant: tool %q registered twice", t.Name))
	}
	if t.Run == nil {
		panic(fmt.Sprintf("assistant: tool %q has no Run", t.Name))
	}
	if t.MinRole == "" {
		panic(fmt.Sprintf("assistant: tool %q declares no MinRole", t.Name))
	}
	if !middleware.RoleAtLeast(t.MinRole, models.RoleViewer) {
		panic(fmt.Sprintf("assistant: tool %q has unknown MinRole %q", t.Name, t.MinRole))
	}
	if t.ReadOnly && (t.Destructive || t.Confirm != nil) {
		panic(fmt.Sprintf("assistant: tool %q is read-only but destructive or confirmed", t.Name))
	}
	if t.ReadOnly && len(t.Domains) > 0 {
		panic(fmt.Sprintf("assistant: tool %q is read-only but declares domains", t.Name))
	}
	if !t.ReadOnly && len(t.Domains) == 0 {
		panic(fmt.Sprintf("assistant: tool %q writes but declares no domains", t.Name))
	}
	for _, d := range t.Domains {
		if !knownDomains[d] {
			panic(fmt.Sprintf("assistant: tool %q declares unknown domain %q", t.Name, d))
		}
	}
	for _, p := range t.Params {
		switch p.Type {
		case "string", "integer", "boolean":
		default:
			panic(fmt.Sprintf("assistant: tool %q param %q has unsupported type %q", t.Name, p.Name, p.Type))
		}
	}
	r.tools[t.Name] = t
	r.order = append(r.order, t.Name)
	sort.Strings(r.order)
}

// Get finds a tool by name.
func (r *Registry) Get(name string) (*Tool, bool) {
	t, ok := r.tools[name]
	return t, ok
}

// ForRole lists the tools a role may see, in name order.
//
// DETERMINISTIC ORDER ON PURPOSE: the tool list is the first thing in the
// model's prompt, and a list that reshuffled between requests would defeat
// prompt caching on every turn.
func (r *Registry) ForRole(role string) []Definition {
	out := []Definition{}
	for _, name := range r.order {
		t := r.tools[name]
		if !middleware.RoleAtLeast(role, t.MinRole) {
			continue
		}
		out = append(out, Definition{
			Name:        t.Name,
			Description: t.Description,
			InputSchema: t.schema(),
			ReadOnly:    t.ReadOnly,
			Domains:     append([]string(nil), t.Domains...),
		})
	}
	return out
}

// Names lists the visible tools' names, for the capabilities response.
func (r *Registry) Names(role string) []string {
	defs := r.ForRole(role)
	names := make([]string, 0, len(defs))
	for _, d := range defs {
		names = append(names, d.Name)
	}
	return names
}

// Effects maps each visible write tool to the domains it changes, for the
// capabilities response: the console's fallback when a tool.result event
// reaches it without domains (a replay, say).
func (r *Registry) Effects(role string) map[string][]string {
	out := map[string][]string{}
	for _, d := range r.ForRole(role) {
		if len(d.Domains) > 0 {
			out[d.Name] = d.Domains
		}
	}
	return out
}

// Domains reports what a tool changes, or nil for a read or an unknown name.
func (r *Registry) Domains(name string) []string {
	t, ok := r.tools[name]
	if !ok || t.ReadOnly {
		return nil
	}
	return append([]string(nil), t.Domains...)
}

func (t *Tool) schema() map[string]any {
	properties := map[string]any{}
	required := []string{}
	for _, p := range t.Params {
		prop := map[string]any{"type": p.Type, "description": p.Description}
		if len(p.Enum) > 0 {
			prop["enum"] = p.Enum
		}
		if p.Min != nil {
			prop["minimum"] = *p.Min
		}
		if p.Max != nil {
			prop["maximum"] = *p.Max
		}
		if p.MaxLen > 0 {
			prop["maxLength"] = p.MaxLen
		}
		if p.Format != "" {
			prop["format"] = p.Format
		}
		properties[p.Name] = prop
		if p.Required {
			required = append(required, p.Name)
		}
	}
	schema := map[string]any{
		"type":                 "object",
		"properties":           properties,
		"additionalProperties": false,
	}
	if len(required) > 0 {
		schema["required"] = required
	}
	return schema
}

// ValidateArgs checks the model's arguments against the tool's parameters and
// returns them typed. Unknown arguments are refused rather than dropped: a
// tool that silently ignored "site_id" when the model misspelt it would run
// with a wider scope than the model asked for.
func (t *Tool) ValidateArgs(raw json.RawMessage) (Args, error) {
	var in map[string]any
	if len(raw) == 0 {
		in = map[string]any{}
	} else if err := json.Unmarshal(raw, &in); err != nil {
		return nil, fmt.Errorf("arguments are not a JSON object")
	}
	known := map[string]Param{}
	for _, p := range t.Params {
		known[p.Name] = p
	}
	for name := range in {
		if _, ok := known[name]; !ok {
			return nil, fmt.Errorf("unknown argument %q", name)
		}
	}
	out := Args{}
	for _, p := range t.Params {
		v, present := in[p.Name]
		if !present || v == nil {
			if p.Required {
				return nil, fmt.Errorf("%s is required", p.Name)
			}
			continue
		}
		switch p.Type {
		case "string":
			s, ok := v.(string)
			if !ok {
				return nil, fmt.Errorf("%s must be a string", p.Name)
			}
			s = strings.TrimSpace(s)
			if p.Required && s == "" {
				return nil, fmt.Errorf("%s is required", p.Name)
			}
			if s == "" {
				continue
			}
			if p.MaxLen > 0 && len(s) > p.MaxLen {
				return nil, fmt.Errorf("%s is longer than %d characters", p.Name, p.MaxLen)
			}
			if len(p.Enum) > 0 && !contains(p.Enum, s) {
				return nil, fmt.Errorf("%s must be one of %s", p.Name, strings.Join(p.Enum, ", "))
			}
			if p.Format == "date" && !looksLikeDate(s) {
				return nil, fmt.Errorf("%s must be a date in YYYY-MM-DD form", p.Name)
			}
			if p.Identifier && !safeIdentifier(s) {
				return nil, fmt.Errorf("%s must not contain spaces, slashes or punctuation such as ? # %%", p.Name)
			}
			out[p.Name] = s
		case "integer":
			f, ok := v.(float64)
			if !ok || f != float64(int(f)) {
				return nil, fmt.Errorf("%s must be a whole number", p.Name)
			}
			n := int(f)
			if p.Min != nil && n < *p.Min {
				return nil, fmt.Errorf("%s must be at least %d", p.Name, *p.Min)
			}
			if p.Max != nil && n > *p.Max {
				return nil, fmt.Errorf("%s must be at most %d", p.Name, *p.Max)
			}
			out[p.Name] = n
		case "boolean":
			b, ok := v.(bool)
			if !ok {
				return nil, fmt.Errorf("%s must be true or false", p.Name)
			}
			out[p.Name] = b
		}
	}
	return out, nil
}

func contains(list []string, s string) bool {
	for _, item := range list {
		if item == s {
			return true
		}
	}
	return false
}

// safeIdentifier accepts printable ASCII with none of the characters that
// carry meaning in a URL path or query. The set matches what the console's
// own external-id rule allows (printable ASCII, no spaces) less the path and
// query delimiters; serials and public ids are narrower still.
func safeIdentifier(s string) bool {
	for i := 0; i < len(s); i++ {
		b := s[i]
		if b < 0x21 || b > 0x7E {
			return false
		}
		switch b {
		case '/', '\\', '?', '#', '%':
			return false
		}
	}
	return true
}

func looksLikeDate(s string) bool {
	_, err := time.Parse("2006-01-02", s)
	return err == nil
}

// intPtr is a small helper for Param bounds.
func intPtr(n int) *int { return &n }
