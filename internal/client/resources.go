package client

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
)

// Workspace is `WorkspaceBody`.
type Workspace struct {
	ID             string `json:"id"`
	OrganizationID string `json:"organization_id"`
	Name           string `json:"name"`
}

func (c *Client) GetWorkspace(ctx context.Context, id string) (*Workspace, error) {
	var out Workspace
	err := c.do(ctx, request{method: http.MethodGet, path: "/v1/workspaces/" + url.PathEscape(id)}, &out)
	return &out, err
}

// Agent is `AgentBody`.
type Agent struct {
	ID          string `json:"id"`
	WorkspaceID string `json:"workspace_id"`
	Name        string `json:"name"`
}

type agentName struct {
	Name string `json:"name"`
}

func (c *Client) CreateAgent(ctx context.Context, name string) (*Agent, error) {
	req, err := jsonRequest(http.MethodPost, "/v1/agents", agentName{Name: name})
	if err != nil {
		return nil, err
	}
	var out Agent
	return &out, c.do(ctx, req, &out)
}

func (c *Client) GetAgent(ctx context.Context, id string) (*Agent, error) {
	var out Agent
	err := c.do(ctx, request{method: http.MethodGet, path: agentPath(id)}, &out)
	return &out, err
}

func (c *Client) RenameAgent(ctx context.Context, id, name string) (*Agent, error) {
	req, err := jsonRequest(http.MethodPatch, agentPath(id), agentName{Name: name})
	if err != nil {
		return nil, err
	}
	var out Agent
	return &out, c.do(ctx, req, &out)
}

func (c *Client) DeleteAgent(ctx context.Context, id string) error {
	return c.do(ctx, request{method: http.MethodDelete, path: agentPath(id)}, nil)
}

func agentPath(id string) string {
	return "/v1/agents/" + url.PathEscape(id)
}

// AgentSecurity is `AgentSecurityBody`, and the `PUT` body it answers.
type AgentSecurity struct {
	AllowedOrigins []string `json:"allowed_origins"`
}

func (c *Client) GetAgentSecurity(ctx context.Context, agentID string) (*AgentSecurity, error) {
	var out AgentSecurity
	err := c.do(ctx, request{method: http.MethodGet, path: agentPath(agentID) + "/security"}, &out)
	return &out, err
}

func (c *Client) PutAgentSecurity(ctx context.Context, agentID string, security AgentSecurity) (*AgentSecurity, error) {
	if security.AllowedOrigins == nil {
		security.AllowedOrigins = []string{}
	}
	req, err := jsonRequest(http.MethodPut, agentPath(agentID)+"/security", security)
	if err != nil {
		return nil, err
	}
	var out AgentSecurity
	return &out, c.do(ctx, req, &out)
}

// AgentVersion is `AgentVersionBody`. Config is the `AgentConfigBody` the
// version was published with; AgentConfig decodes it.
type AgentVersion struct {
	ID              string          `json:"id"`
	AgentID         string          `json:"agent_id"`
	Version         int64           `json:"version"`
	ModelProviderID string          `json:"model_provider_id"`
	Config          json.RawMessage `json:"config"`
}

func (c *Client) PublishAgentVersion(ctx context.Context, agentID string, config AgentConfig) (*AgentVersion, error) {
	body, err := config.publishBody()
	if err != nil {
		return nil, err
	}
	req, err := jsonRequest(http.MethodPost, agentPath(agentID)+"/versions", body)
	if err != nil {
		return nil, err
	}
	var out AgentVersion
	return &out, c.do(ctx, req, &out)
}

// LatestAgentVersion is the newest published version, or nil for an agent
// that has none.
func (c *Client) LatestAgentVersion(ctx context.Context, agentID string) (*AgentVersion, error) {
	return latest[AgentVersion](ctx, c, agentPath(agentID)+"/versions")
}

