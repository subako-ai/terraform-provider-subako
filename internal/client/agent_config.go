package client

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
)

// AgentConfig is everything one published agent version carries:
// `AgentConfigBody`, and the model provider the version publishes against.
// It is held in its canonical form (see Canonical), so two configs that would
// publish the same version compare equal.
type AgentConfig struct {
	ModelProviderID string
	SystemPrompt    *string
	Model           ModelConfig
	MCP             []MCPGrant
	Skills          []SkillGrant
	Sandboxes       []SandboxGrant
	Functions       []FunctionGrant
	Volume          VolumeConfig
	Builtin         BuiltinGrants
}

// configBody is `AgentConfigBody`. A version answers every field, and
// publishing takes the same shape, so one type reads and writes it.
type configBody struct {
	SystemPrompt *string         `json:"system_prompt"`
	Model        json.RawMessage `json:"model"`
	MCP          []MCPGrant      `json:"mcp"`
	Skills       []SkillGrant    `json:"skills"`
	Sandboxes    []SandboxGrant  `json:"sandboxes"`
	Functions    []FunctionGrant `json:"functions"`
	Volume       VolumeConfig    `json:"volume"`
	Builtin      BuiltinGrants   `json:"builtin"`
}

// publishBody is `PublishAgentVersionBody`: the config with the model
// provider beside it.
type publishBody struct {
	ModelProviderID string `json:"model_provider_id"`
	configBody
}

func (c AgentConfig) publishBody() (publishBody, error) {
	model, err := json.Marshal(c.Model)
	if err != nil {
		return publishBody{}, fmt.Errorf("encode model: %w", err)
	}
	return publishBody{
		ModelProviderID: c.ModelProviderID,
		configBody: configBody{
			SystemPrompt: c.SystemPrompt,
			Model:        model,
			MCP:          orEmpty(c.MCP),
			Skills:       orEmpty(c.Skills),
			Sandboxes:    orEmpty(c.Sandboxes),
			Functions:    orEmpty(c.Functions),
			Volume:       c.Volume,
			Builtin:      c.Builtin,
		},
	}, nil
}

// AgentConfig decodes the config the version was published with. A field
// this provider has no type for is refused rather than dropped: the next
// publish would otherwise remove it without the plan saying so.
func (v *AgentVersion) AgentConfig() (AgentConfig, error) {
	var body configBody
	if err := strictUnmarshal(v.Config, &body); err != nil {
		return AgentConfig{}, fmt.Errorf("decode agent version %d config: %w", v.Version, err)
	}
	model, err := decodeModel(body.Model)
	if err != nil {
		return AgentConfig{}, fmt.Errorf("decode agent version %d model: %w", v.Version, err)
	}
	return AgentConfig{
		ModelProviderID: v.ModelProviderID,
		SystemPrompt:    body.SystemPrompt,
		Model:           model,
		MCP:             body.MCP,
		Skills:          body.Skills,
		Sandboxes:       body.Sandboxes,
		Functions:       body.Functions,
		Volume:          body.Volume,
		Builtin:         body.Builtin,
	}.Canonical(), nil
}

// Equal reports whether two configs would publish the same version. Both
// sides are Canonical, so they compare field by field.
func (c AgentConfig) Equal(other AgentConfig) bool {
	return reflect.DeepEqual(c, other)
}

// Canonical folds every empty list to nil and spells out each default the
// server applies to a value left out, so a config read back from a version
// equals the one that published it.
func (c AgentConfig) Canonical() AgentConfig {
	out := c
	out.Model = canonicalModel(c.Model)
	out.MCP = nil
	for _, grant := range c.MCP {
		grant.Tools = nilIfEmpty(grant.Tools)
		out.MCP = append(out.MCP, grant)
	}
	out.Skills = nilIfEmpty(slices.Clone(c.Skills))
	out.Sandboxes = nil
	for _, grant := range c.Sandboxes {
		grant.Tools = nilIfEmpty(grant.Tools)
		if grant.Network == "" {
			grant.Network = SandboxNetworkUnrestricted
		}
		out.Sandboxes = append(out.Sandboxes, grant)
	}
	out.Functions = nil
	for _, grant := range c.Functions {
		grant.Tools = nilIfEmpty(grant.Tools)
		grant.HTTPCredentials = nilIfEmpty(grant.HTTPCredentials)
		if grant.Network == nil {
			grant.Network = NoNetwork{}
		}
		out.Functions = append(out.Functions, grant)
	}
	if out.Volume.Data == "" {
		out.Volume.Data = VolumeDataDisabled
	}
	if out.Builtin.SessionDisplayName == "" {
		out.Builtin.SessionDisplayName = DisplayNameNever
	}
	if c.Builtin.AskUser != nil {
		widgets := make([]Widget, len(c.Builtin.AskUser.Widgets))
		for i, widget := range c.Builtin.AskUser.Widgets {
			widgets[i] = canonicalWidget(widget)
		}
		out.Builtin.AskUser = &AskUserGrant{Widgets: widgets}
	}
	return out
}

