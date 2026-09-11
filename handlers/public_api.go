package handlers

import (
	"crypto/rand"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"sync"

	"github.com/gin-gonic/gin"

	"access-terminal-cloud-api/middleware"
	"access-terminal-cloud-api/models"
	"access-terminal-cloud-api/service"
)

// The public API v1 handlers -- API_SPEC.md section 18.
//
// ---------------------------------------------------------------------------
// WHAT A HANDLER HERE IS ALLOWED TO DO
// ---------------------------------------------------------------------------
//
// Read the request into a service input, take the tenant from the middleware,
// call ONE service method, and write the result or hand the error to
// RespondServiceError. Nothing else. No handler in this file queries the
// database, reads a company id from the request, or chooses an HTTP status
// for a failure -- the registry does that from the code the service returned.
//
// THE TENANT IS NEVER READ FROM THE REQUEST. middleware.Tenant(c) is the only
// source, and it is nil unless APICredentialAuthMiddleware ran; a nil here
// means the route was mounted on the wrong group and is answered as a missing
// credential rather than guessed.
//
// UNKNOWN INPUT IS REFUSED, NOT IGNORED (section 18, "Errors"): a query
// parameter this version does not define is unknown_parameter, and one that
// tries to name the tenant is tenant_identity_not_permitted -- checked first,
// because "you cannot choose your company" is the more useful answer than
// "unknown parameter" for exactly that mistake.

// publicServices is the wired service layer. Built once, lazily, so that a
// router constructed without ConfigurePublicAPI (every test does this) still
// serves -- with an ephemeral cursor key that is fine for a single process.
type publicServices struct {
	members *service.MemberService
	sites   *service.SiteService
}

var (
	publicOnce sync.Once
	publicMu   sync.RWMutex
	public     *publicServices
)

// ConfigurePublicAPI wires the public services with the cursor signing key.
//
// Called from main before the router is built. The key must be at least 32
// bytes (models.NewCursorSigner enforces it); main decides where it comes
// from and what to do when it is absent.
func ConfigurePublicAPI(cursorKey []byte) error {
	signer, err := models.NewCursorSigner(cursorKey)
	if err != nil {
		return err
	}
	publicMu.Lock()
	defer publicMu.Unlock()
	public = &publicServices{
		members: service.NewMemberService(signer),
		sites:   service.NewSiteService(),
	}
	return nil
}

// EphemeralCursorKey draws a random signing key for a process that was given
// none. Cursors it signs do not survive a restart and are not honoured by a
// sibling instance -- both answer cursor_invalid, and a client starts the
// listing again. The safe direction: nothing is served that was not signed.
func EphemeralCursorKey() []byte {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		// crypto/rand failing is not a condition to serve through.
		panic(fmt.Sprintf("public api: generating an ephemeral cursor key: %v", err))
	}
	return key
}

func publicAPI() *publicServices {
	publicOnce.Do(func() {
		publicMu.RLock()
		configured := public != nil
		publicMu.RUnlock()
		if !configured {
			if err := ConfigurePublicAPI(EphemeralCursorKey()); err != nil {
				panic(err)
			}
			log.Printf("public api: no cursor signing key configured; using an ephemeral one " +
				"(cursors will not survive a restart or span instances)")
		}
	})
	publicMu.RLock()
	defer publicMu.RUnlock()
	return public
}

// tenantOrRefuse is the one place a handler obtains its tenant.
func tenantOrRefuse(c *gin.Context) *service.TenantContext {
	tc := middleware.Tenant(c)
	if tc == nil {
		RespondServiceError(c, "public tenant", service.ErrCredentialMissing())
	}
	return tc
}

// tenantParams are the names a caller might use to choose a company. They are
// refused by name, before anything else, whatever the endpoint.
var tenantParams = []string{"company_id", "company"}

// checkQuery refuses a tenant-naming parameter, then anything not in `allowed`.
func checkQuery(c *gin.Context, allowed ...string) bool {
	query := c.Request.URL.Query()
	for _, name := range tenantParams {
		if _, present := query[name]; present {
			RespondServiceError(c, "public query", service.ErrTenantIdentity(name))
			return false
		}
	}
	permitted := make(map[string]bool, len(allowed))
	for _, name := range allowed {
		permitted[name] = true
	}
	for name := range query {
		if !permitted[name] {
			RespondServiceError(c, "public query", service.ErrUnknownParameter(name))
			return false
		}
	}
	return true
}

