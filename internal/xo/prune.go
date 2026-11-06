package xo

import (
	"cmp"
	"context"
	"fmt"
	"regexp"
	"slices"
	"strconv"

	"github.com/gofrs/uuid"
	"github.com/vatesfr/xenorchestra-go-sdk/pkg/payloads"
)

// PruneSpec selects the old templates of one image flavor in one pool.
type PruneSpec struct {
	PoolID      uuid.UUID
	Platform    string
	Arch        string
	SchematicID string    // full schematic ID
	Keep        int       // number of templates to keep, newest Talos versions first
	Current     uuid.UUID // the template of this run, always kept
	DryRun      bool      // only report what would be deleted
}

// Prune deletes the templates of the same platform, arch and schematic as
// spec, except the Keep newest Talos versions and the current one. It only
// considers templates this tool named and tagged, so templates made by hand
// are never touched. It returns the templates it deleted (or would delete).
func (b *Builder) Prune(ctx context.Context, spec PruneSpec) ([]*payloads.Template, error) {
	if spec.Keep < 1 {
		return nil, fmt.Errorf("prune: keep must be at least 1, got %d", spec.Keep)
	}
	all, err := b.api.Templates(ctx, "$pool:"+spec.PoolID.String())
	if err != nil {
		return nil, fmt.Errorf("prune: list templates: %w", err)
	}

	name := regexp.MustCompile(`^talos-(v\d+\.\d+\.\d+(?:-[0-9A-Za-z.]+)?)-` +
		regexp.QuoteMeta(spec.Platform+"-"+spec.Arch+"-"+spec.SchematicID[:8]) + `$`)
	tag := "talos-schematic=" + spec.SchematicID[:12]

	type candidate struct {
		t       *payloads.Template
		version talosVersion
	}
	var candidates []candidate
	for _, t := range all {
		m := name.FindStringSubmatch(t.NameLabel)
		if m == nil || t.Pool != spec.PoolID || !slices.Contains(t.Tags, tag) {
			continue
		}
		v, ok := parseTalosVersion(m[1])
		if !ok {
			continue
		}
		candidates = append(candidates, candidate{t, v})
	}
	// Newest first.
	slices.SortFunc(candidates, func(a, b candidate) int { return b.version.compare(a.version) })

	var pruned []*payloads.Template
	kept := 0
	for _, c := range candidates {
		if c.t.UUID == spec.Current {
			continue // kept, and counted below
		}
		if kept < spec.Keep-1 || (kept < spec.Keep && spec.Current == uuid.Nil) {
			kept++
			continue
		}
		pruned = append(pruned, c.t)
	}

	for _, t := range pruned {
		if spec.DryRun {
			b.logf("prune: would delete template %q (%s)", t.NameLabel, t.UUID)
			continue
		}
		b.logf("prune: deleting template %q (%s)", t.NameLabel, t.UUID)
		if err := b.api.DeleteTemplate(ctx, t.UUID); err != nil {
			return pruned, fmt.Errorf("prune: delete template %s: %w", t.UUID, err)
		}
	}
	return pruned, nil
}

// talosVersion is a parsed vX.Y.Z[-pre] version.
type talosVersion struct {
	n   [3]int
	pre string // empty for a release
}

var versionRE = regexp.MustCompile(`^v(\d+)\.(\d+)\.(\d+)(?:-([0-9A-Za-z.]+))?$`)

func parseTalosVersion(s string) (talosVersion, bool) {
	m := versionRE.FindStringSubmatch(s)
	if m == nil {
		return talosVersion{}, false
	}
	var v talosVersion
	for i := range 3 {
		v.n[i], _ = strconv.Atoi(m[i+1])
	}
	v.pre = m[4]
	return v, true
}

// compare orders versions; a release is newer than its pre-releases.
func (v talosVersion) compare(o talosVersion) int {
	if c := slices.Compare(v.n[:], o.n[:]); c != 0 {
		return c
	}
	switch {
	case v.pre == o.pre:
		return 0
	case v.pre == "":
		return 1
	case o.pre == "":
		return -1
	}
	return cmp.Compare(v.pre, o.pre)
}
