package provider

import (
	"context"
	"net/url"
	"regexp"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/listvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-framework/types/basetypes"

	"github.com/subako-ai/terraform-provider-subako/internal/client"
)

var (
	_ resource.ResourceWithConfigure        = (*oauthClientResource)(nil)
	_ resource.ResourceWithImportState      = (*oauthClientResource)(nil)
	_ resource.ResourceWithConfigValidators = (*oauthClientResource)(nil)
)

func newOAuthClientResource() resource.Resource {
	return &oauthClientResource{}
}

// oauthClientResource is an OAuth client the workspace created at a
// provider, which vault credentials connect through. Its secret is
// write-only: the API never returns it, so `client_secret_wo_version` is what
// names which secret the client holds, and bumping it sends a new one in
// place.
type oauthClientResource struct {
	client *client.Client
}

type oauthClientModel struct {
	ID                    types.String `tfsdk:"id"`
	WorkspaceID           types.String `tfsdk:"workspace_id"`
	DisplayName           types.String `tfsdk:"display_name"`
	AuthorizationEndpoint types.String `tfsdk:"authorization_endpoint"`
	TokenEndpoint         types.String `tfsdk:"token_endpoint"`
	ClientID              types.String `tfsdk:"client_id"`
	TokenEndpointAuth     types.Object `tfsdk:"token_endpoint_auth"`
	ClientSecretVersion   types.Int64  `tfsdk:"client_secret_wo_version"`
	Scopes                types.List   `tfsdk:"scopes"`
	AuthorizationParams   types.List   `tfsdk:"authorization_params"`
}

// tokenEndpointAuthConfig is `token_endpoint_auth`: one attribute per method,
// of which exactlyOneVariant leaves exactly one set.
type tokenEndpointAuthConfig struct {
	None              *emptyObject        `tfsdk:"none"`
	ClientSecretPost  *clientSecretConfig `tfsdk:"client_secret_post"`
	ClientSecretBasic *clientSecretConfig `tfsdk:"client_secret_basic"`
}

type clientSecretConfig struct {
	ClientSecret types.String `tfsdk:"client_secret_wo"`
}

type authorizationParamModel struct {
	Name  types.String `tfsdk:"name"`
	Value types.String `tfsdk:"value"`
}

var (
	clientSecretType      = types.ObjectType{AttrTypes: map[string]attr.Type{"client_secret_wo": types.StringType}}
	tokenEndpointAuthType = types.ObjectType{AttrTypes: map[string]attr.Type{
		client.TokenEndpointAuthNone:              emptyObjectType,
		client.TokenEndpointAuthClientSecretPost:  clientSecretType,
		client.TokenEndpointAuthClientSecretBasic: clientSecretType,
	}}
	authorizationParamType = types.ObjectType{AttrTypes: map[string]attr.Type{
		"name":  types.StringType,
		"value": types.StringType,
	}}
)

// Spellings the server takes for a scope and an authorization parameter's
// name.
var (
	// CODESYNC(oauth-scope-charset)
	scopePattern = regexp.MustCompile(`^[\x21\x23-\x5b\x5d-\x7e]+$`)
	// CODESYNC(oauth-param-name-charset)
	paramNamePattern = regexp.MustCompile(`^[A-Za-z0-9._~-]+$`)
)

// reservedAuthorizationParams are the authorization URL parameters Subako
// sets itself.
// CODESYNC(oauth-reserved-params)
var reservedAuthorizationParams = []string{
	"response_type", "client_id", "redirect_uri", "state",
	"code_challenge", "code_challenge_method", "scope", "resource",
}

func (r *oauthClientResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_oauth_client"
}

func clientSecretVariant(description string) schema.SingleNestedAttribute {
	return variant(description, map[string]schema.Attribute{
		"client_secret_wo": schema.StringAttribute{
			Required:  true,
			Sensitive: true,
			WriteOnly: true,
			Description: "The client secret the provider issued. Write-only: never stored in the plan " +
				"or the state. It travels when the client is created, when the method changes, and " +
				"whenever `client_secret_wo_version` changes.",
			// CODESYNC(oauth-client-secret-len)
			Validators: []validator.String{charsBetween(1, 1024)},
		},
	})
}