// Skill is `SkillBody`.
type Skill struct {
	ID            string `json:"id"`
	DisplayName   string `json:"display_name"`
	Name          string `json:"name"`
	LatestVersion int64  `json:"latest_version"`
}

// ManifestEntry is one file a stored skill version or function package
// holds.
type ManifestEntry struct {
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	Sha256 string `json:"sha256"`
}

// SkillVersion is `SkillVersionBody`.
type SkillVersion struct {
	ID          string          `json:"id"`
	SkillID     string          `json:"skill_id"`
	Version     int64           `json:"version"`
	Manifest    []ManifestEntry `json:"manifest"`
	BundleBytes int64           `json:"bundle_bytes"`
}

func skillPath(id string) string {
	return "/v1/skills/" + url.PathEscape(id)
}

func bundleRequest(path string, bundle []byte) request {
	return request{
		method:      http.MethodPost,
		path:        path,
		body:        bundle,
		contentType: "application/gzip",
	}
}

// CreateSkill uploads a gzipped tar as a new skill's first version.
func (c *Client) CreateSkill(ctx context.Context, bundle []byte) (*SkillVersion, error) {
	var out SkillVersion
	return &out, c.do(ctx, bundleRequest("/v1/skills", bundle), &out)
}

// PushSkillVersion uploads a gzipped tar as a skill's next version.
func (c *Client) PushSkillVersion(ctx context.Context, skillID string, bundle []byte) (*SkillVersion, error) {
	var out SkillVersion
	return &out, c.do(ctx, bundleRequest(skillPath(skillID)+"/versions", bundle), &out)
}

func (c *Client) GetSkill(ctx context.Context, id string) (*Skill, error) {
	var out Skill
	err := c.do(ctx, request{method: http.MethodGet, path: skillPath(id)}, &out)
	return &out, err
}

func (c *Client) RenameSkill(ctx context.Context, id, displayName string) (*Skill, error) {
	req, err := jsonRequest(http.MethodPatch, skillPath(id), map[string]string{"display_name": displayName})
	if err != nil {
		return nil, err
	}
	var out Skill
	return &out, c.do(ctx, req, &out)
}

func (c *Client) DeleteSkill(ctx context.Context, id string) error {
	return c.do(ctx, request{method: http.MethodDelete, path: skillPath(id)}, nil)
}

func (c *Client) LatestSkillVersion(ctx context.Context, skillID string) (*SkillVersion, error) {
	return latest[SkillVersion](ctx, c, skillPath(skillID)+"/versions")
}

// Function is `FunctionBody`: a function package.
type Function struct {
	ID            string `json:"id"`
	DisplayName   string `json:"display_name"`
	Name          string `json:"name"`
	LatestVersion int64  `json:"latest_version"`
}

// FunctionManifest is the part of `FunctionManifestBody` a bundle's
// `subako-package.json` states; the functions are listed by the module.
type FunctionManifest struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

// FunctionAssets is `FunctionAssetsBody`: the files under a bundle's
// `assets/`, each listed by its path under that directory.
type FunctionAssets struct {
	Files []ManifestEntry `json:"files"`
}

// FunctionVersion is `FunctionVersionBody`. A nil Assets is a bundle that
// held no `assets/`.
type FunctionVersion struct {
	ID           string           `json:"id"`
	FunctionID   string           `json:"function_id"`
	Version      int64            `json:"version"`
	Manifest     FunctionManifest `json:"manifest"`
	ModuleSha256 string           `json:"module_sha256"`
	Assets       *FunctionAssets  `json:"assets"`
}

func functionPath(id string) string {
	return "/v1/functions/" + url.PathEscape(id)
}

// CreateFunction uploads a gzipped tar as a new function package's first
// version.
func (c *Client) CreateFunction(ctx context.Context, bundle []byte) (*FunctionVersion, error) {
	var out FunctionVersion
	return &out, c.do(ctx, bundleRequest("/v1/functions", bundle), &out)
}

