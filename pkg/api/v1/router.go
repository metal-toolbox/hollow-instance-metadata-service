package metadataservice

import (
	"database/sql"
	"errors"
	"fmt"
	"path"
	"reflect"
	"strings"
	"text/template"

	"github.com/gin-gonic/gin"
	"github.com/go-playground/validator/v10"
	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"go.uber.org/zap"

	"go.hollow.sh/toolbox/ginjwt"

	"go.hollow.sh/metadataservice/internal/lookup"
	"go.hollow.sh/metadataservice/internal/middleware"
	"go.hollow.sh/metadataservice/internal/models"
)

const (
	// V1URI is the path prefix for all v1 endpoints
	V1URI = "/api/v1"

	// MetadataURI is the path to the regular metadata endpoint, called by the
	// instances themselves to retrieve their metadata.
	MetadataURI = "/metadata"

	// UserdataURI is the path to the regular userdata endpoint, called by the
	// instances themselves to retrieve their userdata.
	UserdataURI = "/userdata"

	// InternalMetadataURI is the path to the internal (authenticated) endpoint
	// used for updating & retrieving metadata for any instance
	InternalMetadataURI = "/device-metadata"

	// InternalUserdataURI is the path to the internal (authenticated) endpoint
	// used for updating & retrieving metadata for any instance
	InternalUserdataURI = "/device-userdata"

	// InternalMetadataWithIDURI is the path to the internal (authenticated)
	// endpoint used for retrieving the stored metadata for an instance
	InternalMetadataWithIDURI = "/device-metadata/:instance-id"

	// InternalUserdataWithIDURI is the path to the internal (authenticated)
	// endpoint used for retrieving the stored metadata for an instance
	InternalUserdataWithIDURI = "/device-userdata/:instance-id"

	// InternalMetadataRefreshWithURI is the path to the internal (authenticated)
	// endpoint used for forcing a refresh of the metadata for an instance
	InternalMetadataRefreshWithURI = "/device-metadata/refresh/:instance-id"

	// InternalUserdataRefreshWithURI is the path to the internal (authenticated)
	// endpoint used for forcing a refresh of the userdata for an instance
	InternalUserdataRefreshWithURI = "/device-userdata/refresh/:instance-id"

	scopePrefix = "metadata"
)

var (
	validate *validator.Validate

	// errNotFound wraps the two sorts of "not found" errors we might encounter
	// - the item wasn't found in the DB
	// - the item wasn't found in the upstream lookup service
	errNotFound = errors.New("not found")

	// ErrUUIDNotFound is returned when an expected uuid is not provided.
	ErrUUIDNotFound = errors.New("uuid not found")

	// ErrInvalidUUID is returned when an invalid uuid is provided.
	ErrInvalidUUID = errors.New("invalid uuid")
)

// Router provides a router for the v1 API
type Router struct {
	AuthMW         *ginjwt.Middleware
	DB             *sqlx.DB
	Logger         *zap.Logger
	LookupEnabled  bool
	LookupClient   lookup.Client
	TemplateFields map[string]template.Template
}

