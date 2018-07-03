package sync

import (
	"github.com/go-kit/kit/log"
	"github.com/pkg/errors"

	"github.com/weaveworks/flux/cluster"
	"github.com/weaveworks/flux/policy"
	"github.com/weaveworks/flux/resource"
)

// Sync synchronises the cluster to the files in a directory
func Sync(m cluster.Manifests, repoResources map[string]resource.Resource, clus cluster.Cluster,
	deletes bool, logger log.Logger, nsWhitelist map[string]bool) error {
	// Get a map of resources defined in the cluster
	clusterBytes, err := clus.Export()

	if err != nil {
		return errors.Wrap(err, "exporting resource defs from cluster")
	}
	clusterResources, err := m.ParseManifests(clusterBytes)
	if err != nil {
		return errors.Wrap(err, "parsing exported resources")
	}

	// Everything that's in the cluster but not in the repo, delete;
	// everything that's in the repo, apply. This is an approximation
	// to figuring out what's changed, and applying that. We're
	// relying on Kubernetes to decide for each application if it is a
	// no-op.
	sync := cluster.SyncDef{}

	// DANGER ZONE (tamara) This works and is dangerous. At the moment will delete Flux and
	// other pods unless the relevant manifests are part of the user repo. Needs a lot of thought
	// before this cleanup cluster feature can be unleashed on the world.
	if deletes {
		for id, res := range clusterResources {
			prepareSyncDelete(logger, repoResources, id, res, &sync, nsWhitelist)
		}
	}

	for id, res := range repoResources {
		prepareSyncApply(logger, clusterResources, id, res, &sync, nsWhitelist)
	}

	return clus.Sync(sync)
}

func prepareSyncDelete(logger log.Logger, repoResources map[string]resource.Resource, id string,
	res resource.Resource, sync *cluster.SyncDef, nsWhitelist map[string]bool) {
	if len(repoResources) == 0 {
		return
	}
	if res.Policy().Contains(policy.Ignore) {
		logger.Log("resource", res.ResourceID(), "ignore", "delete")
		return
	}
	if len(nsWhitelist) > 0 && ! nsWhitelist[res.Namespace()] {
		logger.Log("resource", res.ResourceID(), "ignore", "delete", "reason", "namespace", "namespace", res.Namespace())
		return
	}
	if _, ok := repoResources[id]; !ok {
		sync.Actions = append(sync.Actions, cluster.SyncAction{
			Delete:     res,
		})
	}
}

func prepareSyncApply(logger log.Logger, clusterResources map[string]resource.Resource, id string,
	res resource.Resource, sync *cluster.SyncDef, nsWhitelist map[string]bool) {
	if res.Policy().Contains(policy.Ignore) {
		logger.Log("resource", res.ResourceID(), "ignore", "apply")
		return
	}
	if len(nsWhitelist) > 0 && ! nsWhitelist[res.Namespace()] {
		logger.Log("resource", res.ResourceID(), "ignore", "apply", "reason", "namespace", "namespace", res.Namespace())
		return
	}
	if cres, ok := clusterResources[id]; ok {
		if cres.Policy().Contains(policy.Ignore) {
			logger.Log("resource", res.ResourceID(), "ignore", "apply")
			return
		}
	}
	sync.Actions = append(sync.Actions, cluster.SyncAction{
		Apply:      res,
	})
}
