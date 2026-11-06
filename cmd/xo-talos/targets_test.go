package main

import (
	"strings"
	"testing"

	"github.com/gofrs/uuid"
)

func ids(n int) []uuid.UUID {
	out := make([]uuid.UUID, n)
	for i := range out {
		out[i] = uuid.Must(uuid.NewV4())
	}
	return out
}

func TestPlanTargets(t *testing.T) {
	srs, pools, nets := ids(2), ids(2), ids(4)
	srPool := map[uuid.UUID]uuid.UUID{srs[0]: pools[0], srs[1]: pools[1]}
	netPool := map[uuid.UUID]uuid.UUID{nets[0]: pools[0], nets[1]: pools[1], nets[2]: pools[1], nets[3]: pools[0]}

	targets, err := planTargets(srs, srPool, []uuid.UUID{nets[1]}, []uuid.UUID{nets[2], nets[3]}, netPool, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(targets) != 2 {
		t.Fatalf("%d targets", len(targets))
	}
	want := []target{
		{sr: srs[0], pool: pools[0], verifyNetwork: nets[3]},
		{sr: srs[1], pool: pools[1], network: nets[1], verifyNetwork: nets[2]},
	}
	for i := range want {
		if targets[i] != want[i] {
			t.Errorf("target %d = %+v, want %+v", i, targets[i], want[i])
		}
	}
}

func TestPlanTargetsErrors(t *testing.T) {
	srs, pools, nets := ids(2), ids(3), ids(3)
	netPool := map[uuid.UUID]uuid.UUID{nets[0]: pools[0], nets[1]: pools[0], nets[2]: pools[2]}
	split := map[uuid.UUID]uuid.UUID{srs[0]: pools[0], srs[1]: pools[1]}
	tests := []struct {
		name       string
		srPool     map[uuid.UUID]uuid.UUID
		networks   []uuid.UUID
		verifyNets []uuid.UUID
		verify     bool
		want       string
	}{
		{"two SRs in one pool", map[uuid.UUID]uuid.UUID{srs[0]: pools[0], srs[1]: pools[0]}, nil, nil, false, "both in pool"},
		{"network outside the pools", split, []uuid.UUID{nets[2]}, nil, false, "none of the SRs"},
		{"two networks in one pool", split, []uuid.UUID{nets[0], nets[1]}, nil, false, "both in pool"},
		{"verify without network", split, nil, []uuid.UUID{nets[0]}, true, "missing for pool " + pools[1].String()},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := planTargets(srs, tt.srPool, tt.networks, tt.verifyNets, netPool, tt.verify)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestUUIDList(t *testing.T) {
	var l uuidList
	a, b, c := ids(3)[0], ids(3)[1], ids(3)[2]
	if err := l.Set(a.String() + ", " + b.String()); err != nil {
		t.Fatal(err)
	}
	if err := l.Set(c.String()); err != nil {
		t.Fatal(err)
	}
	if len(l) != 3 || l[2] != c {
		t.Fatalf("list = %v", l)
	}
	if err := l.Set("nope"); err == nil {
		t.Fatal("accepted an invalid UUID")
	}
}
