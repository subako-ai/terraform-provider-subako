package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"regexp"

	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-framework/types/basetypes"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"

	"github.com/subako-ai/terraform-provider-subako/internal/client"
)

var (
	_ resource.ResourceWithConfigure        = (*modelProviderResource)(nil)
	_ resource.ResourceWithImportState      = (*modelProviderResource)(nil)
	_ resource.ResourceWithConfigValidators = (*modelProviderResource)(nil)
	_ resource.ResourceWithUpgradeState     = (*modelProviderResource)(nil)
)

func newModelProviderResource() resource.Resource {
	return &modelProviderResource{}
}

// modelProviderResource is a workspace's own model provider, authenticating
// upstream with its own API key or as the workspace through workload identity
// federation. A key is write-only: it never reaches the plan or the state,
// and the API never returns it, so `api_key_wo_version` is what names which
// key the provider holds, and bumping it rotates the key in place.
type modelProviderResource struct {
	client *client.Client
}

type modelProviderModel struct {
	ID            types.String `tfsdk:"id"`
	Format        types.String `tfsdk:"format"`
	DisplayName   types.String `tfsdk:"display_name"`
	Description   types.String `tfsdk:"description"`
	BaseURL       types.String `tfsdk:"base_url"`
	Auth          types.Object `tfsdk:"auth"`
	APIKeyVersion types.Int64  `tfsdk:"api_key_wo_version"`
}

// modelProviderAuthConfig is `auth`: one attribute per way of
// authenticating, of which exactlyOneVariant leaves exactly one set.
type modelProviderAuthConfig struct {
	APIKey                    *apiKeyConfig                    `tfsdk:"api_key"`
	AWSWebIdentity            *awsWebIdentityConfig            `tfsdk:"aws_web_identity"`
	AnthropicWorkloadIdentity *anthropicWorkloadIdentityConfig `tfsdk:"anthropic_workload_identity"`
	OpenAIWorkloadIdentity    *openaiWorkloadIdentityConfig    `tfsdk:"openai_workload_identity"`
}

type apiKeyConfig struct {
	APIKey types.String `tfsdk:"api_key_wo"`
}

type awsWebIdentityConfig struct {
	RoleARN types.String `tfsdk:"role_arn"`
	Region  types.String `tfsdk:"region"`
}

type anthropicWorkloadIdentityConfig struct {
	OrganizationID   types.String `tfsdk:"organization_id"`
	ServiceAccountID types.String `tfsdk:"service_account_id"`
	FederationRuleID types.String `tfsdk:"federation_rule_id"`
	WorkspaceID      types.String `tfsdk:"workspace_id"`
}

type openaiWorkloadIdentityConfig struct {
	IdentityProviderID types.String `tfsdk:"identity_provider_id"`
	ServiceAccountID   types.String `tfsdk:"service_account_id"`
}

var (
	apiKeyAuthType         = types.ObjectType{AttrTypes: map[string]attr.Type{"api_key_wo": types.StringType}}
	awsWebIdentityAuthType = types.ObjectType{AttrTypes: map[string]attr.Type{
		"role_arn": types.StringType,
		"region":   types.StringType,
	}}
	anthropicWorkloadIdentityAuthType = types.ObjectType{AttrTypes: map[string]attr.Type{
		"organization_id":    types.StringType,
		"service_account_id": types.StringType,
		"federation_rule_id": types.StringType,
		"workspace_id":       types.StringType,
	}}
	openaiWorkloadIdentityAuthType = types.ObjectType{AttrTypes: map[string]attr.Type{
		"identity_provider_id": types.StringType,
		"service_account_id":   types.StringType,
	}}
	modelProviderAuthType = types.ObjectType{AttrTypes: map[string]attr.Type{
		client.AuthAPIKey:                    apiKeyAuthType,
		client.AuthAWSWebIdentity:            awsWebIdentityAuthType,
		client.AuthAnthropicWorkloadIdentity: anthropicWorkloadIdentityAuthType,
		client.AuthOpenAIWorkloadIdentity:    openaiWorkloadIdentityAuthType,
	}}
)

// Spellings the server takes for the ids a federated auth names.
var (
	// CODESYNC(openai-id-charset)
	openaiIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)
)

// modelProviderSchemaVersion is the version of the state the schema below
// writes.
const modelProviderSchemaVersion = 1

func (r *modelProviderResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_model_provider"
}

