package provider

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-jsontypes/jsontypes"
	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/listvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/setvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/defaults"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/listdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/objectdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/setplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/subako-ai/terraform-provider-subako/internal/client"
)

// The agent schema mirrors `AgentConfigBody` field for field. A value the
// server fills in when it is left out has that value as its default here, so
// a configuration leaving it out plans what a version reads back. A sum type
// on the wire is a nested object holding one attribute per variant, which
// exactlyOneVariant leaves exactly one of set.

// agentSchemaVersion is the version of the state the schema below writes.
const agentSchemaVersion = 1

// maxTokenCount is what the server's token counts are: a u32.
const maxTokenCount = 4294967295

func tokenCountValidators() []validator.Int64 {
	return []validator.Int64{int64validator.Between(0, maxTokenCount)}
}

func oneOf(values []string) []validator.String {
	return []validator.String{stringvalidator.OneOf(values...)}
}

// CODESYNC(platform-release-charset)
// CODESYNC(package-name-charset)
// CODESYNC(widget-id-charset)
var (
	platformReleasePattern = regexp.MustCompile(`^[a-z0-9-]+$`)
	packageNamePattern     = regexp.MustCompile(`^[a-z0-9-]+$`)
	widgetIDPattern        = regexp.MustCompile(`^[a-z0-9_-]+$`)
)

// variant is one alternative of a sum type, as the nested object that
// carries its settings.
func variant(description string, attributes map[string]schema.Attribute) schema.SingleNestedAttribute {
	return schema.SingleNestedAttribute{
		Optional:    true,
		Description: description,
		Attributes:  attributes,
	}
}

// emptyVariant is an alternative that carries no settings: `{}` selects it.
func emptyVariant(description string) schema.SingleNestedAttribute {
	return variant(description, map[string]schema.Attribute{})
}

// toolsAttribute is a grant's per-tool policies.
func toolsAttribute() schema.ListNestedAttribute {
	policy := oneOf(client.PolicyActions)
	return schema.ListNestedAttribute{
		Optional:    true,
		Computed:    true,
		Description: "Per-tool policies. A tool not named here takes `default_policy`.",
		Default:     emptyList(toolRuleType),
		NestedObject: schema.NestedAttributeObject{
			Attributes: map[string]schema.Attribute{
				"name":   schema.StringAttribute{Required: true},
				"policy": schema.StringAttribute{Required: true, Validators: policy},
			},
		},
	}
}

var toolRuleType = types.ObjectType{AttrTypes: map[string]attr.Type{
	"name":   types.StringType,
	"policy": types.StringType,
}}

func defaultPolicyAttribute(applies string) schema.StringAttribute {
	return schema.StringAttribute{
		Required:    true,
		Description: "Applies to every tool `tools` does not name, including one " + applies + ".",
		Validators:  oneOf(client.PolicyActions),
	}
}

// emptyList is an optional list's default: left out, a grant list is empty.
func emptyList(elem attr.Type) defaults.List {
	return listdefault.StaticValue(types.ListValueMust(elem, []attr.Value{}))
}

