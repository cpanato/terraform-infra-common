# `dashboard/cloudevent-receiver`

This module provisions a Google Cloud Monitoring dashboard for a regionalized
Cloud Run service that receives Cloud Events from one or more
`cloudevent-trigger`.

It assumes the service has the same name in all regions.

```hcl
// Create a network with several regional subnets
module "networking" {
  source = "chainguard-dev/common/infra//modules/networking"

  name       = "my-networking"
  project_id = var.project_id
  regions    = [...]
}

// Run a regionalized cloud run service "receiver" to handle events.
module "receiver" {
  source = "chainguard-dev/common/infra//modules/regional-go-service"

  project_id = var.project_id
  name       = "receiver"
  regions    = module.networking.regional-networks

  service_account = google_service_account.receiver.email
  containers = {
    "receiver" = {
      source = {
        working_dir = path.module
        importpath  = "./cmd/receiver"
      }
      ports = [{ container_port = 8080 }]
    }
  }
}

module "cloudevent-trigger" {
  for_each = module.networking.regional-networks

  source = "chainguard-dev/common/infra//modules/cloudevent-trigger"

  name       = "my-trigger"
  project_id = var.project_id
  broker     = module.cloudevent-broker.broker[each.key]
  filter     = { "type" : "dev.chainguard.foo" }

  depends_on = [google_cloud_run_v2_service.sockeye]
  private-service = {
    region = each.key
    name   = google_cloud_run_v2_service.receiver[each.key].name
  }
}

// Set up a dashboard for a regionalized event handler named "receiver".
module "receiver-dashboard" {
  source       = "chainguard-dev/common/infra//modules/dashboard/cloudevent-receiver"
  service_name = "receiver"

  triggers = {
    "type dev.chainguard.foo": "my-trigger"
  }
}
```

The dashboard it creates includes widgets for service logs, request count,
latency (p50,p95,p99), instance count grouped by revision, CPU and memory
utilization, startup latency, and sent/received bytes.

<!-- BEGIN_TF_DOCS -->
## Requirements

No requirements.

## Providers

No providers.

## Modules

| Name | Source | Version |
|------|--------|---------|
| <a name="module_dashboard"></a> [dashboard](#module\_dashboard) | ../ | n/a |
| <a name="module_errgrp"></a> [errgrp](#module\_errgrp) | ../sections/errgrp | n/a |
| <a name="module_github"></a> [github](#module\_github) | ../sections/github | n/a |
| <a name="module_grpc"></a> [grpc](#module\_grpc) | ../sections/grpc | n/a |
| <a name="module_http"></a> [http](#module\_http) | ../sections/http | n/a |
| <a name="module_layout"></a> [layout](#module\_layout) | ../sections/layout | n/a |
| <a name="module_logs"></a> [logs](#module\_logs) | ../sections/logs | n/a |
| <a name="module_resources"></a> [resources](#module\_resources) | ../sections/resources | n/a |
| <a name="module_subscription"></a> [subscription](#module\_subscription) | ../sections/subscription | n/a |
| <a name="module_trigger-dashboards"></a> [trigger-dashboards](#module\_trigger-dashboards) | ../ | n/a |
| <a name="module_trigger_layout"></a> [trigger\_layout](#module\_trigger\_layout) | ../sections/layout | n/a |
| <a name="module_width"></a> [width](#module\_width) | ../sections/width | n/a |

## Resources

No resources.

## Inputs

| Name | Description | Type | Default | Required |
|------|-------------|------|---------|:--------:|
| <a name="input_alerts"></a> [alerts](#input\_alerts) | A mapping from alerting policy names to the alert ids to add to the dashboard. | `map(string)` | `{}` | no |
| <a name="input_enable_oom_policy"></a> [enable\_oom\_policy](#input\_enable\_oom\_policy) | Toggle for enabling/disabling OOM alert policy. | `bool` | `true` | no |
| <a name="input_labels"></a> [labels](#input\_labels) | Additional labels to apply to the dashboard. | `map` | `{}` | no |
| <a name="input_notification_channels"></a> [notification\_channels](#input\_notification\_channels) | List of notification channels to alert. | `list(string)` | n/a | yes |
| <a name="input_project_id"></a> [project\_id](#input\_project\_id) | ID of the GCP project | `string` | n/a | yes |
| <a name="input_sections"></a> [sections](#input\_sections) | Sections to include in the dashboard | <pre>object({<br/>    http   = optional(bool, true)  // Include HTTP section<br/>    grpc   = optional(bool, true)  // Include GRPC section<br/>    github = optional(bool, false) // Include GitHub API section<br/>  })</pre> | <pre>{<br/>  "github": false,<br/>  "grpc": true,<br/>  "http": true<br/>}</pre> | no |
| <a name="input_service_name"></a> [service\_name](#input\_service\_name) | Name of the service(s) to monitor | `string` | n/a | yes |
| <a name="input_split_triggers"></a> [split\_triggers](#input\_split\_triggers) | Opt-in flag to split into per-trigger dashboards. Helpful when hitting widget limits | `bool` | `false` | no |
| <a name="input_triggers"></a> [triggers](#input\_triggers) | A mapping from a descriptive name to a subscription name prefix, an alert threshold, and list of notification channels. | <pre>map(object({<br/>    subscription_prefix   = string<br/>    alert_threshold       = optional(number, 50000)<br/>    notification_channels = optional(list(string), [])<br/>  }))</pre> | n/a | yes |

## Outputs

| Name | Description |
|------|-------------|
| <a name="output_json"></a> [json](#output\_json) | n/a |
<!-- END_TF_DOCS -->
