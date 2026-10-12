// SPDX-License-Identifier: AGPL-3.0-only

package nativecontract

import (
	"bytes"
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"io"
	"math"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

//go:embed contract.json
var contractJSON []byte

// ValidationError contains only an allowlisted reason and a schema field path;
// values, prompts and authority never enter errors or logs.
type ValidationError struct {
	Reason Reason
	Path   string
}

func (e *ValidationError) Error() string       { return string(e.Reason) + " at " + e.Path }
func invalid(reason Reason, path string) error { return &ValidationError{reason, path} }

type wireSchema struct {
	Ref        string                 `json:"$ref"`
	Type       string                 `json:"type"`
	Format     string                 `json:"format"`
	Properties map[string]*wireSchema `json:"properties"`
	Required   []string               `json:"required"`
	Enum       []string               `json:"enum"`
	AnyOf      []*wireSchema          `json:"anyOf"`
	OneOf      []*wireSchema          `json:"oneOf"`
	Items      *wireSchema            `json:"items"`
	Pattern    string                 `json:"pattern"`
	MinLength  *int                   `json:"minLength"`
	MaxLength  *int                   `json:"maxLength"`
	MaxItems   *int                   `json:"maxItems"`
	Minimum    *float64               `json:"minimum"`
	Maximum    *float64               `json:"maximum"`
	pattern    *regexp.Regexp
}

// Validator is an immutable, bounded validator for precisely the frozen C0
// schema vocabulary. Shape validation does not grant permission, native writer
// ownership, consent, step-up, final-drain authority or provider qualification.
type Validator struct{ schemas map[string]*wireSchema }

func NewValidator() (*Validator, error) {
	var spec struct {
		Schemas map[string]*wireSchema `json:"schemas"`
	}
	if err := json.Unmarshal(contractJSON, &spec); err != nil {
		return nil, err
	}
	var compile func(*wireSchema) error
	compile = func(s *wireSchema) error {
		if s == nil {
			return nil
		}
		if s.Pattern != "" {
			var err error
			s.pattern, err = regexp.Compile(s.Pattern)
			if err != nil {
				return err
			}
		}
		for _, p := range s.Properties {
			if err := compile(p); err != nil {
				return err
			}
		}
		for _, list := range [][]*wireSchema{s.OneOf, s.AnyOf} {
			for _, p := range list {
				if err := compile(p); err != nil {
					return err
				}
			}
		}
		return compile(s.Items)
	}
	for _, schema := range spec.Schemas {
		if err := compile(schema); err != nil {
			return nil, err
		}
	}
	return &Validator{spec.Schemas}, nil
}

func wireLimit(schema string) int {
	switch schema {
	case "NativeStreamEvent", "NativeExpertFrame", "NativeTerminalFrame":
		return ExportedFrameBytes
	case "NativeReceipt":
		return ReceiptBytes
	case "NativePublish":
		return PublishBatchBytes
	case "NativeReceiptPage", "NativeEventPage", "NativePendingCommandPage":
		return ReceiptPageBytes
	case "NativeInterrupt", "NativeStop", "NativeApprovalDecision", "NativeEndControl", "NativeResize", "NativeTakeIntent", "NativeReturnIntent":
		return ControlBytes
	default:
		return RequestBodyBytes
	}
}

// decodeBounded rejects duplicates, trailing values, invalid UTF-8, oversize
// documents and excessive nesting before constructing the decoded tree.
func decodeBounded(data []byte, limit int) (any, error) {
	if len(data) > limit {
		return nil, invalid(ReasonBufferFull, "$")
	}
	if !utf8.Valid(data) {
		return nil, invalid(ReasonInvalidInput, "$")
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.UseNumber()
	var read func(int) (any, error)
	read = func(depth int) (any, error) {
		if depth > NativeFrameDecodeDepth {
			return nil, invalid(ReasonBufferFull, "$")
		}
		t, err := d.Token()
		if err != nil {
			return nil, invalid(ReasonInvalidInput, "$")
		}
		if delim, ok := t.(json.Delim); ok {
			switch delim {
			case '{':
				m := map[string]any{}
				for d.More() {
					key, err := d.Token()
					if err != nil {
						return nil, invalid(ReasonInvalidInput, "$")
					}
					name, ok := key.(string)
					if !ok {
						return nil, invalid(ReasonInvalidInput, "$")
					}
					if _, duplicate := m[name]; duplicate {
						return nil, invalid(ReasonInvalidInput, "$")
					}
					value, err := read(depth + 1)
					if err != nil {
						return nil, err
					}
					m[name] = value
				}
				end, err := d.Token()
				if err != nil || end != json.Delim('}') {
					return nil, invalid(ReasonInvalidInput, "$")
				}
				return m, nil
			case '[':
				a := []any{}
				for d.More() {
					value, err := read(depth + 1)
					if err != nil {
						return nil, err
					}
					a = append(a, value)
				}
				end, err := d.Token()
				if err != nil || end != json.Delim(']') {
					return nil, invalid(ReasonInvalidInput, "$")
				}
				return a, nil
			default:
				return nil, invalid(ReasonInvalidInput, "$")
			}
		}
		return t, nil
	}
	value, err := read(0)
	if err != nil {
		return nil, err
	}
	if _, err := d.Token(); err != io.EOF {
		return nil, invalid(ReasonInvalidInput, "$")
	}
	return value, nil
}

func (v *Validator) ValidateJSON(schema string, data []byte) error {
	_, err := v.decode(schema, data)
	return err
}

func (v *Validator) DecodeJSON(schema string, data []byte, dst any) error {
	if err := v.ValidateJSON(schema, data); err != nil {
		return err
	}
	return json.Unmarshal(data, dst)
}

func (v *Validator) decode(name string, data []byte) (any, error) {
	schema, ok := v.schemas[name]
	if !ok {
		return nil, invalid(ReasonUnsupported, "$")
	}
	value, err := decodeBounded(data, wireLimit(name))
	if err != nil {
		return nil, err
	}
	if err := v.validate(schema, value, "$"); err != nil {
		return nil, err
	}
	if err := v.conditions(name, value, "$"); err != nil {
		return nil, err
	}
	return value, nil
}

func (v *Validator) validate(s *wireSchema, value any, path string) error {
	if s.Ref != "" {
		name := strings.TrimPrefix(s.Ref, "#/components/schemas/")
		target, ok := v.schemas[name]
		if !ok {
			return invalid(ReasonUnsupported, path)
		}
		if err := v.validate(target, value, path); err != nil {
			return err
		}
		return v.conditions(name, value, path)
	}
	for _, union := range []struct {
		arms  []*wireSchema
		exact bool
	}{{s.OneOf, true}, {s.AnyOf, false}} {
		if len(union.arms) == 0 {
			continue
		}
		// Exact discriminator dispatch rejects unknown arms without interpreting
		// attacker-chosen payloads as another command/event type.
		if m, ok := value.(map[string]any); ok {
			for _, discriminator := range []string{"kind", "method"} {
				if union.arms[0].Properties[discriminator] == nil {
					continue
				}
				for _, arm := range union.arms {
					p := arm.Properties[discriminator]
					if p != nil && len(p.Enum) == 1 && m[discriminator] == p.Enum[0] {
						return v.validate(arm, value, path)
					}
				}
				return invalid(ReasonUnsupported, path+"."+discriminator)
			}
		}
		count := 0
		for _, arm := range union.arms {
			if v.validate(arm, value, path) == nil {
				count++
			}
		}
		if count == 0 || union.exact && count != 1 {
			return invalid(ReasonInvalidInput, path)
		}
		return nil
	}
	if len(s.Enum) > 0 {
		found := false
		for _, allowed := range s.Enum {
			if value == allowed {
				found = true
				break
			}
		}
		if !found {
			return invalid(ReasonUnsupported, path)
		}
	}
	switch s.Type {
	case "null":
		if value != nil {
			return invalid(ReasonInvalidInput, path)
		}
	case "object":
		m, ok := value.(map[string]any)
		if !ok {
			return invalid(ReasonInvalidInput, path)
		}
		for _, key := range s.Required {
			if _, ok := m[key]; !ok {
				return invalid(ReasonInvalidInput, path+"."+key)
			}
		}
		for key, val := range m {
			prop, ok := s.Properties[key]
			if !ok {
				return invalid(ReasonInvalidInput, path)
			}
			if err := v.validate(prop, val, path+"."+key); err != nil {
				return err
			}
		}
	case "array":
		a, ok := value.([]any)
		if !ok {
			return invalid(ReasonInvalidInput, path)
		}
		if s.MaxItems != nil && len(a) > *s.MaxItems {
			return invalid(ReasonBufferFull, path)
		}
		for _, item := range a {
			if err := v.validate(s.Items, item, path+"[]"); err != nil {
				return err
			}
		}
	case "string":
		str, ok := value.(string)
		if !ok {
			return invalid(ReasonInvalidInput, path)
		}
		if s.MaxLength != nil && len(str) > *s.MaxLength {
			return invalid(ReasonBufferFull, path)
		}
		if s.MinLength != nil && utf8.RuneCountInString(str) < *s.MinLength {
			return invalid(ReasonInvalidInput, path)
		}
		if s.pattern != nil && !s.pattern.MatchString(str) {
			return invalid(ReasonInvalidInput, path)
		}
		if s.Pattern == "^(0|[1-9][0-9]{0,19})$" {
			if _, err := strconv.ParseUint(str, 10, 64); err != nil {
				return invalid(ReasonBufferFull, path)
			}
		}
		switch s.Format {
		case "uuid":
			if !isUUID(str) {
				return invalid(ReasonInvalidInput, path)
			}
		case "date-time":
			if _, err := time.Parse(time.RFC3339Nano, str); err != nil {
				return invalid(ReasonInvalidInput, path)
			}
		case "uri":
			u, err := url.Parse(str)
			if err != nil || u.Host == "" || u.User != nil || u.Scheme != "https" && u.Scheme != "http" {
				return invalid(ReasonInvalidInput, path)
			}
		}
	case "boolean":
		if _, ok := value.(bool); !ok {
			return invalid(ReasonInvalidInput, path)
		}
	case "integer", "number":
		num, ok := value.(json.Number)
		if !ok {
			return invalid(ReasonInvalidInput, path)
		}
		if s.Type == "integer" {
			if _, err := num.Int64(); err != nil {
				return invalid(ReasonInvalidInput, path)
			}
		}
		n, err := num.Float64()
		if err != nil || math.IsInf(n, 0) || math.IsNaN(n) || s.Type == "integer" && math.Trunc(n) != n {
			return invalid(ReasonInvalidInput, path)
		}
		if s.Minimum != nil && n < *s.Minimum || s.Maximum != nil && n > *s.Maximum {
			return invalid(ReasonBufferFull, path)
		}
	}
	return nil
}

func isUUID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i, c := range s {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			if c != '-' {
				return false
			}
			continue
		}
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F') {
			return false
		}
	}
	return true
}