func modelAttribute() schema.SingleNestedAttribute {
	imageInput := func() schema.StringAttribute {
		return schema.StringAttribute{
			Optional:    true,
			Computed:    true,
			Description: "Whether the model reads images: `supported` or `unsupported`. A model that does not is shown a note in place of each image a tool returns.",
			Default:     stringdefault.StaticString(client.ImageInputSupported),
			Validators:  oneOf(client.ImageInputs),
		}
	}
	return schema.SingleNestedAttribute{
		Required: true,
		Description: "The model the agent runs, as exactly one of `anthropic` and " +
			"`openai_responses`. The one you set must match the model provider's format.",
		Validators: []validator.Object{exactlyOneVariant()},
		Attributes: map[string]schema.Attribute{
			client.FormatAnthropic: variant("An Anthropic Messages model.", map[string]schema.Attribute{
				"name": schema.StringAttribute{
					Required:    true,
					Description: "The model name, as Anthropic spells it.",
				},
				"max_tokens": schema.Int64Attribute{
					Required:    true,
					Description: "Bounds one model call: its thinking, its answer, and its tool calls together.",
					Validators:  tokenCountValidators(),
				},
				"context_window": schema.Int64Attribute{
					Required:    true,
					Description: "The most the model reads at once.",
					Validators:  tokenCountValidators(),
				},
				"image_input": imageInput(),
				"thinking": schema.SingleNestedAttribute{
					Optional: true,
					Description: "How the model thinks before it answers, as exactly one of `adaptive`, " +
						"`disabled`, `between_tools`, and `enabled`. Left unset, the model's own default.",
					Validators: []validator.Object{exactlyOneVariant()},
					Attributes: map[string]schema.Attribute{
						client.ThinkingAdaptive:     emptyVariant("The model decides how much to think."),
						client.ThinkingDisabled:     emptyVariant("No thinking."),
						client.ThinkingBetweenTools: emptyVariant("Thinking between tool calls."),
						client.ThinkingEnabled: variant("Thinking within a fixed budget.", map[string]schema.Attribute{
							"budget_tokens": schema.Int64Attribute{Required: true, Validators: tokenCountValidators()},
						}),
					},
				},
				"effort": schema.StringAttribute{
					Optional:    true,
					Description: "How much effort the model spends. Left unset, the model's own default.",
					Validators:  oneOf(client.AnthropicEfforts),
				},
			}),
			client.FormatOpenAIResponses: variant("An OpenAI Responses model.", map[string]schema.Attribute{
				"name": schema.StringAttribute{
					Required:    true,
					Description: "The model name, as the provider spells it.",
				},
				"max_tokens": schema.Int64Attribute{
					Required:   true,
					Validators: tokenCountValidators(),
				},
				"context_window": schema.Int64Attribute{
					Required:   true,
					Validators: tokenCountValidators(),
				},
				"image_input": imageInput(),
				"reasoning_effort": schema.StringAttribute{
					Optional:    true,
					Description: "Left unset, the model's own default.",
					Validators:  oneOf(client.ReasoningEfforts),
				},
			}),
		},
	}
}

func mcpServersAttribute() schema.ListNestedAttribute {
	return schema.ListNestedAttribute{
		Optional:    true,
		Computed:    true,
		Description: "MCP servers the agent may reach, each under a tool policy.",
		Default:     emptyList(mcpServerType),
		NestedObject: schema.NestedAttributeObject{
			Attributes: map[string]schema.Attribute{
				"name": schema.StringAttribute{
					Required:    true,
					Description: "Unique within `mcp_servers`.",
					Validators:  grantNameValidators(),
				},
				"url":            schema.StringAttribute{Required: true},
				"default_policy": defaultPolicyAttribute("the server adds after this version is published"),
				"tools":          toolsAttribute(),
			},
		},
	}
}

var mcpServerType = types.ObjectType{AttrTypes: map[string]attr.Type{
	"name":           types.StringType,
	"url":            types.StringType,
	"default_policy": types.StringType,
	"tools":          types.ListType{ElemType: toolRuleType},
}}

func skillsAttribute() schema.ListNestedAttribute {
	return schema.ListNestedAttribute{
		Optional:    true,
		Computed:    true,
		Description: "Skills the agent may read.",
		Default:     emptyList(skillGrantType),
		NestedObject: schema.NestedAttributeObject{
			Attributes: map[string]schema.Attribute{
				"name": schema.StringAttribute{
					Required:    true,
					Description: "Unique within `skills`.",
					Validators:  grantNameValidators(),
				},
				"skill_id": schema.StringAttribute{
					Required:   true,
					Validators: uuidValidators(),
				},
				"version": schema.Int64Attribute{
					Optional: true,
					Description: "Pins one skill version. Left unset, the latest version, " +
						"resolved when each session is created.",
					Validators: []validator.Int64{int64validator.AtLeast(1)},
				},
			},
		},
	}
}

