//go:build integration

package npm

import (
	"context"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/guided-traffic/trove/test/integration/testenv"
)

// rawPut sends a PUT whose body the test streams: no length unless contentLength is
// set, and with expectContinue the client waits for the server's go-ahead before it
// sends the body, so a server that refuses on the headers is heard.
func rawPut(t testing.TB, r *testenv.Replica, path, token string, body io.Reader, contentLength int64, expectContinue bool) testenv.Answer {
	t.Helper()
	ans, err := rawPutErr(r, path, token, body, contentLength, expectContinue)
	if err != nil {
		t.Fatalf("PUT %s: %v", path, err)
	}
	return ans
}

func rawPutErr(r *testenv.Replica, path, token string, body io.Reader, contentLength int64, expectContinue bool) (testenv.Answer, error) {
	return rawPutTo(r.Addr("default"), path, token, body, contentLength, expectContinue)
}

func rawPutTo(addr, path, token string, body io.Reader, contentLength int64, expectContinue bool) (testenv.Answer, error) {
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPut, "http://"+addr+path, body)
	if err != nil {
		return testenv.Answer{}, err
	}
	req.Host = testenv.DefaultHost
	req.ContentLength = contentLength
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	if expectContinue {
		req.Header.Set("Expect", "100-continue")
	}
	tr := &http.Transport{DisableKeepAlives: true, ExpectContinueTimeout: 5 * time.Second}
	defer tr.CloseIdleConnections()
	res, err := (&http.Client{Transport: tr}).Do(req)
	if err != nil {
		return testenv.Answer{}, err
	}
	b, _ := io.ReadAll(res.Body)
	_ = res.Body.Close()
	return testenv.Answer{Status: res.StatusCode, Header: res.Header, Body: b}, nil
}