func (v *Validator) conditions(name string, value any, path string) error {
	m, ok := value.(map[string]any)
	if !ok {
		return nil
	}
	switch name {
	case "NativeStartIntent":
		if m["role"] == "worker" && (m["work_id"] == nil || m["parent_harness_session_id"] == nil) {
			return invalid(ReasonGrantMissing, path)
		}
		if m["role"] == "coordinator" && m["work_id"] != nil {
			return invalid(ReasonInvalidInput, path+".work_id")
		}
	case "NativeStop":
		if (m["kind"] == "force") != (m["force_authorization_id"] != nil) {
			return invalid(ReasonGrantMissing, path+".force_authorization_id")
		}
	case "NativeApprovalDecision":
		if (m["decision"] == "answer") != (m["answer"] != nil) {
			return invalid(ReasonInvalidInput, path+".answer")
		}
	case "NativeCapabilities":
		for _, pair := range [][2]string{{"take", "take_supported"}, {"interrupt", "interrupt_supported"}, {"input", "chat_supported"}} {
			if m[pair[0]] != m[pair[1]] {
				return invalid(ReasonInvalidInput, path+"."+pair[1])
			}
		}
	case "NativeReceipt":
		if m["binding"] == nil && (m["operation"] != "start" || m["state"] != "accepted" && m["state"] != "queued") {
			return invalid(ReasonIdentityMismatch, path+".binding")
		}
	case "NativeSessionView":
		if binding, ok := m["current_binding"].(map[string]any); ok {
			if m["owned_session_id"] != binding["owned_session_id"] || (m["role"] == "worker") != (binding["run_id"] != nil) {
				return invalid(ReasonIdentityMismatch, path+".current_binding")
			}
		}
	case "NativeClaimResult":
		kind := m["claim_kind"]
		command := kind == "command"
		launch := kind == "start" || kind == "resume"
		if (m["command"] != nil) != command || (m["command_actor"] != nil) != command || m["launch_authorized"] != launch {
			return invalid(ReasonGrantMissing, path)
		}
		if (kind == "final_drain" || kind == "resume" || command) && m["expected_binding"] == nil {
			return invalid(ReasonIdentityMismatch, path+".expected_binding")
		}
		if kind == "final_drain" && m["final_drain_grant"] == nil {
			return invalid(ReasonGrantMissing, path+".final_drain_grant")
		}
		policy := m["policy"].(map[string]any)
		launchConfig := m["launch_config"].(map[string]any)
		for _, field := range []string{"tool_policy_digest", "settings_policy_digest"} {
			if policy[field] != launchConfig[field] {
				return invalid(ReasonIdentityMismatch, path+".launch_config."+field)
			}
		}
	case "NativePTYInput", "NativeTerminalFrame":
		encoded := m["data_base64"].(string)
		limit := PTYChunkBytes
		if name == "NativePTYInput" {
			limit = PTYInputDecodedBytes
		}
		// Check allocation bound before Base64 decode, then actual decoded bytes.
		if base64.StdEncoding.DecodedLen(len(encoded)) > limit+2 {
			return invalid(ReasonBufferFull, path+".data_base64")
		}
		decoded, err := base64.StdEncoding.Strict().DecodeString(encoded)
		if err != nil {
			return invalid(ReasonInvalidInput, path+".data_base64")
		}
		if len(decoded) > limit {
			return invalid(ReasonBufferFull, path+".data_base64")
		}
	case "NativeAssistantFinal":
		if m["inferred"] == true && m["input_id"] == nil {
			return invalid(ReasonInvalidInput, path+".input_id")
		}
	case "NativeExpertFrame":
		frame, err := decodeBounded([]byte(m["frame_json"].(string)), ExportedFrameBytes)
		if err != nil {
			return err
		}
		body, ok := frame.(map[string]any)
		if !ok {
			return invalid(ReasonInvalidInput, path+".frame_json")
		}
		if m["omitted"] == true && len(body) != 0 {
			return invalid(ReasonFrameOmitted, path+".frame_json")
		}
	case "NativePublish":
		binding := m["expected_binding"].(map[string]any)
		for _, event := range m["events"].([]any) {
			if !equalBindingMaps(binding, event.(map[string]any)["binding"].(map[string]any)) {
				return invalid(ReasonWrongGeneration, path+".events[].binding")
			}
		}
		if snapshot, ok := m["snapshot"].(map[string]any); ok {
			if b, ok := snapshot["current_binding"].(map[string]any); !ok || !equalBindingMaps(binding, b) {
				return invalid(ReasonWrongGeneration, path+".snapshot.current_binding")
			}
		}
	case "NativeStreamEvent":
		encoded, err := json.Marshal(value)
		if err != nil || len(encoded) > ExportedFrameBytes {
			return invalid(ReasonBufferFull, path)
		}
		kind := m["kind"].(string)
		target := StreamPayloadSchemas()[kind]
		if target == "" {
			return invalid(ReasonUnsupported, path+".kind")
		}
		if err := v.validate(v.schemas[target], m["payload"], path+".payload"); err != nil {
			return err
		}
		if err := v.conditions(target, m["payload"], path+".payload"); err != nil {
			return err
		}
		if m["seq"] != m["sequence"] {
			return invalid(ReasonInvalidInput, path+".seq")
		}
		if m["source"] != "native" {
			return invalid(ReasonUnsupported, path+".source")
		}
		payload := m["payload"].(map[string]any)
		if kind == "input.not_confirmed" || kind == "input.generation_changed" || kind == "input.session_ended" || kind == "input.refused" {
			retryable := kind == "input.not_confirmed" || kind == "input.generation_changed"
			if payload["retryable"] != retryable {
				return invalid(ReasonInvalidInput, path+".payload.retryable")
			}
		}
		if generation, exists := payload["generation"]; exists && generation != m["generation"] {
			return invalid(ReasonWrongGeneration, path+".payload.generation")
		}
		for _, field := range []string{"binding", "current_binding"} {
			if binding, exists := payload[field]; exists {
				b, ok := binding.(map[string]any)
				if !ok || !equalBindingMaps(m["binding"].(map[string]any), b) {
					return invalid(ReasonWrongGeneration, path+".payload."+field)
				}
			}
		}
	}
	return nil
}

