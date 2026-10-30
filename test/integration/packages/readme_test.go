//go:build integration

package packages

import (
	"strings"
	"testing"

	"github.com/guided-traffic/trove/test/integration/testenv"
)

const hostileReadme = `# Hostile

[toc](#user-content-usage)

<script>alert(1)</script>

<img src="https://img.example/x.png" onerror="alert(1)">

![build status](https://img.shields.example/badge.svg)

![own](/api/npm/npm-local/left-pad)

<img src="http://trove.test/files/npm-local/x.png">

[js](javascript:alert(1)) <a href="https://example.com" target="_blank">out</a>

## Usage

<form action="https://evil.example"><input name="a"></form>
`

// TestR_API_029_ReadmeIsRenderedAndSanitizedOnTheServer: the acceptance cases of
// R-API-029 and R-UI-008 through the API.
func TestR_API_029_ReadmeIsRenderedAndSanitizedOnTheServer(t *testing.T) {
	e := newEnv(t, `TROVE_UI_README_EXTERNAL_IMAGE_ORIGINS=["https://img.example"]`)
	e.seed()
	e.publish("npm-local", spec{Name: "hostile", Version: "1.0.0", Readme: hostileReadme})
	e.publish("npm-local", spec{Name: "bare", Version: "1.0.0", Readme: "-"})
	id := e.idOf(e.tokB, "hostile")

	a := e.get(e.tokB, "/api/v1/packages/"+id+"/versions/1.0.0/readme")
	if a.Status != 200 {
		t.Fatalf("%d %s", a.Status, a.Body)
	}
	r := a.JSON(t)
	html, _ := r["html"].(string)
	if r["contentType"] != "text/markdown" || r["truncated"] != false {
		t.Fatalf("%v", r)
	}
	// The listed origin keeps its image, the unlisted badge becomes a link, the images
	// of trove's own origin are gone.
	if num(r["externalImagesBlocked"]) != 1 {
		t.Errorf("externalImagesBlocked = %v\n%s", r["externalImagesBlocked"], html)
	}
	for _, want := range []string{
		`<h1 id="user-content-hostile">Hostile</h1>`, `<h2 id="user-content-usage">Usage</h2>`, `href="#user-content-usage"`,
		`src="https://img.example/x.png"`, `>External image: build status</a>`,
		`href="https://example.com" rel="nofollow noopener noreferrer"`,
	} {
		if !strings.Contains(html, want) {
			t.Errorf("%q is missing in\n%s", want, html)
		}
	}
	for _, bad := range []string{"<script", "onerror", "javascript:", "target=", "<form", "<input", "left-pad", "trove.test", "alert(1)</script"} {
		if strings.Contains(html, bad) {
			t.Errorf("%q survived in\n%s", bad, html)
		}
	}

	t.Run("a version without a README answers 404 and says so in hasReadme", func(t *testing.T) {
		bare := e.idOf(e.tokB, "bare")
		if a := e.get(e.tokB, "/api/v1/packages/"+bare+"/versions/1.0.0/readme"); a.Status != 404 {
			t.Fatalf("readme: %d %s", a.Status, a.Body)
		}
		if d := e.get(e.tokB, "/api/v1/packages/"+bare+"/versions/1.0.0").JSON(t); d["hasReadme"] != false {
			t.Fatalf("hasReadme %v", d["hasReadme"])
		}
		if d := e.get(e.tokB, "/api/v1/packages/"+id+"/versions/1.0.0").JSON(t); d["hasReadme"] != true {
			t.Fatalf("hasReadme %v", d["hasReadme"])
		}
	})
	t.Run("the tag follows the source, and a README the caller cannot see answers the same 404", func(t *testing.T) {
		first := a.Header.Get("ETag")
		if again := e.get(e.tokB, "/api/v1/packages/"+id+"/versions/1.0.0/readme", "If-None-Match", first); again.Status != 304 {
			t.Fatalf("%d", again.Status)
		}
		e.publish("npm-local", spec{Name: "hostile", Version: "1.1.0", Readme: hostileReadme + "\nmore\n"})
		if b := e.get(e.tokB, "/api/v1/packages/"+id+"/versions/1.1.0/readme"); b.Status != 200 || b.Header.Get("ETag") == first {
			t.Fatalf("%d, tag %q", b.Status, b.Header.Get("ETag"))
		}
		// Same source, another version: the tag is a hash of the source, the renderer
		// version and the image origins, so it is the same.
		e.publish("npm-local", spec{Name: "hostile", Version: "1.2.0", Readme: hostileReadme})
		if c := e.get(e.tokB, "/api/v1/packages/"+id+"/versions/1.2.0/readme"); c.Header.Get("ETag") != first {
			t.Fatalf("the same source has the tag %q, not %q", c.Header.Get("ETag"), first)
		}
		hidden := e.get(e.tokD, "/api/v1/packages/"+id+"/versions/1.0.0/readme")
		absent := e.get(e.tokB, "/api/v1/packages/"+id+"/versions/0.0.1/readme")
		if hidden.Status != 404 || hidden.Problem(t) != absent.Problem(t) {
			t.Fatalf("hidden %d %s, absent %d %s", hidden.Status, hidden.Body, absent.Status, absent.Body)
		}
	})
	t.Run("a changed list of image origins changes the tag", func(t *testing.T) {
		other := e.st.Start(t, testenv.ReplicaOptions{Name: "web2", Env: []string{`TROVE_UI_README_EXTERNAL_IMAGE_ORIGINS=["https://img.example","https://img.shields.example"]`}})
		b := e.st.Do(other, "GET", "/api/v1/packages/"+id+"/versions/1.0.0/readme", e.tokB, nil)
		if b.Status != 200 || b.Header.Get("ETag") == a.Header.Get("ETag") {
			t.Fatalf("%d, tag %q vs %q", b.Status, b.Header.Get("ETag"), a.Header.Get("ETag"))
		}
		// The second origin is now listed: the badge is an image, nothing is blocked.
		rb := b.JSON(t)
		if num(rb["externalImagesBlocked"]) != 0 || !strings.Contains(rb["html"].(string), `src="https://img.shields.example/badge.svg"`) {
			t.Fatalf("%v", rb)
		}
	})
	t.Run("every image becomes a link while no origin is listed", func(t *testing.T) {
		none := e.st.Start(t, testenv.ReplicaOptions{Name: "web3"})
		b := e.st.Do(none, "GET", "/api/v1/packages/"+id+"/versions/1.0.0/readme", e.tokB, nil).JSON(t)
		if strings.Contains(b["html"].(string), "<img") || num(b["externalImagesBlocked"]) != 2 {
			t.Fatalf("%v", b)
		}
	})
}
