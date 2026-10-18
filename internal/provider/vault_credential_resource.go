package provider

import (
	"context"
	"encoding/json"
	"regexp"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-jsontypes/jsontypes"
	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/listvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/resourcevalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/objectplanmodifier"
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
	_ resource.ResourceWithConfigure        = (*vaultCredentialResource)(nil)
	_ resource.ResourceWithImportState      = (*vaultCredentialResource)(nil)
	_ resource.ResourceWithConfigValidators = (*vaultCredentialResource)(nil)
	_ resource.ResourceWithUpgradeState     = (*vaultCredentialResource)(nil)
)

func newVaultCredentialResource() resource.Resource {
	return &vaultCredentialResource{}
}

// vaultCredentialResource is one credential in a vault. The API stores a
// credential once and never returns its secret, so every change replaces it
// and the secret attributes are write-only.
type vaultCredentialResource struct {
	client *client.Client
}

type vaultCredentialModel struct {
	ID             types.String `tfsdk:"id"`
	VaultID        types.String `tfsdk:"vault_id"`
	DisplayName    types.String `tfsdk:"display_name"`
	MCP            types.Object `tfsdk:"mcp"`
	HTTP           types.Object `tfsdk:"http"`
	SecretsVersion types.Int64  `tfsdk:"secrets_wo_version"`
}

// What a credential is for, one nested attribute per protocol, of which the
// schema's ExactlyOneOf leaves exactly one set. An `mcp` credential's auth is
// one nested attribute per scheme, of which exactlyOneVariant leaves exactly
// one set.
type (
	mcpCredentialConfig struct {
		Target types.String  `tfsdk:"target"`
		Auth   mcpAuthConfig `tfsdk:"auth"`
	}
	mcpAuthConfig struct {
		StaticBearer *staticBearerConfig `tfsdk:"static_bearer"`
		OAuth        *oauthConfig        `tfsdk:"oauth"`
	}
	httpCredentialConfig struct {
		Origins []types.String           `tfsdk:"origins"`
		Headers []credentialHeaderConfig `tfsdk:"headers"`
	}
	credentialHeaderConfig struct {
		Name  types.String `tfsdk:"name"`
		Value types.String `tfsdk:"value_wo"`
	}
	staticBearerConfig struct {
		Token types.String `tfsdk:"token_wo"`
	}
	oauthConfig struct {
		AccessToken types.String   `tfsdk:"access_token_wo"`
		ExpiresAt   types.String   `tfsdk:"expires_at"`
		Refresh     *refreshConfig `tfsdk:"refresh"`
	}
	refreshConfig struct {
		TokenEndpoint     types.String         `tfsdk:"token_endpoint"`
		ClientID          types.String         `tfsdk:"client_id"`
		Scope             types.String         `tfsdk:"scope"`
		RefreshToken      types.String         `tfsdk:"refresh_token_wo"`
		TokenEndpointAuth jsontypes.Normalized `tfsdk:"token_endpoint_auth_wo"`
	}
)

var (
	refreshTypes = map[string]attr.Type{
		"token_endpoint":         types.StringType,
		"client_id":              types.StringType,
		"scope":                  types.StringType,
		"refresh_token_wo":       types.StringType,
		"token_endpoint_auth_wo": jsontypes.NormalizedType{},
	}
	staticBearerTypes = map[string]attr.Type{
		"token_wo": types.StringType,
	}
	oauthTypes = map[string]attr.Type{
		"access_token_wo": types.StringType,
		"expires_at":      types.StringType,
		"refresh":         types.ObjectType{AttrTypes: refreshTypes},
	}
	mcpAuthTypes = map[string]attr.Type{
		"static_bearer": types.ObjectType{AttrTypes: staticBearerTypes},
		"oauth":         types.ObjectType{AttrTypes: oauthTypes},
	}
	mcpCredentialTypes = map[string]attr.Type{
		"target": types.StringType,
		"auth":   types.ObjectType{AttrTypes: mcpAuthTypes},
	}
	credentialHeaderType = types.ObjectType{AttrTypes: map[string]attr.Type{
		"name":     types.StringType,
		"value_wo": types.StringType,
	}}
	httpCredentialTypes = map[string]attr.Type{
		"origins": types.ListType{ElemType: types.StringType},
		"headers": types.ListType{ElemType: credentialHeaderType},
	}
)