func nilIfEmpty[T any](list []T) []T {
	if len(list) == 0 {
		return nil
	}
	return list
}

// orEmpty spells a nil list as an empty one: the server takes a list left
// out, but not one written as null.
func orEmpty[T any](list []T) []T {
	if list == nil {
		return []T{}
	}
	return list
}

// strictUnmarshal decodes data into v, refusing a field v does not name.
func strictUnmarshal(data []byte, v any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	return decoder.Decode(v)
}

// tagOf reads the tag a variant carries under key, without the rest.
func tagOf(data []byte, key string) (string, error) {
	var tags map[string]json.RawMessage
	if err := json.Unmarshal(data, &tags); err != nil {
		return "", err
	}
	raw, ok := tags[key]
	if !ok {
		return "", fmt.Errorf("missing %q", key)
	}
	var tag string
	if err := json.Unmarshal(raw, &tag); err != nil {
		return "", fmt.Errorf("%q is not a string: %w", key, err)
	}
	return tag, nil
}

// oneOf checks that value is one a closed set of spellings holds.
func oneOf(what, value string, allowed ...string) error {
	if slices.Contains(allowed, value) {
		return nil
	}
	return fmt.Errorf("unknown %s %q", what, value)
}

// Model wire formats, as `ModelProviderFormatBody` spells them.
const (
	FormatAnthropic       = "anthropic"
	FormatOpenAIResponses = "openai_responses"
)

// Image input, as `ImageInputBody` spells it.
const (
	ImageInputSupported   = "supported"
	ImageInputUnsupported = "unsupported"
)

// ImageInputs are the spellings of `ImageInputBody`.
var ImageInputs = []string{ImageInputSupported, ImageInputUnsupported}

// AnthropicEfforts are the spellings of `AnthropicEffortBody`.
var AnthropicEfforts = []string{"low", "medium", "high", "xhigh", "max"}

// ReasoningEfforts are the spellings of `ReasoningEffortBody`.
var ReasoningEfforts = []string{"none", "minimal", "low", "medium", "high", "xhigh"}

// ModelConfig is `ModelConfigBody`: one wire format and the settings that
// format carries. The two types below are its only forms -- the unexported
// method seals the interface -- and each writes its own format tag.
type ModelConfig interface {
	json.Marshaler
	// Format is the tag the wire carries, as `ModelProviderFormatBody`
	// spells it.
	Format() string
	sealedModelConfig()
}

// AnthropicModel is `AnthropicModelBody`. A nil Thinking or Effort takes the
// model's own default.
type AnthropicModel struct {
	Model         string
	MaxTokens     int64
	ContextWindow int64
	ImageInput    string
	Thinking      AnthropicThinking
	Effort        *string
}

// OpenAIResponsesModel is `OpenaiResponsesModelBody`.
type OpenAIResponsesModel struct {
	Model           string
	MaxTokens       int64
	ContextWindow   int64
	ImageInput      string
	ReasoningEffort *string
}

func (AnthropicModel) Format() string       { return FormatAnthropic }
func (OpenAIResponsesModel) Format() string { return FormatOpenAIResponses }

func (AnthropicModel) sealedModelConfig()       {}
func (OpenAIResponsesModel) sealedModelConfig() {}

type anthropicBody struct {
	Format        string          `json:"format"`
	Model         string          `json:"model"`
	MaxTokens     int64           `json:"max_tokens"`
	ContextWindow int64           `json:"context_window"`
	ImageInput    string          `json:"image_input"`
	Thinking      json.RawMessage `json:"thinking"`
	Effort        *string         `json:"effort"`
}

