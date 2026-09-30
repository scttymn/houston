package models

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// valid is a payload as houston deploy sends it, as JSON fields.
func valid() map[string]json.RawMessage {
	return raw(`{"name":"shop","app_service":"web","services":["web","db"],"domains":["shop.example.com"],
		"variables":[{"name":"SECRET_KEY_BASE","required":true},{"name":"OPTIONAL","required":"yes"}],
		"health":"/up","port":3000,"deploy_rule":{"on":"push","branch":"main"},
		"volumes":[{"name":"data","path":"/rails/storage"}],"databases":[{"service":"db","image":"postgres:17"}],
		"backups":{"schedule":"daily 04:30","keep_auto":7,"keep_deploy":5},"maintenance_page":"<h1>Back soon</h1>",
		"details":{"images":{"db":"postgres:17"},"cpus":"2","memory":"1g","console":"bin/rails console"}}`)
}

func raw(s string) map[string]json.RawMessage {
	var m map[string]json.RawMessage
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		panic(err)
	}
	return m
}

func TestParseSync(t *testing.T) {
	s, errs := ParseSync(valid())
	if len(errs) > 0 {
		t.Fatalf("a valid payload: %v", errs)
	}
	want := Sync{Name: "shop", AppService: "web", Services: []string{"web", "db"}, Domains: []string{"shop.example.com"},
		Variables: []Variable{{Name: "SECRET_KEY_BASE", Required: true}, {Name: "OPTIONAL"}}, Health: "/up", Port: 3000,
		DeployRule: json.RawMessage(`{"on":"push","branch":"main"}`), Volumes: []Volume{{Name: "data", Path: "/rails/storage"}},
		Databases: []Database{{Service: "db", Image: "postgres:17"}}, KeepAuto: 7, KeepDeploy: 5, BackupSchedule: "daily 04:30",
		MaintenancePage: "<h1>Back soon</h1>", Details: Details{Images: map[string]string{"db": "postgres:17"}, CPUs: "2", Memory: "1g", Console: "bin/rails console"}}
	if !reflect.DeepEqual(s, want) {
		t.Errorf("parsed\n %+v\nwant\n %+v", s, want)
	}

	// What an older CLI leaves out has Rails' defaults.
	m := valid()
	for _, k := range []string{"volumes", "databases", "backups", "maintenance_page", "details", "deploy_rule"} {
		delete(m, k)
	}
	s, errs = ParseSync(m)
	if len(errs) > 0 || s.KeepAuto != 14 || s.KeepDeploy != 10 || s.BackupSchedule != "daily 03:00" || string(s.DeployRule) != "{}" ||
		s.Volumes == nil || s.Databases == nil || s.Details.Images == nil {
		t.Errorf("defaults: %+v %v", s, errs)
	}

	// Which fields name a restore's check or a runner's deploy, and what it serves.
	m = valid()
	m["restore_deploy"], m["serving_generation"], m["serving_sha"] = json.RawMessage(`12`), json.RawMessage(`2`), json.RawMessage(`"abc"`)
	if s, _ = ParseSync(m); s.RestoreDeploy == nil || *s.RestoreDeploy != 12 || s.ClaimedDeploy != nil || s.ServingGeneration != 2 || s.ServingSHA != "abc" {
		t.Errorf("restore: %+v", s)
	}
}