var skillGrantType = types.ObjectType{AttrTypes: map[string]attr.Type{
	"name":     types.StringType,
	"skill_id": types.StringType,
	"version":  types.Int64Type,
}}

func sandboxesAttribute() schema.ListNestedAttribute {
	return schema.ListNestedAttribute{
		Optional:    true,
		Computed:    true,
		Description: "Linux sandboxes each session owns, each served with an `exec` tool.",
		Default:     emptyList(sandboxGrantType),
		NestedObject: schema.NestedAttributeObject{
			Attributes: map[string]schema.Attribute{
				"name": schema.StringAttribute{
					Required:    true,
					Description: "Unique within `sandboxes`.",
					Validators:  grantNameValidators(),
				},
				"environment": schema.SingleNestedAttribute{
					Required:    true,
					Description: "The image the sandbox boots, as `platform`.",
					Validators:  []validator.Object{exactlyOneVariant()},
					Attributes: map[string]schema.Attribute{
						"platform": variant("The deployment's current image for a platform release line.", map[string]schema.Attribute{
							"release": schema.StringAttribute{
								Required:    true,
								Description: "The release line, e.g. `2026`. A line the deployment does not offer is refused at apply.",
								Validators: []validator.String{
									// CODESYNC(platform-release-len)
									charsBetween(1, 32),
									stringvalidator.RegexMatches(platformReleasePattern, "must be lowercase alphanumeric plus hyphen"),
								},
							},
						}),
					},
				},
				"network": schema.StringAttribute{
					Optional: true,
					Computed: true,
					Description: "Where the sandbox may reach: `unrestricted`, the internet, or `none`, which still " +
						"reaches the platform's own storage.",
					Default:    stringdefault.StaticString(client.SandboxNetworkUnrestricted),
					Validators: oneOf(client.SandboxNetworks),
				},
				"default_policy": defaultPolicyAttribute("the platform adds after this version is published"),
				"tools":          toolsAttribute(),
			},
		},
	}
}

var (
	emptyObjectType  = types.ObjectType{AttrTypes: map[string]attr.Type{}}
	sandboxGrantType = types.ObjectType{AttrTypes: map[string]attr.Type{
		"name": types.StringType,
		"environment": types.ObjectType{AttrTypes: map[string]attr.Type{
			"platform": types.ObjectType{AttrTypes: map[string]attr.Type{"release": types.StringType}},
		}},
		"network":        types.StringType,
		"default_policy": types.StringType,
		"tools":          types.ListType{ElemType: toolRuleType},
	}}
	functionNetworkType = types.ObjectType{AttrTypes: map[string]attr.Type{
		"none":         emptyObjectType,
		"unrestricted": emptyObjectType,
		"allowlist":    types.ObjectType{AttrTypes: map[string]attr.Type{"hosts": types.ListType{ElemType: types.StringType}}},
	}}
	functionGrantType = types.ObjectType{AttrTypes: map[string]attr.Type{
		"name": types.StringType,
		"source": types.ObjectType{AttrTypes: map[string]attr.Type{
			"platform": types.ObjectType{AttrTypes: map[string]attr.Type{"package": types.StringType}},
			"workspace": types.ObjectType{AttrTypes: map[string]attr.Type{
				"function_id": types.StringType,
				"version":     types.Int64Type,
			}},
		}},
		"default_policy":   types.StringType,
		"tools":            types.ListType{ElemType: toolRuleType},
		"timeout_secs":     types.Int64Type,
		"network":          functionNetworkType,
		"http_credentials": types.ListType{ElemType: types.StringType},
	}}
)