func equalBindingMaps(a, b map[string]any) bool {
	if len(a) != len(b) {
		return false
	}
	for k, value := range a {
		if b[k] != value {
			return false
		}
	}
	return true
}

// ValidationContext is trusted transport context, never decoded from the body.
// Runtime authorization must additionally revalidate the grant under the final
// access/native write lock. A schema-valid body alone is never write authority.
type ValidationContext struct {
	Binding                                          *Binding
	OwnedSessionID, PrincipalKind, View, Mode, State string
	Generation                                       int64
	Capabilities                                     *Capabilities
	Web, Private                                     bool
}

func (v *Validator) ValidateBoundJSON(name string, data []byte, ctx ValidationContext) error {
	value, err := v.decode(name, data)
	if err != nil {
		return err
	}
	if ctx.Binding == nil {
		return invalid(ReasonIdentityMismatch, "$.binding")
	}
	bound, _ := json.Marshal(ctx.Binding)
	var expected map[string]any
	d := json.NewDecoder(bytes.NewReader(bound))
	d.UseNumber()
	if err := d.Decode(&expected); err != nil {
		return err
	}
	var visit func(any) error
	visit = func(value any) error {
		switch item := value.(type) {
		case map[string]any:
			if item["terminal_viewer_url"] != nil && ctx.PrincipalKind != "person" {
				return invalid(ReasonWrongPrincipal, "$.terminal_viewer_url")
			}
			for _, field := range []string{"binding", "expected_binding", "current_binding"} {
				if binding, exists := item[field]; exists && binding != nil {
					actual, ok := binding.(map[string]any)
					if !ok || !equalBindingMaps(expected, actual) {
						return invalid(ReasonWrongGeneration, "$."+field)
					}
				}
			}
			if id, exists := item["owned_session_id"]; exists && id != ctx.Binding.OwnedSessionID {
				return invalid(ReasonIdentityMismatch, "$.owned_session_id")
			}
			if generation, exists := item["generation"]; exists && generation != json.Number(strconv.FormatInt(ctx.Generation, 10)) {
				return invalid(ReasonWrongGeneration, "$.generation")
			}
			for _, val := range item {
				if err := visit(val); err != nil {
					return err
				}
			}
		case []any:
			for _, val := range item {
				if err := visit(val); err != nil {
					return err
				}
			}
		}
		return nil
	}
	if ctx.OwnedSessionID != "" && ctx.OwnedSessionID != ctx.Binding.OwnedSessionID {
		return invalid(ReasonIdentityMismatch, "$.owned_session_id")
	}
	if err := visit(value); err != nil {
		return err
	}
	if name == "NativeReceipt" && ctx.Private && value.(map[string]any)["binding"] == nil {
		return invalid(ReasonIdentityMismatch, "$.binding")
	}
	if name == "NativeStreamEvent" {
		return validateStreamAccess(value.(map[string]any)["kind"].(string), ctx)
	}
	if name == "NativePublish" || name == "NativeEventPage" {
		field := "events"
		if name == "NativeEventPage" {
			field = "items"
		}
		for _, event := range value.(map[string]any)[field].([]any) {
			if err := validateStreamAccess(event.(map[string]any)["kind"].(string), ctx); err != nil {
				return err
			}
		}
	}
	return validateOperationAccess(name, ctx)
}