// CODESYNC(credential-header-name-charset)
var headerNamePattern = regexp.MustCompile("^[A-Za-z0-9!#$%&'*+.^_`|~-]+$")

// reservedHeaders are the headers the client sending a request, or a proxy
// relaying it, owns.
// CODESYNC(credential-reserved-headers)
var reservedHeaders = []string{
	"host", "content-length", "transfer-encoding", "connection", "keep-alive", "te", "trailer", "upgrade",
}

// How the vault authenticates to the token endpoint when the configuration
// names no method.
const defaultTokenEndpointAuth = `{"type":"none"}`

// jsonObjectValidator refuses a JSON value that is not an object, which is
// what the token endpoint auth is on the wire. The attribute's own type has
// already refused anything that is not JSON at all.
type jsonObjectValidator struct{}

func (jsonObjectValidator) Description(context.Context) string {
	return "must be a JSON object"
}

func (v jsonObjectValidator) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}

func (v jsonObjectValidator) ValidateString(_ context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	encoded, ok := plannedString(req.ConfigValue).Get()
	if !ok {
		return
	}
	var object map[string]any
	if err := json.Unmarshal([]byte(encoded), &object); err != nil || object == nil {
		resp.Diagnostics.AddAttributeError(req.Path, "Not a JSON object",
			"This setting takes a JSON object, such as "+defaultTokenEndpointAuth+".")
	}
}

func (r *vaultCredentialResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_vault_credential"
}

func (r *vaultCredentialResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = vaultCredentialSchema()
}

