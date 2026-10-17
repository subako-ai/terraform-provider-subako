package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework-jsontypes/jsontypes"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-framework/types/basetypes"

	"github.com/subako-ai/terraform-provider-subako/internal/client"
)

// The structs below are the agent schema's nested objects as the framework
// reflects them. They are read only from values that are all known -- a plan
// is compared and sent only once it is -- so a nested list is a Go slice and
// an optional object a pointer, nil where the value is null.

type agentModelConfig struct {
	Anthropic       *anthropicModelConfig       `tfsdk:"anthropic"`
	OpenAIResponses *openaiResponsesModelConfig `tfsdk:"openai_responses"`
}

type anthropicModelConfig struct {
	Name          types.String   `tfsdk:"name"`
	MaxTokens     types.Int64    `tfsdk:"max_tokens"`
	ContextWindow types.Int64    `tfsdk:"context_window"`
	ImageInput    types.String   `tfsdk:"image_input"`
	Thinking      *thinkingModel `tfsdk:"thinking"`
	Effort        types.String   `tfsdk:"effort"`
}

type openaiResponsesModelConfig struct {
	Name            types.String `tfsdk:"name"`
	MaxTokens       types.Int64  `tfsdk:"max_tokens"`
	ContextWindow   types.Int64  `tfsdk:"context_window"`
	ImageInput      types.String `tfsdk:"image_input"`
	ReasoningEffort types.String `tfsdk:"reasoning_effort"`
}

// emptyObject is a variant that carries no settings.
type emptyObject struct{}

type thinkingModel struct {
	Adaptive     *emptyObject          `tfsdk:"adaptive"`
	Disabled     *emptyObject          `tfsdk:"disabled"`
	BetweenTools *emptyObject          `tfsdk:"between_tools"`
	Enabled      *enabledThinkingModel `tfsdk:"enabled"`
}

type enabledThinkingModel struct {
	BudgetTokens types.Int64 `tfsdk:"budget_tokens"`
}

type toolRuleModel struct {
	Name   types.String `tfsdk:"name"`
	Policy types.String `tfsdk:"policy"`
}

type mcpServerModel struct {
	Name          types.String    `tfsdk:"name"`
	URL           types.String    `tfsdk:"url"`
	DefaultPolicy types.String    `tfsdk:"default_policy"`
	Tools         []toolRuleModel `tfsdk:"tools"`
}

type skillGrantModel struct {
	Name    types.String `tfsdk:"name"`
	SkillID types.String `tfsdk:"skill_id"`
	Version types.Int64  `tfsdk:"version"`
}

type sandboxGrantModel struct {
	Name          types.String     `tfsdk:"name"`
	Environment   environmentModel `tfsdk:"environment"`
	Network       types.String     `tfsdk:"network"`
	DefaultPolicy types.String     `tfsdk:"default_policy"`
	Tools         []toolRuleModel  `tfsdk:"tools"`
}

type environmentModel struct {
	Platform *platformEnvironmentModel `tfsdk:"platform"`
}

type platformEnvironmentModel struct {
	Release types.String `tfsdk:"release"`
}

type functionGrantModel struct {
	Name            types.String         `tfsdk:"name"`
	Source          functionSourceModel  `tfsdk:"source"`
	DefaultPolicy   types.String         `tfsdk:"default_policy"`
	Tools           []toolRuleModel      `tfsdk:"tools"`
	TimeoutSecs     types.Int64          `tfsdk:"timeout_secs"`
	Network         functionNetworkModel `tfsdk:"network"`
	HTTPCredentials []types.String       `tfsdk:"http_credentials"`
}

type functionSourceModel struct {
	Platform  *platformFunctionModel  `tfsdk:"platform"`
	Workspace *workspaceFunctionModel `tfsdk:"workspace"`
}

type platformFunctionModel struct {
	Package types.String `tfsdk:"package"`
}

type workspaceFunctionModel struct {
	FunctionID types.String `tfsdk:"function_id"`
	Version    types.Int64  `tfsdk:"version"`
}

type functionNetworkModel struct {
	None         *emptyObject    `tfsdk:"none"`
	Unrestricted *emptyObject    `tfsdk:"unrestricted"`
	Allowlist    *allowlistModel `tfsdk:"allowlist"`
}

type allowlistModel struct {
	Hosts []types.String `tfsdk:"hosts"`
}