// noNetwork is a function grant's network left out: no request leaves.
var noNetwork = types.ObjectValueMust(functionNetworkType.AttrTypes, map[string]attr.Value{
	"none":         types.ObjectValueMust(emptyObjectType.AttrTypes, map[string]attr.Value{}),
	"unrestricted": types.ObjectNull(emptyObjectType.AttrTypes),
	"allowlist":    types.ObjectNull(functionNetworkType.AttrTypes["allowlist"].(types.ObjectType).AttrTypes),
})

func functionsAttribute() schema.ListNestedAttribute {
	return schema.ListNestedAttribute{
		Optional:    true,
		Computed:    true,
		Description: "Function packages whose tools the agent may call.",
		Default:     emptyList(functionGrantType),
		NestedObject: schema.NestedAttributeObject{
			Attributes: map[string]schema.Attribute{
				"name": schema.StringAttribute{
					Required:    true,
					Description: "Unique within `functions`.",
					Validators:  grantNameValidators(),
				},
				"source": schema.SingleNestedAttribute{
					Required:    true,
					Description: "Where the package comes from, as exactly one of `platform` and `workspace`.",
					Validators:  []validator.Object{exactlyOneVariant()},
					Attributes: map[string]schema.Attribute{
						"platform": variant("A package the deployment ships.", map[string]schema.Attribute{
							"package": schema.StringAttribute{
								Required:    true,
								Description: "The package name, e.g. `files`.",
								Validators: []validator.String{
									// CODESYNC(grant-name-len)
									charsBetween(1, 32),
									stringvalidator.RegexMatches(packageNamePattern, "must be lowercase alphanumeric plus hyphen"),
								},
							},
						}),
						"workspace": variant("A package this workspace uploaded, such as a `subako_function`.", map[string]schema.Attribute{
							"function_id": schema.StringAttribute{
								Required:   true,
								Validators: uuidValidators(),
							},
							"version": schema.Int64Attribute{
								Optional: true,
								Description: "Pins one package version. Left unset, the latest version, " +
									"resolved when each session is created.",
								Validators: []validator.Int64{int64validator.AtLeast(1)},
							},
						}),
					},
				},
				"default_policy": defaultPolicyAttribute("the package adds after this version is published"),
				"tools":          toolsAttribute(),
				"timeout_secs": schema.Int64Attribute{
					Optional:    true,
					Description: "How long one call may run, in seconds. Left unset, 60.",
					// CODESYNC(function-max-timeout)
					Validators: []validator.Int64{int64validator.Between(1, 300)},
				},
				"network": schema.SingleNestedAttribute{
					Optional: true,
					Computed: true,
					Description: "Where the package's HTTP requests may go, as exactly one of `none`, " +
						"`unrestricted`, and `allowlist`. Left unset, `none`. Every one refuses private and " +
						"metadata addresses.",
					Default:    objectdefault.StaticValue(noNetwork),
					Validators: []validator.Object{exactlyOneVariant()},
					Attributes: map[string]schema.Attribute{
						"none":         emptyVariant("No request leaves."),
						"unrestricted": emptyVariant("Any public host."),
						"allowlist": variant("Only the hosts a pattern admits.", map[string]schema.Attribute{
							"hosts": schema.ListAttribute{
								Required:    true,
								ElementType: types.StringType,
								// CODESYNC(function-allowlist-len)
								Validators: []validator.List{listvalidator.SizeBetween(1, 64)},
							},
						}),
					},
				},
				"http_credentials": schema.ListAttribute{
					Optional:    true,
					Computed:    true,
					ElementType: types.StringType,
					Description: "Origins whose credential, from the session's vaults, is attached to each " +
						"request the package sends there. Each must be reachable under `network`.",
					Default: emptyList(types.StringType),
					// CODESYNC(function-http-credentials-len)
					Validators: []validator.List{listvalidator.SizeAtMost(16)},
				},
			},
		},
	}
}

var volumeType = types.ObjectType{AttrTypes: map[string]attr.Type{"data": types.StringType}}

