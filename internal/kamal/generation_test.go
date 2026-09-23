package kamal

import (
	"reflect"
	"testing"
)

// Data generations: a restore builds generation g+1 beside the live one.
// Generation 1 is today's names; generation g ≥ 2 has its own volumes and
// accessories (docs/plans/restore.md, Batch 3).
func TestNames(t *testing.T) {
	one, two := Names{Project: "equip", Generation: 1}, Names{Project: "equip", Generation: 2}
	zero := Names{Project: "equip"}
	for _, c := range []struct{ got, want string }{
		{one.Volume("storage"), "equip_storage"},
		{zero.Volume("storage"), "equip_storage"},
		{two.Volume("storage"), "equip.g2_storage"},
		{one.Accessory("db"), "db"},
		{two.Accessory("db"), "db-g2"},
		{one.Container("db"), "equip-db"},
		{two.Container("db"), "equip-db-g2"},
	} {
		if c.got != c.want {
			t.Errorf("got %q, want %q", c.got, c.want)
		}
	}
}

func TestConfigGeneration2(t *testing.T) {
	p := load(t, "testdata/phoenix.compose.yml")
	g2 := target
	g2.Generation = 2
	assertGolden(t, config(t, p, g2), "testdata/phoenix.g2.deploy.yml")

	if got := AppVolumes(p, 2); !reflect.DeepEqual(got, []string{"phoenixapp.g2_media:/media"}) {
		t.Errorf("AppVolumes(2) = %v", got)
	}
	labels := AccessoryLabels(p, 2)
	if _, ok := labels["db-g2"]; !ok || len(labels) != 2 {
		t.Errorf("AccessoryLabels(2) = %v", labels)
	}
	secrets, err := ResolveSecrets(p, 2, func(name string) (string, bool) { return "pw", true })
	if err != nil || secrets["APP__DATABASE_URL"] != "postgres://postgres:pw@phoenixapp-db-g2/phoenixapp" {
		t.Errorf("ResolveSecrets(2) = %v, %v", secrets["APP__DATABASE_URL"], err)
	}
}

// What a restore removes: a generation's accessory containers, and the
// volumes that begin with its prefix, which no other project's or
// generation's do.
func TestAccessoriesAndVolumePrefix(t *testing.T) {
	p := load(t, "testdata/phoenix.compose.yml")
	if got := Accessories(p, 1); !reflect.DeepEqual(got, []string{"phoenixapp-cache", "phoenixapp-db"}) {
		t.Errorf("Accessories(1) = %v", got)
	}
	if got := Accessories(p, 3); !reflect.DeepEqual(got, []string{"phoenixapp-cache-g3", "phoenixapp-db-g3"}) {
		t.Errorf("Accessories(3) = %v", got)
	}
	for _, c := range []struct {
		generation int
		want       string
	}{{1, "phoenixapp_"}, {0, "phoenixapp_"}, {2, "phoenixapp.g2_"}} {
		if got := (Names{Project: "phoenixapp", Generation: c.generation}).VolumePrefix(); got != c.want {
			t.Errorf("VolumePrefix(%d) = %q, want %q", c.generation, got, c.want)
		}
	}
	if _, labelled := config(t, p, target)["labels"]; labelled {
		t.Errorf("generation 1's app is labelled: its deploy.yml is what it always was")
	}
}
