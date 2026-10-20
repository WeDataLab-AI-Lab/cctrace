package envgen

import (
	"bytes"
	"encoding/json"
	"fmt"

	"cctrace/internal/profile"
)

// PlanServerPolicySettings plans a policy transition without accessing files or
// regenerating hooks. Foreign settings cause deferral; an unrecorded CA remains
// user owned even when it happens to match the policy's CA.
func PlanServerPolicySettings(data []byte, oldProfile, newProfile *profile.Profile) ([]byte, error) {
	if oldProfile == nil || newProfile == nil {
		return nil, fmt.Errorf("policy requires both profiles")
	}
	settings := make(map[string]interface{})
	if len(data) > 0 {
		if !json.Valid(data) {
			return nil, fmt.Errorf("invalid Claude settings")
		}
		decoder := json.NewDecoder(bytes.NewReader(data))
		decoder.UseNumber()
		if err := decoder.Decode(&settings); err != nil || settings == nil {
			return nil, fmt.Errorf("invalid Claude settings: must be an object")
		}
	}
	for _, key := range []string{"env", cctraceMetaKey} {
		if raw, exists := settings[key]; exists {
			if _, ok := raw.(map[string]interface{}); !ok {
				return nil, fmt.Errorf("invalid Claude %s: must be an object", key)
			}
		}
	}
	snapshot := takeSnapshot(settings)
	env, _ := settings["env"].(map[string]interface{})
	if env == nil {
		env = make(map[string]interface{})
	}
	before, after := BuildEnvMap(oldProfile), BuildEnvMap(newProfile)
	for key := range otelKeySet {
		if value, exists := env[key]; exists && value != before[key] {
			return nil, fmt.Errorf("managed Claude setting %s conflicts with profile", key)
		}
	}
	ca := claudeCAFile(newProfile)
	raw, hasCA := env[envNodeExtraCACerts]
	current, _ := raw.(string)
	recorded := recordedNodeExtraCACerts(settings)
	owned := hasCA && recorded != "" && current == recorded && recorded == claudeCAFile(oldProfile)
	if hasCA && !owned && ca != "" && raw != ca {
		return nil, fmt.Errorf("user-owned Claude NODE_EXTRA_CA_CERTS")
	}
	for key := range otelKeySet {
		delete(env, key)
	}
	for key, value := range after {
		env[key] = value
	}
	switch {
	case owned || !hasCA:
		if ca == "" {
			delete(env, envNodeExtraCACerts)
		} else {
			env[envNodeExtraCACerts] = ca
		}
		setRecordedNodeExtraCACerts(settings, ca)
	default:
		setRecordedNodeExtraCACerts(settings, "")
	}
	settings["env"] = env
	setManagedStamp(settings, managedSettingsHash(newProfile), "")
	if err := snapshot.verify(settings); err != nil {
		return nil, err
	}
	out, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return nil, err
	}
	if err := validateSettingsJSON(out); err != nil {
		return nil, err
	}
	return append(out, '\n'), nil
}