func volumeAttribute() schema.SingleNestedAttribute {
	return schema.SingleNestedAttribute{
		Optional:    true,
		Computed:    true,
		Description: "The session tree's file volume.",
		Default: objectdefault.StaticValue(types.ObjectValueMust(volumeType.AttrTypes, map[string]attr.Value{
			"data": types.StringValue(client.VolumeDataDisabled),
		})),
		Attributes: map[string]schema.Attribute{
			"data": schema.StringAttribute{
				Optional:    true,
				Computed:    true,
				Description: "`enabled` mounts a persistent `/volume/data` root for the session tree's functions.",
				Default:     stringdefault.StaticString(client.VolumeDataDisabled),
				Validators:  oneOf(client.VolumeData),
			},
		},
	}
}

var (
	customWidgetType = types.ObjectType{AttrTypes: map[string]attr.Type{
		"id":          types.StringType,
		"description": types.StringType,
		"props":       jsontypes.NormalizedType{},
		"answer":      jsontypes.NormalizedType{},
	}}
	widgetType = types.ObjectType{AttrTypes: map[string]attr.Type{
		"select":  emptyObjectType,
		"text":    emptyObjectType,
		"confirm": emptyObjectType,
		"custom":  customWidgetType,
	}}
	builtinType = types.ObjectType{AttrTypes: map[string]attr.Type{
		"session_display_name": types.StringType,
		"ask_user": types.ObjectType{AttrTypes: map[string]attr.Type{
			"widgets": types.ListType{ElemType: widgetType},
		}},
	}}
)

func builtinAttribute() schema.SingleNestedAttribute {
	return schema.SingleNestedAttribute{
		Optional:    true,
		Computed:    true,
		Description: "The servers the platform serves itself.",
		Default: objectdefault.StaticValue(types.ObjectValueMust(builtinType.AttrTypes, map[string]attr.Value{
			"session_display_name": types.StringValue(client.DisplayNameNever),
			"ask_user":             types.ObjectNull(builtinType.AttrTypes["ask_user"].(types.ObjectType).AttrTypes),
		})),
		Attributes: map[string]schema.Attribute{
			"session_display_name": schema.StringAttribute{
				Optional: true,
				Computed: true,
				Description: "When the agent names the session it runs in: `never`, `empty` (only while it " +
					"has no name), or `until_user_edits`.",
				Default:    stringdefault.StaticString(client.DisplayNameNever),
				Validators: oneOf(client.DisplayNameWhens),
			},
			"ask_user": schema.SingleNestedAttribute{
				Optional: true,
				Description: "Lets the agent put forms to a person, built from the widgets listed. Left unset, " +
					"it cannot. A child session never asks.",
				Attributes: map[string]schema.Attribute{
					"widgets": schema.ListNestedAttribute{
						Required: true,
						Description: "Each widget once, as exactly one of `select`, `text`, `confirm`, " +
							"and `custom`.",
						// CODESYNC(ask-user-max-widgets)
						Validators: []validator.List{listvalidator.SizeBetween(1, 32)},
						NestedObject: schema.NestedAttributeObject{
							Validators: []validator.Object{exactlyOneVariant()},
							Attributes: map[string]schema.Attribute{
								"select":  emptyVariant("A pick among options the agent writes."),
								"text":    emptyVariant("Free text."),
								"confirm": emptyVariant("Yes or no."),
								"custom": variant("A widget the app's own clients draw from its id.", map[string]schema.Attribute{
									"id": schema.StringAttribute{
										Required: true,
										Description: "The renderer's key. A change to `props` or `answer` that would break " +
											"a client takes a new id.",
										Validators: []validator.String{
											// CODESYNC(widget-id-len)
											charsBetween(1, 64),
											stringvalidator.RegexMatches(widgetIDPattern, "must be lowercase alphanumeric plus hyphen and underscore"),
										},
									},
									"description": schema.StringAttribute{
										Required:    true,
										Description: "What the model reads to choose the widget.",
										// CODESYNC(widget-description-len)
										Validators: []validator.String{charsAtMost(1024)},
									},
									"props": schema.StringAttribute{
										Required:    true,
										CustomType:  jsontypes.NormalizedType{},
										Description: "The JSON Schema of what the agent fills in, as `jsonencode` writes it.",
									},
									"answer": schema.StringAttribute{
										Required:    true,
										CustomType:  jsontypes.NormalizedType{},
										Description: "The JSON Schema an answer must match, as `jsonencode` writes it.",
									},
								}),
							},
						},
					},
				},
			},
		},
	}
}