func validateOperationAccess(name string, ctx ValidationContext) error {
	if name == "NativeTakeIntent" || name == "NativePTYInput" || name == "NativeResize" {
		if ctx.PrincipalKind != "person" {
			return invalid(ReasonWrongPrincipal, "$")
		}
		if ctx.Web {
			return invalid(ReasonUnsupported, "$")
		}
	}
	if name == "NativeEndControl" || name == "NativeApprovalDecision" || name == "NativeReturnIntent" {
		if ctx.PrincipalKind != "person" {
			return invalid(ReasonWrongPrincipal, "$")
		}
	}
	c := ctx.Capabilities
	if c == nil {
		return invalid(ReasonQualificationMissing, "$.capabilities")
	}
	supported := true
	switch name {
	case "NativeInput":
		supported = c.Input && c.ChatSupported
	case "NativeInterrupt":
		supported = c.Interrupt && c.InterruptSupported
	case "NativeStop":
		supported = c.Stop
	case "NativeResumeIntent":
		supported = c.Resume
	case "NativeTakeIntent":
		supported = c.Take && c.TakeSupported
	case "NativeApprovalDecision":
		supported = c.Approvals || c.Questions
	case "NativePTYInput", "NativeResize":
		supported = c.TerminalView && ctx.Mode == "terminal" && ctx.State == "taken"
	}
	if !supported {
		return invalid(ReasonUnsupported, "$")
	}
	return nil
}