// anthropicID is an Anthropic tagged id: the prefix, then letters and digits.
func anthropicID(prefix, description string, required bool) schema.StringAttribute {
	return schema.StringAttribute{
		Required:    required,
		Optional:    !required,
		Description: description,
		Validators: []validator.String{
			// CODESYNC(anthropic-id-len)
			charsBetween(1, 128),
			stringvalidator.RegexMatches(regexp.MustCompile(`^`+prefix+`[A-Za-z0-9]+$`),
				"must be "+prefix+" followed by letters and digits"),
		},
	}
}

// openaiID is an id OpenAI's dashboard shows.
func openaiID(description string) schema.StringAttribute {
	return schema.StringAttribute{
		Required:    true,
		Description: description,
		Validators: []validator.String{
			// CODESYNC(openai-id-len)
			charsBetween(1, 128),
			stringvalidator.RegexMatches(openaiIDPattern, "must be letters, digits, underscores and hyphens"),
		},
	}
}

func (r *modelProviderResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "A model provider of the workspace's own, authenticating with its own API key " +
			"or as the workspace through workload identity federation. A changed `auth` is put " +
			"in place: the provider keeps its id, and the agent versions naming it keep running. " +
			"Deleting it deletes it: agent versions that still name it can no longer run. Use " +
			"`create_before_destroy` on a change that does replace one agents use -- a changed " +
			"`format` -- so they publish versions naming the new one first.",
		Version: modelProviderSchemaVersion,
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"format": schema.StringAttribute{
				Required:      true,
				Description:   "`anthropic` or `openai_responses`. The API cannot change it, so changing it replaces the provider.",
				Validators:    []validator.String{stringvalidator.OneOf(client.FormatAnthropic, client.FormatOpenAIResponses)},
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"display_name": schema.StringAttribute{
				Required: true,
				// CODESYNC(display-name-len)
				Validators: []validator.String{charsAtMost(200)},
			},
			"description": schema.StringAttribute{
				Optional: true,
				Computed: true,
				Default:  stringdefault.StaticString(""),
				// CODESYNC(description-len)
				Validators: []validator.String{charsAtMost(1024)},
			},
			"base_url": schema.StringAttribute{
				Required: true,
				Description: "The upstream base URL, as the format's own client spells it: " +
					"`https://api.anthropic.com` or `https://api.openai.com/v1`. The API does not " +
					"return it, so a change made outside Terraform goes unnoticed. An Anthropic " +
					"workload identity calls `https://api.anthropic.com` alone, an OpenAI one " +
					"`https://api.openai.com/v1` alone, and an AWS role a Bedrock host in its region.",
				// CODESYNC(model-provider-base-url-len)
				Validators: []validator.String{charsBetween(1, 2048)},
			},
			"auth": schema.SingleNestedAttribute{
				Required: true,
				Description: "How the provider authenticates upstream, as exactly one of `api_key`, " +
					"`aws_web_identity`, `anthropic_workload_identity`, and `openai_workload_identity`. " +
					"The last three hand over no secret: the account trusts this workspace's subject, " +
					"which the `subako_workload_identity` data source reads. Before a federated auth is " +
					"put in place, Subako checks that it trusts this workspace and no other.",
				Validators: []validator.Object{exactlyOneVariant()},
				Attributes: map[string]schema.Attribute{
					client.AuthAPIKey: variant("An API key the workspace hands over.", map[string]schema.Attribute{
						"api_key_wo": schema.StringAttribute{
							Required:  true,
							Sensitive: true,
							WriteOnly: true,
							Description: "The upstream API key. Write-only: never stored in the plan or the " +
								"state. It travels when the provider is created, when `auth` changes to it, " +
								"and whenever `api_key_wo_version` changes.",
							// CODESYNC(model-provider-api-key-len)
							Validators: []validator.String{charsBetween(1, 16384)},
						},
					}),
					client.AuthAWSWebIdentity: variant("A role in your own AWS account, which Subako calls "+
						"Amazon Bedrock as.", map[string]schema.Attribute{
						"role_arn": schema.StringAttribute{
							Required: true,
							Description: "The IAM role to assume, `arn:aws:iam::<account>:role/<name>`. Its " +
								"trust policy must admit this workspace's subject alone.",
							// CODESYNC(aws-role-arn-len)
							Validators: []validator.String{charsBetween(1, 2048)},
						},
						"region": schema.StringAttribute{
							Required:    true,
							Description: "The region Bedrock is called in, which `base_url`'s host must name.",
							// CODESYNC(aws-region-len)
							Validators: []validator.String{charsBetween(1, 32)},
						},
					}),
					client.AuthAnthropicWorkloadIdentity: variant("A service account in your own Anthropic "+
						"organization, under a federation rule there. Needs `format = \"anthropic\"`.",
						map[string]schema.Attribute{
							"organization_id": schema.StringAttribute{
								Required:    true,
								Description: "The Anthropic organization's id, a UUID.",
								Validators:  uuidValidators(),
							},
							"service_account_id": anthropicID("svac_",
								"The service account the calls act as, `svac_...`.", true),
							"federation_rule_id": anthropicID("fdrl_",
								"The federation rule this workspace's token is checked against, `fdrl_...`. "+
									"Its match must admit this workspace's subject alone.", true),
							"workspace_id": anthropicID("wrkspc_",
								"The Anthropic workspace the calls are scoped to, `wrkspc_...`. Needed only "+
									"when the rule covers more than one Anthropic workspace.", false),
						}),
					client.AuthOpenAIWorkloadIdentity: variant("A service account in your own OpenAI "+
						"organization, through a workload identity provider there. Needs "+
						"`format = \"openai_responses\"`.", map[string]schema.Attribute{
						"identity_provider_id": openaiID("The workload identity provider that trusts the " +
							"deployment's issuer, `idp_...`."),
						"service_account_id": openaiID("The service account the calls act as. The " +
							"identity provider's mapping to it must admit this workspace's subject alone."),
					}),
				},
			},
			"api_key_wo_version": schema.Int64Attribute{
				Optional: true,
				Description: "Which `api_key_wo` this provider holds. Bump it to send a new one, " +
					"which rotates the key in place: the provider keeps its id and the agent " +
					"versions naming it keep running. Required with an API key, so a rotation is " +
					"always expressible -- a changed `api_key_wo` alone is invisible to Terraform.",
				Validators: []validator.Int64{int64validator.AtLeast(0)},
			},
		},
	}
}