// pageRequest reads limit and cursor. A limit that is not an integer is
// refused here; one that is an integer outside the bounds is refused by the
// service, with the same code, so the two cannot disagree about the message.
func pageRequest(c *gin.Context) (service.PageRequest, bool) {
	var page service.PageRequest
	if raw := c.Query("limit"); raw != "" {
		limit, err := strconv.Atoi(raw)
		if err != nil {
			RespondServiceError(c, "public page", service.ErrInvalidField("limit",
				fmt.Sprintf("limit must be an integer between 1 and %d.", service.MaxPageSize)))
			return page, false
		}
		if limit == 0 {
			// Zero means "not supplied" to the service; a caller who sent it
			// literally asked for nothing, which is out of bounds.
			RespondServiceError(c, "public page", service.ErrInvalidField("limit",
				fmt.Sprintf("limit must be an integer between 1 and %d.", service.MaxPageSize)))
			return page, false
		}
		page.Limit = limit
	}
	page.Cursor = c.Query("cursor")
	return page, true
}

// listEnvelope is the paginated wire shape: data, has_more, next_cursor
// (null when has_more is false).
type listEnvelope[T any] struct {
	Data       []T     `json:"data"`
	HasMore    bool    `json:"has_more"`
	NextCursor *string `json:"next_cursor"`
}

func envelope[T any](page *service.Page[T]) listEnvelope[T] {
	out := listEnvelope[T]{Data: page.Items, HasMore: page.HasMore}
	if out.Data == nil {
		out.Data = []T{}
	}
	if page.HasMore {
		cursor := page.NextCursor
		out.NextCursor = &cursor
	}
	return out
}

// ---------------------------------------------------------------------------
// Members
// ---------------------------------------------------------------------------

// PublicListMembers handles GET /api/public/v1/members.
func PublicListMembers(c *gin.Context) {
	tc := tenantOrRefuse(c)
	if tc == nil {
		return
	}
	if !checkQuery(c, "limit", "cursor") {
		return
	}
	page, ok := pageRequest(c)
	if !ok {
		return
	}
	result, err := publicAPI().members.List(c.Request.Context(), tc, page)
	if err != nil {
		RespondServiceError(c, "public list members", err)
		return
	}
	c.JSON(http.StatusOK, envelope(result))
}

// PublicGetMember handles GET /api/public/v1/members/{member_id}.
func PublicGetMember(c *gin.Context) {
	tc := tenantOrRefuse(c)
	if tc == nil {
		return
	}
	if !checkQuery(c) {
		return
	}
	member, err := publicAPI().members.Get(c.Request.Context(), tc, c.Param("member_id"))
	if err != nil {
		RespondServiceError(c, "public get member", err)
		return
	}
	c.JSON(http.StatusOK, member)
}

// ---------------------------------------------------------------------------
// Sites
// ---------------------------------------------------------------------------

// PublicListSites handles GET /api/public/v1/sites. Not paginated: the answer
// is every site the credential reaches, under `data` alone.
func PublicListSites(c *gin.Context) {
	tc := tenantOrRefuse(c)
	if tc == nil {
		return
	}
	if !checkQuery(c) {
		return
	}
	sites, err := publicAPI().sites.List(c.Request.Context(), tc)
	if err != nil {
		RespondServiceError(c, "public list sites", err)
		return
	}
	if sites == nil {
		sites = []service.Site{}
	}
	c.JSON(http.StatusOK, gin.H{"data": sites})
}

// PublicGetSite handles GET /api/public/v1/sites/{site_id}.
func PublicGetSite(c *gin.Context) {
	tc := tenantOrRefuse(c)
	if tc == nil {
		return
	}
	if !checkQuery(c) {
		return
	}
	site, err := publicAPI().sites.Get(c.Request.Context(), tc, c.Param("site_id"))
	if err != nil {
		RespondServiceError(c, "public get site", err)
		return
	}
	c.JSON(http.StatusOK, site)
}