func validateStreamAccess(kind string, ctx ValidationContext) error {
	terminal := kind == "terminal" || ctx.View == "terminal"
	expert := kind == "expert" || ctx.View == "expert"
	if (terminal || expert) && ctx.PrincipalKind != "person" {
		return invalid(ReasonWrongPrincipal, "$")
	}
	if ctx.Capabilities == nil {
		return invalid(ReasonQualificationMissing, "$.capabilities")
	}
	if !ctx.Capabilities.EventsSupported || terminal && (!ctx.Capabilities.TerminalView || ctx.Mode != "terminal" || ctx.State != "taken") || expert && !ctx.Capabilities.Expert {
		return invalid(ReasonUnsupported, "$")
	}
	if ctx.View == "structured" && (kind == "terminal" || kind == "expert") || ctx.View == "terminal" && kind == "expert" || ctx.View == "expert" && kind == "terminal" {
		return invalid(ReasonUnsupported, "$.kind")
	}
	return nil
}

// DecodeLocal enforces method/header agreement, request correlation and exact
// binding before an adapter can call Host. Kernel peer/owner-socket and grant
// verification remain the transport's responsibility.
func (v *Validator) DecodeLocal(data []byte, protocol, method string, ctx ValidationContext) (LocalEnvelope, error) {
	var envelope LocalEnvelope
	if protocol != LocalProtocol {
		return envelope, invalid(ReasonUnsupported, "$.protocol")
	}
	value, err := v.decode("NativeLocalEnvelope", data)
	if err != nil {
		return envelope, err
	}
	m := value.(map[string]any)
	if m["protocol"] != protocol || m["method"] != method {
		return envelope, invalid(ReasonUnsupported, "$.method")
	}
	params := m["params"].(map[string]any)
	requestID := params["request_id"]
	if method == "start" {
		requestID = params["intent"].(map[string]any)["request_id"]
		if m["expected_binding"] != nil {
			return envelope, invalid(ReasonIdentityMismatch, "$.expected_binding")
		}
	}
	if requestID != m["request_id"] {
		return envelope, invalid(ReasonIdentityMismatch, "$.request_id")
	}
	if method != "start" {
		var schema string
		for _, route := range LocalRoutes() {
			if route.OperationID == method {
				schema = route.RequestSchema
				break
			}
		}
		if err := v.ValidateBoundJSON("NativeLocalEnvelope", data, ctx); err != nil {
			return envelope, err
		}
		if err := validateOperationAccess(schema, ctx); err != nil {
			return envelope, err
		}
	}
	err = json.Unmarshal(data, &envelope)
	return envelope, err
}