func (r *oauthClientResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	replace := []planmodifier.String{stringplanmodifier.RequiresReplace()}
	resp.Schema = schema.Schema{
		Description: "An OAuth client the workspace created at a provider, for connecting vault " +
			"credentials to a provider that registers no client on the spot. Credentials connect " +
			"through it with a person's sign-in, so Terraform manages the client and not them. " +
			"A client a credential was connected through cannot be deleted.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"workspace_id": schema.StringAttribute{
				Computed:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"display_name": schema.StringAttribute{
				Required: true,
				// CODESYNC(display-name-len)
				Validators: []validator.String{charsAtMost(200)},
			},
			"authorization_endpoint": schema.StringAttribute{
				Required: true,
				Description: "The provider's authorization endpoint, which the browser is sent to. Fixed " +
					"once the client exists, so changing it replaces the client.",
				// CODESYNC(oauth-endpoint-len)
				Validators:    []validator.String{charsBetween(1, 2048)},
				PlanModifiers: replace,
			},
			"token_endpoint": schema.StringAttribute{
				Required: true,
				Description: "The provider's token endpoint, which Subako redeems codes and refreshes " +
					"tokens at. Fixed once the client exists, so changing it replaces the client.",
				// CODESYNC(oauth-endpoint-len)
				Validators:    []validator.String{charsBetween(1, 2048)},
				PlanModifiers: replace,
			},
			"client_id": schema.StringAttribute{
				Required: true,
				Description: "The client identifier the provider issued. Fixed once the client exists, " +
					"so changing it replaces the client.",
				// CODESYNC(oauth-client-identifier-len)
				Validators:    []validator.String{charsBetween(1, 512)},
				PlanModifiers: replace,
			},
			"token_endpoint_auth": schema.SingleNestedAttribute{
				Required: true,
				Description: "How Subako authenticates at the token endpoint as the client, as exactly one " +
					"of `none` (a public client, which PKCE alone proves), `client_secret_post`, and " +
					"`client_secret_basic`.",
				Validators: []validator.Object{exactlyOneVariant()},
				Attributes: map[string]schema.Attribute{
					client.TokenEndpointAuthNone:              emptyVariant("A public client: PKCE alone proves it."),
					client.TokenEndpointAuthClientSecretPost:  clientSecretVariant("The client secret, sent in the token request's form."),
					client.TokenEndpointAuthClientSecretBasic: clientSecretVariant("The client secret, sent in a `Basic` header."),
				},
			},
			"client_secret_wo_version": schema.Int64Attribute{
				Optional: true,
				Description: "Which `client_secret_wo` the client holds. Bump it to send a new one in " +
					"place. Required with a client secret, so a rotation is always expressible -- a " +
					"changed secret alone is invisible to Terraform.",
				Validators: []validator.Int64{int64validator.AtLeast(0)},
			},
			"scopes": schema.ListAttribute{
				Optional:    true,
				Computed:    true,
				ElementType: types.StringType,
				Description: "The scopes the client asks for, each once. Left unset, none: the provider's default.",
				Default:     emptyList(types.StringType),
				Validators: []validator.List{
					// CODESYNC(oauth-scopes-len)
					listvalidator.SizeAtMost(64),
					listvalidator.UniqueValues(),
					listvalidator.ValueStringsAre(
						// CODESYNC(oauth-scope-len)
						charsBetween(1, 256),
						stringvalidator.RegexMatches(scopePattern,
							"must be printable ASCII without spaces, quotes, or backslashes"),
					),
				},
			},
			"authorization_params": schema.ListNestedAttribute{
				Optional: true,
				Computed: true,
				Description: "Parameters the authorization URL carries beside the ones Subako sets, in " +
					"order, such as `access_type = offline`. Their values are no secret: the browser " +
					"carries them to the provider.",
				Default: emptyList(authorizationParamType),
				// CODESYNC(oauth-params-len)
				Validators: []validator.List{listvalidator.SizeAtMost(16)},
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"name": schema.StringAttribute{
							Required:    true,
							Description: "Unique within `authorization_params`.",
							Validators: []validator.String{
								// CODESYNC(oauth-param-name-len)
								charsBetween(1, 64),
								stringvalidator.RegexMatches(paramNamePattern, "must be letters, digits, and - . _ ~"),
								stringvalidator.NoneOf(reservedAuthorizationParams...),
							},
						},
						"value": schema.StringAttribute{
							Required: true,
							// CODESYNC(oauth-param-value-len)
							Validators: []validator.String{charsAtMost(512)},
						},
					},
				},
			},
		},
	}
}

// ConfigValidators asks a client with a secret for the version naming it.
func (r *oauthClientResource) ConfigValidators(_ context.Context) []resource.ConfigValidator {
	return []resource.ConfigValidator{secretVersionValidator{}}
}

type secretVersionValidator struct{}

func (secretVersionValidator) Description(context.Context) string {
	return "a client secret needs client_secret_wo_version"
}

func (v secretVersionValidator) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}