// PushFunctionVersion uploads a gzipped tar as a package's next version.
func (c *Client) PushFunctionVersion(ctx context.Context, functionID string, bundle []byte) (*FunctionVersion, error) {
	var out FunctionVersion
	return &out, c.do(ctx, bundleRequest(functionPath(functionID)+"/versions", bundle), &out)
}

func (c *Client) GetFunction(ctx context.Context, id string) (*Function, error) {
	var out Function
	err := c.do(ctx, request{method: http.MethodGet, path: functionPath(id)}, &out)
	return &out, err
}

func (c *Client) RenameFunction(ctx context.Context, id, displayName string) (*Function, error) {
	req, err := jsonRequest(http.MethodPatch, functionPath(id), map[string]string{"display_name": displayName})
	if err != nil {
		return nil, err
	}
	var out Function
	return &out, c.do(ctx, req, &out)
}

func (c *Client) DeleteFunction(ctx context.Context, id string) error {
	return c.do(ctx, request{method: http.MethodDelete, path: functionPath(id)}, nil)
}

func (c *Client) LatestFunctionVersion(ctx context.Context, functionID string) (*FunctionVersion, error) {
	return latest[FunctionVersion](ctx, c, functionPath(functionID)+"/versions")
}

// Vault is `VaultBody`. Metadata is whatever JSON is stored under it: the
// create and update bodies take an unconstrained value, so a vault another
// client made may carry anything at all there.
type Vault struct {
	ID          string          `json:"id"`
	WorkspaceID string          `json:"workspace_id"`
	DisplayName string          `json:"display_name"`
	Metadata    json.RawMessage `json:"metadata"`
}

// createdID is the id a create route answers with and nothing more.
type createdID struct {
	ID string `json:"id"`
}

func vaultPath(id string) string {
	return "/v1/vaults/" + url.PathEscape(id)
}

// CreateVault answers the new vault's id; the server returns nothing more.
func (c *Client) CreateVault(ctx context.Context, displayName string, metadata map[string]string) (string, error) {
	req, err := jsonRequest(http.MethodPost, "/v1/vaults", map[string]any{
		"display_name": displayName,
		"metadata":     metadataObject(metadata),
	})
	if err != nil {
		return "", err
	}
	var out createdID
	if err := c.do(ctx, req, &out); err != nil {
		return "", err
	}
	return out.ID, nil
}

func (c *Client) GetVault(ctx context.Context, id string) (*Vault, error) {
	var out Vault
	err := c.do(ctx, request{method: http.MethodGet, path: vaultPath(id)}, &out)
	return &out, err
}

// UpdateVault replaces both fields: the provider always knows the whole
// desired vault.
func (c *Client) UpdateVault(ctx context.Context, id, displayName string, metadata map[string]string) (*Vault, error) {
	req, err := jsonRequest(http.MethodPatch, vaultPath(id), map[string]any{
		"display_name": displayName,
		"metadata":     metadataObject(metadata),
	})
	if err != nil {
		return nil, err
	}
	var out Vault
	return &out, c.do(ctx, req, &out)
}

func (c *Client) DeleteVault(ctx context.Context, id string) error {
	return c.do(ctx, request{method: http.MethodDelete, path: vaultPath(id)}, nil)
}

func metadataObject(metadata map[string]string) map[string]string {
	if metadata == nil {
		return map[string]string{}
	}
	return metadata
}

// Protocols a credential speaks, as `CredentialBody` tags them.
const (
	ProtocolMCP  = "mcp"
	ProtocolHTTP = "http"
)

// Credential is `CredentialBody`: metadata only. Target is an `mcp`
// credential's; Origins and Headers are an `http` one's.
type Credential struct {
	ID          string                 `json:"id"`
	VaultID     string                 `json:"vault_id"`
	Protocol    string                 `json:"protocol"`
	AuthScheme  string                 `json:"auth_scheme"`
	Target      string                 `json:"target"`
	Origins     []string               `json:"origins"`
	Headers     []CredentialHeaderName `json:"headers"`
	DisplayName string                 `json:"display_name"`
}