// Each rule of the Rails app's ProjectSync#validate, and its words: the
// field, and one change that breaks it.
func TestParseSyncRefuses(t *testing.T) {
	long := func(n int) string { return strings.Repeat("x", n) }
	for _, c := range []struct {
		field, value, message string
	}{
		{"name", `"Shop"`, "must be a DNS label (a-z, 0-9, -)"},
		{"name", `"shop-"`, "must be a DNS label (a-z, 0-9, -)"},
		{"name", `"` + long(64) + `"`, "must be a DNS label (a-z, 0-9, -)"},
		{"name", `7`, "must be a DNS label (a-z, 0-9, -)"},
		{"name", `"admin"`, "admin is reserved"},
		{"name", `"hooks"`, "hooks is reserved"},
		{"services", `[]`, "must be a list of compose service names"},
		{"services", `["web","-db"]`, "must be a list of compose service names"},
		{"services", `"web"`, "must be a list of compose service names"},
		{"app_service", `"worker"`, "must be one of the services"},
		{"variables", `[{"name":"1BAD"}]`, "must be a list of {name, required} with variable names"},
		{"variables", `["X"]`, "must be a list of {name, required} with variable names"},
		{"variables", `null`, "must be a list of {name, required} with variable names"},
		{"domains", `["Shop.example.com"]`, "must be a list of lowercase hostnames"},
		{"domains", `["localhost"]`, "must be a list of lowercase hostnames"},
		{"domains", `["a..b"]`, "must be a list of lowercase hostnames"},
		{"domains", `["` + long(63) + "." + long(63) + "." + long(63) + "." + long(62) + `"]`, "must be a list of lowercase hostnames"}, // 254
		{"port", `0`, "must be a port number"},
		{"port", `65536`, "must be a port number"},
		{"port", `3000.0`, "must be a port number"},
		{"port", `"3000"`, "must be a port number"},
		{"health", `"up"`, "must be a path starting with /"},
		{"health", `"/up now"`, "must be a path starting with /"},
		{"deploy_rule", `[]`, "must be an object"},
		{"volumes", `[{"name":"data","path":"relative"}]`, "must be a list of at most 50 {name, path} with absolute paths"},
		{"volumes", `[{"name":"data","path":"/a:b"}]`, "must be a list of at most 50 {name, path} with absolute paths"},
		{"volumes", `[{"name":"data","path":"/` + long(4096) + `"}]`, "must be a list of at most 50 {name, path} with absolute paths"},
		{"volumes", `[` + strings.Repeat(`{"name":"v","path":"/v"},`, 50) + `{"name":"v","path":"/v"}]`, "must be a list of at most 50 {name, path} with absolute paths"},
		{"maintenance_page", `"` + long(512<<10+1) + `"`, "must be the page's HTML, at most 512 KB of UTF-8"},
		{"maintenance_page", `7`, "must be the page's HTML, at most 512 KB of UTF-8"},
		{"backups", `{"keep_auto":0,"keep_deploy":5}`, "must be {schedule: \"daily HH:MM\", keep_auto, keep_deploy}, keeps from 1 to 1000"},
		{"backups", `{"keep_auto":7,"keep_deploy":1001}`, "must be {schedule: \"daily HH:MM\", keep_auto, keep_deploy}, keeps from 1 to 1000"},
		{"backups", `{"keep_auto":7,"keep_deploy":5,"schedule":"daily 24:00"}`, "must be {schedule: \"daily HH:MM\", keep_auto, keep_deploy}, keeps from 1 to 1000"},
		{"backups", `{"keep_auto":7}`, "must be {schedule: \"daily HH:MM\", keep_auto, keep_deploy}, keeps from 1 to 1000"},
		{"details", `{"images":{"worker":"x"}}`, "must be {images: {service: image}, cpus, memory, console}, as short text"},
		{"details", `{"gpus":"1"}`, "must be {images: {service: image}, cpus, memory, console}, as short text"},
		{"details", `{"cpus":"` + long(33) + `"}`, "must be {images: {service: image}, cpus, memory, console}, as short text"},
		{"details", `{"console":"a\tb"}`, "must be {images: {service: image}, cpus, memory, console}, as short text"},
		{"details", `{"memory":"1g\u0085"}`, "must be {images: {service: image}, cpus, memory, console}, as short text"},
		{"databases", `[{"service":"cache","image":"redis"}]`, "must be a list of {service, image} naming the project's services"},
		{"databases", `[{"service":"db","image":"  "}]`, "must be a list of {service, image} naming the project's services"},
	} {
		m := valid()
		if c.field == "services" { // what names a service would be refused too
			delete(m, "databases")
			delete(m, "details")
		}
		m[c.field] = json.RawMessage(c.value)
		_, errs := ParseSync(m)
		if got := errs[c.field]; len(got) != 1 || got[0] != c.message {
			t.Errorf("%s = %.40s: %v", c.field, c.value, errs)
		}
		if len(errs) != 1 {
			t.Errorf("%s = %.40s: other fields refused too: %v", c.field, c.value, errs)
		}
	}
	// A domain of 253 characters is one.
	m := valid()
	m["domains"] = json.RawMessage(`["` + strings.Repeat("x", 63) + "." + strings.Repeat("x", 63) + "." + strings.Repeat("x", 63) + "." + strings.Repeat("x", 61) + `"]`)
	if _, errs := ParseSync(m); len(errs) > 0 {
		t.Errorf("253 characters: %v", errs)
	}

	// Short text counts characters, not bytes; unicode is text, and only
	// control characters (Ruby's [[:cntrl:]]) aren't.
	m = valid()
	m["details"] = json.RawMessage(`{"console":"` + strings.Repeat("é", 498) + "\u200b\u00ad" + `"}`)
	if _, errs := ParseSync(m); len(errs) > 0 {
		t.Errorf("500 characters of unicode: %v", errs)
	}
}
