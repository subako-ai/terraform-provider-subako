# Terraform Provider for Subako

Manage a [Subako](https://subako.ai) workspace as code: the agents it runs, the
skills and function packages they load, the model providers they call, and the
vaults holding the credentials they present, with the OAuth clients those
credentials connect through.

```hcl
terraform {
  required_version = ">= 1.11"
  required_providers {
    subako = {
      source = "subako-ai/subako"
    }
  }
}

provider "subako" {}

resource "subako_agent" "release" {
  name              = "release"
  model_provider_id = subako_model_provider.anthropic.id
  system_prompt     = "You prepare releases. Read the merged pull requests and draft the notes."

  model = {
    anthropic = {
      name           = "claude-sonnet-5"
      max_tokens     = 8192
      context_window = 1000000
    }
  }
}
```

Terraform 1.11 or later is required: credentials are passed as write-only
arguments, so no secret is ever written to your state.
[`examples/`](examples) is a working configuration using every resource below.

## Installing

`terraform init` installs it from the Terraform Registry, as it does any other
provider.

## Authenticating

Signed in with the Subako CLI, there is nothing to set:

```sh
subako login --org <your organization>
subako workspace use <your workspace>
terraform apply
```

With no token set, the provider runs `subako token` for the signed-in user's
access token, and runs it again as that nears its expiry, or with `--renew` if
the server refuses it, so an apply that outlives one token carries on with the
next. `$SUBAKO_CLI` names the program when `subako` is not on the `PATH`.

Each setting is looked for in the provider block, then in its environment
variable:

| Argument | Environment variable | When neither sets it |
| --- | --- | --- |
| `token` | `SUBAKO_TOKEN` | the signed-in Subako CLI's |
| `server` | `SUBAKO_SERVER` | `https://api.us.cloud.subako.ai`, or the CLI's server |
| `workspace_id` | `SUBAKO_WORKSPACE` | the workspace `subako workspace use` selected |

A token you set goes to whichever server you name. The Subako CLI's token is
for the one server it signed in to, so with no token set, a `server` naming
another one is refused rather than sent the CLI's token. An API key names its
own workspace.

### Pin the workspace in a shared configuration

A workspace taken from the CLI follows whatever `subako workspace use` last
chose, and the provider warns when it takes one. Name it in a configuration
others apply:

```hcl
provider "subako" {
  workspace_id = "01a07f90-..."
}
```

### In CI

A job has no login. Mint an API key and set it:

```sh
export SUBAKO_TOKEN=sbk_ak_...
terraform apply -auto-approve
```

Grant it the agent, skill, function, model provider, vault, and credential
permissions, plus `workspace.read`. Leave out the session permissions, and the
configuration cannot spend credit.

## What you can manage

| Resource | Manages |
| --- | --- |
| `subako_agent` | An agent: its model, system prompt, MCP servers, skills, sandboxes, functions, volume, built-in servers, and allowed origins |
| `subako_skill` | A skill, published from a directory of files |
| `subako_function` | A function package, published from a built bundle directory |
| `subako_model_provider` | A model upstream bound to your own API key, or to a role in your own AWS account |
| `subako_vault` | A vault an agent's sessions draw credentials from |
| `subako_vault_credential` | One credential in a vault: for an MCP server, or headers sent to HTTP origins |
| `subako_oauth_client` | An OAuth client you created at a provider, which vault credentials connect through |

| Data source | Reads |
| --- | --- |
| `subako_model_providers` | Every model provider the workspace can use, including the ones Subako publishes |
| `subako_workspace` | The workspace the credential acts in |
| `subako_workload_identity` | The issuer and subject a cloud account trusts so a model provider can authenticate as the workspace |

### Terraform owns an agent's whole config

Every part of an agent's config has an attribute, and one you leave out is the
server's default: no sandboxes, no functions, `session_display_name = "never"`.
Publishing a version replaces the whole config, so a change made in the console
shows up in the next plan, and the apply after it undoes it.

To leave a part to the console, name it under `ignore_changes`. Terraform then
plans against what the latest version holds for it, and a version it publishes
carries that along:

```hcl
resource "subako_agent" "release" {
  # ...

  lifecycle {
    ignore_changes = [sandboxes]
  }
}
```

A part of the config that is one of several kinds -- the model, its thinking, a
function's source and network, a form widget -- is an object holding one
attribute per kind, of which you set exactly one. A kind with no settings is
set with `{}`:

```hcl
model = {
  anthropic = {
    name           = "claude-sonnet-5"
    max_tokens     = 8192
    context_window = 1000000
    thinking       = { enabled = { budget_tokens = 4096 } } # or { adaptive = {} }
  }
}

functions = [{
  name           = "github"
  source         = { workspace = { function_id = subako_function.github.id } }
  default_policy = "allow"
  network        = { allowlist = { hosts = ["api.github.com"] } }
}]
```

Terraform drops an attribute a nested object does not have rather than
refusing it, so a misspelled optional setting is ignored: check the plan for
the values you meant.

### Function packages come from a built bundle

`subako_function` uploads the directory a function build writes:
`subako-package.json` and `module.wasm` at its root, and the files the module
reads under `assets/`. Nothing else may sit in it. Terraform pushes a new
version when the manifest's name or description, the module, or any asset
changes:

```hcl
resource "subako_function" "github" {
  source_dir = "${path.module}/functions/github/dist"
}
```

An agent granting it with no `version` follows the latest one from each new
session on, so pushing a version needs no new agent version.

### A credential is for an MCP server or for HTTP origins

`mcp` authenticates against one MCP server; `http` is sent as headers with
every request a function makes to its origins, which an agent's function grant
names under `http_credentials`:

```hcl
resource "subako_vault_credential" "github_api" {
  vault_id           = subako_vault.team.id
  secrets_wo_version = 1

  http = {
    origins = ["https://api.github.com"]
    headers = [{ name = "authorization", value_wo = "Bearer ${var.github_token}" }]
  }
}
```

A credential cannot change in place, so any change replaces it, and bumping
`secrets_wo_version` is how new secret values are sent.

### OAuth clients are managed here, their sign-ins in the console

Some providers register no OAuth client on the spot: Google's APIs, for one.
Create the client at the provider, register the redirect URI the console's
OAuth clients page shows, and describe the client with `subako_oauth_client`.
Its credentials are connected in the console, since each needs a person to
sign in:

```hcl
resource "subako_oauth_client" "google" {
  display_name           = "Google"
  authorization_endpoint = "https://accounts.google.com/o/oauth2/v2/auth"
  token_endpoint         = "https://oauth2.googleapis.com/token"
  client_id              = "1234.apps.googleusercontent.com"

  token_endpoint_auth      = { client_secret_post = { client_secret_wo = var.google_client_secret } }
  client_secret_wo_version = 1

  scopes               = ["https://www.googleapis.com/auth/drive.readonly"]
  authorization_params = [{ name = "access_type", value = "offline" }]
}
```

The endpoints and `client_id` are fixed once the client exists, so changing one
replaces it, and a client a credential was connected through cannot be
deleted. Everything else changes in place, and a credential picks up a new
secret on its next refresh.

### Secrets stay out of your state

Every argument carrying a secret is write-only: `api_key_wo` on a model
provider, `client_secret_wo` on an OAuth client, `token_wo` and
`access_token_wo` on a credential. Terraform sends
them and forgets them. A companion `*_wo_version` argument is what you change
to send a new value:

```hcl
resource "subako_model_provider" "anthropic" {
  format       = "anthropic"
  display_name = "Anthropic"
  base_url     = "https://api.anthropic.com"
  api_key_wo   = var.anthropic_api_key

  # Bump this to rotate the key. The provider keeps its id, so the agent
  # versions naming it keep running.
  api_key_wo_version = 1
}
```

### A model provider can hold no secret at all

A model provider on Amazon Bedrock can authenticate as an IAM role in your
own AWS account instead of with a key. Subako issues the workspace a
short-lived token, and the role's trust policy decides whether to admit it:

```hcl
data "subako_workload_identity" "this" {}

resource "aws_iam_openid_connect_provider" "subako" {
  url            = data.subako_workload_identity.this.issuer
  client_id_list = [data.subako_workload_identity.this.aws.audience]
}

resource "aws_iam_role" "subako_bedrock" {
  name = "subako-bedrock"
  assume_role_policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect    = "Allow"
      Principal = { Federated = aws_iam_openid_connect_provider.subako.arn }
      Action    = "sts:AssumeRoleWithWebIdentity"
      Condition = {
        StringEquals = {
          "${data.subako_workload_identity.this.aws.issuer_key}:aud" = data.subako_workload_identity.this.aws.audience
          "${data.subako_workload_identity.this.aws.issuer_key}:sub" = data.subako_workload_identity.this.subject
        }
      }
    }]
  })
}

resource "subako_model_provider" "bedrock" {
  format       = "anthropic"
  display_name = "Claude on Bedrock"
  base_url     = "https://bedrock-runtime.us-east-1.amazonaws.com/anthropic"
  aws_web_identity = {
    role_arn = aws_iam_role.subako_bedrock.arn
    region   = "us-east-1"
  }
}
```

The role also needs `bedrock:InvokeModel` and
`bedrock:InvokeModelWithResponseStream` on the models it calls. Subako refuses
a role that does not admit this workspace, or that admits any other, so the
`sub` condition is required.

## Development

```sh
mise trust && mise install
mise run test    # the suite drives a real Terraform binary
mise run lint
mise run fmt
```

To run a local change through Terraform, build it and point a
[`dev_overrides`](https://developer.hashicorp.com/terraform/cli/config/config-file#development-overrides-for-provider-developers)
block at the result:

```sh
mise run build    # writes bin/terraform-provider-subako
```

```hcl
# ~/.terraformrc
provider_installation {
  dev_overrides {
    "subako-ai/subako" = "/path/to/terraform-provider-subako/bin"
  }
  direct {}
}
```

Terraform then runs that binary with no `terraform init`, and warns on every
run that it is doing so.

## Releasing

A tag `v<version>` builds, signs, and publishes a release with GoReleaser
([`.goreleaser.yml`](.goreleaser.yml)), and the Terraform Registry picks it up
from there. The release is signed with the key in the repository secrets
`GPG_PRIVATE_KEY` and `PASSPHRASE`, whose public half the registry holds.

## License

Apache License 2.0; see [LICENSE](LICENSE) and [NOTICE](NOTICE). The HashiCorp
plugin libraries this links stay under their own MPL-2.0 terms.
