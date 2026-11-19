// templates.go notification templates (PRD08 + S007 custom allowlisted templates):
// versioned bilingual zh-CN/en-US copy and deterministic allowlisted {{placeholder}} rendering;
// bodies may contain only bounded snapshot metadata (order ID/display number, sanitized single-line
// order title, submitter, timestamps, state, flow, stage, datasource, approvers, executors, bounded
// reason codes, and back-link) — never raw SQL, literals, comments, or credentials.
package notification

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/cookieY/YearningX/backend/internal/crypto"
)

// MaxMessageTemplateRunes is the maximum character length of a custom notification rule template
// (aligned with migration 000028 char_length(message_template) <= 2000).
const MaxMessageTemplateRunes = 2000

// MaxOrderTitlePlaceholderRunes is the single-line truncation limit for {{order_title}}.
const MaxOrderTitlePlaceholderRunes = 120

// AllowlistedPlaceholders lists every permitted {{placeholder}} variable name across notification templates.
var AllowlistedPlaceholders = []string{
	"order_display_number",
	"order_no",
	"order_id",
	"order_title",
	"event_type",
	"event_label",
	"order_state",
	"flow_name",
	"stage_position",
	"stage_name",
	"datasource_name",
	"datasource_engine",
	"submitter",
	"submitter_username",
	"submitted_at",
	"approvers",
	"approved_by",
	"reviewer",
	"approval_decision",
	"approved_at",
	"reviewed_at",
	"executors",
	"executed_by",
	"executor",
	"execution_started_at",
	"executed_at",
	"execution_finished_at",
	"reason_code",
	"order_url",
	"grant_flow_name",
	"grant_datasources",
	"revoked_by",
	"revoked_at",
	"occurred_at",
}

var allowlistedPlaceholderSet = func() map[string]bool {
	m := make(map[string]bool, len(AllowlistedPlaceholders))
	for _, k := range AllowlistedPlaceholders {
		m[k] = true
	}
	return m
}()

// forbiddenStaticPatterns blocks raw SQL statement prefixes and credential assignment patterns in static template text
// and in sanitized {{order_title}} values so notifications never leak SQL or secrets.
var forbiddenStaticPatterns = []string{
	"select ", "insert into ", "delete from ", "alter table ", "drop table ",
	"create table ", "truncate table ", "password=", "secret=", "access_token=",
}

// locale supports the two fixed languages (S006 general.default_locale vocabulary).
var localeText = map[string]map[string]string{
	"zh-CN": {
		"order_submitted":             "\u5de5\u5355 %s \u5df2\u63d0\u4ea4\uff0c\u7b49\u5f85\u5ba1\u6279\u3002",
		"approval_decided":            "\u5de5\u5355 %s \u5ba1\u6279\u6709\u65b0\u8fdb\u5c55\uff08%s\uff09\u3002",
		"order_rejected":              "\u5de5\u5355 %s \u5df2\u88ab\u62d2\u7edd\u3002",
		"execution_scheduled":         "\u5de5\u5355 %s \u5df2\u9884\u7ea6\u6267\u884c\u3002",
		"execution_started":           "\u5de5\u5355 %s \u5f00\u59cb\u6267\u884c\u3002",
		"execution_succeeded":         "\u5de5\u5355 %s \u6267\u884c\u6210\u529f\u3002",
		"execution_failed":            "\u5de5\u5355 %s \u6267\u884c\u5931\u8d25\uff08%s\uff09\u3002",
		"execution_partial":           "\u5de5\u5355 %s \u90e8\u5206\u5931\u8d25\uff08%s\uff09\u3002",
		"execution_unknown":           "\u5de5\u5355 %s \u6267\u884c\u7ed3\u679c\u672a\u77e5\uff08%s\uff09\uff0c\u8bf7\u51bb\u7ed3\u6267\u884c\u4eba\u6838\u9a8c\u3002",
		"execution_blocked":           "\u5de5\u5355 %s \u56e0\u6570\u636e\u6e90\u4e0d\u53ef\u7528\u88ab\u963b\u65ad\u3002",
		"execution_missed":            "\u5de5\u5355 %s \u9884\u7ea6\u5df2\u9519\u8fc7\uff0c\u4e0d\u81ea\u52a8\u8865\u8dd1\uff0c\u8bf7\u91cd\u65b0\u53d1\u8d77\u3002",
		"execution_pending":           "\u5de5\u5355 %s \u5ba1\u6279\u5df2\u5168\u90e8\u901a\u8fc7\uff0c\u7b49\u5f85\u6267\u884c\u3002",
		"schedule_cancelled":          "\u5de5\u5355 %s \u7684\u6267\u884c\u9884\u7ea6\u5df2\u53d6\u6d88\u3002",
		"execution_cancelled":         "\u5de5\u5355 %s \u6267\u884c\u5df2\u53d6\u6d88\u3002",
		"execution_partial_cancelled": "\u5de5\u5355 %s \u90e8\u5206\u53d6\u6d88\u5df2\u5b8c\u6210\uff08\u5df2\u6267\u884c\u9636\u6bb5\u4fdd\u7559\uff0c\u4e0d\u81ea\u52a8\u56de\u6eda\uff09\u3002",
		"step_activated":              "\u5de5\u5355 %s \u6709\u65b0\u7684\u5ba1\u6279\u6b65\u9aa4\u5f85\u5904\u7406\u3002",
		"order_withdrawn":             "\u5de5\u5355 %s \u5df2\u88ab\u64a4\u56de\u3002",
		"order_voided":                "\u5de5\u5355 %s \u5df2\u4f5c\u5e9f\u3002",
		"budget_warning":              "AI\u65e5\u9884\u7b97\u5df2\u8fbe\u9608\u503c\u3002",
		"budget_exhausted":            "AI\u65e5\u9884\u7b97\u5df2\u8017\u5c3d\u3002",
		"provider_failover":           "AI Provider\u5df2\u89e6\u53d1\u6545\u969c\u5207\u6362\u3002",
	},
	"en-US": {
		"order_submitted":             "Order %s submitted and pending approval.",
		"approval_decided":            "Order %s has an approval update (%s).",
		"order_rejected":              "Order %s was rejected.",
		"execution_scheduled":         "Order %s scheduled for execution.",
		"execution_started":           "Order %s execution started.",
		"execution_succeeded":         "Order %s executed successfully.",
		"execution_failed":            "Order %s execution failed (%s).",
		"execution_partial":           "Order %s partially failed (%s).",
		"execution_unknown":           "Order %s result unknown (%s); frozen executor verification required.",
		"execution_blocked":           "Order %s blocked by datasource unavailability.",
		"execution_missed":            "Order %s schedule missed; no auto catch-up, please reschedule.",
		"execution_pending":           "Order %s fully approved and awaiting execution.",
		"schedule_cancelled":          "The execution schedule for order %s was cancelled.",
		"execution_cancelled":         "Order %s execution cancelled.",
		"execution_partial_cancelled": "Order %s partial cancellation completed (executed stages retained; no automatic rollback).",
		"step_activated":              "Order %s has a new approval step pending.",
		"order_withdrawn":             "Order %s was withdrawn.",
		"order_voided":                "Order %s was voided.",
		"budget_warning":              "AI daily budget threshold reached.",
		"budget_exhausted":            "AI daily budget exhausted.",
		"provider_failover":           "AI provider failover triggered.",
	},
}