type openaiResponsesBody struct {
	Format          string  `json:"format"`
	Model           string  `json:"model"`
	MaxTokens       int64   `json:"max_tokens"`
	ContextWindow   int64   `json:"context_window"`
	ImageInput      string  `json:"image_input"`
	ReasoningEffort *string `json:"reasoning_effort"`
}

func (m AnthropicModel) MarshalJSON() ([]byte, error) {
	thinking := json.RawMessage("null")
	if m.Thinking != nil {
		encoded, err := json.Marshal(m.Thinking)
		if err != nil {
			return nil, err
		}
		thinking = encoded
	}
	return json.Marshal(anthropicBody{
		Format:        m.Format(),
		Model:         m.Model,
		MaxTokens:     m.MaxTokens,
		ContextWindow: m.ContextWindow,
		ImageInput:    imageInputOrDefault(m.ImageInput),
		Thinking:      thinking,
		Effort:        m.Effort,
	})
}

func (m OpenAIResponsesModel) MarshalJSON() ([]byte, error) {
	return json.Marshal(openaiResponsesBody{
		Format:          m.Format(),
		Model:           m.Model,
		MaxTokens:       m.MaxTokens,
		ContextWindow:   m.ContextWindow,
		ImageInput:      imageInputOrDefault(m.ImageInput),
		ReasoningEffort: m.ReasoningEffort,
	})
}

// imageInputOrDefault is what the server reads an absent image input as.
func imageInputOrDefault(input string) string {
	if input == "" {
		return ImageInputSupported
	}
	return input
}

// decodeModel reads `ModelConfigBody`. A format this provider has no type
// for is drift between server and provider, so it is refused rather than
// guessed at.
func decodeModel(data []byte) (ModelConfig, error) {
	format, err := tagOf(data, "format")
	if err != nil {
		return nil, err
	}
	switch format {
	case FormatAnthropic:
		var body anthropicBody
		if err := strictUnmarshal(data, &body); err != nil {
			return nil, err
		}
		if err := oneOf("image input", body.ImageInput, ImageInputs...); err != nil {
			return nil, err
		}
		if body.Effort != nil {
			if err := oneOf("anthropic effort", *body.Effort, AnthropicEfforts...); err != nil {
				return nil, err
			}
		}
		var thinking AnthropicThinking
		if len(body.Thinking) > 0 && string(body.Thinking) != "null" {
			if thinking, err = decodeThinking(body.Thinking); err != nil {
				return nil, err
			}
		}
		return AnthropicModel{
			Model:         body.Model,
			MaxTokens:     body.MaxTokens,
			ContextWindow: body.ContextWindow,
			ImageInput:    body.ImageInput,
			Thinking:      thinking,
			Effort:        body.Effort,
		}, nil
	case FormatOpenAIResponses:
		var body openaiResponsesBody
		if err := strictUnmarshal(data, &body); err != nil {
			return nil, err
		}
		if err := oneOf("image input", body.ImageInput, ImageInputs...); err != nil {
			return nil, err
		}
		if body.ReasoningEffort != nil {
			if err := oneOf("reasoning effort", *body.ReasoningEffort, ReasoningEfforts...); err != nil {
				return nil, err
			}
		}
		return OpenAIResponsesModel{
			Model:           body.Model,
			MaxTokens:       body.MaxTokens,
			ContextWindow:   body.ContextWindow,
			ImageInput:      body.ImageInput,
			ReasoningEffort: body.ReasoningEffort,
		}, nil
	default:
		return nil, fmt.Errorf("unknown model format %q", format)
	}
}

func canonicalModel(model ModelConfig) ModelConfig {
	switch m := model.(type) {
	case AnthropicModel:
		m.ImageInput = imageInputOrDefault(m.ImageInput)
		return m
	case OpenAIResponsesModel:
		m.ImageInput = imageInputOrDefault(m.ImageInput)
		return m
	}
	return model
}

// AnthropicThinking is `AnthropicThinkingBody`: how much the model thinks
// before it answers. The four types below are its only forms.
type AnthropicThinking interface {
	json.Marshaler
	sealedAnthropicThinking()
}

