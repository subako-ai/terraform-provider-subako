# Most of what this provider manages, in one workspace: an agent, the model
# provider it calls, the skill it loads, the vault its sessions draw
# credentials from, and an OAuth client credentials can connect through. A
# `subako_function` needs a built bundle, so the agent below grants one of the
# packages the platform ships instead.
#
#   subako workspace use <name>
#   terraform init
#   TF_VAR_anthropic_api_key=... TF_VAR_github_token=... \
#     TF_VAR_google_client_secret=... terraform apply
#
# None of the secrets below reaches the state: each is a write-only argument.

terraform {
  # Write-only arguments need Terraform 1.11.
  required_version = ">= 1.11"

  # No backend: state is the configuration's own, and this example keeps it
  # beside itself. A real one names S3, HCP Terraform, or whatever the rest of
  # the estate uses.

  required_providers {
    subako = {
      source = "subako-ai/subako"
    }
  }
}

# The signed-in Subako CLI supplies the server, the credential, and the
# workspace. Name workspace_id here in a configuration others apply.
provider "subako" {}

variable "anthropic_api_key" {
  description = "The key this workspace's own Anthropic provider holds. Never stored in the state."
  type        = string
  sensitive   = true
  ephemeral   = true
}

variable "google_client_secret" {
  description = "The secret of the OAuth client created in Google Cloud. Never stored in the state."
  type        = string
  sensitive   = true
  ephemeral   = true
}

variable "github_token" {
  description = "A token for the GitHub MCP server. Never stored in the state."
  type        = string
  sensitive   = true
  ephemeral   = true
}

data "subako_model_providers" "all" {}

resource "subako_model_provider" "anthropic" {
  format       = "anthropic"
  display_name = "Anthropic"
  base_url     = "https://api.anthropic.com"
  api_key_wo   = var.anthropic_api_key

  # Which key the provider holds. Bump to send a new one, which rotates the
  # key in place: the provider keeps its id.
  api_key_wo_version = 1
}

resource "subako_skill" "release_notes" {
  source_dir = "${path.module}/skills/release-notes"
}

resource "subako_agent" "release" {
  name              = "release"
  model_provider_id = subako_model_provider.anthropic.id
  system_prompt     = "You prepare releases. Read the merged pull requests and draft the notes."

  model = {
    anthropic = {
      name           = "claude-sonnet-5"
      max_tokens     = 8192
      context_window = 1000000
      thinking       = { adaptive = {} }
    }
  }

  mcp_servers = [{
    name           = "github"
    url            = "https://api.githubcopilot.com/mcp/"
    default_policy = "allow"
    tools = [
      { name = "merge_pull_request", policy = "require_approval" },
      { name = "delete_repository", policy = "deny" },
    ]
  }]

  skills = [{
    name     = "release-notes"
    skill_id = subako_skill.release_notes.id
  }]

  functions = [{
    name           = "files"
    source         = { platform = { package = "files" } }
    default_policy = "allow"
  }]

  volume = { data = "enabled" }

  builtin = {
    session_display_name = "until_user_edits"
    ask_user             = { widgets = [{ select = {} }, { confirm = {} }] }
  }

  allowed_origins = ["https://release.example.com"]
}

resource "subako_vault" "team" {
  display_name = "release team"
  metadata = {
    owner = "platform"
  }
}

resource "subako_vault_credential" "github" {
  vault_id     = subako_vault.team.id
  target       = "https://api.githubcopilot.com/mcp/"
  display_name = "GitHub"

  static_bearer = {
    token_wo = var.github_token
  }

  # Bump to send a new token.
  secrets_wo_version = 1
}

# A client created in Google Cloud, with the redirect URI the console's OAuth
# clients page shows registered there. Credentials connect through it in the
# console, each with a person's sign-in.
resource "subako_oauth_client" "google" {
  display_name           = "Google"
  authorization_endpoint = "https://accounts.google.com/o/oauth2/v2/auth"
  token_endpoint         = "https://oauth2.googleapis.com/token"
  client_id              = "1234.apps.googleusercontent.com"

  token_endpoint_auth = { client_secret_post = { client_secret_wo = var.google_client_secret } }

  # Bump to send a new secret, in place.
  client_secret_wo_version = 1

  scopes               = ["https://www.googleapis.com/auth/drive.readonly"]
  authorization_params = [{ name = "access_type", value = "offline" }]
}

output "agent_id" {
  value = subako_agent.release.id
}

output "agent_version" {
  value = subako_agent.release.version
}

output "vault_id" {
  value = subako_vault.team.id
}

# The providers the platform offers, which an agent can name instead of the
# one above.
output "platform_model_providers" {
  value = [for p in data.subako_model_providers.all.providers : p.id if p.type == "platform"]
}