// eventLabels provides bilingual human labels for {{event_label}}.
var eventLabels = map[string]map[string]string{
	"zh-CN": {
		"change_order.submitted":                   "\u5de5\u5355\u5df2\u63d0\u4ea4",
		"change_order.step_activated":              "\u5ba1\u6279\u6b65\u9aa4\u5df2\u6fc0\u6d3b",
		"change_order.approval_decided":            "\u5ba1\u6279\u51b3\u5b9a\u5df2\u66f4\u65b0",
		"change_order.stage_ready_for_execution":   "\u9636\u6bb5\u5f85\u6267\u884c",
		"change_order.rejected":                    "\u5de5\u5355\u5df2\u62d2\u7edd",
		"change_order.withdrawn":                   "\u5de5\u5355\u5df2\u64a4\u56de",
		"change_order.voided":                      "\u5de5\u5355\u5df2\u4f5c\u5e9f",
		"execution.scheduled":                      "\u5df2\u9884\u7ea6\u6267\u884c",
		"execution.schedule_cancelled":             "\u9884\u7ea6\u5df2\u53d6\u6d88",
		"execution.started":                        "\u6267\u884c\u5df2\u5f00\u59cb",
		"execution.succeeded":                      "\u6267\u884c\u5df2\u6210\u529f",
		"execution.failed":                         "\u6267\u884c\u5931\u8d25",
		"execution.partial_failed":                 "\u90e8\u5206\u6267\u884c\u5931\u8d25",
		"execution.cancelled":                      "\u6267\u884c\u5df2\u53d6\u6d88",
		"execution.partial_cancelled":              "\u90e8\u5206\u6267\u884c\u5df2\u53d6\u6d88",
		"execution.result_unknown":                 "\u6267\u884c\u7ed3\u679c\u672a\u77e5",
		"execution.blocked_datasource_unavailable": "\u6570\u636e\u6e90\u4e0d\u53ef\u7528\u963b\u65ad",
		"execution.missed_schedule":                "\u9884\u7ea6\u5df2\u9519\u8fc7",
		"query_grant.revoked":                      "\u67e5\u8be2\u6388\u6743\u5df2\u64a4\u9500",
		"ai.budget_warning":                        "AI\u65e5\u9884\u7b97\u9884\u8b66",
		"ai.budget_exhausted":                      "AI\u65e5\u9884\u7b97\u8017\u5c3d",
		"ai.provider_failover":                     "AI\u6545\u969c\u5207\u6362",
	},
	"en-US": {
		"change_order.submitted":                   "Order Submitted",
		"change_order.step_activated":              "Approval Step Activated",
		"change_order.approval_decided":            "Approval Decided",
		"change_order.stage_ready_for_execution":   "Stage Ready for Execution",
		"change_order.rejected":                    "Order Rejected",
		"change_order.withdrawn":                   "Order Withdrawn",
		"change_order.voided":                      "Order Voided",
		"execution.scheduled":                      "Execution Scheduled",
		"execution.schedule_cancelled":             "Schedule Cancelled",
		"execution.started":                        "Execution Started",
		"execution.succeeded":                      "Execution Succeeded",
		"execution.failed":                         "Execution Failed",
		"execution.partial_failed":                 "Execution Partially Failed",
		"execution.cancelled":                      "Execution Cancelled",
		"execution.partial_cancelled":              "Execution Partially Cancelled",
		"execution.result_unknown":                 "Execution Result Unknown",
		"execution.blocked_datasource_unavailable": "Blocked by Datasource Unavailability",
		"execution.missed_schedule":                "Execution Schedule Missed",
		"query_grant.revoked":                      "Query Grant Revoked",
		"ai.budget_warning":                        "AI Budget Warning",
		"ai.budget_exhausted":                      "AI Budget Exhausted",
		"ai.provider_failover":                     "AI Provider Failover",
	},
}

// templateKey maps domain event types to template keys (bounded mapping; unknown events -> empty = do not send).
var templateKey = map[string]string{
	"change_order.submitted":                   "order_submitted",
	"change_order.approval_decided":            "approval_decided",
	"change_order.rejected":                    "order_rejected",
	"execution.scheduled":                      "execution_scheduled",
	"execution.started":                        "execution_started",
	"execution.succeeded":                      "execution_succeeded",
	"execution.failed":                         "execution_failed",
	"execution.partial_failed":                 "execution_partial",
	"execution.result_unknown":                 "execution_unknown",
	"execution.blocked_datasource_unavailable": "execution_blocked",
	"execution.missed_schedule":                "execution_missed",
	"execution.cancelled":                      "execution_cancelled",
	"change_order.stage_ready_for_execution":   "execution_pending",
	"execution.schedule_cancelled":             "schedule_cancelled",
	"execution.partial_cancelled":              "execution_partial_cancelled",
	"change_order.withdrawn":                   "order_withdrawn",
	"change_order.voided":                      "order_voided",
	"change_order.step_activated":              "step_activated",
	"query_grant.revoked":                      "grant_revoked",
	"ai.budget_warning":                        "budget_warning",
	"ai.budget_exhausted":                      "budget_exhausted",
	"ai.provider_failover":                     "provider_failover",
}