// Thinking kinds, as `AnthropicThinkingBody` tags them.
const (
	ThinkingAdaptive     = "adaptive"
	ThinkingDisabled     = "disabled"
	ThinkingBetweenTools = "between_tools"
	ThinkingEnabled      = "enabled"
)

type (
	// AdaptiveThinking lets the model decide how much to think.
	AdaptiveThinking struct{}
	// DisabledThinking turns thinking off.
	DisabledThinking struct{}
	// BetweenToolsThinking thinks between tool calls.
	BetweenToolsThinking struct{}
	// EnabledThinking thinks within a fixed budget.
	EnabledThinking struct {
		BudgetTokens int64
	}
)

func (AdaptiveThinking) sealedAnthropicThinking()     {}
func (DisabledThinking) sealedAnthropicThinking()     {}
func (BetweenToolsThinking) sealedAnthropicThinking() {}
func (EnabledThinking) sealedAnthropicThinking()      {}

type typeTag struct {
	Type string `json:"type"`
}

type enabledThinkingBody struct {
	Type         string `json:"type"`
	BudgetTokens int64  `json:"budget_tokens"`
}

func (AdaptiveThinking) MarshalJSON() ([]byte, error) {
	return json.Marshal(typeTag{ThinkingAdaptive})
}

func (DisabledThinking) MarshalJSON() ([]byte, error) {
	return json.Marshal(typeTag{ThinkingDisabled})
}

func (BetweenToolsThinking) MarshalJSON() ([]byte, error) {
	return json.Marshal(typeTag{ThinkingBetweenTools})
}

func (t EnabledThinking) MarshalJSON() ([]byte, error) {
	return json.Marshal(enabledThinkingBody{Type: ThinkingEnabled, BudgetTokens: t.BudgetTokens})
}

func decodeThinking(data []byte) (AnthropicThinking, error) {
	kind, err := tagOf(data, "type")
	if err != nil {
		return nil, fmt.Errorf("thinking: %w", err)
	}
	if kind == ThinkingEnabled {
		var body enabledThinkingBody
		if err := strictUnmarshal(data, &body); err != nil {
			return nil, fmt.Errorf("thinking: %w", err)
		}
		return EnabledThinking{BudgetTokens: body.BudgetTokens}, nil
	}
	if err := strictUnmarshal(data, &typeTag{}); err != nil {
		return nil, fmt.Errorf("thinking: %w", err)
	}
	switch kind {
	case ThinkingAdaptive:
		return AdaptiveThinking{}, nil
	case ThinkingDisabled:
		return DisabledThinking{}, nil
	case ThinkingBetweenTools:
		return BetweenToolsThinking{}, nil
	}
	return nil, fmt.Errorf("unknown thinking %q", kind)
}

// Policy actions, as `PolicyActionBody` spells them.
var PolicyActions = []string{"allow", "require_approval", "deny"}

// MCPGrant is `McpGrantBody`.
type MCPGrant struct {
	Name          string     `json:"name"`
	URL           string     `json:"url"`
	DefaultPolicy string     `json:"default_policy"`
	Tools         []ToolRule `json:"tools,omitempty"`
}

// ToolRule is `ToolRuleBody`.
type ToolRule struct {
	Name   string `json:"name"`
	Policy string `json:"policy"`
}

// SkillGrant is `SkillGrantBody`. A nil Version is `latest`.
type SkillGrant struct {
	Name    string
	SkillID string
	Version *int64
}

// versionPin is `VersionPinBody`. A nil number is `latest`.
type versionPin struct {
	Type   string `json:"type"`
	Number *int64 `json:"number,omitempty"`
}

func pinOf(version *int64) versionPin {
	if version == nil {
		return versionPin{Type: "latest"}
	}
	return versionPin{Type: "pinned", Number: version}
}

func (p versionPin) version() (*int64, error) {
	switch p.Type {
	case "latest":
		if p.Number != nil {
			return nil, errors.New("a latest version pin names a number")
		}
		return nil, nil
	case "pinned":
		if p.Number == nil {
			return nil, errors.New("a pinned version pin names no number")
		}
		return p.Number, nil
	}
	return nil, fmt.Errorf("unknown version pin %q", p.Type)
}

type skillGrantJSON struct {
	Name    string     `json:"name"`
	SkillID string     `json:"skill_id"`
	Version versionPin `json:"version"`
}

