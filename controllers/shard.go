package controllers

import (
	"context"
	"fmt"
	"sort"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	sealsv1 "github.com/helmetica-framework/sigillum/api/v1"
	"github.com/helmetica-framework/sigillum/controllers/gatewayapi"
	gwv1 "sigs.k8s.io/gateway-api/apis/v1"
)

// apiListenerLimit is the Gateway API's own cap on listeners per ListenerSet.
// A class that sets maxListenersPerShard to 0 means "use this", not
// "unlimited": there is no such thing as unlimited here.
const apiListenerLimit = 64

// effectiveMax turns a class's maxListenersPerShard into the number to compare
// against.
func effectiveMax(limit int32) int32 {
	if limit <= 0 || limit > apiListenerLimit {
		return apiListenerLimit
	}

	return limit
}

// listShards returns every Gateway matching the class's shardSelector, sorted.
//
// A nil shardSelector matches nothing, not everything. metav1 spells those two
// differently on purpose, and a class with no selector pulling in every Gateway
// in the cluster would be a very bad surprise.
func (r *SealManager) listShards(ctx context.Context, class *sealsv1.ExposureClass) ([]gatewayapi.ShardRef, error) {
	selector, err := metav1.LabelSelectorAsSelector(class.Spec.ShardSelector)
	if err != nil {
		return nil, fmt.Errorf("parsing shard selector: %w", err)
	}

	list := &gwv1.GatewayList{}
	err = r.List(ctx, list, client.MatchingLabelsSelector{Selector: selector})
	if err != nil {
		return nil, fmt.Errorf("listing gateways: %w", err)
	}

	shards := make([]gatewayapi.ShardRef, 0, len(list.Items))
	for _, s := range list.Items {
		shards = append(shards, gatewayapi.ShardRef{
			Namespace: s.GetNamespace(),
			Name:      s.GetName(),
		})
	}

	sortShards(shards)

	return shards, nil
}

// sortShards orders shards by namespace then name.
//
// List returns whatever order the cache felt like. Without a fixed order,
// placement flaps between reconciles and exposures move shards for no reason.
func sortShards(shards []gatewayapi.ShardRef) {
	sort.Slice(shards, func(i, j int) bool {
		if shards[i].Namespace != shards[j].Namespace {
			return shards[i].Namespace < shards[j].Namespace
		}

		return shards[i].Name < shards[j].Name
	})
}

// shardCounts counts the listeners each shard is already allocating.
func shardCounts(sets []gwv1.ListenerSet) map[gatewayapi.ShardRef]int {
	counts := map[gatewayapi.ShardRef]int{}

	for i := range sets {
		parent, usable := gatewayapi.ParentShard(&sets[i])
		if usable {
			counts[parent] += len(gatewayapi.Listeners(&sets[i]))
		}
	}

	return counts
}

// stickyShard reports the shard this Seal's own ListenerSet already sits on,
// when that shard still matches the class selector.
//
// A false result means the exposure moves and its address changes. The new
// address is reported in status.exposures[] and nowhere else.
func stickyShard(own *gwv1.ListenerSet, shards []gatewayapi.ShardRef) (gatewayapi.ShardRef, bool) {
	parent, usable := gatewayapi.ParentShard(own)
	if !usable {
		return gatewayapi.ShardRef{}, false
	}

	for i := range shards {
		if shards[i].Name == parent.Name && shards[i].Namespace == parent.Namespace {
			return parent, true
		}
	}

	return gatewayapi.ShardRef{}, false
}

// placeExposure returns the first shard with room, in shards order.
//
// Every shard being full is an error rather than a retry: creating gateways is
// out of scope, so nothing the controller does later can fix it. The message
// has to name the action, because the Seal's owner cannot take it themselves.
func placeExposure(
	shards []gatewayapi.ShardRef,
	counts map[gatewayapi.ShardRef]int,
	class *sealsv1.ExposureClass,
) (gatewayapi.ShardRef, error) {
	if len(shards) == 0 {
		return gatewayapi.ShardRef{}, fmt.Errorf(
			"no gateway matches exposure class %q: ask the platform operator to add a Gateway matching %s",
			class.GetName(), selectorText(class),
		)
	}

	limit := effectiveMax(class.Spec.MaxListenersPerShard)

	full := make([]string, 0, len(shards))
	for _, shard := range shards {
		count := counts[shard]
		if count < int(limit) {
			return shard, nil
		}

		full = append(full, fmt.Sprintf("%s %d/%d", shard.Name, count, limit))
	}

	return gatewayapi.ShardRef{}, fmt.Errorf(
		"no shard in exposure class %q has capacity (%s): ask the platform operator to add a Gateway matching %s",
		class.GetName(), strings.Join(full, ", "), selectorText(class),
	)
}

// selectorText renders a class's shardSelector the way an operator would type
// it as a label on a Gateway. The LabelSelector struct's own String is a Go
// dump, which is not something anyone can copy into a manifest.
func selectorText(class *sealsv1.ExposureClass) string {
	selector, err := metav1.LabelSelectorAsSelector(class.Spec.ShardSelector)
	if err != nil {
		return "<invalid shardSelector>"
	}

	// A nil selector and an empty one both render as "", and neither gives the
	// operator anything to act on.
	text := selector.String()
	if text == "" {
		return "<no shardSelector>"
	}

	return text
}