// defaultPlaceholderTemplates provides the rich built-in default template with allowlisted {{placeholder}} variables.
var defaultPlaceholderTemplates = map[string]map[string]string{
	"zh-CN": {
		"order_submitted":             "\u5de5\u5355 {{order_display_number}} \u5df2\u63d0\u4ea4\uff0c\u7b49\u5f85\u5ba1\u6279\u3002\n\u5de5\u5355ID\uff1a{{order_id}}\n\u6807\u9898\uff1a{{order_title}}\n\u63d0\u4ea4\u4eba\uff1a{{submitter}}\n\u63d0\u4ea4\u65f6\u95f4\uff1a{{submitted_at}}\n\u5f53\u524d\u72b6\u6001\uff1a{{order_state}}\n\u5ba1\u6838\u4eba\uff1a{{approvers}}\n\u6267\u884c\u4eba\uff1a{{executors}}",
		"step_activated":              "\u5de5\u5355 {{order_display_number}} \u6709\u65b0\u7684\u5ba1\u6279\u6b65\u9aa4\u5f85\u5904\u7406\u3002\n\u5de5\u5355ID\uff1a{{order_id}}\n\u6807\u9898\uff1a{{order_title}}\n\u63d0\u4ea4\u4eba\uff1a{{submitter}}\n\u63d0\u4ea4\u65f6\u95f4\uff1a{{submitted_at}}\n\u5f53\u524d\u72b6\u6001\uff1a{{order_state}}\n\u5f53\u524d\u5ba1\u6838\u4eba\uff1a{{approvers}}\n\u5173\u8054\u5ba1\u6838\u4eba\uff1a{{approved_by}}\uff08{{approved_at}}\uff09",
		"approval_decided":            "\u5de5\u5355 {{order_display_number}} \u5ba1\u6279\u6709\u65b0\u8fdb\u5c55\uff08{{reason_code}}\uff09\u3002\n\u5de5\u5355ID\uff1a{{order_id}}\n\u6807\u9898\uff1a{{order_title}}\n\u63d0\u4ea4\u4eba\uff1a{{submitter}}\n\u5f53\u524d\u72b6\u6001\uff1a{{order_state}}\n\u5ba1\u6838\u4eba\uff1a{{approved_by}}\n\u5ba1\u6838\u65f6\u95f4\uff1a{{approved_at}}\n\u6267\u884c\u4eba\uff1a{{executors}}",
		"execution_pending":           "\u5de5\u5355 {{order_display_number}} \u5ba1\u6279\u5df2\u5168\u90e8\u901a\u8fc7\uff0c\u7b49\u5f85\u6267\u884c\u3002\n\u5de5\u5355ID\uff1a{{order_id}}\n\u6807\u9898\uff1a{{order_title}}\n\u63d0\u4ea4\u4eba\uff1a{{submitter}}\n\u5f53\u524d\u72b6\u6001\uff1a{{order_state}}\n\u5ba1\u6838\u4eba\uff1a{{approved_by}}\n\u5ba1\u6838\u65f6\u95f4\uff1a{{approved_at}}\n\u6267\u884c\u4eba\uff1a{{executors}}",
		"order_rejected":              "\u5de5\u5355 {{order_display_number}} \u5df2\u88ab\u62d2\u7edd\u3002\n\u5de5\u5355ID\uff1a{{order_id}}\n\u6807\u9898\uff1a{{order_title}}\n\u63d0\u4ea4\u4eba\uff1a{{submitter}}\n\u5f53\u524d\u72b6\u6001\uff1a{{order_state}}\n\u5ba1\u6838\u4eba\uff1a{{approved_by}}\n\u5ba1\u6838\u65f6\u95f4\uff1a{{approved_at}}",
		"order_withdrawn":             "\u5de5\u5355 {{order_display_number}} \u5df2\u88ab\u64a4\u56de\u3002\n\u5de5\u5355ID\uff1a{{order_id}}\n\u6807\u9898\uff1a{{order_title}}\n\u63d0\u4ea4\u4eba\uff1a{{submitter}}\n\u63d0\u4ea4\u65f6\u95f4\uff1a{{submitted_at}}\n\u5f53\u524d\u72b6\u6001\uff1a{{order_state}}",
		"order_voided":                "\u5de5\u5355 {{order_display_number}} \u5df2\u4f5c\u5e9f\u3002\n\u5de5\u5355ID\uff1a{{order_id}}\n\u6807\u9898\uff1a{{order_title}}\n\u63d0\u4ea4\u4eba\uff1a{{submitter}}\n\u63d0\u4ea4\u65f6\u95f4\uff1a{{submitted_at}}\n\u5f53\u524d\u72b6\u6001\uff1a{{order_state}}",
		"execution_scheduled":         "\u5de5\u5355 {{order_display_number}} \u5df2\u9884\u7ea6\u6267\u884c\u3002\n\u5de5\u5355ID\uff1a{{order_id}}\n\u6807\u9898\uff1a{{order_title}}\n\u63d0\u4ea4\u4eba\uff1a{{submitter}}\n\u5f53\u524d\u72b6\u6001\uff1a{{order_state}}\n\u6267\u884c\u4eba\uff1a{{executors}}",
		"schedule_cancelled":          "\u5de5\u5355 {{order_display_number}} \u7684\u6267\u884c\u9884\u7ea6\u5df2\u53d6\u6d88\u3002\n\u5de5\u5355ID\uff1a{{order_id}}\n\u6807\u9898\uff1a{{order_title}}\n\u63d0\u4ea4\u4eba\uff1a{{submitter}}\n\u5f53\u524d\u72b6\u6001\uff1a{{order_state}}\n\u6267\u884c\u4eba\uff1a{{executors}}",
		"execution_started":           "\u5de5\u5355 {{order_display_number}} \u5f00\u59cb\u6267\u884c\u3002\n\u5de5\u5355ID\uff1a{{order_id}}\n\u6807\u9898\uff1a{{order_title}}\n\u63d0\u4ea4\u4eba\uff1a{{submitter}}\n\u5f53\u524d\u72b6\u6001\uff1a{{order_state}}\n\u6267\u884c\u4eba\uff1a{{executed_by}}\n\u5f00\u59cb\u65f6\u95f4\uff1a{{execution_started_at}}",
		"execution_succeeded":         "\u5de5\u5355 {{order_display_number}} \u6267\u884c\u6210\u529f\u3002\n\u5de5\u5355ID\uff1a{{order_id}}\n\u6807\u9898\uff1a{{order_title}}\n\u63d0\u4ea4\u4eba\uff1a{{submitter}}\n\u5f53\u524d\u72b6\u6001\uff1a{{order_state}}\n\u5ba1\u6838\u4eba\uff1a{{approved_by}}\uff08{{approved_at}}\uff09\n\u6267\u884c\u4eba\uff1a{{executed_by}}\n\u6267\u884c\u65f6\u95f4\uff1a{{executed_at}}",
		"execution_failed":            "\u5de5\u5355 {{order_display_number}} \u6267\u884c\u5931\u8d25\uff08{{reason_code}}\uff09\u3002\n\u5de5\u5355ID\uff1a{{order_id}}\n\u6807\u9898\uff1a{{order_title}}\n\u63d0\u4ea4\u4eba\uff1a{{submitter}}\n\u5f53\u524d\u72b6\u6001\uff1a{{order_state}}\n\u6267\u884c\u4eba\uff1a{{executed_by}}\n\u6267\u884c\u65f6\u95f4\uff1a{{executed_at}}",
		"execution_partial":           "\u5de5\u5355 {{order_display_number}} \u90e8\u5206\u5931\u8d25\uff08{{reason_code}}\uff09\u3002\n\u5de5\u5355ID\uff1a{{order_id}}\n\u6807\u9898\uff1a{{order_title}}\n\u63d0\u4ea4\u4eba\uff1a{{submitter}}\n\u5f53\u524d\u72b6\u6001\uff1a{{order_state}}\n\u6267\u884c\u4eba\uff1a{{executed_by}}\n\u6267\u884c\u65f6\u95f4\uff1a{{executed_at}}",
		"execution_unknown":           "\u5de5\u5355 {{order_display_number}} \u6267\u884c\u7ed3\u679c\u672a\u77e5\uff08{{reason_code}}\uff09\uff0c\u8bf7\u51bb\u7ed3\u6267\u884c\u4eba\u6838\u9a8c\u3002\n\u5de5\u5355ID\uff1a{{order_id}}\n\u6807\u9898\uff1a{{order_title}}\n\u63d0\u4ea4\u4eba\uff1a{{submitter}}\n\u5f53\u524d\u72b6\u6001\uff1a{{order_state}}\n\u6267\u884c\u4eba\uff1a{{executed_by}}\n\u6267\u884c\u65f6\u95f4\uff1a{{executed_at}}",
		"execution_blocked":           "\u5de5\u5355 {{order_display_number}} \u56e0\u6570\u636e\u6e90\u4e0d\u53ef\u7528\u88ab\u963b\u65ad\u3002\n\u5de5\u5355ID\uff1a{{order_id}}\n\u6807\u9898\uff1a{{order_title}}\n\u63d0\u4ea4\u4eba\uff1a{{submitter}}\n\u5f53\u524d\u72b6\u6001\uff1a{{order_state}}\n\u6570\u636e\u6e90\uff1a{{datasource_name}}\n\u6267\u884c\u4eba\uff1a{{executors}}",
		"execution_missed":            "\u5de5\u5355 {{order_display_number}} \u9884\u7ea6\u5df2\u9519\u8fc7\uff0c\u4e0d\u81ea\u52a8\u8865\u8dd1\uff0c\u8bf7\u91cd\u65b0\u53d1\u8d77\u3002\n\u5de5\u5355ID\uff1a{{order_id}}\n\u6807\u9898\uff1a{{order_title}}\n\u63d0\u4ea4\u4eba\uff1a{{submitter}}\n\u5f53\u524d\u72b6\u6001\uff1a{{order_state}}\n\u6267\u884c\u4eba\uff1a{{executors}}",
		"execution_cancelled":         "\u5de5\u5355 {{order_display_number}} \u6267\u884c\u5df2\u53d6\u6d88\u3002\n\u5de5\u5355ID\uff1a{{order_id}}\n\u6807\u9898\uff1a{{order_title}}\n\u63d0\u4ea4\u4eba\uff1a{{submitter}}\n\u5f53\u524d\u72b6\u6001\uff1a{{order_state}}\n\u6267\u884c\u4eba\uff1a{{executed_by}}\n\u6267\u884c\u65f6\u95f4\uff1a{{executed_at}}",
		"execution_partial_cancelled": "\u5de5\u5355 {{order_display_number}} \u90e8\u5206\u53d6\u6d88\u5df2\u5b8c\u6210\uff08\u5df2\u6267\u884c\u9636\u6bb5\u4fdd\u7559\uff0c\u4e0d\u81ea\u52a8\u56de\u6eda\uff09\u3002\n\u5de5\u5355ID\uff1a{{order_id}}\n\u6807\u9898\uff1a{{order_title}}\n\u63d0\u4ea4\u4eba\uff1a{{submitter}}\n\u5f53\u524d\u72b6\u6001\uff1a{{order_state}}\n\u6267\u884c\u4eba\uff1a{{executed_by}}\n\u6267\u884c\u65f6\u95f4\uff1a{{executed_at}}",
		"grant_revoked":               "\u60a8\u7684\u67e5\u8be2\u8bbf\u95ee\u8d44\u683c\u5df2\u88ab\u64a4\u9500\u3002\u67e5\u8be2\u6d41\u7a0b\uff1a{{grant_flow_name}}\uff1b\u53d7\u5f71\u54cd\u6570\u636e\u6e90\uff1a{{grant_datasources}}\uff1b\u539f\u56e0\uff1a{{reason_code}}\uff1b\u64cd\u4f5c\u8005\uff1a{{revoked_by}}\uff1b\u65f6\u95f4\uff1a{{revoked_at}}\u3002\u5982\u9700\u7ee7\u7eed\u8bf7\u91cd\u65b0\u7533\u8bf7\u3002",
		"budget_warning":              "AI\u65e5\u9884\u7b97\u5df2\u8fbe\u9608\u503c\u3002",
		"budget_exhausted":            "AI\u65e5\u9884\u7b97\u5df2\u8017\u5c3d\u3002",
		"provider_failover":           "AI Provider\u5df2\u89e6\u53d1\u6545\u969c\u5207\u6362\u3002",
	},
	"en-US": {
		"order_submitted":             "Order {{order_display_number}} submitted and pending approval.\nOrder ID: {{order_id}}\nTitle: {{order_title}}\nSubmitter: {{submitter}}\nSubmitted At: {{submitted_at}}\nState: {{order_state}}\nApprovers: {{approvers}}\nExecutors: {{executors}}",
		"step_activated":              "Order {{order_display_number}} has a new approval step pending.\nOrder ID: {{order_id}}\nTitle: {{order_title}}\nSubmitter: {{submitter}}\nSubmitted At: {{submitted_at}}\nState: {{order_state}}\nApprovers: {{approvers}}\nLast Approved By: {{approved_by}} ({{approved_at}})",
		"approval_decided":            "Order {{order_display_number}} has an approval update ({{reason_code}}).\nOrder ID: {{order_id}}\nTitle: {{order_title}}\nSubmitter: {{submitter}}\nState: {{order_state}}\nApproved By: {{approved_by}}\nApproved At: {{approved_at}}\nExecutors: {{executors}}",
		"execution_pending":           "Order {{order_display_number}} fully approved and awaiting execution.\nOrder ID: {{order_id}}\nTitle: {{order_title}}\nSubmitter: {{submitter}}\nState: {{order_state}}\nApproved By: {{approved_by}}\nApproved At: {{approved_at}}\nExecutors: {{executors}}",
		"order_rejected":              "Order {{order_display_number}} was rejected.\nOrder ID: {{order_id}}\nTitle: {{order_title}}\nSubmitter: {{submitter}}\nState: {{order_state}}\nReviewed By: {{approved_by}}\nReviewed At: {{approved_at}}",
		"order_withdrawn":             "Order {{order_display_number}} was withdrawn.\nOrder ID: {{order_id}}\nTitle: {{order_title}}\nSubmitter: {{submitter}}\nSubmitted At: {{submitted_at}}\nState: {{order_state}}",
		"order_voided":                "Order {{order_display_number}} was voided.\nOrder ID: {{order_id}}\nTitle: {{order_title}}\nSubmitter: {{submitter}}\nSubmitted At: {{submitted_at}}\nState: {{order_state}}",
		"execution_scheduled":         "Order {{order_display_number}} scheduled for execution.\nOrder ID: {{order_id}}\nTitle: {{order_title}}\nSubmitter: {{submitter}}\nState: {{order_state}}\nExecutors: {{executors}}",
		"schedule_cancelled":          "The execution schedule for order {{order_display_number}} was cancelled.\nOrder ID: {{order_id}}\nTitle: {{order_title}}\nSubmitter: {{submitter}}\nState: {{order_state}}\nExecutors: {{executors}}",
		"execution_started":           "Order {{order_display_number}} execution started.\nOrder ID: {{order_id}}\nTitle: {{order_title}}\nSubmitter: {{submitter}}\nState: {{order_state}}\nExecuted By: {{executed_by}}\nStarted At: {{execution_started_at}}",
		"execution_succeeded":         "Order {{order_display_number}} executed successfully.\nOrder ID: {{order_id}}\nTitle: {{order_title}}\nSubmitter: {{submitter}}\nState: {{order_state}}\nApproved By: {{approved_by}} ({{approved_at}})\nExecuted By: {{executed_by}}\nExecuted At: {{executed_at}}",
		"execution_failed":            "Order {{order_display_number}} execution failed ({{reason_code}}).\nOrder ID: {{order_id}}\nTitle: {{order_title}}\nSubmitter: {{submitter}}\nState: {{order_state}}\nExecuted By: {{executed_by}}\nExecuted At: {{executed_at}}",
		"execution_partial":           "Order {{order_display_number}} partially failed ({{reason_code}}).\nOrder ID: {{order_id}}\nTitle: {{order_title}}\nSubmitter: {{submitter}}\nState: {{order_state}}\nExecuted By: {{executed_by}}\nExecuted At: {{executed_at}}",
		"execution_unknown":           "Order {{order_display_number}} result unknown ({{reason_code}}); frozen executor verification required.\nOrder ID: {{order_id}}\nTitle: {{order_title}}\nSubmitter: {{submitter}}\nState: {{order_state}}\nExecuted By: {{executed_by}}\nExecuted At: {{executed_at}}",
		"execution_blocked":           "Order {{order_display_number}} blocked by datasource unavailability.\nOrder ID: {{order_id}}\nTitle: {{order_title}}\nSubmitter: {{submitter}}\nState: {{order_state}}\nDatasource: {{datasource_name}}\nExecutors: {{executors}}",
		"execution_missed":            "Order {{order_display_number}} schedule missed; no auto catch-up, please reschedule.\nOrder ID: {{order_id}}\nTitle: {{order_title}}\nSubmitter: {{submitter}}\nState: {{order_state}}\nExecutors: {{executors}}",
		"execution_cancelled":         "Order {{order_display_number}} execution cancelled.\nOrder ID: {{order_id}}\nTitle: {{order_title}}\nSubmitter: {{submitter}}\nState: {{order_state}}\nExecuted By: {{executed_by}}\nExecuted At: {{executed_at}}",
		"execution_partial_cancelled": "Order {{order_display_number}} partial cancellation completed (executed stages retained; no automatic rollback).\nOrder ID: {{order_id}}\nTitle: {{order_title}}\nSubmitter: {{submitter}}\nState: {{order_state}}\nExecuted By: {{executed_by}}\nExecuted At: {{executed_at}}",
		"grant_revoked":               "Your query access grant was revoked. Flow: {{grant_flow_name}}; affected datasources: {{grant_datasources}}; reason: {{reason_code}}; operator: {{revoked_by}}; time: {{revoked_at}}. Reapply if needed.",
		"budget_warning":              "AI daily budget threshold reached.",
		"budget_exhausted":            "AI daily budget exhausted.",
		"provider_failover":           "AI provider failover triggered.",
	},
}