func (g SkillGrant) MarshalJSON() ([]byte, error) {
	return json.Marshal(skillGrantJSON{Name: g.Name, SkillID: g.SkillID, Version: pinOf(g.Version)})
}

func (g *SkillGrant) UnmarshalJSON(data []byte) error {
	var raw skillGrantJSON
	if err := strictUnmarshal(data, &raw); err != nil {
		return err
	}
	version, err := raw.Version.version()
	if err != nil {
		return fmt.Errorf("skill grant %q: %w", raw.Name, err)
	}
	*g = SkillGrant{Name: raw.Name, SkillID: raw.SkillID, Version: version}
	return nil
}

// Sandbox network reaches, as `SandboxNetworkBody` tags them.
const (
	SandboxNetworkUnrestricted = "unrestricted"
	SandboxNetworkNone         = "none"
)

// SandboxNetworks are the tags of `SandboxNetworkBody`.
var SandboxNetworks = []string{SandboxNetworkUnrestricted, SandboxNetworkNone}

// SandboxGrant is `SandboxGrantBody`. Release names the platform image line
// the sandbox boots, the one environment the wire spells.
type SandboxGrant struct {
	Name          string
	Release       string
	Network       string
	DefaultPolicy string
	Tools         []ToolRule
}

type platformEnvironmentBody struct {
	Type    string `json:"type"`
	Release string `json:"release"`
}

type sandboxGrantJSON struct {
	Name          string                  `json:"name"`
	Environment   platformEnvironmentBody `json:"environment"`
	Network       typeTag                 `json:"network"`
	DefaultPolicy string                  `json:"default_policy"`
	Tools         []ToolRule              `json:"tools"`
}

func (g SandboxGrant) MarshalJSON() ([]byte, error) {
	network := g.Network
	if network == "" {
		network = SandboxNetworkUnrestricted
	}
	return json.Marshal(sandboxGrantJSON{
		Name:          g.Name,
		Environment:   platformEnvironmentBody{Type: "platform", Release: g.Release},
		Network:       typeTag{network},
		DefaultPolicy: g.DefaultPolicy,
		Tools:         orEmpty(g.Tools),
	})
}

func (g *SandboxGrant) UnmarshalJSON(data []byte) error {
	var raw sandboxGrantJSON
	if err := strictUnmarshal(data, &raw); err != nil {
		return err
	}
	if err := oneOf("sandbox environment", raw.Environment.Type, "platform"); err != nil {
		return fmt.Errorf("sandbox grant %q: %w", raw.Name, err)
	}
	if err := oneOf("sandbox network", raw.Network.Type, SandboxNetworks...); err != nil {
		return fmt.Errorf("sandbox grant %q: %w", raw.Name, err)
	}
	*g = SandboxGrant{
		Name:          raw.Name,
		Release:       raw.Environment.Release,
		Network:       raw.Network.Type,
		DefaultPolicy: raw.DefaultPolicy,
		Tools:         raw.Tools,
	}
	return nil
}

// FunctionGrant is `FunctionGrantBody`. A nil TimeoutSecs takes the server's
// default; a nil Network is NoNetwork.
type FunctionGrant struct {
	Name            string
	Source          FunctionSource
	DefaultPolicy   string
	Tools           []ToolRule
	TimeoutSecs     *int64
	Network         FunctionNetwork
	HTTPCredentials []string
}

type functionGrantJSON struct {
	Name            string          `json:"name"`
	Source          json.RawMessage `json:"source"`
	DefaultPolicy   string          `json:"default_policy"`
	Tools           []ToolRule      `json:"tools"`
	TimeoutSecs     *int64          `json:"timeout_secs"`
	Network         json.RawMessage `json:"network"`
	HTTPCredentials []string        `json:"http_credentials"`
}

func (g FunctionGrant) MarshalJSON() ([]byte, error) {
	source, err := json.Marshal(g.Source)
	if err != nil {
		return nil, err
	}
	var reach FunctionNetwork = NoNetwork{}
	if g.Network != nil {
		reach = g.Network
	}
	network, err := json.Marshal(reach)
	if err != nil {
		return nil, err
	}
	return json.Marshal(functionGrantJSON{
		Name:            g.Name,
		Source:          source,
		DefaultPolicy:   g.DefaultPolicy,
		Tools:           orEmpty(g.Tools),
		TimeoutSecs:     g.TimeoutSecs,
		Network:         network,
		HTTPCredentials: orEmpty(g.HTTPCredentials),
	})
}

