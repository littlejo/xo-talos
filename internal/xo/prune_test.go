package xo

import (
	"context"
	"slices"
	"testing"

	"github.com/gofrs/uuid"
	"github.com/vatesfr/xenorchestra-go-sdk/pkg/payloads"
)

const testSchematicID = "53b20d86399013eadfd44ee49804c1fef069bfdee3b43f3f3f5a2f57c03338ac"

// addTemplate adds a template with one disk to the fake, tagged like the tool
// does unless tags are given.
func addTemplate(f *fakeAPI, name string, tags ...string) uuid.UUID {
	if tags == nil {
		tags = []string{"talos", "talos-schematic=" + testSchematicID[:12]}
	}
	id, disk := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
	f.vdis[disk] = nil
	f.vms[id] = &fakeVM{vm: payloads.VM{ID: id, NameLabel: name, Tags: tags, PowerState: payloads.PowerStateHalted}, disks: []uuid.UUID{disk}, template: true}
	return id
}

func pruneFixture() (*fakeAPI, map[string]uuid.UUID) {
	f := newFakeAPI()
	ids := map[string]uuid.UUID{}
	for _, v := range []string{"v1.13.9", "v1.14.4", "v1.14.5", "v1.9.6", "v1.15.0-alpha.2", "v1.14.10"} {
		ids[v] = addTemplate(f, "talos-"+v+"-nocloud-amd64-53b20d86")
	}
	// Never candidates:
	ids["other-schematic"] = addTemplate(f, "talos-v1.12.0-nocloud-amd64-7e6294f6")
	ids["other-arch"] = addTemplate(f, "talos-v1.12.0-nocloud-arm64-53b20d86")
	ids["untagged"] = addTemplate(f, "talos-v1.12.0-nocloud-amd64-53b20d86", "talos")
	ids["custom-name"] = addTemplate(f, "my-talos-v1.12.0-nocloud-amd64-53b20d86")
	return f, ids
}

func spec(f *fakeAPI, keep int, current uuid.UUID) PruneSpec {
	return PruneSpec{PoolID: f.pool, Platform: "nocloud", Arch: "amd64", SchematicID: testSchematicID, Keep: keep, Current: current}
}

func remaining(f *fakeAPI) []string {
	var names []string
	for _, v := range f.vms {
		if v.template {
			names = append(names, v.vm.NameLabel)
		}
	}
	slices.Sort(names)
	return names
}

func TestPrune(t *testing.T) {
	f, ids := pruneFixture()
	pruned, err := newTestBuilder(f).Prune(context.Background(), spec(f, 2, ids["v1.14.10"]))
	if err != nil {
		t.Fatal(err)
	}
	// Kept: v1.15.0-alpha.2 (newest) and v1.14.10 (current); v1.14.10 > v1.14.5
	// numerically.
	want := []string{
		"my-talos-v1.12.0-nocloud-amd64-53b20d86",
		"talos-v1.12.0-nocloud-amd64-53b20d86", // untagged
		"talos-v1.12.0-nocloud-amd64-7e6294f6",
		"talos-v1.12.0-nocloud-arm64-53b20d86",
		"talos-v1.14.10-nocloud-amd64-53b20d86",
		"talos-v1.15.0-alpha.2-nocloud-amd64-53b20d86",
	}
	if got := remaining(f); !slices.Equal(got, want) {
		t.Fatalf("remaining templates:\n got %v\nwant %v", got, want)
	}
	if len(pruned) != 4 {
		t.Fatalf("pruned %d templates, want 4", len(pruned))
	}
	if len(f.vdis) != len(want) {
		t.Fatalf("%d disks left for %d templates", len(f.vdis), len(want))
	}
}

func TestPruneKeepsCurrentEvenIfOld(t *testing.T) {
	f, ids := pruneFixture()
	if _, err := newTestBuilder(f).Prune(context.Background(), spec(f, 1, ids["v1.9.6"])); err != nil {
		t.Fatal(err)
	}
	if f.vms[ids["v1.9.6"]] == nil {
		t.Fatal("deleted the current template")
	}
	for _, v := range []string{"v1.13.9", "v1.14.4", "v1.14.5", "v1.14.10", "v1.15.0-alpha.2"} {
		if f.vms[ids[v]] != nil {
			t.Errorf("kept %s with --prune-keep 1", v)
		}
	}
}

func TestPruneDryRun(t *testing.T) {
	f, ids := pruneFixture()
	before := remaining(f)
	pruned, err := newTestBuilder(f).Prune(context.Background(), PruneSpec{
		PoolID: f.pool, Platform: "nocloud", Arch: "amd64", SchematicID: testSchematicID,
		Keep: 1, Current: ids["v1.14.10"], DryRun: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(pruned) != 5 {
		t.Fatalf("would prune %d templates, want 5", len(pruned))
	}
	if !slices.Equal(remaining(f), before) || slices.Contains(f.calls, "DeleteTemplate") {
		t.Fatal("dry run deleted templates")
	}
}

func TestPruneRejectsKeepZero(t *testing.T) {
	f, ids := pruneFixture()
	if _, err := newTestBuilder(f).Prune(context.Background(), spec(f, 0, ids["v1.14.10"])); err == nil {
		t.Fatal("accepted keep 0")
	}
	if slices.Contains(f.calls, "DeleteTemplate") {
		t.Fatal("deleted templates with keep 0")
	}
}

func TestTalosVersionCompare(t *testing.T) {
	order := []string{"v1.9.6", "v1.13.9", "v1.14.5", "v1.14.10", "v1.15.0-alpha.1", "v1.15.0-alpha.2", "v1.15.0-beta.0", "v1.15.0"}
	for i := 1; i < len(order); i++ {
		a, _ := parseTalosVersion(order[i-1])
		b, _ := parseTalosVersion(order[i])
		if a.compare(b) >= 0 || b.compare(a) <= 0 {
			t.Errorf("%s should sort before %s", order[i-1], order[i])
		}
	}
	if _, ok := parseTalosVersion("v1.14"); ok {
		t.Error("parsed an incomplete version")
	}
}
