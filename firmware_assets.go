package main

import (
	"embed"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

// Firmware images, served to terminals over an ordinary HTTPS GET.
//
// EMBEDDED RATHER THAN READ FROM DISK, and that is not a preference. Render
// runs this service with no persistent disk -- there is no `disk:` entry on any
// service in render.yaml -- so anything written to the container filesystem at
// runtime is gone at the next deploy or restart. The compiled image is the only
// durable place to put a byte-exact artifact without introducing a storage
// service.
//
// WHY NOT SOMEWHERE ELSE. The terminal's OTA client trusts exactly two roots,
// GTS Root R1 and R4, compiled into the firmware. GitHub Releases chains to
// ISRG and is REJECTED at TLS; the artifact was previously served from a
// temporary cloudflared tunnel whose hostname cannot be recreated if the
// process dies. api.accesslink.store chains to GTS Root R4, which the firmware
// already trusts because it is the same host the API is on.
//
// THE COST, so it is not discovered later: every retained image adds its own
// size to the Go binary and to git history. Keep the current production target
// and at most one rollback predecessor here. If this ever needs to hold many
// concurrent versions, that is the signal to move to object storage rather
// than to keep growing the image.
//
//go:embed firmware/*.bin
var firmwareFS embed.FS

// firmwareDir is the embedded directory prefix. Declared once so the guard
// below and the read cannot drift apart.
const firmwareDir = "firmware/"

// serveFirmware answers GET /firmware/<file>.bin with exact bytes and nothing
// else.
//
// PUBLIC AND UNAUTHENTICATED BY DESIGN. A terminal fetching an update holds a
// device key, but the OTA download presents no credential -- and the image is
// not a secret. Its integrity does not rest on this route: the catalogue row
// carries a SHA-256 and a byte count, and the device verifies both before it
// switches boot partitions. A wrong or truncated body is refused there.
func serveFirmware(c *gin.Context) {
	name := c.Param("file")

	// Traversal, subdirectories and anything that is not a firmware image are
	// all one answer: not found.
	//
	// gin's :file wildcard already refuses a path separator, so this is a
	// second wall rather than the only one -- and the extension check keeps the
	// route from becoming a general reader of whatever else the embed glob
	// might match after a future edit.
	if name == "" ||
		strings.Contains(name, "/") ||
		strings.Contains(name, `\`) ||
		strings.Contains(name, "..") ||
		!strings.HasSuffix(name, ".bin") {
		c.Status(http.StatusNotFound)
		return
	}

	data, err := firmwareFS.ReadFile(firmwareDir + name)
	if err != nil {
		// A MISSING ARTIFACT IS A BARE 404. It must never fall through to
		// another handler or return an application document: a terminal that
		// wrote an HTML error page into its OTA slot would brick itself, and
		// the device cannot tell a 200 carrying HTML from a 200 carrying an
		// image until after it has written it.
		c.Status(http.StatusNotFound)
		return
	}

	// c.Data, not c.File: exact bytes, one explicit content type, no charset
	// appended, no MIME sniffing and no transformation. The router installs no
	// compression middleware, so nothing re-encodes this on the way out.
	c.Data(http.StatusOK, "application/octet-stream", data)
}