func (g *FunctionGrant) UnmarshalJSON(data []byte) error {
	var raw functionGrantJSON
	if err := strictUnmarshal(data, &raw); err != nil {
		return err
	}
	source, err := decodeFunctionSource(raw.Source)
	if err != nil {
		return fmt.Errorf("function grant %q: %w", raw.Name, err)
	}
	network, err := decodeFunctionNetwork(raw.Network)
	if err != nil {
		return fmt.Errorf("function grant %q: %w", raw.Name, err)
	}
	*g = FunctionGrant{
		Name:            raw.Name,
		Source:          source,
		DefaultPolicy:   raw.DefaultPolicy,
		Tools:           raw.Tools,
		TimeoutSecs:     raw.TimeoutSecs,
		Network:         network,
		HTTPCredentials: raw.HTTPCredentials,
	}
	return nil
}

// FunctionSource is `FunctionSourceBody`: where a granted package comes
// from. The two types below are its only forms.
type FunctionSource interface {
	json.Marshaler
	sealedFunctionSource()
}

// PlatformFunction is a package the deployment ships.
type PlatformFunction struct {
	Package string
}

// WorkspaceFunction is a package the workspace uploaded. A nil Version
// follows the latest version, resolved when each session is created.
type WorkspaceFunction struct {
	FunctionID string
	Version    *int64
}

func (PlatformFunction) sealedFunctionSource()  {}
func (WorkspaceFunction) sealedFunctionSource() {}

type platformFunctionBody struct {
	Type    string `json:"type"`
	Package string `json:"package"`
}

type workspaceFunctionBody struct {
	Type       string     `json:"type"`
	FunctionID string     `json:"function_id"`
	Version    versionPin `json:"version"`
}

func (s PlatformFunction) MarshalJSON() ([]byte, error) {
	return json.Marshal(platformFunctionBody{Type: "platform", Package: s.Package})
}

func (s WorkspaceFunction) MarshalJSON() ([]byte, error) {
	return json.Marshal(workspaceFunctionBody{Type: "workspace", FunctionID: s.FunctionID, Version: pinOf(s.Version)})
}

func decodeFunctionSource(data []byte) (FunctionSource, error) {
	kind, err := tagOf(data, "type")
	if err != nil {
		return nil, fmt.Errorf("source: %w", err)
	}
	switch kind {
	case "platform":
		var body platformFunctionBody
		if err := strictUnmarshal(data, &body); err != nil {
			return nil, fmt.Errorf("source: %w", err)
		}
		return PlatformFunction{Package: body.Package}, nil
	case "workspace":
		var body workspaceFunctionBody
		if err := strictUnmarshal(data, &body); err != nil {
			return nil, fmt.Errorf("source: %w", err)
		}
		version, err := body.Version.version()
		if err != nil {
			return nil, fmt.Errorf("source: %w", err)
		}
		return WorkspaceFunction{FunctionID: body.FunctionID, Version: version}, nil
	}
	return nil, fmt.Errorf("unknown function source %q", kind)
}

// FunctionNetwork is `FunctionNetworkBody`: where a package's HTTP requests
// may go. The three types below are its only forms.
type FunctionNetwork interface {
	json.Marshaler
	sealedFunctionNetwork()
}

type (
	// NoNetwork lets no request leave.
	NoNetwork struct{}
	// UnrestrictedNetwork reaches any public host.
	UnrestrictedNetwork struct{}
	// AllowlistNetwork reaches only the hosts a pattern admits.
	AllowlistNetwork struct {
		Hosts []string
	}
)

func (NoNetwork) sealedFunctionNetwork()           {}
func (UnrestrictedNetwork) sealedFunctionNetwork() {}
func (AllowlistNetwork) sealedFunctionNetwork()    {}

type allowlistBody struct {
	Type  string   `json:"type"`
	Hosts []string `json:"hosts"`
}

func (NoNetwork) MarshalJSON() ([]byte, error) {
	return json.Marshal(typeTag{"none"})
}

func (UnrestrictedNetwork) MarshalJSON() ([]byte, error) {
	return json.Marshal(typeTag{"unrestricted"})
}