func (secretVersionValidator) ValidateResource(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var config oauthClientModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() || config.ClientSecretVersion.IsUnknown() || !config.ClientSecretVersion.IsNull() {
		return
	}
	switch method, _ := methodOf(config.TokenEndpointAuth); method {
	case client.TokenEndpointAuthClientSecretPost, client.TokenEndpointAuthClientSecretBasic:
		resp.Diagnostics.AddAttributeError(path.Root("client_secret_wo_version"), "Missing secret version",
			"A client with a client secret needs client_secret_wo_version, which is what a rotation bumps.")
	}
}

func (r *oauthClientResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = clientFrom(req.ProviderData, &resp.Diagnostics)
}

// methodOf names the method `token_endpoint_auth` sets, and whether it names
// one at all.
func methodOf(auth types.Object) (string, bool) {
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

// tokenEndpointAuth reads the method and secret the configuration carries.
// Only the config holds write-only values, so this never reads the plan or
// the state.
func (m oauthClientModel) tokenEndpointAuth(ctx context.Context) (client.TokenEndpointAuth, diag.Diagnostics) {
	var config tokenEndpointAuthConfig
	diags := m.TokenEndpointAuth.As(ctx, &config, basetypes.ObjectAsOptions{})
	switch {
	case diags.HasError():
		return nil, diags
	case config.None != nil:
		return client.PublicClient{}, diags
	case config.ClientSecretPost != nil:
		return client.ClientSecretPost{ClientSecret: config.ClientSecretPost.ClientSecret.ValueString()}, diags
	case config.ClientSecretBasic != nil:
		return client.ClientSecretBasic{ClientSecret: config.ClientSecretBasic.ClientSecret.ValueString()}, diags
	}
	ambiguous(&diags, "token_endpoint_auth")
	return nil, diags
}

// lists reads the scopes and parameters a model whose values are known
// carries.
func (m oauthClientModel) lists(ctx context.Context) ([]string, []client.AuthorizationParam, diag.Diagnostics) {
	var diags diag.Diagnostics
	var scopes []string
	diags.Append(m.Scopes.ElementsAs(ctx, &scopes, false)...)
	var models []authorizationParamModel
	diags.Append(m.AuthorizationParams.ElementsAs(ctx, &models, false)...)
	var params []client.AuthorizationParam
	for _, param := range models {
		params = append(params, client.AuthorizationParam{Name: param.Name.ValueString(), Value: param.Value.ValueString()})
	}
	return scopes, params, diags
}

// sameEndpoint reports whether two URLs name the same endpoint once the
// server's normalization -- a lowercase scheme and host, a default port
// dropped, an empty path spelled `/` -- has been applied to both.
func sameEndpoint(a, b string) bool {
	normalize := func(raw string) (string, bool) {
		u, err := url.Parse(raw)
		if err != nil {
			return "", false
		}
		scheme, host := strings.ToLower(u.Scheme), strings.ToLower(u.Hostname())
		if port := u.Port(); port != "" && !(scheme == "https" && port == "443") && !(scheme == "http" && port == "80") {
			host += ":" + port
		}
		p := u.EscapedPath()
		if p == "" {
			p = "/"
		}
		return scheme + "://" + host + p + "?" + u.RawQuery, true
	}
	na, okA := normalize(a)
	nb, okB := normalize(b)
	return okA && okB && na == nb
}

// endpoint is what the state holds for an endpoint the server answers: the
// spelling it already holds where the two name the same endpoint, so a
// configuration need not spell it the server's way.
func endpoint(held types.String, answered string) types.String {
	if value, ok := plannedString(held).Get(); ok && sameEndpoint(value, answered) {
		return held
	}
	return types.StringValue(answered)
}

// read refreshes m from the server. The server answers the method but never
// the secret, so a method the state already spells keeps what it carries.
func (r *oauthClientResource) read(ctx context.Context, m *oauthClientModel) (found bool, diags diag.Diagnostics) {
	id := m.ID.ValueString()
	current, err := r.client.GetOAuthClient(ctx, id)
	if client.IsNotFound(err) {
		return false, diags
	}
	if err != nil {
		apiError(&diags, "read OAuth client "+id, err)
		return true, diags
	}
	m.WorkspaceID = types.StringValue(current.WorkspaceID)
	m.DisplayName = types.StringValue(current.DisplayName)
	m.AuthorizationEndpoint = endpoint(m.AuthorizationEndpoint, current.AuthorizationEndpoint)
	m.TokenEndpoint = endpoint(m.TokenEndpoint, current.TokenEndpoint)
	m.ClientID = types.StringValue(current.ClientID)

	if held, _ := methodOf(m.TokenEndpointAuth); held != current.TokenEndpointAuthMethod {
		variants := map[string]attr.Value{
			client.TokenEndpointAuthNone:              types.ObjectNull(emptyObjectType.AttrTypes),
			client.TokenEndpointAuthClientSecretPost:  types.ObjectNull(clientSecretType.AttrTypes),
			client.TokenEndpointAuthClientSecretBasic: types.ObjectNull(clientSecretType.AttrTypes),
		}
		switch current.TokenEndpointAuthMethod {
		case client.TokenEndpointAuthNone:
			variants[client.TokenEndpointAuthNone] = types.ObjectValueMust(emptyObjectType.AttrTypes, map[string]attr.Value{})
		case client.TokenEndpointAuthClientSecretPost, client.TokenEndpointAuthClientSecretBasic:
			variants[current.TokenEndpointAuthMethod] = types.ObjectValueMust(clientSecretType.AttrTypes,
				map[string]attr.Value{"client_secret_wo": types.StringNull()})
		default:
			diags.AddError("Unknown token endpoint auth method",
				"OAuth client "+id+" authenticates with "+current.TokenEndpointAuthMethod+", which this provider does not manage.")
			return true, diags
		}
		m.TokenEndpointAuth = types.ObjectValueMust(tokenEndpointAuthType.AttrTypes, variants)
	}

	var d diag.Diagnostics
	m.Scopes, d = types.ListValueFrom(ctx, types.StringType, orEmpty(current.Scopes))
	diags.Append(d...)
	params := []authorizationParamModel{}
	for _, param := range current.AuthorizationParams {
		params = append(params, authorizationParamModel{Name: types.StringValue(param.Name), Value: types.StringValue(param.Value)})
	}
	m.AuthorizationParams, d = types.ListValueFrom(ctx, authorizationParamType, params)
	diags.Append(d...)
	return true, diags
}

func orEmpty[T any](list []T) []T {
	if list == nil {
		return []T{}
	}
	return list
}

func (r *oauthClientResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan, config oauthClientModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}
	auth, diags := config.tokenEndpointAuth(ctx)
	resp.Diagnostics.Append(diags...)
	scopes, params, diags := plan.lists(ctx)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	id, err := r.client.CreateOAuthClient(ctx, client.NewOAuthClient{
		DisplayName:           plan.DisplayName.ValueString(),
		AuthorizationEndpoint: plan.AuthorizationEndpoint.ValueString(),
		TokenEndpoint:         plan.TokenEndpoint.ValueString(),
		ClientID:              plan.ClientID.ValueString(),
		TokenEndpointAuth:     auth,
		Scopes:                scopes,
		AuthorizationParams:   params,
	})
	if err != nil {
		apiError(&resp.Diagnostics, "create OAuth client", err)
		return
	}
	// Recorded before the read-back, so a failure there taints this client
	// rather than orphaning it.
	created := plan
	created.ID = types.StringValue(id)
	created.WorkspaceID = types.StringNull()
	next := created
	finishCreate(ctx, resp, subject{"OAuth client", id}, r.read, next, created)
}