func vaultCredentialSchema() schema.Schema {
	replaceString := []planmodifier.String{stringplanmodifier.RequiresReplace()}
	replaceObject := []planmodifier.Object{objectplanmodifier.RequiresReplace()}
	return schema.Schema{
		Version: 1,
		Description: "A credential a vault presents: to an MCP server, or to HTTP origins a function's " +
			"requests reach. Credentials cannot be changed in place, so any change replaces this one. " +
			"A credential connected through an OAuth sign-in is connected in the console instead.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"vault_id": schema.StringAttribute{
				Required:      true,
				PlanModifiers: replaceString,
			},
			"display_name": schema.StringAttribute{
				Optional: true,
				Computed: true,
				Default:  stringdefault.StaticString(""),
				// CODESYNC(display-name-len)
				Validators:    []validator.String{charsAtMost(200)},
				PlanModifiers: replaceString,
			},
			"mcp": schema.SingleNestedAttribute{
				Optional:      true,
				Description:   "A credential for one MCP server. Exactly one of this and `http`.",
				PlanModifiers: replaceObject,
				Attributes: map[string]schema.Attribute{
					"target": schema.StringAttribute{
						Required:    true,
						Description: "The exact MCP server URL the credential authenticates against. Unique within the vault.",
						// CODESYNC(target-len)
						Validators: []validator.String{charsBetween(1, 2048)},
					},
					"auth": schema.SingleNestedAttribute{
						Required:    true,
						Description: "How the credential authenticates, as exactly one of `static_bearer` and `oauth`.",
						Validators:  []validator.Object{exactlyOneVariant()},
						Attributes: map[string]schema.Attribute{
							"static_bearer": variant("A bearer token supplied once.", map[string]schema.Attribute{
								"token_wo": schema.StringAttribute{
									Required:    true,
									Sensitive:   true,
									WriteOnly:   true,
									Description: "The bearer token. Write-only.",
								},
							}),
							"oauth": variant("An OAuth access token, and what the vault needs to refresh it.", map[string]schema.Attribute{
								"access_token_wo": schema.StringAttribute{
									Required:    true,
									Sensitive:   true,
									WriteOnly:   true,
									Description: "The OAuth access token. Write-only.",
								},
								"expires_at": schema.StringAttribute{
									Optional:    true,
									Description: "When the access token expires, as RFC 3339.",
								},
								"refresh": schema.SingleNestedAttribute{
									Optional:    true,
									Description: "Set this to have the vault refresh the access token itself.",
									Attributes: map[string]schema.Attribute{
										"token_endpoint": schema.StringAttribute{
											Required:    true,
											Description: "Where the vault refreshes the access token.",
										},
										"client_id": schema.StringAttribute{
											Required: true,
										},
										"scope": schema.StringAttribute{
											Optional: true,
										},
										"refresh_token_wo": schema.StringAttribute{
											Required:    true,
											Sensitive:   true,
											WriteOnly:   true,
											Description: "The refresh token. Write-only.",
										},
										"token_endpoint_auth_wo": schema.StringAttribute{
											Optional:   true,
											Sensitive:  true,
											WriteOnly:  true,
											CustomType: jsontypes.NormalizedType{},
											Description: "How the vault authenticates to the token endpoint, as a JSON " +
												"object. Write-only. Defaults to `" + defaultTokenEndpointAuth + "`.",
											Validators: []validator.String{jsonObjectValidator{}},
										},
									},
								},
							}),
						},
					},
				},
			},
			"http": schema.SingleNestedAttribute{
				Optional: true,
				Description: "A credential sent as headers with every request a function makes to its " +
					"origins, which an agent's function grant names under `http_credentials`. Exactly " +
					"one of this and `mcp`.",
				PlanModifiers: replaceObject,
				Attributes: map[string]schema.Attribute{
					"origins": schema.ListAttribute{
						Required:    true,
						ElementType: types.StringType,
						Description: "The origins the credential is sent to, as `https://host[:port]` with nothing " +
							"past it, each once. No other credential in the vault may be sent to one of them.",
						Validators: []validator.List{
							// CODESYNC(credential-origins-len)
							listvalidator.SizeBetween(1, 16),
							listvalidator.UniqueValues(),
							// CODESYNC(target-len)
							listvalidator.ValueStringsAre(charsBetween(1, 2048)),
						},
					},
					"headers": schema.ListNestedAttribute{
						Required:    true,
						Description: "The headers the credential is sent as, each name once.",
						// CODESYNC(credential-headers-len)
						Validators: []validator.List{listvalidator.SizeBetween(1, 16)},
						NestedObject: schema.NestedAttributeObject{
							Attributes: map[string]schema.Attribute{
								"name": schema.StringAttribute{
									Required: true,
									Description: "The header's name, compared without regard to case. A header the " +
										"client or a proxy owns (`host`, `content-length`, `proxy-*`, and the like) is refused.",
									Validators: []validator.String{
										// CODESYNC(credential-header-name-len)
										charsBetween(1, 256),
										stringvalidator.RegexMatches(headerNamePattern, "must be an HTTP header name"),
										stringvalidator.NoneOfCaseInsensitive(reservedHeaders...),
									},
								},
								"value_wo": schema.StringAttribute{
									Required:    true,
									Sensitive:   true,
									WriteOnly:   true,
									Description: "The header's value, such as `Bearer <token>`. Write-only.",
									// CODESYNC(credential-header-value-len)
									Validators: []validator.String{charsBetween(1, 8192)},
								},
							},
						},
					},
				},
			},
			"secrets_wo_version": schema.Int64Attribute{
				Required: true,
				Description: "Which write-only secrets this credential holds. Bump it to send new " +
					"ones, which replaces the credential. Required, so a rotation is always " +
					"expressible -- a changed secret alone is invisible to Terraform.",
				Validators:    []validator.Int64{int64validator.AtLeast(0)},
				PlanModifiers: []planmodifier.Int64{int64planmodifier.RequiresReplace()},
			},
		},
	}
}