func (n AllowlistNetwork) MarshalJSON() ([]byte, error) {
	return json.Marshal(allowlistBody{Type: "allowlist", Hosts: orEmpty(n.Hosts)})
}

func decodeFunctionNetwork(data []byte) (FunctionNetwork, error) {
	kind, err := tagOf(data, "type")
	if err != nil {
		return nil, fmt.Errorf("network: %w", err)
	}
	if kind == "allowlist" {
		var body allowlistBody
		if err := strictUnmarshal(data, &body); err != nil {
			return nil, fmt.Errorf("network: %w", err)
		}
		return AllowlistNetwork{Hosts: body.Hosts}, nil
	}
	if err := strictUnmarshal(data, &typeTag{}); err != nil {
		return nil, fmt.Errorf("network: %w", err)
	}
	switch kind {
	case "none":
		return NoNetwork{}, nil
	case "unrestricted":
		return UnrestrictedNetwork{}, nil
	}
	return nil, fmt.Errorf("unknown function network %q", kind)
}

// Volume data roots, as `VolumeDataBody` tags them.
const (
	VolumeDataDisabled = "disabled"
	VolumeDataEnabled  = "enabled"
)

// VolumeData are the tags of `VolumeDataBody`.
var VolumeData = []string{VolumeDataDisabled, VolumeDataEnabled}

// VolumeConfig is `VolumeConfigBody`.
type VolumeConfig struct {
	Data string
}

type volumeConfigJSON struct {
	Data typeTag `json:"data"`
}

func (v VolumeConfig) MarshalJSON() ([]byte, error) {
	data := v.Data
	if data == "" {
		data = VolumeDataDisabled
	}
	return json.Marshal(volumeConfigJSON{Data: typeTag{data}})
}

func (v *VolumeConfig) UnmarshalJSON(data []byte) error {
	var raw volumeConfigJSON
	if err := strictUnmarshal(data, &raw); err != nil {
		return err
	}
	if err := oneOf("volume data", raw.Data.Type, VolumeData...); err != nil {
		return err
	}
	*v = VolumeConfig{Data: raw.Data.Type}
	return nil
}

// When an agent names its session, as `DisplayNameWhenBody` spells it.
const (
	DisplayNameNever          = "never"
	DisplayNameEmpty          = "empty"
	DisplayNameUntilUserEdits = "until_user_edits"
)

// DisplayNameWhens are the spellings of `DisplayNameWhenBody`.
var DisplayNameWhens = []string{DisplayNameNever, DisplayNameEmpty, DisplayNameUntilUserEdits}

// BuiltinGrants is `BuiltinGrantsBody`. A nil AskUser grants no forms.
type BuiltinGrants struct {
	SessionDisplayName string
	AskUser            *AskUserGrant
}

type displayNameGrantJSON struct {
	When string `json:"when"`
}

type builtinGrantsJSON struct {
	SessionDisplayName displayNameGrantJSON `json:"session_display_name"`
	AskUser            *AskUserGrant        `json:"ask_user"`
}

func (b BuiltinGrants) MarshalJSON() ([]byte, error) {
	when := b.SessionDisplayName
	if when == "" {
		when = DisplayNameNever
	}
	return json.Marshal(builtinGrantsJSON{
		SessionDisplayName: displayNameGrantJSON{When: when},
		AskUser:            b.AskUser,
	})
}

func (b *BuiltinGrants) UnmarshalJSON(data []byte) error {
	var raw builtinGrantsJSON
	if err := strictUnmarshal(data, &raw); err != nil {
		return err
	}
	if err := oneOf("session display name setting", raw.SessionDisplayName.When, DisplayNameWhens...); err != nil {
		return err
	}
	*b = BuiltinGrants{SessionDisplayName: raw.SessionDisplayName.When, AskUser: raw.AskUser}
	return nil
}

// AskUserGrant is `AskUserGrantBody`: the widgets an agent's forms may use.
type AskUserGrant struct {
	Widgets []Widget
}

type askUserGrantJSON struct {
	Widgets []json.RawMessage `json:"widgets"`
}