// DefaultPlaceholderTemplate returns the built-in default template with {{placeholder}} variables for an event.
func DefaultPlaceholderTemplate(eventType, locale string) string {
	key, ok := templateKey[eventType]
	if !ok {
		return ""
	}
	if locale != "en-US" {
		locale = "zh-CN"
	}
	return defaultPlaceholderTemplates[locale][key]
}

// ValidateMessageTemplate validates a custom notification rule template against length, control-character,
// balanced {{...}} syntax, allowlisted placeholder names, and forbidden SQL/secret literal patterns.
func ValidateMessageTemplate(tpl string) error {
	if tpl == "" {
		return nil
	}
	if utf8.RuneCountInString(tpl) > MaxMessageTemplateRunes {
		return ErrValidation
	}
	for _, r := range tpl {
		if (r < 0x20 && r != '\n' && r != '\r' && r != '\t') || r == 0x7f {
			return ErrValidation
		}
	}
	var staticBuf strings.Builder
	rest := tpl
	for len(rest) > 0 {
		openIdx := strings.Index(rest, "{{")
		closeIdx := strings.Index(rest, "}}")
		if closeIdx >= 0 && (openIdx < 0 || closeIdx < openIdx) {
			return ErrValidation // unmatched closing braces
		}
		if openIdx < 0 {
			staticBuf.WriteString(rest)
			break
		}
		staticBuf.WriteString(rest[:openIdx])
		afterOpen := rest[openIdx+2:]
		relClose := strings.Index(afterOpen, "}}")
		if relClose < 0 {
			return ErrValidation // unclosed {{
		}
		token := strings.TrimSpace(afterOpen[:relClose])
		if token == "" || strings.ContainsAny(token, "\r\n\t {}") || !allowlistedPlaceholderSet[token] {
			return ErrValidation
		}
		rest = afterOpen[relClose+2:]
	}
	lowerStatic := strings.ToLower(staticBuf.String())
	for _, pat := range forbiddenStaticPatterns {
		if strings.Contains(lowerStatic, pat) {
			return ErrValidation
		}
	}
	return nil
}