// ConfigValidators asks an API key for the version naming it, and a federated
// auth for the format its API speaks.
func (r *modelProviderResource) ConfigValidators(_ context.Context) []resource.ConfigValidator {
	return []resource.ConfigValidator{modelProviderAuthValidator{}}
}

type modelProviderAuthValidator struct{}

func (modelProviderAuthValidator) Description(context.Context) string {
	return "an API key needs api_key_wo_version, and a federated auth the format its API speaks"
}

func (v modelProviderAuthValidator) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}

func (modelProviderAuthValidator) ValidateResource(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var config modelProviderModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}
	format, formatKnown := plannedString(config.Format).Get()
	needsFormat := func(kind, want string) {
		if formatKnown && format != want {
			resp.Diagnostics.AddAttributeError(path.Root("format"), "Format does not match auth",
				fmt.Sprintf("A provider authenticating with %s speaks %s, not %s.", kind, want, format))
		}
	}
	switch kind, _ := authKindOf(config.Auth); kind {
	case client.AuthAPIKey:
		if config.APIKeyVersion.IsNull() {
			resp.Diagnostics.AddAttributeError(path.Root("api_key_wo_version"), "Missing key version",
				"A provider with an API key needs api_key_wo_version, which is what a rotation bumps.")
		}
	case client.AuthAnthropicWorkloadIdentity:
		needsFormat(kind, client.FormatAnthropic)
	case client.AuthOpenAIWorkloadIdentity:
		needsFormat(kind, client.FormatOpenAIResponses)
	}
}

func (r *modelProviderResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = clientFrom(req.ProviderData, &resp.Diagnostics)
}

// authKindOf names the variant `auth` sets, and whether it names one at all.
func authKindOf(auth types.Object) (string, bool) {
	if _, ok := planned(auth).Get(); !ok {
		return "", false
	}
	for name, value := range auth.Attributes() {
		if !value.IsNull() && !value.IsUnknown() {
			return name, true
		}
	}
	return "", false
}

