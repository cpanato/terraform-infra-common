variable "title" { type = string }
variable "group_by_fields" { default = [] }
variable "filter" { type = list(string) }
variable "plot_type" { default = "LINE" }
variable "alignment_period" { default = "60s" }
variable "primary_align" { default = "ALIGN_RATE" }
variable "primary_reduce" { default = "REDUCE_NONE" }
variable "secondary_align" { default = "ALIGN_NONE" }
variable "secondary_reduce" { default = "REDUCE_NONE" }
variable "thresholds" { default = [] }

// https://cloud.google.com/monitoring/api/ref_v3/rest/v1/projects.dashboards#XyChart
output "widget" {
  value = {
    title = var.title
    xyChart = {
      chartOptions = { mode = "COLOR" }
      dataSets = [{
        minAlignmentPeriod = var.alignment_period
        plotType           = var.plot_type
        targetAxis         = "Y1"
        timeSeriesQuery = {
          timeSeriesFilter = {
            aggregation = {
              alignmentPeriod    = var.alignment_period
              perSeriesAligner   = var.primary_align
              crossSeriesReducer = var.primary_reduce
              groupByFields      = var.group_by_fields
            }
            filter = join("\n", var.filter)
            secondaryAggregation = {
              alignmentPeriod    = var.alignment_period
              perSeriesAligner   = var.secondary_align
              crossSeriesReducer = var.secondary_reduce
              groupByFields      = var.group_by_fields
            }
          }
        }
      }]
      thresholds = [
        for threshold in var.thresholds : {
          value      = threshold
          targetAxis = "Y1"
        }
      ],
      timeshiftDuration = "0s"
      yAxis = {
        label = "y1Axis"
        scale = "LINEAR"
      }
    }
  }
}