// CredentialHeaderName is `CredentialHeaderNameBody`: a header an `http`
// credential is sent as, without its value.
type CredentialHeaderName struct {
	Name string `json:"name"`
}

// NewMCPCredential is `AddCredentialBody` for an MCP server. Payload carries
// the secret.
type NewMCPCredential struct {
	Target      string
	DisplayName string
	Payload     CredentialPayload
}

func (c NewMCPCredential) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Protocol    string            `json:"protocol"`
		Target      string            `json:"target"`
		DisplayName string            `json:"display_name"`
		Payload     CredentialPayload `json:"payload"`
	}{ProtocolMCP, c.Target, c.DisplayName, c.Payload})
}

// NewHTTPCredential is `AddCredentialBody` for HTTP origins: headers sent
// with every request to them. Each header's value is a secret.
type NewHTTPCredential struct {
	Origins     []string
	DisplayName string
	Headers     []CredentialHeader
}

// CredentialHeader is `CredentialHeaderBody`.
type CredentialHeader struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// AuthHeader is how an `http` credential given headers authenticates.
const AuthHeader = "header"

func (c NewHTTPCredential) MarshalJSON() ([]byte, error) {
	type payload struct {
		AuthScheme string             `json:"auth_scheme"`
		Headers    []CredentialHeader `json:"headers"`
	}
	return json.Marshal(struct {
		Protocol    string   `json:"protocol"`
		Origins     []string `json:"origins"`
		DisplayName string   `json:"display_name"`
		Payload     payload  `json:"payload"`
	}{ProtocolHTTP, orEmpty(c.Origins), c.DisplayName, payload{AuthHeader, orEmpty(c.Headers)}})
}

// NewCredential is a new credential's body: NewMCPCredential or
// NewHTTPCredential.
type NewCredential interface {
	json.Marshaler
	sealedNewCredential()
}

func (NewMCPCredential) sealedNewCredential()  {}
func (NewHTTPCredential) sealedNewCredential() {}

// Auth schemes, as `McpSchemeBody` spells them.
const (
	AuthStaticBearer = "static_bearer"
	AuthOAuth        = "oauth"
)

// CredentialPayload is `CredentialPayloadBody`: the secret a credential
// seals, and how it authenticates with it. The two types below are its only
// forms -- the unexported method seals the interface -- and each writes its
// own auth scheme tag.
type CredentialPayload interface {
	json.Marshaler
	// AuthScheme is the tag the wire carries, as `AuthSchemeBody` spells it.
	AuthScheme() string
	sealedCredentialPayload()
}

// StaticBearerPayload is a bearer token supplied once.
type StaticBearerPayload struct {
	Token string
}

// OAuthPayload is an access token, with what the vault needs to refresh it
// once it expires.
type OAuthPayload struct {
	AccessToken string
	ExpiresAt   *string
	Refresh     *RefreshBlock
}

func (StaticBearerPayload) AuthScheme() string { return AuthStaticBearer }
func (OAuthPayload) AuthScheme() string        { return AuthOAuth }

func (StaticBearerPayload) sealedCredentialPayload() {}
func (OAuthPayload) sealedCredentialPayload()        {}

func (p StaticBearerPayload) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		AuthScheme string `json:"auth_scheme"`
		Token      string `json:"token"`
	}{p.AuthScheme(), p.Token})
}

func (p OAuthPayload) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		AuthScheme  string        `json:"auth_scheme"`
		AccessToken string        `json:"access_token"`
		ExpiresAt   *string       `json:"expires_at,omitempty"`
		Refresh     *RefreshBlock `json:"refresh,omitempty"`
	}{p.AuthScheme(), p.AccessToken, p.ExpiresAt, p.Refresh})
}