// RenderTemplateString replaces allowlisted {{placeholder}} tokens with bounded values from vars.
func RenderTemplateString(tpl string, vars map[string]string) string {
	if tpl == "" {
		return ""
	}
	var out strings.Builder
	rest := tpl
	for len(rest) > 0 {
		openIdx := strings.Index(rest, "{{")
		if openIdx < 0 {
			out.WriteString(rest)
			break
		}
		out.WriteString(rest[:openIdx])
		afterOpen := rest[openIdx+2:]
		relClose := strings.Index(afterOpen, "}}")
		if relClose < 0 {
			out.WriteString(rest[openIdx:])
			break
		}
		token := strings.TrimSpace(afterOpen[:relClose])
		if allowlistedPlaceholderSet[token] {
			val := strings.TrimSpace(vars[token])
			if val == "" {
				val = "-"
			}
			out.WriteString(val)
		} else {
			out.WriteString("-")
		}
		rest = afterOpen[relClose+2:]
	}
	return out.String()
}

// RenderEvent renders a notification message body (event type + order reference + bounded reason code).
// Order reference = display_number (frozen YR-... format); reason accepts only tokens matching
// ^[A-Z0-9_]{1,64}$ and falls back to generic wording otherwise — never passing through free text.
func RenderEvent(eventType, locale, orderRef, reason string) string {
	key, ok := templateKey[eventType]
	if !ok || key == "grant_revoked" {
		return ""
	}
	if locale != "en-US" {
		locale = "zh-CN"
	}
	texts := localeText[locale]
	if texts == nil {
		return ""
	}
	tpl, ok := texts[key]
	if !ok {
		return ""
	}
	if !validReason(reason) {
		reason = ""
	}
	switch key {
	case "order_submitted", "execution_scheduled", "execution_started",
		"execution_succeeded", "execution_blocked", "execution_missed",
		"execution_cancelled", "execution_partial_cancelled",
		"execution_pending", "schedule_cancelled", "step_activated",
		"order_withdrawn", "order_voided", "order_rejected":
		return fmt.Sprintf(tpl, orderRef)
	case "approval_decided":
		return fmt.Sprintf(tpl, orderRef, reasonOr(reason, locale, "reviewed"))
	case "execution_failed", "execution_partial", "execution_unknown":
		return fmt.Sprintf(tpl, orderRef, reasonOr(reason, locale, "failed"))
	case "budget_warning", "budget_exhausted", "provider_failover":
		return tpl
	}
	return ""
}

// boundedText strips control characters and truncates length (unified sanitizer for display fields).
func boundedText(s string, max int) string {
	if len(s) > max {
		s = s[:max]
	}
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, s)
}