// ConfigValidators states the rule the schema cannot: a credential is for
// one protocol, so the two nested attributes are alternatives.
func (r *vaultCredentialResource) ConfigValidators(_ context.Context) []resource.ConfigValidator {
	return []resource.ConfigValidator{
		resourcevalidator.ExactlyOneOf(
			path.MatchRoot("mcp"),
			path.MatchRoot("http"),
		),
	}
}

func (r *vaultCredentialResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = clientFrom(req.ProviderData, &resp.Diagnostics)
}

// newCredential reads the credential the configuration carries, secrets and
// all. Only the config holds write-only values, so this never reads the plan
// or the state.
func (m vaultCredentialModel) newCredential(ctx context.Context) (client.NewCredential, diag.Diagnostics) {
	var diags diag.Diagnostics
	_, mcp := planned(m.MCP).Get()
	_, http := planned(m.HTTP).Get()
	switch {
	case mcp && !http:
		var config mcpCredentialConfig
		diags.Append(m.MCP.As(ctx, &config, basetypes.ObjectAsOptions{})...)
		if diags.HasError() {
			return nil, diags
		}
		payload := config.Auth.payload(&diags)
		return client.NewMCPCredential{
			Target:      config.Target.ValueString(),
			DisplayName: m.DisplayName.ValueString(),
			Payload:     payload,
		}, diags

	case http && !mcp:
		var config httpCredentialConfig
		diags.Append(m.HTTP.As(ctx, &config, basetypes.ObjectAsOptions{})...)
		credential := client.NewHTTPCredential{
			Origins:     stringValues(config.Origins),
			DisplayName: m.DisplayName.ValueString(),
		}
		for _, header := range config.Headers {
			credential.Headers = append(credential.Headers, client.CredentialHeader{
				Name:  header.Name.ValueString(),
				Value: header.Value.ValueString(),
			})
		}
		return credential, diags
	}
	diags.AddError("Ambiguous credential", "Set exactly one of mcp and http.")
	return nil, diags
}

func (a mcpAuthConfig) payload(diags *diag.Diagnostics) client.CredentialPayload {
	switch {
	case a.StaticBearer != nil:
		return client.StaticBearerPayload{Token: a.StaticBearer.Token.ValueString()}
	case a.OAuth != nil:
		payload := client.OAuthPayload{
			AccessToken: a.OAuth.AccessToken.ValueString(),
			ExpiresAt:   stringPointer(a.OAuth.ExpiresAt),
		}
		if refresh := a.OAuth.Refresh; refresh != nil {
			auth := json.RawMessage(defaultTokenEndpointAuth)
			if named, ok := plannedString(refresh.TokenEndpointAuth.StringValue).Get(); ok {
				auth = json.RawMessage(named)
			}
			payload.Refresh = &client.RefreshBlock{
				TokenEndpoint:     refresh.TokenEndpoint.ValueString(),
				ClientID:          refresh.ClientID.ValueString(),
				Scope:             stringPointer(refresh.Scope),
				RefreshToken:      refresh.RefreshToken.ValueString(),
				TokenEndpointAuth: auth,
			}
		}
		return payload
	}
	ambiguous(diags, "mcp")
	return nil
}

