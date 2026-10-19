package controllers

import (
	"context"
	"fmt"
	"math/rand/v2"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	sealsv1 "github.com/helmetica-framework/sigillum/api/v1"
	"github.com/helmetica-framework/sigillum/controllers/gatewayapi"
)

// maxConflictMoves is how many times in a row one exposure is moved off a
// conflicted port before sigillum stops trying.
const maxConflictMoves = 3

// conflictAction is what to do with one exposure's port after reading its
// listener state.
type conflictAction int

const (
	keepPort conflictAction = iota
	movePort
	giveUp
)

// takenPorts collects every listener port already claimed on shard.
func takenPorts(sets []unstructured.Unstructured, shard gatewayapi.ShardRef, own *unstructured.Unstructured) map[int32]struct{} {
	return takenPortsWithConflicts(sets, shard, own, nil)
}

// takenPortsWithConflicts is takenPorts with one exception: ports held by our
// own listeners whose names are in conflicted stay taken. Handing one of those
// back would reproduce the conflict the gateway controller just reported.
func takenPortsWithConflicts(
	sets []unstructured.Unstructured,
	shard gatewayapi.ShardRef,
	own *unstructured.Unstructured,
	conflicted map[string]struct{},
) map[int32]struct{} {
	taken := map[int32]struct{}{}

	for i := range sets {
		set := &sets[i]

		parent, ok := gatewayapi.ParentShard(set)
		if !ok || parent != shard {
			continue
		}

		ours := own != nil &&
			set.GetName() == own.GetName() &&
			set.GetNamespace() == own.GetNamespace()

		for _, l := range gatewayapi.Listeners(set) {
			// Our own ports stay ours, so a re-reconcile keeps the address we
			// already published. The exception is a listener the gateway
			// controller marked Conflicted: handing that port back would just
			// reproduce the conflict.
			if _, bad := conflicted[l.Name]; ours && !bad {
				continue
			}

			taken[l.Port] = struct{}{}
		}
	}

	return taken
}

func (r *SealManager) random() *rand.Rand {
	if r.Rand == nil {
		r.Rand = rand.New(rand.NewPCG(rand.Uint64(), rand.Uint64()))
	}

	return r.Rand
}

// allocatePort picks a port in rng that is not in taken.
//
// The choice is random rather than lowest-first so a just-released port is
// unlikely to be handed straight to a different service while clients still
// hold connections to the old one.
func allocatePort(rng sealsv1.PortRange, taken map[int32]struct{}, rnd *rand.Rand) (int32, error) {
	free := make([]int32, 0, rng.To-rng.From+1)

	for p := rng.From; p <= rng.To; p++ {
		if _, ok := taken[p]; !ok {
			free = append(free, p)
		}
	}

	if len(free) == 0 {
		return 0, fmt.Errorf("no free port in range %d-%d", rng.From, rng.To)
	}

	return free[rnd.IntN(len(free))], nil
}

// listAllListenerSets reads every ListenerSet in the cluster. live picks the
// reader.
//
// APIReader, not Client, when live: the cache has not seen the ListenerSet this
// same controller wrote a moment ago, so a cached list hands the same port to
// the next Seal.
//
// The caller passes live only when an exposure actually needs a new port. A
// settled Seal allocates nothing, and an uncached cluster-wide List on every
// reconcile of every Seal does not scale.
func (r *SealManager) listAllListenerSets(ctx context.Context, live bool) ([]unstructured.Unstructured, error) {
	list := gatewayapi.NewList(gatewayapi.ListenerSetGVK)

	if live {
		err := r.APIReader.List(ctx, list)
		if err != nil {
			return nil, fmt.Errorf("listing listenersets live: %w", err)
		}
	} else {
		err := r.List(ctx, list)
		if err != nil {
			return nil, fmt.Errorf("listing listenersets via cache: %w", err)
		}
	}

	return list.Items, nil
}

// stepConflict decides what to do with an exposure's port and returns its new
// conflict count.
//
// A move breaks stickiness on purpose: a conflicted listener has no
// traffic, so there is no address to keep stable.
func stepConflict(count int32, state gatewayapi.ListenerState) (conflictAction, int32) {
	switch state {
	case gatewayapi.ListenerPending:
		return keepPort, count
	case gatewayapi.ListenerConflicted:
		if count >= maxConflictMoves {
			return giveUp, count
		}

		return movePort, count + 1
	default:
		return keepPort, 0
	}
}

// conflictMessage is the exposure status message once sigillum gives up
// moving a port.
func conflictMessage(exposure string, port int32, shard gatewayapi.ShardRef) string {
	return fmt.Sprintf(
		"exposure %q: port %d on shard %s/%s still conflicts after %d moves, sigillum stopped moving it: ask the platform operator to check the shard's listeners",
		exposure, port, shard.Namespace, shard.Name, maxConflictMoves,
	)
}