func (r *oauthClientResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	refresh(ctx, req, resp, r.read)
}

func (r *oauthClientResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, config, state oauthClientModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	id := state.ID.ValueString()
	scopes, params, diags := plan.lists(ctx)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	update := client.OAuthClientUpdate{
		DisplayName:         plan.DisplayName.ValueString(),
		Scopes:              scopes,
		AuthorizationParams: params,
	}
	// The secret is write-only, so a changed method or version is the only
	// thing that tells a new secret from an apply leaving it as it stands. A
	// version the state never held -- an imported client's -- is a change
	// like any other, and the configuration's secret becomes the client's.
	wanted, _ := methodOf(plan.TokenEndpointAuth)
	held, _ := methodOf(state.TokenEndpointAuth)
	if wanted != held || !plannedInt64(plan.ClientSecretVersion).Equal(plannedInt64(state.ClientSecretVersion)) {
		auth, diags := config.tokenEndpointAuth(ctx)
		resp.Diagnostics.Append(diags...)
		if resp.Diagnostics.HasError() {
			return
		}
		update.TokenEndpointAuth = auth
	}
	run(&resp.Diagnostics, step{
		action: "update OAuth client " + id,
		call: func() error {
			_, err := r.client.UpdateOAuthClient(ctx, id, update)
			return err
		},
	})

	next := plan
	next.ID = state.ID
	finishUpdate(ctx, resp, subject{"OAuth client", id}, r.read, next, state)
}

func (r *oauthClientResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state oauthClientModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	id := state.ID.ValueString()
	remove(resp, "delete OAuth client "+id+
		" (a client cannot be deleted while a credential connected through it remains)",
		func() error { return r.client.DeleteOAuthClient(ctx, id) })
}

func (r *oauthClientResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}