type volumeModel struct {
	Data types.String `tfsdk:"data"`
}

type builtinModel struct {
	SessionDisplayName types.String  `tfsdk:"session_display_name"`
	AskUser            *askUserModel `tfsdk:"ask_user"`
}

type askUserModel struct {
	Widgets []widgetModel `tfsdk:"widgets"`
}

type widgetModel struct {
	Select  *emptyObject       `tfsdk:"select"`
	Text    *emptyObject       `tfsdk:"text"`
	Confirm *emptyObject       `tfsdk:"confirm"`
	Custom  *customWidgetModel `tfsdk:"custom"`
}

type customWidgetModel struct {
	ID          types.String         `tfsdk:"id"`
	Description types.String         `tfsdk:"description"`
	Props       jsontypes.Normalized `tfsdk:"props"`
	Answer      jsontypes.Normalized `tfsdk:"answer"`
}

// ambiguous raises a sum type's object holding other than one variant. The
// schema refuses such a configuration, so reaching this is a provider bug.
func ambiguous(diags *diag.Diagnostics, at string) {
	diags.AddAttributeError(path.Root(at), "Ambiguous "+at, "Exactly one variant must be set.")
}

// configValues are the agent attributes that make up a version's config.
func (m agentModel) configValues() []attr.Value {
	return []attr.Value{
		m.ModelProviderID, m.SystemPrompt, m.Model, m.MCPServers, m.Skills,
		m.Sandboxes, m.Functions, m.Volume, m.Builtin,
	}
}

// agentConfig reads the config a version should carry out of a model whose
// config values are all known.
func agentConfig(ctx context.Context, m agentModel) (client.AgentConfig, diag.Diagnostics) {
	var diags diag.Diagnostics
	var model agentModelConfig
	diags.Append(m.Model.As(ctx, &model, basetypes.ObjectAsOptions{})...)
	var servers []mcpServerModel
	diags.Append(m.MCPServers.ElementsAs(ctx, &servers, false)...)
	var skills []skillGrantModel
	diags.Append(m.Skills.ElementsAs(ctx, &skills, false)...)
	var sandboxes []sandboxGrantModel
	diags.Append(m.Sandboxes.ElementsAs(ctx, &sandboxes, false)...)
	var functions []functionGrantModel
	diags.Append(m.Functions.ElementsAs(ctx, &functions, false)...)
	var volume volumeModel
	diags.Append(m.Volume.As(ctx, &volume, basetypes.ObjectAsOptions{})...)
	var builtin builtinModel
	diags.Append(m.Builtin.As(ctx, &builtin, basetypes.ObjectAsOptions{})...)
	if diags.HasError() {
		return client.AgentConfig{}, diags
	}

	config := client.AgentConfig{
		ModelProviderID: m.ModelProviderID.ValueString(),
		SystemPrompt:    stringPointer(m.SystemPrompt),
		Model:           model.wire(&diags),
		Volume:          client.VolumeConfig{Data: volume.Data.ValueString()},
		Builtin:         builtin.wire(&diags),
	}
	for _, server := range servers {
		config.MCP = append(config.MCP, client.MCPGrant{
			Name:          server.Name.ValueString(),
			URL:           server.URL.ValueString(),
			DefaultPolicy: server.DefaultPolicy.ValueString(),
			Tools:         toolRules(server.Tools),
		})
	}
	for _, skill := range skills {
		config.Skills = append(config.Skills, client.SkillGrant{
			Name:    skill.Name.ValueString(),
			SkillID: skill.SkillID.ValueString(),
			Version: int64Pointer(skill.Version),
		})
	}
	for _, sandbox := range sandboxes {
		if sandbox.Environment.Platform == nil {
			ambiguous(&diags, "sandboxes")
			continue
		}
		config.Sandboxes = append(config.Sandboxes, client.SandboxGrant{
			Name:          sandbox.Name.ValueString(),
			Release:       sandbox.Environment.Platform.Release.ValueString(),
			Network:       sandbox.Network.ValueString(),
			DefaultPolicy: sandbox.DefaultPolicy.ValueString(),
			Tools:         toolRules(sandbox.Tools),
		})
	}
	for _, function := range functions {
		config.Functions = append(config.Functions, function.wire(&diags))
	}
	if diags.HasError() {
		return client.AgentConfig{}, diags
	}
	return config.Canonical(), diags
}

