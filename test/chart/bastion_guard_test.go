//go:build chart

package chart_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestR_BAS_045_TheClientAddressNeedsTheTrustedProxiesBehindTheIngress (Q-BAS-130): with the Ingress on, every
// request reaches the Bastion from the ingress controller's pods, so without config.server.trusted_proxies all
// callers count as one address. The render refuses it and says what to list.
func TestR_BAS_045_TheClientAddressNeedsTheTrustedProxiesBehindTheIngress(t *testing.T) {
	noProxies := []string{"config.server.trusted_proxies=null"}
	t.Run("the Ingress on and no trusted proxy fails naming the value and what to list", func(t *testing.T) {
		bstFails(t, bstRequired(), noProxies, "config.server.trusted_proxies", "ingress controller", "every caller's address")
	})
	t.Run("the Ingress off needs none", func(t *testing.T) {
		bstRender(t, bstRequired(), append(noProxies, "ingress.enabled=false")...)
	})
	t.Run("a list renders into the configuration", func(t *testing.T) {
		_, docs := bstRender(t, bstRequired(), "config.server.trusted_proxies={10.1.0.0/16}")
		srv, _ := bstConfig(t, docs)["server"].(map[string]any)
		got, _ := srv["trusted_proxies"].([]any)
		if len(got) != 1 || got[0] != "10.1.0.0/16" {
			t.Fatalf("server.trusted_proxies = %v", srv["trusted_proxies"])
		}
	})
}

// TestR_BAS_030_TheSpoolFitsTheTemporaryVolume (Q-BAS-131): the fills in progress are bounded at run time by
// config.bastion.cache.spool_max_bytes; the chart sizes the volume the fills are written to from it and refuses a
// combination in which the pod could be evicted.
func TestR_BAS_030_TheSpoolFitsTheTemporaryVolume(t *testing.T) {
	bucket := []string{bastionSet("bucket-valkey")}
	tmpLimit := func(t *testing.T, files []string, sets ...string) string {
		t.Helper()
		_, docs := bstRender(t, files, sets...)
		vols, _ := at(bstWorkload(t, docs), "spec", "template", "spec", "volumes").([]any)
		for _, v := range vols {
			m, _ := v.(map[string]any)
			if m["name"] == "tmp" {
				return str(m, "emptyDir", "sizeLimit")
			}
		}
		t.Fatal("no tmp volume")
		return ""
	}
	t.Run("the bucket backend's default tmp holds the spool and 256Mi", func(t *testing.T) {
		if got := tmpLimit(t, bucket); got != "2304Mi" {
			t.Fatalf("tmp sizeLimit = %q, want 2304Mi (2GiB spool + 256Mi)", got)
		}
		if got := tmpLimit(t, bucket, "config.bastion.cache.spool_max_bytes=4GiB", "config.bastion.cache.max_object_bytes=4GiB"); got != "4352Mi" {
			t.Fatalf("tmp sizeLimit = %q, want 4352Mi", got)
		}
	})
	t.Run("the volume backend keeps 1Gi for /tmp", func(t *testing.T) {
		if got := tmpLimit(t, bstRequired()); got != "1Gi" {
			t.Fatalf("tmp sizeLimit = %q, want 1Gi", got)
		}
	})
	t.Run("a tmp volume that cannot hold the spool of the bucket backend fails", func(t *testing.T) {
		bstFails(t, bucket, []string{"tmp.sizeLimit=1Gi"}, "tmp.sizeLimit", "spool_max_bytes", "evicted")
		if got := tmpLimit(t, bucket, "tmp.sizeLimit=3Gi"); got != "3Gi" {
			t.Fatalf("a set tmp.sizeLimit is used as written, got %q", got)
		}
	})
	t.Run("a spool smaller than the largest object fails", func(t *testing.T) {
		bstFails(t, bstRequired(), []string{"config.bastion.cache.spool_max_bytes=512MiB"}, "spool_max_bytes", "max_object_bytes")
	})
	t.Run("the volume backend needs room for the cache and the spool", func(t *testing.T) {
		bstFails(t, bstRequired(), []string{"cacheVolume.size=21Gi"}, "cacheVolume.size", "spool_max_bytes")
		bstRender(t, bstRequired(), "cacheVolume.size=22Gi")
	})
	t.Run("a size that is no size fails", func(t *testing.T) {
		bstFails(t, bucket, []string{"tmp.sizeLimit=lots"}, "not a size")
	})
}

// TestR_BAS_047_TheValuesDoNotSuggestAnAnywhereEgress: the example of the cache bucket's egress is a narrow
// range, and says why (Q-BAS-130 sibling of R-BAS-047: a wide range undoes the egress promise).
func TestR_BAS_047_TheValuesDoNotSuggestAnAnywhereEgress(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(bastionDir(), "values.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	for i, l := range strings.Split(string(raw), "\n") {
		if strings.Contains(l, "cidr: 0.0.0.0/0}") {
			t.Errorf("values.yaml:%d suggests an egress to anywhere: %s", i+1, strings.TrimSpace(l))
		}
	}
	if !strings.Contains(string(raw), "undoes the egress promise") {
		t.Error("values.yaml does not say that a wide range undoes the egress promise")
	}
	sets, _ := filepath.Glob(filepath.Join(bastionDir(), "ci", "*.yaml"))
	for _, f := range sets {
		b, _ := os.ReadFile(f)
		if strings.Contains(string(b), "cidr: 0.0.0.0/0") {
			t.Errorf("%s carries an egress to anywhere", filepath.Base(f))
		}
	}
}