// RefreshBlock is `RefreshBlockBody`.
type RefreshBlock struct {
	TokenEndpoint     string          `json:"token_endpoint"`
	ClientID          string          `json:"client_id"`
	Scope             *string         `json:"scope,omitempty"`
	RefreshToken      string          `json:"refresh_token"`
	TokenEndpointAuth json.RawMessage `json:"token_endpoint_auth"`
}

func (c *Client) AddCredential(ctx context.Context, vaultID string, credential NewCredential) (string, error) {
	req, err := jsonRequest(http.MethodPost, vaultPath(vaultID)+"/credentials", credential)
	if err != nil {
		return "", err
	}
	var out createdID
	if err := c.do(ctx, req, &out); err != nil {
		return "", err
	}
	return out.ID, nil
}

// FindCredential walks a vault's credentials for one id. A missing
// credential is a not-found error, like a missing resource elsewhere.
func (c *Client) FindCredential(ctx context.Context, vaultID, credentialID string) (*Credential, error) {
	all, err := listAll[Credential](ctx, c, vaultPath(vaultID)+"/credentials")
	if err != nil {
		return nil, err
	}
	for i := range all {
		if all[i].ID == credentialID {
			return &all[i], nil
		}
	}
	return nil, &Error{Status: http.StatusNotFound, Code: "not_found", Message: "credential not found"}
}

func (c *Client) DeleteCredential(ctx context.Context, vaultID, credentialID string) error {
	path := vaultPath(vaultID) + "/credentials/" + url.PathEscape(credentialID)
	return c.do(ctx, request{method: http.MethodDelete, path: path}, nil)
}

// OAuthClient is `OAuthClientBody`: a client the workspace created at a
// provider, which vault credentials connect through. It carries no secret.
type OAuthClient struct {
	ID                      string               `json:"id"`
	WorkspaceID             string               `json:"workspace_id"`
	DisplayName             string               `json:"display_name"`
	AuthorizationEndpoint   string               `json:"authorization_endpoint"`
	TokenEndpoint           string               `json:"token_endpoint"`
	ClientID                string               `json:"client_id"`
	TokenEndpointAuthMethod string               `json:"token_endpoint_auth_method"`
	Scopes                  []string             `json:"scopes"`
	AuthorizationParams     []AuthorizationParam `json:"authorization_params"`
}

// AuthorizationParam is `AuthorizationParamBody`: a parameter the
// authorization URL carries beside the ones Subako sets.
type AuthorizationParam struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// Token endpoint auth methods, as `TokenEndpointAuthMethodBody` spells them.
const (
	TokenEndpointAuthNone              = "none"
	TokenEndpointAuthClientSecretPost  = "client_secret_post"
	TokenEndpointAuthClientSecretBasic = "client_secret_basic"
)

// TokenEndpointAuth is `TokenEndpointAuthBody`: how Subako authenticates at
// the provider's token endpoint as the client. The three types below are its
// only forms, and each writes its own method tag.
type TokenEndpointAuth interface {
	json.Marshaler
	// Method is the tag the wire carries.
	Method() string
	sealedTokenEndpointAuth()
}

type (
	// PublicClient authenticates with PKCE alone.
	PublicClient struct{}
	// ClientSecretPost sends the client secret in the token request's form.
	ClientSecretPost struct {
		ClientSecret string
	}
	// ClientSecretBasic sends the client secret in a `Basic` header.
	ClientSecretBasic struct {
		ClientSecret string
	}
)

func (PublicClient) Method() string      { return TokenEndpointAuthNone }
func (ClientSecretPost) Method() string  { return TokenEndpointAuthClientSecretPost }
func (ClientSecretBasic) Method() string { return TokenEndpointAuthClientSecretBasic }

func (PublicClient) sealedTokenEndpointAuth()      {}
func (ClientSecretPost) sealedTokenEndpointAuth()  {}
func (ClientSecretBasic) sealedTokenEndpointAuth() {}

type clientSecretBody struct {
	Method       string `json:"method"`
	ClientSecret string `json:"client_secret"`
}