// sanitizeOrderTitle sanitizes a change order title into a single-line bounded string (<= 120 runes)
// and redacts any SQL statement keywords or secret assignment patterns.
func sanitizeOrderTitle(s string) string {
	cleaned := strings.Map(func(r rune) rune {
		if r == '\r' || r == '\n' || r == '\t' {
			return ' '
		}
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, strings.TrimSpace(s))
	runes := []rune(cleaned)
	if len(runes) > MaxOrderTitlePlaceholderRunes {
		cleaned = string(runes[:MaxOrderTitlePlaceholderRunes]) + "…"
	}
	lower := strings.ToLower(cleaned)
	for _, pat := range forbiddenStaticPatterns {
		if strings.Contains(lower, pat) {
			return "[REDACTED]"
		}
	}
	if cleaned == "" {
		return "-"
	}
	return cleaned
}

// boundedOperator sanitizes an operator name to the safe username character set.
func boundedOperator(s string) string {
	if len(s) > 64 {
		s = s[:64]
	}
	return strings.Map(func(r rune) rune {
		ok := r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' ||
			r == '_' || r == '.' || r == '-'
		if !ok {
			return -1
		}
		return r
	}, s)
}

func reasonOr(reason, locale, fallback string) string {
	if reason == "" {
		if locale == "en-US" {
			return fallback
		}
		switch fallback {
		case "reviewed":
			return "\u5df2\u5904\u7406"
		default:
			return "\u5931\u8d25"
		}
	}
	return reason
}

// validReason checks whether reason matches the bounded reason code format (never passing through free text).
func validReason(reason string) bool {
	if reason == "" || len(reason) > 64 {
		return false
	}
	for _, r := range reason {
		ok := r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_'
		if !ok {
			return false
		}
	}
	return true
}

// orderDisplayNumber resolves display_number from a change order ID (bounded read-only lookup; falls back to truncated ID).
func (w *Worker) orderDisplayNumber(ctx context.Context, orderID string) string {
	var dn string
	if w.db != nil {
		if err := w.db.WithContext(ctx).Raw(
			`select display_number from change_orders where id = ?`, orderID).Scan(&dn).Error; err == nil && dn != "" {
			return dn
		}
	}
	return shortRef(orderID)
}

func shortRef(id string) string {
	if len(id) > 8 {
		return "YR-" + id[:8]
	}
	if id == "" {
		return "YR-PREVIEW"
	}
	return "YR-" + id
}

// EventBody renders the complete notification body for an outbox event using the default template.
func (w *Worker) EventBody(ctx context.Context, ev *OutboxEventModel, reason string) string {
	return w.EventBodyWithTemplate(ctx, ev, reason, "")
}

// EventBodyWithTemplate renders the notification body for an outbox event, using customTemplate when
// non-empty and falling back to the built-in default template for ev.EventType.
func (w *Worker) EventBodyWithTemplate(ctx context.Context, ev *OutboxEventModel, reason, customTemplate string) string {
	if _, ok := templateKey[ev.EventType]; !ok {
		return ""
	}
	locale := w.locale
	if locale != "en-US" {
		locale = "zh-CN"
	}
	customTemplate = strings.TrimSpace(customTemplate)
	if customTemplate != "" && ValidateMessageTemplate(customTemplate) != nil {
		customTemplate = ""
	}
	tpl := customTemplate
	if tpl == "" {
		tpl = DefaultPlaceholderTemplate(ev.EventType, locale)
	}
	if tpl == "" {
		return ""
	}
	var vars map[string]string
	switch {
	case ev.EventType == "query_grant.revoked":
		vars = w.buildGrantRevokedTemplateVars(ctx, ev, reason, locale)
	case strings.HasPrefix(ev.EventType, "change_order.") || strings.HasPrefix(ev.EventType, "execution."):
		vars = w.buildOrderTemplateVars(ctx, ev, reason, locale)
	default:
		vars = w.buildAITemplateVars(ev, reason, locale)
	}
	body := RenderTemplateString(tpl, vars)
	if body == "" {
		return ""
	}
	// S007: append back-link to the system when general.public_base_url is configured and the template
	// did not already include {{order_url}} explicitly.
	if w.publicBaseURL != "" && vars["order_id"] != "" && vars["order_id"] != "-" &&
		!strings.Contains(tpl, "{{order_url}}") &&
		(strings.HasPrefix(ev.EventType, "change_order.") || strings.HasPrefix(ev.EventType, "execution.")) {
		body += "\n" + strings.TrimRight(w.publicBaseURL, "/") + "/changes/orders/" + vars["order_id"]
	}
	return body
}

// buildOrderTemplateVars resolves all allowlisted order/approval/execution placeholders from frozen snapshots.
func (w *Worker) buildOrderTemplateVars(ctx context.Context, ev *OutboxEventModel, reason, locale string) map[string]string {
	orderID := eventOrderID(ev)
	ref := w.orderDisplayNumber(ctx, orderID)
	key := templateKey[ev.EventType]
	fallbackKind := "failed"
	if key == "approval_decided" {
		fallbackKind = "reviewed"
	}
	rc := reason
	if !validReason(rc) {
		rc = ""
	}
	reasonDisplay := reasonOr(rc, locale, fallbackKind)
	if rc == "" && key != "approval_decided" && key != "execution_failed" && key != "execution_partial" && key != "execution_unknown" {
		reasonDisplay = "-"
	}
	occurredAt := "-"
	if !ev.OccurredAt.IsZero() {
		occurredAt = ev.OccurredAt.UTC().Format("2006-01-02T15:04:05Z")
	}
	orderURL := "-"
	if w.publicBaseURL != "" && orderID != "" {
		orderURL = strings.TrimRight(w.publicBaseURL, "/") + "/changes/orders/" + orderID
	}
	vars := map[string]string{
		"order_display_number":  ref,
		"order_no":              ref,
		"order_id":              nonEmptyOr(orderID, "-"),
		"order_title":           "-",
		"event_type":            ev.EventType,
		"event_label":           nonEmptyOr(eventLabels[locale][ev.EventType], ev.EventType),
		"order_state":           "-",
		"flow_name":             "-",
		"stage_position":        "-",
		"stage_name":            "-",
		"datasource_name":       "-",
		"datasource_engine":     "-",
		"submitter":             "-",
		"submitter_username":    "-",
		"submitted_at":          "-",
		"approvers":             "-",
		"approved_by":           "-",
		"reviewer":              "-",
		"approval_decision":     "-",
		"approved_at":           "-",
		"reviewed_at":           "-",
		"executors":             "-",
		"executed_by":           "-",
		"executor":              "-",
		"execution_started_at":  "-",
		"executed_at":           "-",
		"execution_finished_at": "-",
		"reason_code":           reasonDisplay,
		"order_url":             orderURL,
		"occurred_at":           occurredAt,
	}
	if w.db == nil || orderID == "" {
		return vars
	}

	var orderRow struct {
		DisplayNumber        string     `gorm:"column:display_number"`
		SubmitterUsername    string     `gorm:"column:submitter_username_snapshot"`
		SubmitterDisplayName string     `gorm:"column:submitter_display_name_snapshot"`
		Title                string     `gorm:"column:title"`
		State                string     `gorm:"column:state"`
		CurrentStagePosition *int       `gorm:"column:current_stage_position"`
		SubmittedAt          time.Time  `gorm:"column:submitted_at"`
		TerminalAt           *time.Time `gorm:"column:terminal_at"`
	}
	if err := w.db.WithContext(ctx).Raw(`
		select display_number, submitter_username_snapshot, submitter_display_name_snapshot,
		       title, state, current_stage_position, submitted_at, terminal_at
		from change_orders where id = ?`, orderID).Scan(&orderRow).Error; err == nil && orderRow.DisplayNumber != "" {
		vars["order_display_number"] = boundedText(orderRow.DisplayNumber, 64)
		vars["order_no"] = boundedText(orderRow.DisplayNumber, 64)
		vars["order_title"] = sanitizeOrderTitle(orderRow.Title)
		vars["order_state"] = boundedText(orderRow.State, 64)
		subUser := boundedOperator(orderRow.SubmitterUsername)
		subDisp := boundedText(orderRow.SubmitterDisplayName, 64)
		vars["submitter_username"] = nonEmptyOr(subUser, "-")
		if subDisp != "" && subDisp != subUser && subUser != "" {
			vars["submitter"] = fmt.Sprintf("%s (%s)", subDisp, subUser)
		} else {
			vars["submitter"] = nonEmptyOr(subUser, subDisp)
		}
		if !orderRow.SubmittedAt.IsZero() {
			vars["submitted_at"] = orderRow.SubmittedAt.UTC().Format("2006-01-02T15:04:05Z")
		}
		if orderRow.CurrentStagePosition != nil && *orderRow.CurrentStagePosition > 0 {
			vars["stage_position"] = fmt.Sprintf("%d", *orderRow.CurrentStagePosition)
		}
		if orderRow.TerminalAt != nil && !orderRow.TerminalAt.IsZero() {
			ts := orderRow.TerminalAt.UTC().Format("2006-01-02T15:04:05Z")
			vars["executed_at"] = ts
			vars["execution_finished_at"] = ts
		}
	}

	// Flow snapshot: flow_name and stage_name.
	if w.crypter != nil {
		var snapRow struct {
			Ciphertext []byte `gorm:"column:flow_snapshot_ciphertext"`
			Nonce      []byte `gorm:"column:flow_snapshot_nonce"`
			KeyID      string `gorm:"column:flow_snapshot_key_id"`
			Algorithm  string `gorm:"column:flow_snapshot_algorithm"`
		}
		if err := w.db.WithContext(ctx).Raw(`
			select flow_snapshot_ciphertext, flow_snapshot_nonce,
			       flow_snapshot_key_id, flow_snapshot_algorithm
			from order_flow_snapshots where order_id = ?`, orderID).Scan(&snapRow).Error; err == nil && snapRow.Ciphertext != nil {
			if plain, oerr := w.crypter.Open(crypto.PurposePayload, crypto.Envelope{
				Ciphertext: snapRow.Ciphertext, Nonce: snapRow.Nonce,
				KeyID: snapRow.KeyID, Algorithm: snapRow.Algorithm,
			}, crypto.AAD{
				DeploymentID: w.crypter.DeploymentID(), Table: "order_flow_snapshots",
				PrimaryKey: orderID, Column: "flow_snapshot_ciphertext", Purpose: crypto.PurposePayload,
			}); oerr == nil {
				var doc struct {
					FlowName string `json:"flow_name"`
					Stages   []struct {
						Position int    `json:"position"`
						Name     string `json:"name"`
					} `json:"stages"`
				}
				if json.Unmarshal(plain, &doc) == nil {
					if doc.FlowName != "" {
						vars["flow_name"] = boundedText(doc.FlowName, 128)
					}
					stagePos := 1
					if orderRow.CurrentStagePosition != nil && *orderRow.CurrentStagePosition > 0 {
						stagePos = *orderRow.CurrentStagePosition
					}
					for _, st := range doc.Stages {
						if st.Position == stagePos && st.Name != "" {
							vars["stage_name"] = boundedText(st.Name, 128)
							break
						}
					}
				}
			}
		}
	}

	// Current stage datasource & candidate executors.
	var stageRow struct {
		ID               string `gorm:"column:id"`
		DatasourceName   string `gorm:"column:datasource_name_snapshot"`
		DatasourceEngine string `gorm:"column:datasource_engine_snapshot"`
	}
	_ = w.db.WithContext(ctx).Raw(`
		select s.id::text as id, s.datasource_name_snapshot, s.datasource_engine_snapshot
		from order_stages s
		join change_orders o on o.id = s.order_id
		where s.order_id = ? and s.position = coalesce(o.current_stage_position, 1)
		limit 1`, orderID).Scan(&stageRow).Error
	if stageRow.ID != "" {
		vars["datasource_name"] = nonEmptyOr(boundedText(stageRow.DatasourceName, 128), "-")
		vars["datasource_engine"] = nonEmptyOr(boundedText(stageRow.DatasourceEngine, 64), "-")
		var execUsers []string
		_ = w.db.WithContext(ctx).Raw(`
			select username_snapshot from order_execution_actors
			where stage_id = ? order by username_snapshot limit 6`, stageRow.ID).Scan(&execUsers).Error
		if len(execUsers) > 0 {
			vars["executors"] = joinBoundedActors(execUsers, 5)
		}
	}

	// Candidate approvers for the active step (or the step carried in step_activated).
	var apprUsers []string
	if stepID := eventStepID(ev); stepID != "" {
		_ = w.db.WithContext(ctx).Raw(`
			select username_snapshot from order_approval_actors
			where step_id = ? order by username_snapshot limit 6`, stepID).Scan(&apprUsers).Error
	} else {
		_ = w.db.WithContext(ctx).Raw(`
			select a.username_snapshot
			from order_approval_actors a
			join order_approval_steps st on st.id = a.step_id
			join order_stages s on s.id = st.stage_id
			where s.order_id = ? and st.state = 'active'
			order by a.username_snapshot limit 6`, orderID).Scan(&apprUsers).Error
	}
	if len(apprUsers) > 0 {
		vars["approvers"] = joinBoundedActors(apprUsers, 5)
	}

	// Latest approval decision (reviewer + decision + timestamp).
	var decRow struct {
		Reviewer   string    `gorm:"column:reviewer_username_snapshot"`
		Decision   string    `gorm:"column:decision"`
		OccurredAt time.Time `gorm:"column:occurred_at"`
	}
	if err := w.db.WithContext(ctx).Raw(`
		select reviewer_username_snapshot, decision, occurred_at
		from approval_decisions
		where order_id = ?
		order by occurred_at desc limit 1`, orderID).Scan(&decRow).Error; err == nil && decRow.Reviewer != "" {
		rev := boundedOperator(decRow.Reviewer)
		vars["approved_by"] = nonEmptyOr(rev, "-")
		vars["reviewer"] = nonEmptyOr(rev, "-")
		vars["approval_decision"] = nonEmptyOr(boundedText(decRow.Decision, 32), "-")
		if !decRow.OccurredAt.IsZero() {
			ts := decRow.OccurredAt.UTC().Format("2006-01-02T15:04:05Z")
			vars["approved_at"] = ts
			vars["reviewed_at"] = ts
		}
	}

	// Latest execution attempt (executor + started_at + finished_at).
	var execRow struct {
		InitiatedBy string     `gorm:"column:initiated_by_username_snapshot"`
		StartedAt   *time.Time `gorm:"column:started_at"`
		FinishedAt  *time.Time `gorm:"column:finished_at"`
	}
	if err := w.db.WithContext(ctx).Raw(`
		select initiated_by_username_snapshot, started_at, finished_at
		from execution_attempts
		where order_id = ?
		order by created_at desc limit 1`, orderID).Scan(&execRow).Error; err == nil && execRow.InitiatedBy != "" {
		ex := boundedOperator(execRow.InitiatedBy)
		vars["executed_by"] = nonEmptyOr(ex, "-")
		vars["executor"] = nonEmptyOr(ex, "-")
		if execRow.StartedAt != nil && !execRow.StartedAt.IsZero() {
			vars["execution_started_at"] = execRow.StartedAt.UTC().Format("2006-01-02T15:04:05Z")
		}
		if execRow.FinishedAt != nil && !execRow.FinishedAt.IsZero() {
			ts := execRow.FinishedAt.UTC().Format("2006-01-02T15:04:05Z")
			vars["executed_at"] = ts
			vars["execution_finished_at"] = ts
		}
	}

	return vars
}

func joinBoundedActors(users []string, maxCount int) string {
	cleaned := make([]string, 0, len(users))
	for _, u := range users {
		if c := boundedOperator(u); c != "" {
			cleaned = append(cleaned, c)
		}
	}
	if len(cleaned) == 0 {
		return "-"
	}
	if len(cleaned) > maxCount {
		cleaned = append(cleaned[:maxCount], "…")
	}
	return strings.Join(cleaned, ", ")
}

func nonEmptyOr(v, fallback string) string {
	if strings.TrimSpace(v) == "" {
		return fallback
	}
	return v
}

// buildGrantRevokedTemplateVars resolves placeholders for query_grant.revoked events.
func (w *Worker) buildGrantRevokedTemplateVars(ctx context.Context, ev *OutboxEventModel, reason, locale string) map[string]string {
	var payload struct {
		GrantID    string `json:"grant_id"`
		RevokedBy  string `json:"revoked_by"`
		ReasonCode string `json:"reason_code"`
	}
	_ = json.Unmarshal(ev.Payload, &payload)
	grantID := payload.GrantID
	if grantID == "" {
		grantID = strings.TrimPrefix(ev.Subject, "query-grants/")
	}
	var row struct {
		RequestID string `gorm:"column:query_access_request_id"`
		Operator  string `gorm:"column:revoked_by_username_snapshot"`
	}
	if w.db != nil && grantID != "" {
		_ = w.db.WithContext(ctx).Raw(
			`select query_access_request_id, coalesce(revoked_by_username_snapshot, '')
			 from query_grants where id = ?`, grantID).Scan(&row).Error
	}
	operator := payload.RevokedBy
	if operator == "" {
		operator = row.Operator
	}
	var flowName string
	var datasources []string
	if w.db != nil && row.RequestID != "" {
		_ = w.db.WithContext(ctx).Raw(
			`select flow_name_snapshot from query_access_requests where id = ?`,
			row.RequestID).Scan(&flowName).Error
		_ = w.db.WithContext(ctx).Raw(
			`select datasource_name_snapshot from query_access_request_datasources
			 where request_id = ? order by datasource_name_snapshot limit 4`,
			row.RequestID).Scan(&datasources).Error
	}
	dsJoined := "-"
	if len(datasources) > 0 {
		shown := datasources
		if len(shown) > 3 {
			shown = append(shown[:3], "…")
		}
		dsJoined = strings.Join(shown, ", ")
	}
	if !validReason(reason) {
		reason = payload.ReasonCode
	}
	if !validReason(reason) {
		reason = "-"
	}
	occurredAt := "-"
	if !ev.OccurredAt.IsZero() {
		occurredAt = ev.OccurredAt.UTC().Format("2006-01-02T15:04:05Z")
	}
	return map[string]string{
		"event_type":        ev.EventType,
		"event_label":       nonEmptyOr(eventLabels[locale][ev.EventType], ev.EventType),
		"grant_flow_name":   nonEmptyOr(boundedText(flowName, 128), "-"),
		"grant_datasources": nonEmptyOr(boundedText(dsJoined, 384), "-"),
		"reason_code":       reason,
		"revoked_by":        nonEmptyOr(boundedOperator(operator), "-"),
		"revoked_at":        occurredAt,
		"occurred_at":       occurredAt,
	}
}

// buildAITemplateVars resolves placeholders for ai.* budget/failover events.
func (w *Worker) buildAITemplateVars(ev *OutboxEventModel, reason, locale string) map[string]string {
	if !validReason(reason) {
		reason = "-"
	}
	occurredAt := "-"
	if !ev.OccurredAt.IsZero() {
		occurredAt = ev.OccurredAt.UTC().Format("2006-01-02T15:04:05Z")
	}
	return map[string]string{
		"event_type":  ev.EventType,
		"event_label": nonEmptyOr(eventLabels[locale][ev.EventType], ev.EventType),
		"occurred_at": occurredAt,
		"reason_code": reason,
	}
}

// sampleTemplateVars returns deterministic preview placeholder values when no sample_order_id is supplied.
func sampleTemplateVars(locale, eventType string) map[string]string {
	if locale != "en-US" {
		locale = "zh-CN"
	}
	return map[string]string{
		"order_display_number":  "YR-PREVIEW",
		"order_no":              "YR-PREVIEW",
		"order_id":              "00000000-0000-4000-8000-000000000001",
		"order_title":           "orders_index_change",
		"event_type":            eventType,
		"event_label":           nonEmptyOr(eventLabels[locale][eventType], eventType),
		"order_state":           "stage_approval_active",
		"flow_name":             "prod-standard-flow",
		"stage_position":        "1",
		"stage_name":            "prod-primary",
		"datasource_name":       "prod-mysql-01",
		"datasource_engine":     "mysql",
		"submitter":             "alice",
		"submitter_username":    "alice",
		"submitted_at":          "2026-11-10T09:00:00Z",
		"approvers":             "bob, carol",
		"approved_by":           "bob",
		"reviewer":              "bob",
		"approval_decision":     "approve",
		"approved_at":           "2026-11-10T09:05:00Z",
		"reviewed_at":           "2026-11-10T09:05:00Z",
		"executors":             "dba_ops",
		"executed_by":           "dba_ops",
		"executor":              "dba_ops",
		"execution_started_at":  "2026-11-10T09:10:00Z",
		"executed_at":           "2026-11-10T09:10:03Z",
		"execution_finished_at": "2026-11-10T09:10:03Z",
		"reason_code":           "REVIEWED",
		"order_url":             "/changes/orders/00000000-0000-4000-8000-000000000001",
		"grant_flow_name":       "readonly-query-flow",
		"grant_datasources":     "prod-mysql-01",
		"revoked_by":            "admin",
		"revoked_at":            "2026-11-10T09:15:00Z",
		"occurred_at":           "2026-11-10T09:15:00Z",
	}
}