func (g AskUserGrant) MarshalJSON() ([]byte, error) {
	widgets := make([]json.RawMessage, len(g.Widgets))
	for i, widget := range g.Widgets {
		encoded, err := json.Marshal(widget)
		if err != nil {
			return nil, err
		}
		widgets[i] = encoded
	}
	return json.Marshal(askUserGrantJSON{Widgets: widgets})
}

func (g *AskUserGrant) UnmarshalJSON(data []byte) error {
	var raw askUserGrantJSON
	if err := strictUnmarshal(data, &raw); err != nil {
		return err
	}
	widgets := make([]Widget, len(raw.Widgets))
	for i, encoded := range raw.Widgets {
		widget, err := decodeWidget(encoded)
		if err != nil {
			return fmt.Errorf("ask_user widget %d: %w", i, err)
		}
		widgets[i] = widget
	}
	*g = AskUserGrant{Widgets: widgets}
	return nil
}

// Widget is `WidgetGrantBody`: one widget a form may use. The four types
// below are its only forms.
type Widget interface {
	json.Marshaler
	sealedWidget()
}

type (
	// SelectWidget is a pick among options the agent writes.
	SelectWidget struct{}
	// TextWidget is free text.
	TextWidget struct{}
	// ConfirmWidget is yes or no.
	ConfirmWidget struct{}
	// CustomWidget is one the app's own clients draw from its ID. Props and
	// Answer are JSON Schemas, held as compact JSON with sorted keys so equal
	// schemas compare equal.
	CustomWidget struct {
		ID          string
		Description string
		Props       string
		Answer      string
	}
)

func (SelectWidget) sealedWidget()  {}
func (TextWidget) sealedWidget()    {}
func (ConfirmWidget) sealedWidget() {}
func (CustomWidget) sealedWidget()  {}

type customWidgetBody struct {
	Type        string          `json:"type"`
	ID          string          `json:"id"`
	Description string          `json:"description"`
	Props       json.RawMessage `json:"props"`
	Answer      json.RawMessage `json:"answer"`
}

func (SelectWidget) MarshalJSON() ([]byte, error)  { return json.Marshal(typeTag{"select"}) }
func (TextWidget) MarshalJSON() ([]byte, error)    { return json.Marshal(typeTag{"text"}) }
func (ConfirmWidget) MarshalJSON() ([]byte, error) { return json.Marshal(typeTag{"confirm"}) }

func (w CustomWidget) MarshalJSON() ([]byte, error) {
	if !json.Valid([]byte(w.Props)) || !json.Valid([]byte(w.Answer)) {
		return nil, fmt.Errorf("custom widget %q: props and answer must be JSON", w.ID)
	}
	return json.Marshal(customWidgetBody{
		Type:        "custom",
		ID:          w.ID,
		Description: w.Description,
		Props:       json.RawMessage(w.Props),
		Answer:      json.RawMessage(w.Answer),
	})
}

func decodeWidget(data []byte) (Widget, error) {
	kind, err := tagOf(data, "type")
	if err != nil {
		return nil, err
	}
	if kind == "custom" {
		var body customWidgetBody
		if err := strictUnmarshal(data, &body); err != nil {
			return nil, err
		}
		return canonicalWidget(CustomWidget{
			ID:          body.ID,
			Description: body.Description,
			Props:       string(body.Props),
			Answer:      string(body.Answer),
		}), nil
	}
	if err := strictUnmarshal(data, &typeTag{}); err != nil {
		return nil, err
	}
	switch kind {
	case "select":
		return SelectWidget{}, nil
	case "text":
		return TextWidget{}, nil
	case "confirm":
		return ConfirmWidget{}, nil
	}
	return nil, fmt.Errorf("unknown widget %q", kind)
}

// canonicalWidget respells a custom widget's schemas compactly with sorted
// keys. A schema that does not parse is left as written, for the server to
// refuse.
func canonicalWidget(widget Widget) Widget {
	custom, ok := widget.(CustomWidget)
	if !ok {
		return widget
	}
	custom.Props = CanonicalJSON(custom.Props)
	custom.Answer = CanonicalJSON(custom.Answer)
	return custom
}

// CanonicalJSON respells a JSON document compactly with sorted object keys,
// or returns it unchanged when it does not parse.
func CanonicalJSON(document string) string {
	var value any
	decoder := json.NewDecoder(bytes.NewReader([]byte(document)))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return document
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return document
	}
	return string(encoded)
}