func (a PublicClient) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Method string `json:"method"`
	}{a.Method()})
}

func (a ClientSecretPost) MarshalJSON() ([]byte, error) {
	return json.Marshal(clientSecretBody{Method: a.Method(), ClientSecret: a.ClientSecret})
}

func (a ClientSecretBasic) MarshalJSON() ([]byte, error) {
	return json.Marshal(clientSecretBody{Method: a.Method(), ClientSecret: a.ClientSecret})
}

// NewOAuthClient is `CreateOAuthClientBody`.
type NewOAuthClient struct {
	DisplayName           string               `json:"display_name"`
	AuthorizationEndpoint string               `json:"authorization_endpoint"`
	TokenEndpoint         string               `json:"token_endpoint"`
	ClientID              string               `json:"client_id"`
	TokenEndpointAuth     TokenEndpointAuth    `json:"token_endpoint_auth"`
	Scopes                []string             `json:"scopes"`
	AuthorizationParams   []AuthorizationParam `json:"authorization_params"`
}

// OAuthClientUpdate is `UpdateOAuthClientBody`. The provider always knows
// the whole client, so every field but the auth is sent; a nil
// TokenEndpointAuth leaves the method and the secret as they stand.
type OAuthClientUpdate struct {
	DisplayName         string               `json:"display_name"`
	TokenEndpointAuth   TokenEndpointAuth    `json:"token_endpoint_auth,omitempty"`
	Scopes              []string             `json:"scopes"`
	AuthorizationParams []AuthorizationParam `json:"authorization_params"`
}

func oauthClientPath(id string) string {
	return "/v1/oauth-clients/" + url.PathEscape(id)
}

// CreateOAuthClient answers the new client's id; the server returns nothing
// more.
func (c *Client) CreateOAuthClient(ctx context.Context, oauthClient NewOAuthClient) (string, error) {
	oauthClient.Scopes = orEmpty(oauthClient.Scopes)
	oauthClient.AuthorizationParams = orEmpty(oauthClient.AuthorizationParams)
	req, err := jsonRequest(http.MethodPost, "/v1/oauth-clients", oauthClient)
	if err != nil {
		return "", err
	}
	var out createdID
	if err := c.do(ctx, req, &out); err != nil {
		return "", err
	}
	return out.ID, nil
}

func (c *Client) GetOAuthClient(ctx context.Context, id string) (*OAuthClient, error) {
	var out OAuthClient
	err := c.do(ctx, request{method: http.MethodGet, path: oauthClientPath(id)}, &out)
	return &out, err
}

func (c *Client) UpdateOAuthClient(ctx context.Context, id string, update OAuthClientUpdate) (*OAuthClient, error) {
	update.Scopes = orEmpty(update.Scopes)
	update.AuthorizationParams = orEmpty(update.AuthorizationParams)
	req, err := jsonRequest(http.MethodPatch, oauthClientPath(id), update)
	if err != nil {
		return nil, err
	}
	var out OAuthClient
	return &out, c.do(ctx, req, &out)
}

func (c *Client) DeleteOAuthClient(ctx context.Context, id string) error {
	return c.do(ctx, request{method: http.MethodDelete, path: oauthClientPath(id)}, nil)
}

// ModelProvider is `ModelProviderBody`: a platform provider or a workspace's
// own, told apart by Type. RawAuth is a workspace provider's auth as it came,
// which AuthOf reads; a listing never needs it, so an auth this client does
// not know fails only the read that asks for it.
type ModelProvider struct {
	Type        string          `json:"type"`
	ID          string          `json:"id"`
	Format      string          `json:"format"`
	DisplayName string          `json:"display_name"`
	Description string          `json:"description"`
	WorkspaceID *string         `json:"workspace_id"`
	Models      []PlatformModel `json:"models"`
	RawAuth     json.RawMessage `json:"auth"`
}