func agentSchema() schema.Schema {
	return schema.Schema{
		Version: agentSchemaVersion,
		Description: "An agent and the config its sessions run. Changing the config publishes a new " +
			"agent version; renaming or changing `allowed_origins` does not. Terraform owns the whole " +
			"config: a value left out is the server's default, and a change made elsewhere is undone by " +
			"the next apply. List an attribute under `lifecycle.ignore_changes` to leave it to another tool.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"workspace_id": schema.StringAttribute{
				Computed:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"name": schema.StringAttribute{
				Required:    true,
				Description: "A label. Agents in one workspace may share one.",
				// CODESYNC(agent-name-len)
				Validators: []validator.String{charsBetween(1, 200)},
			},
			"model_provider_id": schema.StringAttribute{
				Required:    true,
				Description: "The model provider the agent's model calls go to.",
				Validators:  uuidValidators(),
			},
			"system_prompt": schema.StringAttribute{
				Optional: true,
			},
			"model":       modelAttribute(),
			"mcp_servers": mcpServersAttribute(),
			"skills":      skillsAttribute(),
			"sandboxes":   sandboxesAttribute(),
			"functions":   functionsAttribute(),
			"volume":      volumeAttribute(),
			"builtin":     builtinAttribute(),
			"allowed_origins": schema.SetAttribute{
				Optional:    true,
				Computed:    true,
				ElementType: types.StringType,
				Description: "Browser origins the agent's sessions answer. `*` allows every origin. " +
					"Left unset, Terraform does not manage the list.",
				// CODESYNC(agent-origin-count)
				Validators:    []validator.Set{setvalidator.SizeAtMost(32)},
				PlanModifiers: []planmodifier.Set{setplanmodifier.UseStateForUnknown()},
			},
			"version": schema.Int64Attribute{
				Computed:    true,
				Description: "The number of the version carrying the config.",
			},
			"version_id": schema.StringAttribute{
				Computed: true,
			},
		},
	}
}

// exactlyOneVariant checks a sum type's object: of the attributes it holds,
// one per variant, exactly one is set.
func exactlyOneVariant() validator.Object {
	return variantValidator{}
}

type variantValidator struct{}

func (variantValidator) Description(context.Context) string {
	return "exactly one of the object's attributes must be set"
}

func (v variantValidator) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}

func (variantValidator) ValidateObject(_ context.Context, req validator.ObjectRequest, resp *validator.ObjectResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	var names, set []string
	for name, value := range req.ConfigValue.Attributes() {
		if value.IsUnknown() {
			return
		}
		names = append(names, name)
		if !value.IsNull() {
			set = append(set, name)
		}
	}
	sort.Strings(names)
	sort.Strings(set)
	switch len(set) {
	case 1:
	case 0:
		resp.Diagnostics.AddAttributeError(req.Path, "Missing Attribute Configuration",
			fmt.Sprintf("Exactly one of %s must be set.", strings.Join(names, ", ")))
	default:
		resp.Diagnostics.AddAttributeError(req.Path, "Invalid Attribute Combination",
			fmt.Sprintf("Only one of %s may be set, got %s.", strings.Join(names, ", "), strings.Join(set, " and ")))
	}
}
