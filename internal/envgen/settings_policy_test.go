package envgen

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestPlanServerPolicySettings(t *testing.T) {
	t.Parallel()
	old := fullProfile()
	old.Server.Endpoint = "http://trace.example.test:4317"
	next := *old
	next.Server.Endpoint = "https://trace.example.test:5317"
	next.Server.CACertFile = "policy-root.pem"
	env := map[string]interface{}{"USER_VALUE": "keep"}
	for k, v := range BuildEnvMap(old) {
		env[k] = v
	}
	hooks := map[string]interface{}{"SessionStart": []interface{}{map[string]interface{}{"hooks": []interface{}{map[string]interface{}{"type": "command", "command": "custom cctrace hook"}}}}}
	settings := map[string]interface{}{"env": env, "hooks": hooks, "permissions": map[string]interface{}{"allow": []interface{}{"Read"}}}
	data, _ := json.Marshal(settings)
	out, err := PlanServerPolicySettings(data, old, &next)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]interface{}
	if err = json.Unmarshal(out, &got); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got["hooks"], hooks) || !reflect.DeepEqual(got["permissions"], settings["permissions"]) {
		t.Fatal("user settings changed")
	}
	gotEnv := got["env"].(map[string]interface{})
	if gotEnv[envOTLPEndpoint] != "https://trace.example.test:5318" || gotEnv[envNodeExtraCACerts] != next.Server.CACertFile {
		t.Fatalf("wrong env: %v", gotEnv)
	}
	rotated := next
	rotated.Server.CACertFile = "rotated.pem"
	if _, err = PlanServerPolicySettings(out, &next, &rotated); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{envOTLPEndpoint, envOTLPHeaders, envNodeExtraCACerts} {
		t.Run(key, func(t *testing.T) {
			env[key] = "foreign"
			bad, _ := json.Marshal(settings)
			if _, err := PlanServerPolicySettings(bad, old, &next); err == nil {
				t.Fatal("accepted foreign setting")
			}
			delete(env, key)
			if v, ok := BuildEnvMap(old)[key]; ok {
				env[key] = v
			}
		})
	}
}

func TestPlanServerPolicySettingsDoesNotClaimUserCA(t *testing.T) {
	t.Parallel()
	old := fullProfile()
	old.Server.Endpoint = "https://trace.example.test:4317"
	next := *old
	next.Server.CACertFile = "user.pem"
	out, err := PlanServerPolicySettings([]byte(`{"env":{"NODE_EXTRA_CA_CERTS":"user.pem"}}`), old, &next)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]interface{}
	if err = json.Unmarshal(out, &got); err != nil {
		t.Fatal(err)
	}
	if recordedNodeExtraCACerts(got) != "" {
		t.Fatal("claimed user CA")
	}
	for _, bad := range []string{`null`, `{"env":null}`, `{"env":[]}`, `{"cctrace":"user"}`} {
		if _, err := PlanServerPolicySettings([]byte(bad), old, &next); err == nil {
			t.Fatalf("accepted %s", bad)
		}
	}
}
