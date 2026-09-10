package main

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

// The firmware route is PUBLIC, UNAUTHENTICATED and feeds an OTA slot. The
// failure that matters is not "wrong status code" -- it is a terminal writing
// something that is not a firmware image into flash and bricking itself. These
// assert the properties that prevent that.

const (
	testFirmwareName = "access-terminal-1.3.3.bin"
	testFirmwareSHA  = "d41cdeb3772d13c26768864f1753e9fc8a5f3f084a190ddc4fe5f949a99a0562"
	testFirmwareSize = 1165088
)

func firmwareRouter() *gin.Engine {
	gin.SetMode(gin.TestMode)

	// The handler only, on a bare engine. NewRouter() reaches for a database;
	// this route does not, and the test should not either.
	r := gin.New()
	r.GET("/firmware/:file", serveFirmware)
	return r
}

func TestFirmwareIsServedByteExact(t *testing.T) {
	w := httptest.NewRecorder()
	firmwareRouter().ServeHTTP(w, httptest.NewRequest(http.MethodGet,
		"/firmware/"+testFirmwareName, nil))

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}

	body := w.Body.Bytes()
	if len(body) != testFirmwareSize {
		t.Errorf("length = %d, want %d", len(body), testFirmwareSize)
	}

	// THE DIGEST THE CATALOGUE PROMISES. If these ever disagree, every terminal
	// on the fleet refuses the update -- which is the safe direction, but it is
	// a release stopped for a reason nobody will enjoy diagnosing at the time.
	sum := sha256.Sum256(body)
	if got := hex.EncodeToString(sum[:]); got != testFirmwareSHA {
		t.Errorf("sha256 = %s, want %s", got, testFirmwareSHA)
	}

	// One explicit type, no charset appended, no sniffing.
	if ct := w.Header().Get("Content-Type"); ct != "application/octet-stream" {
		t.Errorf("Content-Type = %q, want application/octet-stream", ct)
	}

	// Nothing may re-encode an image on the way out.
	if ce := w.Header().Get("Content-Encoding"); ce != "" {
		t.Errorf("Content-Encoding = %q, want none", ce)
	}
}

func TestFirmwareMissingIsBare404(t *testing.T) {
	w := httptest.NewRecorder()
	firmwareRouter().ServeHTTP(w, httptest.NewRequest(http.MethodGet,
		"/firmware/access-terminal-9.9.9.bin", nil))

	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", w.Code)
	}

	// EMPTY, not an application document. A terminal cannot tell a 200 carrying
	// HTML from a 200 carrying an image until it has already written it.
	if n := w.Body.Len(); n != 0 {
		t.Errorf("body = %d bytes, want empty; got %q", n, w.Body.String())
	}
}

func TestFirmwareRefusesTraversalAndNonImages(t *testing.T) {
	for _, name := range []string{
		"..%2Fgo.mod",                   // encoded traversal
		"..",                            // bare traversal
		"go.mod",                        // real file, wrong extension
		"nested%2Fx.bin",                // encoded subdirectory
		"access-terminal-1.3.3.bin.txt", // extension smuggling
		"",                              // empty
	} {
		w := httptest.NewRecorder()
		firmwareRouter().ServeHTTP(w, httptest.NewRequest(http.MethodGet,
			"/firmware/"+name, nil))

		// 404 either from the guard or from gin declining to route it at all.
		// What must never happen is a 200.
		if w.Code == http.StatusOK {
			t.Errorf("%q returned 200; it must not be served", name)
		}
	}
}