// AuthOf reads a workspace provider's `ModelProviderAuthBody` by its tag.
func (p *ModelProvider) AuthOf() (ModelProviderAuth, error) {
	var tag struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(p.RawAuth, &tag); err != nil {
		return nil, fmt.Errorf("read model provider auth: %w", err)
	}
	switch tag.Type {
	case AuthAPIKey:
		return APIKeyAuth{}, nil
	case AuthAWSWebIdentity:
		var auth AWSWebIdentityAuth
		return auth, json.Unmarshal(p.RawAuth, &auth)
	case AuthAnthropicWorkloadIdentity:
		var auth AnthropicWorkloadIdentityAuth
		return auth, json.Unmarshal(p.RawAuth, &auth)
	case AuthOpenAIWorkloadIdentity:
		var auth OpenAIWorkloadIdentityAuth
		return auth, json.Unmarshal(p.RawAuth, &auth)
	default:
		return nil, fmt.Errorf("model provider authenticates as %q, which this provider does not manage", tag.Type)
	}
}

// Model provider auths, as `ModelProviderAuthInput` tags them.
const (
	AuthAPIKey                    = "api_key"
	AuthAWSWebIdentity            = "aws_web_identity"
	AuthAnthropicWorkloadIdentity = "anthropic_workload_identity"
	AuthOpenAIWorkloadIdentity    = "openai_workload_identity"
)

// ModelProviderAuth is how a workspace provider authenticates upstream:
// `ModelProviderAuthInput` going in and `ModelProviderAuthBody` coming back.
// The four types below are its only forms, and each writes its own tag. The
// API never returns a key, so an APIKeyAuth read back holds none.
type ModelProviderAuth interface {
	json.Marshaler
	// Type is the tag the wire carries.
	Type() string
	sealedModelProviderAuth()
}

type (
	// APIKeyAuth sends a key the workspace hands over.
	APIKeyAuth struct {
		APIKey string
	}
	// AWSWebIdentityAuth signs each call as a role in the workspace's own AWS
	// account.
	AWSWebIdentityAuth struct {
		RoleARN string `json:"role_arn"`
		Region  string `json:"region"`
	}
	// AnthropicWorkloadIdentityAuth acts as a service account in the
	// workspace's own Anthropic organization, under a federation rule there.
	AnthropicWorkloadIdentityAuth struct {
		OrganizationID   string  `json:"organization_id"`
		ServiceAccountID string  `json:"service_account_id"`
		FederationRuleID string  `json:"federation_rule_id"`
		WorkspaceID      *string `json:"workspace_id,omitempty"`
	}
	// OpenAIWorkloadIdentityAuth acts as a service account in the workspace's
	// own OpenAI organization, through a workload identity provider there.
	OpenAIWorkloadIdentityAuth struct {
		IdentityProviderID string `json:"identity_provider_id"`
		ServiceAccountID   string `json:"service_account_id"`
	}
)

func (APIKeyAuth) Type() string                    { return AuthAPIKey }
func (AWSWebIdentityAuth) Type() string            { return AuthAWSWebIdentity }
func (AnthropicWorkloadIdentityAuth) Type() string { return AuthAnthropicWorkloadIdentity }
func (OpenAIWorkloadIdentityAuth) Type() string    { return AuthOpenAIWorkloadIdentity }

func (APIKeyAuth) sealedModelProviderAuth()                    {}
func (AWSWebIdentityAuth) sealedModelProviderAuth()            {}
func (AnthropicWorkloadIdentityAuth) sealedModelProviderAuth() {}
func (OpenAIWorkloadIdentityAuth) sealedModelProviderAuth()    {}

// tagged writes an auth's fields with its tag beside them.
func tagged(auth ModelProviderAuth, fields any) ([]byte, error) {
	encoded, err := json.Marshal(fields)
	if err != nil {
		return nil, err
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &object); err != nil {
		return nil, err
	}
	object["type"], _ = json.Marshal(auth.Type())
	return json.Marshal(object)
}