func (m agentModelConfig) wire(diags *diag.Diagnostics) client.ModelConfig {
	switch {
	case m.Anthropic != nil && m.OpenAIResponses == nil:
		return client.AnthropicModel{
			Model:         m.Anthropic.Name.ValueString(),
			MaxTokens:     m.Anthropic.MaxTokens.ValueInt64(),
			ContextWindow: m.Anthropic.ContextWindow.ValueInt64(),
			ImageInput:    m.Anthropic.ImageInput.ValueString(),
			Thinking:      m.Anthropic.Thinking.wire(diags),
			Effort:        stringPointer(m.Anthropic.Effort),
		}
	case m.OpenAIResponses != nil && m.Anthropic == nil:
		return client.OpenAIResponsesModel{
			Model:           m.OpenAIResponses.Name.ValueString(),
			MaxTokens:       m.OpenAIResponses.MaxTokens.ValueInt64(),
			ContextWindow:   m.OpenAIResponses.ContextWindow.ValueInt64(),
			ImageInput:      m.OpenAIResponses.ImageInput.ValueString(),
			ReasoningEffort: stringPointer(m.OpenAIResponses.ReasoningEffort),
		}
	}
	ambiguous(diags, "model")
	return nil
}

// wire is nil for thinking left unset, the model's own default.
func (t *thinkingModel) wire(diags *diag.Diagnostics) client.AnthropicThinking {
	switch {
	case t == nil:
		return nil
	case t.Adaptive != nil:
		return client.AdaptiveThinking{}
	case t.Disabled != nil:
		return client.DisabledThinking{}
	case t.BetweenTools != nil:
		return client.BetweenToolsThinking{}
	case t.Enabled != nil:
		return client.EnabledThinking{BudgetTokens: t.Enabled.BudgetTokens.ValueInt64()}
	}
	ambiguous(diags, "model")
	return nil
}

func toolRules(tools []toolRuleModel) []client.ToolRule {
	var rules []client.ToolRule
	for _, tool := range tools {
		rules = append(rules, client.ToolRule{Name: tool.Name.ValueString(), Policy: tool.Policy.ValueString()})
	}
	return rules
}

func stringValues(values []types.String) []string {
	var out []string
	for _, value := range values {
		out = append(out, value.ValueString())
	}
	return out
}

func (f functionGrantModel) wire(diags *diag.Diagnostics) client.FunctionGrant {
	grant := client.FunctionGrant{
		Name:            f.Name.ValueString(),
		DefaultPolicy:   f.DefaultPolicy.ValueString(),
		Tools:           toolRules(f.Tools),
		TimeoutSecs:     int64Pointer(f.TimeoutSecs),
		HTTPCredentials: stringValues(f.HTTPCredentials),
	}
	switch source := f.Source; {
	case source.Platform != nil:
		grant.Source = client.PlatformFunction{Package: source.Platform.Package.ValueString()}
	case source.Workspace != nil:
		grant.Source = client.WorkspaceFunction{
			FunctionID: source.Workspace.FunctionID.ValueString(),
			Version:    int64Pointer(source.Workspace.Version),
		}
	default:
		ambiguous(diags, "functions")
	}
	switch network := f.Network; {
	case network.None != nil:
		grant.Network = client.NoNetwork{}
	case network.Unrestricted != nil:
		grant.Network = client.UnrestrictedNetwork{}
	case network.Allowlist != nil:
		grant.Network = client.AllowlistNetwork{Hosts: stringValues(network.Allowlist.Hosts)}
	default:
		ambiguous(diags, "functions")
	}
	return grant
}

func (b builtinModel) wire(diags *diag.Diagnostics) client.BuiltinGrants {
	grants := client.BuiltinGrants{SessionDisplayName: b.SessionDisplayName.ValueString()}
	if b.AskUser == nil {
		return grants
	}
	grants.AskUser = &client.AskUserGrant{}
	for _, widget := range b.AskUser.Widgets {
		switch {
		case widget.Select != nil:
			grants.AskUser.Widgets = append(grants.AskUser.Widgets, client.SelectWidget{})
		case widget.Text != nil:
			grants.AskUser.Widgets = append(grants.AskUser.Widgets, client.TextWidget{})
		case widget.Confirm != nil:
			grants.AskUser.Widgets = append(grants.AskUser.Widgets, client.ConfirmWidget{})
		case widget.Custom != nil:
			grants.AskUser.Widgets = append(grants.AskUser.Widgets, client.CustomWidget{
				ID:          widget.Custom.ID.ValueString(),
				Description: widget.Custom.Description.ValueString(),
				Props:       widget.Custom.Props.ValueString(),
				Answer:      widget.Custom.Answer.ValueString(),
			})
		default:
			ambiguous(diags, "builtin")
		}
	}
	return grants
}