// Routes will add the routes for this API version to a router group
func (r *Router) Routes(rg *gin.RouterGroup) {
	setupValidator()

	// Unauthenticated endpoints that users can use to fetch metadata and userdata.
	// These lookups are done based on the originating IP of the request.
	rg.GET(MetadataURI, middleware.IdentifyInstanceByIP(r.Logger, r.DB), r.instanceMetadataGet)
	rg.GET(UserdataURI, middleware.IdentifyInstanceByIP(r.Logger, r.DB), r.instanceUserdataGet)

	// Authenticated endpoints
	authMw := r.AuthMW

	// Used to write metadata or userdata for an instance
	rg.POST(InternalMetadataURI, authMw.AuthRequired(), authMw.RequiredScopes(upsertScopes("metadata")), r.instanceMetadataSet)
	rg.POST(InternalUserdataURI, authMw.AuthRequired(), authMw.RequiredScopes(upsertScopes("userdata")), r.instanceUserdataSet)

	// Check whether metadata or userdata exists for an instance, without triggering
	// a refresh if not found in the DB
	rg.HEAD(InternalMetadataWithIDURI, authMw.AuthRequired(), authMw.RequiredScopes(readScopes("metadata")), r.instanceMetadataExistsInternal)
	rg.HEAD(InternalUserdataWithIDURI, authMw.AuthRequired(), authMw.RequiredScopes(readScopes("userdata")), r.instanceUserdataExistsInternal)

	// Force a refresh of metadata or userdata for an instance by looking it up via the lookup client
	rg.POST(InternalMetadataRefreshWithURI, authMw.AuthRequired(), authMw.RequiredScopes(upsertScopes("metadata")), r.instanceMetadataRefreshInternal)
	rg.POST(InternalUserdataRefreshWithURI, authMw.AuthRequired(), authMw.RequiredScopes(upsertScopes("userdata")), r.instanceUserdataRefreshInternal)

	// Retrieve metadata or userdata for an instance by looking it up with the
	// instance ID (instead of the originating IP)
	rg.GET(InternalMetadataWithIDURI, authMw.AuthRequired(), authMw.RequiredScopes(readScopes("metadata")), r.instanceMetadataGetInternal)
	rg.GET(InternalUserdataWithIDURI, authMw.AuthRequired(), authMw.RequiredScopes(readScopes("userdata")), r.instanceUserdataGetInternal)

	// Delete metadata or userdata for an instance
	rg.DELETE(InternalMetadataWithIDURI, authMw.AuthRequired(), authMw.RequiredScopes(deleteScopes("metadata")), r.instanceMetadataDelete)
	rg.DELETE(InternalUserdataWithIDURI, authMw.AuthRequired(), authMw.RequiredScopes(deleteScopes("userdata")), r.instanceUserdataDelete)
}

func (r *Router) getMetadata(c *gin.Context) (*models.InstanceMetadatum, error) {
	instanceID := c.GetString(middleware.ContextKeyInstanceID)

	if instanceID == "" {
		// We couldn't match the request IP to an instance ID that the metadata
		// service already knows about. So we'll try to get it from the upstream
		// lookup service (if it's enabled and configured).
		middleware.MetricMetadataCacheMiss.Inc()
		requestIP := c.GetString(middleware.ContextKeyRequestorIP)

		if r.LookupEnabled && r.LookupClient != nil {
			metadata, err := lookup.MetadataSyncByIP(c.Request.Context(), r.DB, r.Logger, r.LookupClient, requestIP)
			if err != nil && errors.Is(err, lookup.ErrNotFound) {
				return nil, errNotFound
			}

			return metadata, err
		}

		return nil, errNotFound
	}

	// We got an instance ID from the middleware, either because we could match
	// the request IP to an ID, or the request itself provided the instance ID.
	metadata, err := models.FindInstanceMetadatum(c.Request.Context(), r.DB, instanceID)

	if err != nil && errors.Is(err, sql.ErrNoRows) {
		// We couldn't find an instance_metadata row for this instance ID. Try
		// to fetch it from the upstream lookup service (if enabled and configured)
		middleware.MetricMetadataCacheMiss.Inc()

		if r.LookupEnabled && r.LookupClient != nil {
			metadata, err = lookup.MetadataSyncByID(c.Request.Context(), r.DB, r.Logger, r.LookupClient, instanceID)
			if err != nil && errors.Is(err, lookup.ErrNotFound) {
				return nil, errNotFound
			}

			return metadata, err
		}

		return nil, errNotFound
	}

	middleware.MetricMetadataCacheHit.Inc()

	return metadata, err
}