// mcpAuth is the `auth` the state holds for a credential authenticating as
// scheme: the one held already where it names that scheme, since it carries
// settings the server never reports, and otherwise that scheme with nothing
// known but the scheme.
func mcpAuth(held types.Object, scheme string) (types.Object, bool) {
	if heldScheme, ok := methodOf(held); ok && heldScheme == scheme {
		return held, true
	}
	variants := map[string]attr.Value{
		"static_bearer": types.ObjectNull(staticBearerTypes),
		"oauth":         types.ObjectNull(oauthTypes),
	}
	switch scheme {
	case client.AuthStaticBearer:
		variants["static_bearer"] = types.ObjectValueMust(staticBearerTypes, map[string]attr.Value{
			"token_wo": types.StringNull(),
		})
	case client.AuthOAuth:
		variants["oauth"] = types.ObjectValueMust(oauthTypes, map[string]attr.Value{
			"access_token_wo": types.StringNull(),
			"expires_at":      types.StringNull(),
			"refresh":         types.ObjectNull(refreshTypes),
		})
	default:
		return types.ObjectNull(mcpAuthTypes), false
	}
	return types.ObjectValueMust(mcpAuthTypes, variants), true
}

// heldStrings reads a known list of strings, or nothing.
func heldStrings(ctx context.Context, list types.List) []string {
	var out []string
	if _, ok := planned(list).Get(); ok {
		_ = list.ElementsAs(ctx, &out, false)
	}
	return out
}

// read refreshes m from the server. The server returns a credential's
// metadata only, so the protocol and scheme it names decide which variant
// the state holds; a variant the state already spells keeps the settings it
// carries, none of which the server would report, and a value the server
// spells its own way keeps the state's spelling where the two agree.
func (r *vaultCredentialResource) read(ctx context.Context, m *vaultCredentialModel) (found bool, diags diag.Diagnostics) {
	vaultID, id := m.VaultID.ValueString(), m.ID.ValueString()
	credential, err := r.client.FindCredential(ctx, vaultID, id)
	if client.IsNotFound(err) {
		return false, diags
	}
	if err != nil {
		apiError(&diags, "read credential "+id, err)
		return true, diags
	}
	m.DisplayName = types.StringValue(credential.DisplayName)

	switch credential.Protocol {
	case client.ProtocolMCP:
		heldAuth := types.ObjectNull(mcpAuthTypes)
		if _, ok := planned(m.MCP).Get(); ok {
			heldAuth = m.MCP.Attributes()["auth"].(types.Object)
		}
		auth, ok := mcpAuth(heldAuth, credential.AuthScheme)
		if !ok {
			diags.AddError("Unknown auth scheme",
				"Credential "+id+" authenticates as "+credential.AuthScheme+", which this provider does not manage.")
			return true, diags
		}
		m.MCP = types.ObjectValueMust(mcpCredentialTypes, map[string]attr.Value{
			"target": types.StringValue(credential.Target),
			"auth":   auth,
		})
		m.HTTP = types.ObjectNull(httpCredentialTypes)

	case client.ProtocolHTTP:
		if credential.AuthScheme != client.AuthHeader {
			diags.AddError("Credential connected through OAuth",
				"Credential "+id+" was connected through an OAuth sign-in, which only the console can "+
					"repeat. Manage it there, and remove it from this configuration.")
			return true, diags
		}
		var heldOrigins, heldNames []string
		if _, ok := planned(m.HTTP).Get(); ok {
			attributes := m.HTTP.Attributes()
			heldOrigins = heldStrings(ctx, attributes["origins"].(types.List))
			var headers []credentialHeaderConfig
			if list := attributes["headers"].(types.List); !list.IsNull() && !list.IsUnknown() {
				_ = list.ElementsAs(ctx, &headers, false)
			}
			for _, header := range headers {
				heldNames = append(heldNames, header.Name.ValueString())
			}
		}
		origins := credential.Origins
		if len(heldOrigins) == len(origins) {
			same := true
			for i := range origins {
				same = same && sameEndpoint(heldOrigins[i], origins[i])
			}
			if same {
				origins = heldOrigins
			}
		}
		headers := make([]attr.Value, len(credential.Headers))
		for i, header := range credential.Headers {
			name := header.Name
			if len(heldNames) == len(credential.Headers) && strings.EqualFold(heldNames[i], name) {
				name = heldNames[i]
			}
			headers[i] = types.ObjectValueMust(credentialHeaderType.AttrTypes, map[string]attr.Value{
				"name":     types.StringValue(name),
				"value_wo": types.StringNull(),
			})
		}
		originValues, d := types.ListValueFrom(ctx, types.StringType, orEmpty(origins))
		diags.Append(d...)
		m.HTTP = types.ObjectValueMust(httpCredentialTypes, map[string]attr.Value{
			"origins": originValues,
			"headers": types.ListValueMust(credentialHeaderType, headers),
		})
		m.MCP = types.ObjectNull(mcpCredentialTypes)

	default:
		diags.AddError("Unknown protocol",
			"Credential "+id+" speaks "+credential.Protocol+", which this provider does not manage.")
	}
	return true, diags
}