func optionalInt64(n *int64) types.Int64 {
	if n == nil {
		return types.Int64Null()
	}
	return types.Int64Value(*n)
}

func toolRuleModels(rules []client.ToolRule) []toolRuleModel {
	out := []toolRuleModel{}
	for _, rule := range rules {
		out = append(out, toolRuleModel{Name: types.StringValue(rule.Name), Policy: types.StringValue(rule.Policy)})
	}
	return out
}

func stringModels(values []string) []types.String {
	out := []types.String{}
	for _, value := range values {
		out = append(out, types.StringValue(value))
	}
	return out
}

func thinkingOf(thinking client.AnthropicThinking) *thinkingModel {
	switch t := thinking.(type) {
	case client.AdaptiveThinking:
		return &thinkingModel{Adaptive: &emptyObject{}}
	case client.DisabledThinking:
		return &thinkingModel{Disabled: &emptyObject{}}
	case client.BetweenToolsThinking:
		return &thinkingModel{BetweenTools: &emptyObject{}}
	case client.EnabledThinking:
		return &thinkingModel{Enabled: &enabledThinkingModel{BudgetTokens: types.Int64Value(t.BudgetTokens)}}
	}
	return nil
}

func modelOf(model client.ModelConfig) agentModelConfig {
	switch m := model.(type) {
	case client.AnthropicModel:
		return agentModelConfig{Anthropic: &anthropicModelConfig{
			Name:          types.StringValue(m.Model),
			MaxTokens:     types.Int64Value(m.MaxTokens),
			ContextWindow: types.Int64Value(m.ContextWindow),
			ImageInput:    types.StringValue(m.ImageInput),
			Thinking:      thinkingOf(m.Thinking),
			Effort:        types.StringPointerValue(m.Effort),
		}}
	case client.OpenAIResponsesModel:
		return agentModelConfig{OpenAIResponses: &openaiResponsesModelConfig{
			Name:            types.StringValue(m.Model),
			MaxTokens:       types.Int64Value(m.MaxTokens),
			ContextWindow:   types.Int64Value(m.ContextWindow),
			ImageInput:      types.StringValue(m.ImageInput),
			ReasoningEffort: types.StringPointerValue(m.ReasoningEffort),
		}}
	}
	return agentModelConfig{}
}

func functionGrantOf(grant client.FunctionGrant) functionGrantModel {
	out := functionGrantModel{
		Name:            types.StringValue(grant.Name),
		DefaultPolicy:   types.StringValue(grant.DefaultPolicy),
		Tools:           toolRuleModels(grant.Tools),
		TimeoutSecs:     optionalInt64(grant.TimeoutSecs),
		HTTPCredentials: stringModels(grant.HTTPCredentials),
	}
	switch source := grant.Source.(type) {
	case client.PlatformFunction:
		out.Source.Platform = &platformFunctionModel{Package: types.StringValue(source.Package)}
	case client.WorkspaceFunction:
		out.Source.Workspace = &workspaceFunctionModel{
			FunctionID: types.StringValue(source.FunctionID),
			Version:    optionalInt64(source.Version),
		}
	}
	switch network := grant.Network.(type) {
	case client.NoNetwork:
		out.Network.None = &emptyObject{}
	case client.UnrestrictedNetwork:
		out.Network.Unrestricted = &emptyObject{}
	case client.AllowlistNetwork:
		out.Network.Allowlist = &allowlistModel{Hosts: stringModels(network.Hosts)}
	}
	return out
}