// auth reads the auth a model whose values are known carries. Only the
// config holds the write-only key, so a key read from the plan or the state
// is empty.
func (m modelProviderModel) auth(ctx context.Context) (client.ModelProviderAuth, diag.Diagnostics) {
	var config modelProviderAuthConfig
	diags := m.Auth.As(ctx, &config, basetypes.ObjectAsOptions{})
	switch {
	case diags.HasError():
		return nil, diags
	case config.APIKey != nil:
		return client.APIKeyAuth{APIKey: config.APIKey.APIKey.ValueString()}, diags
	case config.AWSWebIdentity != nil:
		return client.AWSWebIdentityAuth{
			RoleARN: config.AWSWebIdentity.RoleARN.ValueString(),
			Region:  config.AWSWebIdentity.Region.ValueString(),
		}, diags
	case config.AnthropicWorkloadIdentity != nil:
		identity := config.AnthropicWorkloadIdentity
		return client.AnthropicWorkloadIdentityAuth{
			OrganizationID:   identity.OrganizationID.ValueString(),
			ServiceAccountID: identity.ServiceAccountID.ValueString(),
			FederationRuleID: identity.FederationRuleID.ValueString(),
			WorkspaceID:      identity.WorkspaceID.ValueStringPointer(),
		}, diags
	case config.OpenAIWorkloadIdentity != nil:
		return client.OpenAIWorkloadIdentityAuth{
			IdentityProviderID: config.OpenAIWorkloadIdentity.IdentityProviderID.ValueString(),
			ServiceAccountID:   config.OpenAIWorkloadIdentity.ServiceAccountID.ValueString(),
		}, diags
	}
	ambiguous(&diags, "auth")
	return nil, diags
}

// authValue is `auth` as the server answers it. The server never returns a
// key, so an API key's variant holds none.
func authValue(auth client.ModelProviderAuth) (types.Object, diag.Diagnostics) {
	variants := map[string]attr.Value{
		client.AuthAPIKey:                    types.ObjectNull(apiKeyAuthType.AttrTypes),
		client.AuthAWSWebIdentity:            types.ObjectNull(awsWebIdentityAuthType.AttrTypes),
		client.AuthAnthropicWorkloadIdentity: types.ObjectNull(anthropicWorkloadIdentityAuthType.AttrTypes),
		client.AuthOpenAIWorkloadIdentity:    types.ObjectNull(openaiWorkloadIdentityAuthType.AttrTypes),
	}
	var diags diag.Diagnostics
	var value types.Object
	switch a := auth.(type) {
	case client.APIKeyAuth:
		value, diags = types.ObjectValue(apiKeyAuthType.AttrTypes, map[string]attr.Value{
			"api_key_wo": types.StringNull(),
		})
	case client.AWSWebIdentityAuth:
		value, diags = types.ObjectValue(awsWebIdentityAuthType.AttrTypes, map[string]attr.Value{
			"role_arn": types.StringValue(a.RoleARN),
			"region":   types.StringValue(a.Region),
		})
	case client.AnthropicWorkloadIdentityAuth:
		value, diags = types.ObjectValue(anthropicWorkloadIdentityAuthType.AttrTypes, map[string]attr.Value{
			"organization_id":    types.StringValue(a.OrganizationID),
			"service_account_id": types.StringValue(a.ServiceAccountID),
			"federation_rule_id": types.StringValue(a.FederationRuleID),
			"workspace_id":       types.StringPointerValue(a.WorkspaceID),
		})
	case client.OpenAIWorkloadIdentityAuth:
		value, diags = types.ObjectValue(openaiWorkloadIdentityAuthType.AttrTypes, map[string]attr.Value{
			"identity_provider_id": types.StringValue(a.IdentityProviderID),
			"service_account_id":   types.StringValue(a.ServiceAccountID),
		})
	default:
		diags.AddError("Unknown model provider auth", fmt.Sprintf("%T is no auth this provider manages", auth))
		return types.ObjectNull(modelProviderAuthType.AttrTypes), diags
	}
	variants[auth.Type()] = value
	object, d := types.ObjectValue(modelProviderAuthType.AttrTypes, variants)
	diags.Append(d...)
	return object, diags
}

// The kinds of model provider `ModelProviderOwnerBody` tells apart.
const ownerWorkspace = "byok"

