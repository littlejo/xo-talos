package main

import (
	"fmt"
	"strings"

	"github.com/gofrs/uuid"
)

// target is one pool to build a template in.
type target struct {
	sr            uuid.UUID
	pool          uuid.UUID
	network       uuid.UUID // optional VIF of the template
	verifyNetwork uuid.UUID // network of the --verify test VM
}

// planTargets gives each SR its pool and the networks of that pool. A
// template cannot be shared across pools, so there is one target per pool:
// two SRs in the same pool are an error. Each network must belong to the
// pool of one of the SRs.
func planTargets(srs []uuid.UUID, srPool map[uuid.UUID]uuid.UUID,
	networks, verifyNetworks []uuid.UUID, netPool map[uuid.UUID]uuid.UUID, verify bool,
) ([]target, error) {
	targets := make([]target, 0, len(srs))
	byPool := map[uuid.UUID]*target{}
	for _, sr := range srs {
		pool := srPool[sr]
		if other, ok := byPool[pool]; ok {
			return nil, fmt.Errorf("SRs %s and %s are both in pool %s; give one SR per pool", other.sr, sr, pool)
		}
		targets = append(targets, target{sr: sr, pool: pool})
		byPool[pool] = &targets[len(targets)-1]
	}

	assign := func(flagName string, ids []uuid.UUID, slot func(*target) *uuid.UUID) error {
		for _, id := range ids {
			t, ok := byPool[netPool[id]]
			if !ok {
				return fmt.Errorf("%s %s is in pool %s, which none of the SRs belongs to", flagName, id, netPool[id])
			}
			if prev := *slot(t); prev != uuid.Nil {
				return fmt.Errorf("%s: networks %s and %s are both in pool %s", flagName, prev, id, t.pool)
			}
			*slot(t) = id
		}
		return nil
	}
	if err := assign("--network", networks, func(t *target) *uuid.UUID { return &t.network }); err != nil {
		return nil, err
	}
	if err := assign("--verify-network", verifyNetworks, func(t *target) *uuid.UUID { return &t.verifyNetwork }); err != nil {
		return nil, err
	}

	if verify {
		var missing []string
		for _, t := range targets {
			if t.verifyNetwork == uuid.Nil {
				missing = append(missing, t.pool.String())
			}
		}
		if len(missing) > 0 {
			return nil, fmt.Errorf("--verify needs a --verify-network in each pool; missing for pool %s", strings.Join(missing, ", "))
		}
	}
	return targets, nil
}

// uuidList is a repeatable flag of UUIDs; each value may also be a
// comma-separated list.
type uuidList []uuid.UUID

func (l *uuidList) String() string {
	parts := make([]string, len(*l))
	for i, id := range *l {
		parts[i] = id.String()
	}
	return strings.Join(parts, ",")
}

func (l *uuidList) Set(value string) error {
	for _, item := range splitList(value) {
		id, err := uuid.FromString(item)
		if err != nil {
			return fmt.Errorf("invalid UUID %q", item)
		}
		*l = append(*l, id)
	}
	return nil
}