func builtinOf(grants client.BuiltinGrants) builtinModel {
	out := builtinModel{SessionDisplayName: types.StringValue(grants.SessionDisplayName)}
	if grants.AskUser == nil {
		return out
	}
	out.AskUser = &askUserModel{Widgets: []widgetModel{}}
	for _, widget := range grants.AskUser.Widgets {
		var model widgetModel
		switch w := widget.(type) {
		case client.SelectWidget:
			model.Select = &emptyObject{}
		case client.TextWidget:
			model.Text = &emptyObject{}
		case client.ConfirmWidget:
			model.Confirm = &emptyObject{}
		case client.CustomWidget:
			model.Custom = &customWidgetModel{
				ID:          types.StringValue(w.ID),
				Description: types.StringValue(w.Description),
				Props:       jsontypes.NewNormalizedValue(w.Props),
				Answer:      jsontypes.NewNormalizedValue(w.Answer),
			}
		}
		out.AskUser.Widgets = append(out.AskUser.Widgets, model)
	}
	return out
}

// setAgentConfig writes a version's config into m, every list spelled out:
// the schema's defaults plan an empty list for one left out.
func setAgentConfig(ctx context.Context, m *agentModel, config client.AgentConfig) diag.Diagnostics {
	var diags diag.Diagnostics
	m.ModelProviderID = types.StringValue(config.ModelProviderID)
	m.SystemPrompt = types.StringPointerValue(config.SystemPrompt)

	var d diag.Diagnostics
	m.Model, d = types.ObjectValueFrom(ctx, agentModelType().AttrTypes, modelOf(config.Model))
	diags.Append(d...)

	servers := []mcpServerModel{}
	for _, grant := range config.MCP {
		servers = append(servers, mcpServerModel{
			Name:          types.StringValue(grant.Name),
			URL:           types.StringValue(grant.URL),
			DefaultPolicy: types.StringValue(grant.DefaultPolicy),
			Tools:         toolRuleModels(grant.Tools),
		})
	}
	m.MCPServers, d = types.ListValueFrom(ctx, mcpServerType, servers)
	diags.Append(d...)

	skills := []skillGrantModel{}
	for _, grant := range config.Skills {
		skills = append(skills, skillGrantModel{
			Name:    types.StringValue(grant.Name),
			SkillID: types.StringValue(grant.SkillID),
			Version: optionalInt64(grant.Version),
		})
	}
	m.Skills, d = types.ListValueFrom(ctx, skillGrantType, skills)
	diags.Append(d...)

	sandboxes := []sandboxGrantModel{}
	for _, grant := range config.Sandboxes {
		sandboxes = append(sandboxes, sandboxGrantModel{
			Name:          types.StringValue(grant.Name),
			Environment:   environmentModel{Platform: &platformEnvironmentModel{Release: types.StringValue(grant.Release)}},
			Network:       types.StringValue(grant.Network),
			DefaultPolicy: types.StringValue(grant.DefaultPolicy),
			Tools:         toolRuleModels(grant.Tools),
		})
	}
	m.Sandboxes, d = types.ListValueFrom(ctx, sandboxGrantType, sandboxes)
	diags.Append(d...)

	functions := []functionGrantModel{}
	for _, grant := range config.Functions {
		functions = append(functions, functionGrantOf(grant))
	}
	m.Functions, d = types.ListValueFrom(ctx, functionGrantType, functions)
	diags.Append(d...)

	m.Volume, d = types.ObjectValueFrom(ctx, volumeType.AttrTypes, volumeModel{Data: types.StringValue(config.Volume.Data)})
	diags.Append(d...)
	m.Builtin, d = types.ObjectValueFrom(ctx, builtinType.AttrTypes, builtinOf(config.Builtin))
	diags.Append(d...)
	return diags
}

// clearAgentConfig empties m's config, for an agent holding no version.
func clearAgentConfig(m *agentModel) {
	m.ModelProviderID = types.StringNull()
	m.SystemPrompt = types.StringNull()
	m.Model = types.ObjectNull(agentModelType().AttrTypes)
	m.MCPServers = types.ListNull(mcpServerType)
	m.Skills = types.ListNull(skillGrantType)
	m.Sandboxes = types.ListNull(sandboxGrantType)
	m.Functions = types.ListNull(functionGrantType)
	m.Volume = types.ObjectNull(volumeType.AttrTypes)
	m.Builtin = types.ObjectNull(builtinType.AttrTypes)
}

// agentModelType is the type the schema gives `model`.
func agentModelType() types.ObjectType {
	return modelAttribute().GetType().(types.ObjectType)
}