func (r *Router) getUserdata(c *gin.Context) (*models.InstanceUserdatum, error) {
	instanceID := c.GetString(middleware.ContextKeyInstanceID)

	if instanceID == "" {
		// We couldn't match the request IP to an instance ID that the metadata
		// service already knows about. So we'll try to get it from the upstream
		// lookup service (if it's enabled and configured).
		middleware.MetricUserdataCacheMiss.Inc()
		requestIP := c.GetString(middleware.ContextKeyRequestorIP)

		if r.LookupEnabled && r.LookupClient != nil {
			userdata, err := lookup.UserdataSyncByIP(c.Request.Context(), r.DB, r.Logger, r.LookupClient, requestIP)
			if err != nil && errors.Is(err, lookup.ErrNotFound) {
				return nil, errNotFound
			}

			return userdata, err
		}

		return nil, errNotFound
	}

	// We got an instance ID from the middleware, either because we could match
	// the request IP to an ID, or the request itself provided the instance ID.
	userdata, err := models.FindInstanceUserdatum(c.Request.Context(), r.DB, instanceID)

	if err != nil && errors.Is(err, sql.ErrNoRows) {
		// We couldn't find an instance_metadata row for this instance ID. Try
		// to fetch it from the upstream lookup service (if enabled and configured)
		if r.LookupEnabled && r.LookupClient != nil {
			userdata, err = lookup.UserdataSyncByID(c.Request.Context(), r.DB, r.Logger, r.LookupClient, instanceID)
			if err != nil && errors.Is(err, lookup.ErrNotFound) {
				return nil, errNotFound
			}

			return userdata, err
		}

		return nil, errNotFound
	}

	middleware.MetricUserdataCacheHit.Inc()

	return userdata, err
}

// GetMetadataPath returns the path used by an instance to fetch Metadata
func GetMetadataPath() string {
	return path.Join(V1URI, MetadataURI)
}

// GetUserdataPath returns the path used by an instance to fetch Userdata
func GetUserdataPath() string {
	return path.Join(V1URI, UserdataURI)
}

// GetInternalMetadataPath returns the path used by an internal, authenticated
// system or used to update or retrieve metadata.
func GetInternalMetadataPath() string {
	return path.Join(V1URI, InternalMetadataURI)
}

// GetInternalMetadataByIDPath returns the path used by an internal,
// authenticated system or user to retrieve the metadata for a specific
// instance.
func GetInternalMetadataByIDPath(id string) string {
	return path.Join(V1URI, InternalMetadataURI, id)
}

// GetInternalUserdataPath returns the patch used by an internal, authenticated
// system or used to update or retrieve userdata.
func GetInternalUserdataPath() string {
	return path.Join(V1URI, InternalUserdataURI)
}

// GetInternalUserdataByIDPath returns the path used by an internal,
// authenticated system or user to retrieve the metadata for a specific
// instance.
func GetInternalUserdataByIDPath(id string) string {
	return path.Join(V1URI, InternalUserdataURI, id)
}

func upsertScopes(items ...string) []string {
	s := []string{"write", "create", "update"}
	for _, i := range items {
		s = append(s, fmt.Sprintf("%s:create:%s", scopePrefix, i))
	}

	for _, i := range items {
		s = append(s, fmt.Sprintf("%s:update:%s", scopePrefix, i))
	}

	return s
}

func readScopes(items ...string) []string {
	s := []string{"read"}
	for _, i := range items {
		s = append(s, fmt.Sprintf("%s:read:%s", scopePrefix, i))
	}

	return s
}

func deleteScopes(items ...string) []string {
	s := []string{"write", "delete"}
	for _, i := range items {
		s = append(s, fmt.Sprintf("%s:delete:%s", scopePrefix, i))
	}

	return s
}

func setupValidator() {
	validate = validator.New()

	splitSliceNum := 2

	// Set up a function to grab the json tag from a struct (if set)
	validate.RegisterTagNameFunc(func(fld reflect.StructField) string {
		name := strings.SplitN(fld.Tag.Get("json"), ",", splitSliceNum)[0]
		if name == "-" {
			return ""
		}

		return name
	})
}

// getUUIDParam parses and validates a UUID from the request params if the param is found
func getUUIDParam(c *gin.Context, name string) (string, error) {
	id, ok := c.Params.Get(name)

	if !ok || id == "" {
		return "", ErrUUIDNotFound
	}

	if _, err := uuid.Parse(id); err != nil {
		return "", ErrInvalidUUID
	}

	return id, nil
}