func (a APIKeyAuth) MarshalJSON() ([]byte, error) {
	return tagged(a, struct {
		APIKey string `json:"api_key"`
	}{a.APIKey})
}

func (a AWSWebIdentityAuth) MarshalJSON() ([]byte, error) {
	type fields AWSWebIdentityAuth
	return tagged(a, fields(a))
}

func (a AnthropicWorkloadIdentityAuth) MarshalJSON() ([]byte, error) {
	type fields AnthropicWorkloadIdentityAuth
	return tagged(a, fields(a))
}

func (a OpenAIWorkloadIdentityAuth) MarshalJSON() ([]byte, error) {
	type fields OpenAIWorkloadIdentityAuth
	return tagged(a, fields(a))
}

// PlatformModel is `PlatformModelBody`.
type PlatformModel struct {
	Model       string `json:"model"`
	DisplayName string `json:"display_name"`
	Description string `json:"description"`
}

// NewModelProvider is `CreateWorkspaceModelProviderBody`.
type NewModelProvider struct {
	Format      string            `json:"format"`
	DisplayName string            `json:"display_name"`
	Description string            `json:"description"`
	BaseURL     string            `json:"base_url"`
	Auth        ModelProviderAuth `json:"auth"`
}

// ModelProviderUpdate is `UpdateModelProviderBody`. A nil Auth leaves the
// stored one as it stands; one sent replaces it whole.
type ModelProviderUpdate struct {
	DisplayName string            `json:"display_name"`
	Description string            `json:"description"`
	BaseURL     string            `json:"base_url"`
	Auth        ModelProviderAuth `json:"auth,omitempty"`
}

func modelProviderPath(id string) string {
	return "/v1/model-providers/" + url.PathEscape(id)
}

func (c *Client) CreateModelProvider(ctx context.Context, provider NewModelProvider) (*ModelProvider, error) {
	req, err := jsonRequest(http.MethodPost, "/v1/model-providers", provider)
	if err != nil {
		return nil, err
	}
	var out ModelProvider
	return &out, c.do(ctx, req, &out)
}

func (c *Client) GetModelProvider(ctx context.Context, id string) (*ModelProvider, error) {
	var out ModelProvider
	err := c.do(ctx, request{method: http.MethodGet, path: modelProviderPath(id)}, &out)
	return &out, err
}

func (c *Client) UpdateModelProvider(ctx context.Context, id string, update ModelProviderUpdate) (*ModelProvider, error) {
	req, err := jsonRequest(http.MethodPatch, modelProviderPath(id), update)
	if err != nil {
		return nil, err
	}
	var out ModelProvider
	return &out, c.do(ctx, req, &out)
}

func (c *Client) DeleteModelProvider(ctx context.Context, id string) error {
	return c.do(ctx, request{method: http.MethodDelete, path: modelProviderPath(id)}, nil)
}

func (c *Client) ListModelProviders(ctx context.Context) ([]ModelProvider, error) {
	return listAll[ModelProvider](ctx, c, "/v1/model-providers")
}

// WorkloadIdentity is `WorkloadIdentityBody`: what an AWS account, an
// Anthropic organization, or an OpenAI organization trusts for the
// workspace's providers to authenticate as the workspace.
type WorkloadIdentity struct {
	Issuer    string           `json:"issuer"`
	Subject   string           `json:"subject"`
	AWS       IdentityAudience `json:"aws"`
	Anthropic IdentityAudience `json:"anthropic"`
	OpenAI    IdentityAudience `json:"openai"`
}

// IdentityAudience is the `aud` one target checks.
type IdentityAudience struct {
	Audience string `json:"audience"`
}

func (c *Client) GetWorkloadIdentity(ctx context.Context) (*WorkloadIdentity, error) {
	var out WorkloadIdentity
	err := c.do(ctx, request{method: http.MethodGet, path: "/v1/workload-identity"}, &out)
	return &out, err
}
