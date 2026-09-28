package envgen

import (
	"fmt"
)

// The env keys cctrace manages. These names are declared once here and used by
// both the writer (BuildEnvMap) and the ownership set below, because the two
// must always agree: a key written but not owned is snapshotted as user data,
// overwritten by the apply, and then rejected by verify with "env key was
// modified", which aborts ApplyToClaudeSettings and breaks `cctrace init`.
const (
	envEnableTelemetry    = "CLAUDE_CODE_ENABLE_TELEMETRY"
	envMetricsExporter    = "OTEL_METRICS_EXPORTER"
	envLogsExporter       = "OTEL_LOGS_EXPORTER"
	envIncludeAccountUUID = "OTEL_METRICS_INCLUDE_ACCOUNT_UUID"
	envOTLPProtocol       = "OTEL_EXPORTER_OTLP_PROTOCOL"
	envOTLPEndpoint       = "OTEL_EXPORTER_OTLP_ENDPOINT"
	envOTLPHeaders        = "OTEL_EXPORTER_OTLP_HEADERS"
	envMetricInterval     = "OTEL_METRIC_EXPORT_INTERVAL"
	envLogsInterval       = "OTEL_LOGS_EXPORT_INTERVAL"
	envBSPMaxQueueSize    = "OTEL_BSP_MAX_QUEUE_SIZE"
	envBSPScheduleDelay   = "OTEL_BSP_SCHEDULE_DELAY"
	envBSPMaxExportBatch  = "OTEL_BSP_MAX_EXPORT_BATCH_SIZE"
	envBSPExportTimeout   = "OTEL_BSP_EXPORT_TIMEOUT"
	envResourceAttributes = "OTEL_RESOURCE_ATTRIBUTES"
)

// otelEnvKeys is the single list of managed key names.
var otelEnvKeys = []string{
	envEnableTelemetry,
	envMetricsExporter,
	envLogsExporter,
	envIncludeAccountUUID,
	envOTLPProtocol,
	envOTLPEndpoint,
	envOTLPHeaders,
	envMetricInterval,
	envLogsInterval,
	envBSPMaxQueueSize,
	envBSPScheduleDelay,
	envBSPMaxExportBatch,
	envBSPExportTimeout,
	envResourceAttributes,
}

// otelKeySet is the set of env keys managed by cctrace, derived from
// otelEnvKeys. Only these keys are written or removed — all others are
// untouched.
var otelKeySet = func() map[string]bool {
	set := make(map[string]bool, len(otelEnvKeys))
	for _, k := range otelEnvKeys {
		set[k] = true
	}
	return set
}()

// settingsSnapshot captures the full state of settings.json before modification.
// Used for runtime verification that only cctrace-managed keys were changed.
type settingsSnapshot struct {
	topLevelKeys map[string]bool            // all top-level keys that existed
	envKeys      map[string]string          // non-OTEL env key → serialized value
	hookCommands map[string]map[string]bool // event → set of non-cctrace hook commands
}

func takeSnapshot(settings map[string]interface{}) *settingsSnapshot {
	s := &settingsSnapshot{
		topLevelKeys: make(map[string]bool),
		envKeys:      make(map[string]string),
		hookCommands: make(map[string]map[string]bool),
	}

	for k := range settings {
		s.topLevelKeys[k] = true
	}

	if env, ok := settings["env"].(map[string]interface{}); ok {
		for k, v := range env {
			if !otelKeySet[k] {
				s.envKeys[k] = fmt.Sprintf("%v", v)
			}
		}
	}

	if hooks, ok := settings["hooks"].(map[string]interface{}); ok {
		for event, v := range hooks {
			cmds := make(map[string]bool)
			eventHooks, _ := v.([]interface{})
			for _, m := range eventHooks {
				matcher, _ := m.(map[string]interface{})
				hookList, _ := matcher["hooks"].([]interface{})
				for _, h := range hookList {
					hm, _ := h.(map[string]interface{})
					cmd, _ := hm["command"].(string)
					if cmd != "" && !isCctraceHook(cmd) {
						cmds[cmd] = true
					}
				}
			}
			s.hookCommands[event] = cmds
		}
	}

	return s
}

// verify checks that only cctrace-managed keys were changed.
func (s *settingsSnapshot) verify(after map[string]interface{}) error {
	// 1. Top-level keys must not be deleted. The cctrace stamp namespace is
	//    exempt — Apply adds it, Remove drops it.
	for k := range s.topLevelKeys {
		if k == cctraceMetaKey {
			continue
		}
		if _, exists := after[k]; !exists {
			return fmt.Errorf("settings guard: top-level key %q was deleted", k)
		}
	}

	// 2. Non-OTEL env keys must be preserved
	env, _ := after["env"].(map[string]interface{})
	for k, oldVal := range s.envKeys {
		newVal, exists := env[k]
		if !exists {
			return fmt.Errorf("settings guard: env key %q was deleted", k)
		}
		if fmt.Sprintf("%v", newVal) != oldVal {
			return fmt.Errorf("settings guard: env key %q was modified (%q → %q)", k, oldVal, newVal)
		}
	}

	// 3. Non-cctrace hooks must be preserved
	hooks, _ := after["hooks"].(map[string]interface{})
	for event, beforeCmds := range s.hookCommands {
		afterCmds := make(map[string]bool)
		eventHooks, _ := hooks[event].([]interface{})
		for _, m := range eventHooks {
			matcher, _ := m.(map[string]interface{})
			hookList, _ := matcher["hooks"].([]interface{})
			for _, h := range hookList {
				hm, _ := h.(map[string]interface{})
				cmd, _ := hm["command"].(string)
				if cmd != "" {
					afterCmds[cmd] = true
				}
			}
		}
		for cmd := range beforeCmds {
			if !afterCmds[cmd] {
				return fmt.Errorf("settings guard: hook %q in %s was deleted", cmd, event)
			}
		}
	}

	return nil
}