// EventID is a position only, never a viewer capability or authorization cursor.
func EventID(generation int64, sequence uint64) (string, error) {
	if generation < 0 || generation > 9007199254740991 {
		return "", invalid(ReasonInvalidInput, "$.generation")
	}
	return strconv.FormatInt(generation, 10) + ":" + strconv.FormatUint(sequence, 10), nil
}

func ParseEventID(id string) (int64, uint64, error) {
	if len(id) > 64 {
		return 0, 0, invalid(ReasonBufferFull, "$.event_id")
	}
	parts := strings.Split(id, ":")
	if len(parts) != 2 {
		return 0, 0, invalid(ReasonInvalidInput, "$.event_id")
	}
	gen, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return 0, 0, invalid(ReasonInvalidInput, "$.event_id")
	}
	seq, err := strconv.ParseUint(parts[1], 10, 64)
	if err != nil {
		return 0, 0, invalid(ReasonInvalidInput, "$.event_id")
	}
	canonical, err := EventID(gen, seq)
	if err != nil || canonical != id {
		return 0, 0, invalid(ReasonInvalidInput, "$.event_id")
	}
	return gen, seq, nil
}

// ValidateCommand rejects nil/extra arms before a Store or Host dispatch.
func ValidateCommand(c Command) error {
	arms := map[string]bool{"input": c.Input != nil, "interrupt": c.Interrupt != nil, "stop": c.Stop != nil, "attach": c.Attach != nil, "renew": c.Renew != nil, "detach": c.Detach != nil, "take": c.Take != nil, "return": c.Return != nil, "approval": c.Approval != nil, "resume": c.Resume != nil, "control_end": c.EndControl != nil}
	if !arms[c.Kind] {
		return invalid(ReasonUnsupported, "$.kind")
	}
	count := 0
	for _, exists := range arms {
		if exists {
			count++
		}
	}
	if count != 1 {
		return invalid(ReasonInvalidInput, "$")
	}
	return nil
}

