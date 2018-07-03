package sync

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"testing"

	"github.com/go-kit/kit/log"

	//	"github.com/weaveworks/flux"
	"context"

	"github.com/weaveworks/flux/cluster"
	"github.com/weaveworks/flux/cluster/kubernetes"
	"github.com/weaveworks/flux/cluster/kubernetes/testfiles"
	"github.com/weaveworks/flux/git"
	"github.com/weaveworks/flux/git/gittest"
	"github.com/weaveworks/flux/resource"
)

func TestSync(t *testing.T) {
	checkout, cleanup := setup(t)
	defer cleanup()

	// Let's test that cluster.Sync gets called with the appropriate
	// things when we add and remove resources from the config.

	// Start with nothing running. We should be told to apply all the things.
	mockCluster := &cluster.Mock{}
	manifests := &kubernetes.Manifests{}
	nsWhitelist := map[string]bool{}
	var clus cluster.Cluster = &syncCluster{mockCluster, map[string][]byte{}}

	resources, err := manifests.LoadManifests(checkout.Dir(), checkout.ManifestDir())
	if err != nil {
		t.Fatal(err)
	}

	if err := Sync(manifests, resources, clus, true, log.NewNopLogger(), nsWhitelist); err != nil {
		t.Fatal(err)
	}
	checkClusterMatchesFiles(t, manifests, clus, checkout.ManifestDir())

	for _, res := range testfiles.ServiceMap(checkout.ManifestDir()) {
		if err := execCommand("rm", res[0]); err != nil {
			t.Fatal(err)
		}
		commitAction := git.CommitAction{Author: "", Message: "deleted " + res[0]}
		if err := checkout.CommitAndPush(context.Background(), commitAction, nil); err != nil {
			t.Fatal(err)
		}
		break
	}

	resources, err = manifests.LoadManifests(checkout.Dir(), checkout.ManifestDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := Sync(manifests, resources, clus, true, log.NewNopLogger(), nsWhitelist); err != nil {
		t.Fatal(err)
	}
	checkClusterMatchesFiles(t, manifests, clus, checkout.ManifestDir())
}

func TestPrepareSyncDelete(t *testing.T) {
	var tests = []struct {
		msg         string
		repoRes     map[string]resource.Resource
		res         resource.Resource
		nsWhitelist map[string]bool
		expected    *cluster.SyncDef
	}{
		{
			msg:         "No repo resources provided during sync delete",
			repoRes:     map[string]resource.Resource{},
			res:         mockResourceWithIgnorePolicy("service", "ns1", "s2"),
			nsWhitelist: map[string]bool{},
			expected:    &cluster.SyncDef{},
		},
		{
			msg:        "No policy to ignore in place during sync delete",
			repoRes:    map[string]resource.Resource{
				"res1": mockResourceWithoutIgnorePolicy("namespace", "ns1", "ns1"),
				"res2": mockResourceWithoutIgnorePolicy("namespace", "ns2", "ns2"),
				"res3": mockResourceWithoutIgnorePolicy("namespace", "ns3", "ns3"),
				"res4": mockResourceWithoutIgnorePolicy("deployment", "ns1", "d1"),
				"res5": mockResourceWithoutIgnorePolicy("deployment", "ns2", "d2"),
				"res6": mockResourceWithoutIgnorePolicy("service", "ns3", "s1"),
			},
			res:         mockResourceWithIgnorePolicy("service", "ns1", "s2"),
			nsWhitelist: map[string]bool{},
			expected:    &cluster.SyncDef{},
		},
		{
			msg:         "No policy to ignore during sync delete",
			repoRes:     map[string]resource.Resource{
				"res1": mockResourceWithoutIgnorePolicy("namespace", "ns1", "ns1"),
				"res2": mockResourceWithoutIgnorePolicy("namespace", "ns2", "ns2"),
				"res3": mockResourceWithoutIgnorePolicy("namespace", "ns3", "ns3"),
				"res4": mockResourceWithoutIgnorePolicy("deployment", "ns1", "d1"),
				"res5": mockResourceWithoutIgnorePolicy("deployment", "ns2", "d2"),
				"res6": mockResourceWithoutIgnorePolicy("service", "ns3", "s1"),
			},
			res:         mockResourceWithoutIgnorePolicy("service", "ns1", "s2"),
			nsWhitelist: map[string]bool{},
			expected:    &cluster.SyncDef{Actions: []cluster.SyncAction{cluster.SyncAction{Delete: mockResourceWithoutIgnorePolicy("service", "ns1", "s2")}}},
		},
		{
			msg:         "Namespace whitelist not on resource during sync delete",
			repoRes:     map[string]resource.Resource{
				"res1": mockResourceWithoutIgnorePolicy("namespace", "ns1", "ns1"),
				"res2": mockResourceWithoutIgnorePolicy("namespace", "ns2", "ns2"),
				"res3": mockResourceWithoutIgnorePolicy("namespace", "ns3", "ns3"),
				"res4": mockResourceWithoutIgnorePolicy("deployment", "ns1", "d1"),
				"res5": mockResourceWithoutIgnorePolicy("deployment", "ns2", "d2"),
				"res6": mockResourceWithoutIgnorePolicy("service", "ns3", "s1"),
			},
			res:         mockResourceWithoutIgnorePolicy("service", "ns1", "s2"),
			nsWhitelist: map[string]bool{"ns2": true},
			expected:    &cluster.SyncDef{},
		},
		{
			msg:         "Namespace whitelist on resource during sync delete",
			repoRes:     map[string]resource.Resource{
				"res1": mockResourceWithoutIgnorePolicy("namespace", "ns1", "ns1"),
				"res2": mockResourceWithoutIgnorePolicy("namespace", "ns2", "ns2"),
				"res3": mockResourceWithoutIgnorePolicy("namespace", "ns3", "ns3"),
				"res4": mockResourceWithoutIgnorePolicy("deployment", "ns1", "d1"),
				"res5": mockResourceWithoutIgnorePolicy("deployment", "ns2", "d2"),
				"res6": mockResourceWithoutIgnorePolicy("service", "ns3", "s1"),
			},
			res:         mockResourceWithoutIgnorePolicy("service", "ns1", "s2"),
			nsWhitelist: map[string]bool{"ns1": true},
			expected:    &cluster.SyncDef{Actions: []cluster.SyncAction{cluster.SyncAction{Delete: mockResourceWithoutIgnorePolicy("service", "ns1", "s2")}}},
		},
	}

	logger := log.NewNopLogger()
	for _, sc := range tests {
		sync := &cluster.SyncDef{}
		prepareSyncDelete(logger, sc.repoRes, sc.res.ResourceID().String(), sc.res, sync, sc.nsWhitelist)

		if !reflect.DeepEqual(sc.expected, sync) {
			t.Errorf("%s: expected %+v, got %+v\n", sc.msg, sc.expected, sync)
		}
	}
}

func TestPrepareSyncApply(t *testing.T) {
	var tests = []struct {
		msg         string
		clusRes     map[string]resource.Resource
		res         resource.Resource
		nsWhitelist map[string]bool
		expected    *cluster.SyncDef
	}{
		{
			msg:         "No repo resources provided during sync apply",
			clusRes:     map[string]resource.Resource{},
			res:         mockResourceWithIgnorePolicy("service", "ns1", "s2"),
			nsWhitelist: map[string]bool{},
			expected:    &cluster.SyncDef{},
		},
		{
			msg:        "No policy to ignore in place during sync apply",
			clusRes:    map[string]resource.Resource{
				"res1": mockResourceWithoutIgnorePolicy("namespace", "ns1", "ns1"),
				"res2": mockResourceWithoutIgnorePolicy("namespace", "ns2", "ns2"),
				"res3": mockResourceWithoutIgnorePolicy("namespace", "ns3", "ns3"),
				"res4": mockResourceWithoutIgnorePolicy("deployment", "ns1", "d1"),
				"res5": mockResourceWithoutIgnorePolicy("deployment", "ns2", "d2"),
				"res6": mockResourceWithoutIgnorePolicy("service", "ns3", "s1"),
			},
			res:         mockResourceWithIgnorePolicy("service", "ns1", "s2"),
			nsWhitelist: map[string]bool{},
			expected:    &cluster.SyncDef{},
		},
		{
			msg:        "No policy to ignore during sync apply",
			clusRes:    map[string]resource.Resource{
				"res1": mockResourceWithoutIgnorePolicy("namespace", "ns1", "ns1"),
				"res2": mockResourceWithoutIgnorePolicy("namespace", "ns2", "ns2"),
				"res3": mockResourceWithoutIgnorePolicy("namespace", "ns3", "ns3"),
				"res4": mockResourceWithoutIgnorePolicy("deployment", "ns1", "d1"),
				"res5": mockResourceWithoutIgnorePolicy("deployment", "ns2", "d2"),
				"res6": mockResourceWithoutIgnorePolicy("service", "ns3", "s1"),
			},
			res:         mockResourceWithoutIgnorePolicy("service", "ns1", "s2"),
			nsWhitelist: map[string]bool{},
			expected:    &cluster.SyncDef{Actions: []cluster.SyncAction{cluster.SyncAction{Apply: mockResourceWithoutIgnorePolicy("service", "ns1", "s2")}}},
		},
		{
			msg:        "Namespace whitelist not on resource during sync apply",
			clusRes:    map[string]resource.Resource{
				"res1": mockResourceWithoutIgnorePolicy("namespace", "ns1", "ns1"),
				"res2": mockResourceWithoutIgnorePolicy("namespace", "ns2", "ns2"),
				"res3": mockResourceWithoutIgnorePolicy("namespace", "ns3", "ns3"),
				"res4": mockResourceWithoutIgnorePolicy("deployment", "ns1", "d1"),
				"res5": mockResourceWithoutIgnorePolicy("deployment", "ns2", "d2"),
				"res6": mockResourceWithoutIgnorePolicy("service", "ns3", "s1"),
			},
			res:         mockResourceWithoutIgnorePolicy("service", "ns1", "s2"),
			nsWhitelist: map[string]bool{"ns2": true},
			expected:    &cluster.SyncDef{},
		},
		{
			msg:        "Namespace whitelist on resource during sync apply",
			clusRes:    map[string]resource.Resource{
				"res1": mockResourceWithoutIgnorePolicy("namespace", "ns1", "ns1"),
				"res2": mockResourceWithoutIgnorePolicy("namespace", "ns2", "ns2"),
				"res3": mockResourceWithoutIgnorePolicy("namespace", "ns3", "ns3"),
				"res4": mockResourceWithoutIgnorePolicy("deployment", "ns1", "d1"),
				"res5": mockResourceWithoutIgnorePolicy("deployment", "ns2", "d2"),
				"res6": mockResourceWithoutIgnorePolicy("service", "ns3", "s1"),
			},
			res:         mockResourceWithoutIgnorePolicy("service", "ns1", "s2"),
			nsWhitelist: map[string]bool{"ns1": true},
			expected:    &cluster.SyncDef{Actions: []cluster.SyncAction{cluster.SyncAction{Apply: mockResourceWithoutIgnorePolicy("service", "ns1", "s2")}}},
		},
	}

	logger := log.NewNopLogger()
	for _, sc := range tests {
		sync := &cluster.SyncDef{}
		prepareSyncApply(logger, sc.clusRes, sc.res.ResourceID().String(), sc.res, sync, sc.nsWhitelist)

		if !reflect.DeepEqual(sc.expected, sync) {
			t.Errorf("%s: expected %+v, got %+v\n", sc.msg, sc.expected, sync)
		}
	}
}

// ---

var gitconf = git.Config{
	SyncTag:   "test-sync",
	NotesRef:  "test-notes",
	UserName:  "testuser",
	UserEmail: "test@example.com",
}

func setup(t *testing.T) (*git.Checkout, func()) {
	return gittest.Checkout(t)
}

func execCommand(cmd string, args ...string) error {
	c := exec.Command(cmd, args...)
	fmt.Printf("exec: %s %s\n", cmd, strings.Join(args, " "))
	c.Stderr = os.Stderr
	c.Stdout = os.Stdout
	return c.Run()
}

// A cluster that keeps track of exactly what it's been told to apply
// or delete and parrots it back when asked to Export. This is as
// mechanically simple as possible!

type syncCluster struct {
	*cluster.Mock
	resources map[string][]byte
}

func (p *syncCluster) Sync(def cluster.SyncDef) error {
	println("=== Syncing ===")
	for _, action := range def.Actions {
		if action.Delete != nil {
			println("Deleting " + action.Delete.ResourceID().String())
			delete(p.resources, action.Delete.ResourceID().String())
		}
		if action.Apply != nil {
			println("Applying " + action.Apply.ResourceID().String())
			p.resources[action.Apply.ResourceID().String()] = action.Apply.Bytes()
		}
	}
	println("=== Done syncing ===")
	return nil
}

func (p *syncCluster) Export() ([]byte, error) {
	// We need a response for Export, which is supposed to supply the
	// entire configuration as a lump of bytes.
	var configs [][]byte
	for _, config := range p.resources {
		configs = append(configs, config)
	}
	return bytes.Join(configs, []byte("\n---\n")), nil
}

func resourcesToStrings(resources map[string]resource.Resource) map[string]string {
	res := map[string]string{}
	for k, r := range resources {
		res[k] = string(r.Bytes())
	}
	return res
}

// Our invariant is that the model we can export from the cluster
// should always reflect what's in git. So, let's check that.
func checkClusterMatchesFiles(t *testing.T, m cluster.Manifests, c cluster.Cluster, dir string) {
	conf, err := c.Export()
	if err != nil {
		t.Fatal(err)
	}
	resources, err := m.ParseManifests(conf)
	if err != nil {
		t.Fatal(err)
	}
	files, err := m.LoadManifests(dir, dir)
	if err != nil {
		t.Fatal(err)
	}

	expected := resourcesToStrings(files)
	got := resourcesToStrings(resources)

	if !reflect.DeepEqual(expected, got) {
		t.Errorf("expected:\n%#v\ngot:\n%#v", expected, got)
	}
}