func (r *vaultCredentialResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan, config vaultCredentialModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}
	credential, diags := config.newCredential(ctx)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	vaultID := plan.VaultID.ValueString()
	id, err := r.client.AddCredential(ctx, vaultID, credential)
	if err != nil {
		apiError(&resp.Diagnostics, "add a credential to vault "+vaultID, err)
		return
	}
	// Recorded before the read-back, so a failure there taints this
	// credential rather than orphaning it.
	created := plan
	created.ID = types.StringValue(id)
	next := created
	finishCreate(ctx, resp, subject{"Credential", id}, r.read, next, created)
}

func (r *vaultCredentialResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	refresh(ctx, req, resp, r.read)
}

// Update exists because the framework asks for it, and no plan reaches it:
// every stored attribute replaces the credential, and a write-only value is
// null in plan and state alike, so none can differ. It sends nothing, and
// records what the server holds.
func (r *vaultCredentialResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state vaultCredentialModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	finishUpdate(ctx, resp, subject{"Credential", plan.ID.ValueString()}, r.read, plan, state)
}

func (r *vaultCredentialResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state vaultCredentialModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	vaultID, id := state.VaultID.ValueString(), state.ID.ValueString()
	remove(resp, "delete credential "+id, func() error {
		return r.client.DeleteCredential(ctx, vaultID, id)
	})
}

// ImportState takes `<vault_id>/<credential_id>`.
func (r *vaultCredentialResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	vaultID, id, ok := strings.Cut(req.ID, "/")
	if !ok || vaultID == "" || id == "" {
		resp.Diagnostics.AddError("Invalid import id", "Expected <vault_id>/<credential_id>, got "+req.ID)
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("vault_id"), vaultID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), id)...)
}

// UpgradeState moves a version 0 state, whose MCP target and auth schemes sat
// at the top level, under `mcp`.
func (r *vaultCredentialResource) UpgradeState(context.Context) map[int64]resource.StateUpgrader {
	return map[int64]resource.StateUpgrader{
		0: {StateUpgrader: upgradeVaultCredentialStateV0},
	}
}

func upgradeVaultCredentialStateV0(_ context.Context, req resource.UpgradeStateRequest, resp *resource.UpgradeStateResponse) {
	upgraded, err := upgradeVaultCredentialJSONV0(req.RawState.JSON)
	if err != nil {
		resp.Diagnostics.AddError("Unreadable credential state", err.Error())
		return
	}
	resp.DynamicValue = &tfprotov6.DynamicValue{JSON: upgraded}
}

func upgradeVaultCredentialJSONV0(raw []byte) ([]byte, error) {
	var state map[string]any
	if err := json.Unmarshal(raw, &state); err != nil {
		return nil, err
	}
	state["mcp"] = map[string]any{
		"target": state["target"],
		"auth":   map[string]any{"static_bearer": state["static_bearer"], "oauth": state["oauth"]},
	}
	state["http"] = nil
	for _, moved := range []string{"target", "static_bearer", "oauth"} {
		delete(state, moved)
	}
	return json.Marshal(state)
}