// read refreshes m from the server. found is false when the provider is gone.
// A platform provider is not one this resource manages, so reading one is a
// refusal rather than a value.
func (r *modelProviderResource) read(ctx context.Context, m *modelProviderModel) (found bool, diags diag.Diagnostics) {
	id := m.ID.ValueString()
	current, err := r.client.GetModelProvider(ctx, id)
	if client.IsNotFound(err) {
		return false, diags
	}
	if err != nil {
		apiError(&diags, "read model provider "+id, err)
		return true, diags
	}
	if current.Type != ownerWorkspace {
		diags.AddError("Not a workspace model provider",
			"Model provider "+id+" is a platform provider, which only the operator manages. "+
				"Read it with the subako_model_providers data source instead.")
		return true, diags
	}
	auth, err := current.AuthOf()
	if err != nil {
		apiError(&diags, "read model provider "+id, err)
		return true, diags
	}
	m.Format = types.StringValue(current.Format)
	m.DisplayName = types.StringValue(current.DisplayName)
	m.Description = types.StringValue(current.Description)
	var d diag.Diagnostics
	m.Auth, d = authValue(auth)
	diags.Append(d...)
	return true, diags
}

func (r *modelProviderResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan, config modelProviderModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}
	auth, diags := config.auth(ctx)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	provider, err := r.client.CreateModelProvider(ctx, client.NewModelProvider{
		Format:      plan.Format.ValueString(),
		DisplayName: plan.DisplayName.ValueString(),
		Description: plan.Description.ValueString(),
		BaseURL:     plan.BaseURL.ValueString(),
		Auth:        auth,
	})
	if err != nil {
		apiError(&resp.Diagnostics, "create model provider", err)
		return
	}
	// Recorded before the read-back, so a failure there taints this provider
	// rather than orphaning it.
	created := plan
	created.ID = types.StringValue(provider.ID)
	next := created
	finishCreate(ctx, resp, subject{"Model provider", provider.ID}, r.read, next, created)
}

func (r *modelProviderResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	refresh(ctx, req, resp, r.read)
}

func (r *modelProviderResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, config, state modelProviderModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	id := state.ID.ValueString()
	update := client.ModelProviderUpdate{
		DisplayName: plan.DisplayName.ValueString(),
		Description: plan.Description.ValueString(),
		BaseURL:     plan.BaseURL.ValueString(),
	}
	// The plan and the state hold every auth but a key, so a changed `auth`
	// or a changed version is the only thing that tells a new auth from an
	// apply leaving it as it stands. A version the state never held -- an
	// imported provider's -- is a change like any other, and the
	// configuration's key becomes the provider's. The key itself lives in the
	// configuration, never in the plan.
	if !plan.Auth.Equal(state.Auth) || !plannedInt64(plan.APIKeyVersion).Equal(plannedInt64(state.APIKeyVersion)) {
		auth, diags := config.auth(ctx)
		resp.Diagnostics.Append(diags...)
		if resp.Diagnostics.HasError() {
			return
		}
		update.Auth = auth
	}
	run(&resp.Diagnostics, step{
		action: "update model provider " + id,
		call: func() error {
			_, err := r.client.UpdateModelProvider(ctx, id, update)
			return err
		},
	})

	next := plan
	next.ID = state.ID
	finishUpdate(ctx, resp, subject{"Model provider", id}, r.read, next, state)
}

func (r *modelProviderResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state modelProviderModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	id := state.ID.ValueString()
	remove(resp, "delete model provider "+id, func() error { return r.client.DeleteModelProvider(ctx, id) })
}

func (r *modelProviderResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}

// UpgradeState moves a version 0 state, whose key sat at the top level,
// under `auth.api_key`.
func (r *modelProviderResource) UpgradeState(context.Context) map[int64]resource.StateUpgrader {
	return map[int64]resource.StateUpgrader{
		0: {StateUpgrader: upgradeModelProviderStateV0},
	}
}

func upgradeModelProviderStateV0(_ context.Context, req resource.UpgradeStateRequest, resp *resource.UpgradeStateResponse) {
	upgraded, err := upgradeModelProviderJSONV0(req.RawState.JSON)
	if err != nil {
		resp.Diagnostics.AddError("Unreadable model provider state", err.Error())
		return
	}
	resp.DynamicValue = &tfprotov6.DynamicValue{JSON: upgraded}
}

func upgradeModelProviderJSONV0(raw []byte) ([]byte, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var state map[string]any
	if err := decoder.Decode(&state); err != nil {
		return nil, fmt.Errorf("decode version 0 state: %w", err)
	}
	state["auth"] = map[string]any{
		client.AuthAPIKey:                    map[string]any{"api_key_wo": nil},
		client.AuthAWSWebIdentity:            nil,
		client.AuthAnthropicWorkloadIdentity: nil,
		client.AuthOpenAIWorkloadIdentity:    nil,
	}
	delete(state, "api_key_wo")
	return json.Marshal(state)
}