// PersonProjection strips URLs and terminal capabilities for agent principals;
// this applies even to owner-workstation agents. Web v1 never advertises take.
func PersonProjection(view SessionView, principalKind string, web bool) SessionView {
	if principalKind != "person" {
		view.TerminalViewerURL = nil
		view.Capabilities.TerminalView = false
		view.Capabilities.Expert = false
		view.Capabilities.Take = false
		view.Capabilities.TakeSupported = false
	}
	if web {
		view.Capabilities.Take = false
		view.Capabilities.TakeSupported = false
	}
	return view
}

// SequenceGuard enforces a gapless publisher sequence in one exact generation.
// The owning relay sets Next from its trusted reader/replay position, not clients.
type SequenceGuard struct {
	Binding    Binding
	Generation int64
	Next       uint64
	exhausted  bool
}

func (g *SequenceGuard) Accept(event StreamEvent) error {
	if !g.Binding.Equal(event.Binding) || g.Generation != event.Generation {
		return invalid(ReasonWrongGeneration, "$.binding")
	}
	seq, err := strconv.ParseUint(event.Seq, 10, 64)
	if err != nil || event.Seq != event.Sequence || strconv.FormatUint(seq, 10) != event.Seq || g.exhausted || seq != g.Next {
		return invalid(ReasonInvalidInput, "$.seq")
	}
	if seq == math.MaxUint64 {
		g.exhausted = true
	} else {
		g.Next++
	}
	return nil
}

func (b Binding) Equal(other Binding) bool {
	run := b.RunID == nil && other.RunID == nil || b.RunID != nil && other.RunID != nil && *b.RunID == *other.RunID
	return run && b.TenantID == other.TenantID && b.ProjectID == other.ProjectID && b.OwnedSessionID == other.OwnedSessionID && b.HarnessSessionID == other.HarnessSessionID && b.RuntimeEpoch == other.RuntimeEpoch && b.DaemonID == other.DaemonID && b.DaemonGeneration == other.DaemonGeneration && b.BindingRevision == other.BindingRevision
}
