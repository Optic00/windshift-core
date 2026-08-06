package logbookapi

import (
	"net/http"

	"windshift/internal/logbook"
	"windshift/internal/models"
	"windshift/internal/restapi"
)

// bucketPermissionService is the subset of *logbook.PermissionService used by
// the shared bucket guards. Keeping the interface small lets the same guard
// code back both the document handlers and the action handlers without
// leaking concrete types across the package.
type bucketPermissionService interface {
	HasBucketPermission(userID int, isAdmin bool, groupIDs []int, bucketID, permission string) (bool, error)
	GetAccessibleBucketIDs(userID int, isAdmin bool, groupIDs []int) ([]string, error)
}

// requireBucket resolves the Logbook actor, validates the bucketID path
// parameter, and enforces the requested bucket permission. Unauthorized and
// malformed requests are answered with 404 so bucket existence is not leaked.
func requireBucket(w http.ResponseWriter, r *http.Request, perms bucketPermissionService, permission string) (*LogbookUser, string, bool) {
	lbUser, ok := requireLogbookAuth(w, r)
	if !ok {
		return nil, "", false
	}

	bucketID := r.PathValue("bucketID")
	if !isValidUUID(bucketID) {
		respondNotFound(w, r)
		return nil, "", false
	}

	if !requireBucketAccessForUser(w, r, perms, lbUser, bucketID, permission) {
		return nil, "", false
	}

	return lbUser, bucketID, true
}

// requireBucketView is a convenience wrapper for requireBucket with view permission.
func requireBucketView(w http.ResponseWriter, r *http.Request, perms bucketPermissionService) (*LogbookUser, string, bool) {
	return requireBucket(w, r, perms, models.LogbookPermissionBucketView)
}

// requireBucketEdit is a convenience wrapper for requireBucket with edit permission.
func requireBucketEdit(w http.ResponseWriter, r *http.Request, perms bucketPermissionService) (*LogbookUser, string, bool) {
	return requireBucket(w, r, perms, models.LogbookPermissionBucketEdit)
}

// requireBucketAdmin is a convenience wrapper for requireBucket with admin permission.
func requireBucketAdmin(w http.ResponseWriter, r *http.Request, perms bucketPermissionService) (*LogbookUser, string, bool) {
	return requireBucket(w, r, perms, models.LogbookPermissionBucketAdmin)
}

// requireBucketAccessForUser checks permission on an already-known bucket for
// an already-resolved user. It exists so callers such as DownloadAttachment
// can authenticate, load their resource, then enforce the bucket gate without
// re-running authentication.
func requireBucketAccessForUser(w http.ResponseWriter, r *http.Request, perms bucketPermissionService, lbUser *LogbookUser, bucketID, permission string) bool {
	has, err := perms.HasBucketPermission(lbUser.ID, lbUser.IsAdmin, lbUser.GroupIDs, bucketID, permission)
	if err != nil {
		respondInternalError(w, r, err)
		return false
	}
	if !has {
		respondNotFound(w, r)
		return false
	}
	return true
}

// requireAccessibleBuckets resolves the actor and returns the IDs of all
// buckets they can access with at least view permission.
func requireAccessibleBuckets(w http.ResponseWriter, r *http.Request, perms bucketPermissionService) ([]string, bool) {
	lbUser, ok := requireLogbookAuth(w, r)
	if !ok {
		return nil, false
	}

	ids, err := perms.GetAccessibleBucketIDs(lbUser.ID, lbUser.IsAdmin, lbUser.GroupIDs)
	if err != nil {
		respondInternalError(w, r, err)
		return nil, false
	}

	return ids, true
}

// requireSystemAdmin resolves the actor and enforces system-admin status.
func requireSystemAdmin(w http.ResponseWriter, r *http.Request) (*LogbookUser, bool) {
	lbUser, ok := requireLogbookAuth(w, r)
	if !ok {
		return nil, false
	}

	if !lbUser.IsAdmin {
		restapi.RespondError(w, r, restapi.ErrAdminRequired)
		return nil, false
	}

	return lbUser, true
}

// ensure *logbook.PermissionService implements bucketPermissionService at compile time.
var _ bucketPermissionService = (*logbook.PermissionService)(nil)
