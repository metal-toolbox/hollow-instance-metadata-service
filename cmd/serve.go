package cmd

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"text/template"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/jmoiron/sqlx"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	"go.hollow.sh/toolbox/ginjwt"
	"go.infratographer.com/x/crdbx"
	"go.infratographer.com/x/otelx"
	"go.uber.org/zap"
	"golang.org/x/oauth2/clientcredentials"

	"go.hollow.sh/metadataservice/internal/config"
	"go.hollow.sh/metadataservice/internal/httpsrv"
	"go.hollow.sh/metadataservice/internal/lookup"
)

const (
	serviceName = "metadata-service"

	dbMaxRetriesDefault       = 5
	dbRetryMaxIntervalDefault = 3 * time.Second
	dbTxTimoutDefault         = 15 * time.Second

	shutdownGracePeriod = 10 * time.Second
)

// serveCmd represents the serve command
var serveCmd = &cobra.Command{
	Use:   "serve",
	Short: "starts the metadata server",
	Run: func(cmd *cobra.Command, _ []string) {
		serve(cmd.Context())
	},
}

func init() {
	rootCmd.AddCommand(serveCmd)
	serveCmd.Flags().String("listen", "0.0.0.0:8000", "address on which to listen")
	viperBindFlag("listen", serveCmd.Flags().Lookup("listen"))

	// Otel flags
	otelx.MustViperFlags(viper.GetViper(), serveCmd.Flags())

	// DB flags
	crdbx.MustViperFlags(viper.GetViper(), serveCmd.Flags())

	serveCmd.Flags().Int("db-tx-max-retries", dbMaxRetriesDefault, "maximum number of times to retry failed db transactions")
	viperBindFlag("crdb.max_retries", serveCmd.Flags().Lookup("db-tx-max-retries"))

	serveCmd.Flags().Duration("db-retry-max-interval", dbRetryMaxIntervalDefault, "maximum number of seconds to sleep between db transaction retries (includes random jitter)")
	viperBindFlag("crdb.retry_interval", serveCmd.Flags().Lookup("db-retry-max-interval"))

	serveCmd.Flags().Duration("db-tx-timeout", dbTxTimoutDefault, "maximum number of seconds to allow db transactions to run for")
	viperBindFlag("crdb.tx_timeout", serveCmd.Flags().Lookup("db-tx-timeout"))

	// OIDC Flags
	serveCmd.Flags().Bool("oidc", true, "use oidc auth")
	viperBindFlag("oidc.enabled", serveCmd.Flags().Lookup("oidc"))

	serveCmd.Flags().String("oidc-aud", "", "expected audient on OIDC JWT")
	viperBindFlag("oidc.audience", serveCmd.Flags().Lookup("oidc-aud"))

	serveCmd.Flags().String("oidc-issuer", "", "expected issuer of OIDC JWT")
	viperBindFlag("oidc.issuer", serveCmd.Flags().Lookup("oidc-issuer"))

	serveCmd.Flags().String("oidc-jwksuri", "", "URI for JWKS listing for JWTs")
	viperBindFlag("oidc.jwksuri", serveCmd.Flags().Lookup("oidc-jwksuri"))

	serveCmd.Flags().String("oidc-roles-claim", "claim", "field containing the permissions of an OIDC JWT")
	viperBindFlag("oidc.claims.roles", serveCmd.Flags().Lookup("oidc-roles-claim"))

	serveCmd.Flags().String("oidc-username-claim", "", "additional fields to output in logs from the JWT token, ex (email)")
	viperBindFlag("oidc.claims.username", serveCmd.Flags().Lookup("oidc-username-claim"))

	// Lookup Service Flags
	serveCmd.Flags().Bool("lookup-enabled", false, "Use the lookup client to attempt to fetch metadata or userdata from an upstream source when it is not cached locally for the instance")
	viperBindFlag("lookup.enabled", serveCmd.Flags().Lookup("lookup-enabled"))

	serveCmd.Flags().String("lookup-service-url", "", "URL to the metadata lookup service (like 'https://metadata-lookup-service.tld/api/v1/') to use when fetching metadata or userdata from an upstream source")
	viperBindFlag("lookup.service.url", serveCmd.Flags().Lookup("lookup-service-url"))

	serveCmd.Flags().String("lookup-oidc-issuer", "", "OIDC JWT issuer to the lookup service")
	viperBindFlag("lookup.oidc.issuer", serveCmd.Flags().Lookup("lookup-oidc-issuer"))

	serveCmd.Flags().String("lookup-oidc-client-id", "", "OIDC Client ID to use by the lookup service client for auth token exchange")
	viperBindFlag("lookup.oidc.clientid", serveCmd.Flags().Lookup("lookup-oidc-client-id"))

	serveCmd.Flags().String("lookup-oidc-client-secret", "", "OIDC Client Secret to use by the lookup service client for auth token exchange")
	viperBindFlag("lookup.oidc.clientsecret", serveCmd.Flags().Lookup("lookup-oidc-client-secret"))

	serveCmd.Flags().String("lookup-oidc-aud", "", "OIDC JWT audience for lookup service")
	viperBindFlag("lookup.oidc.audience", serveCmd.Flags().Lookup("lookup-oidc-aud"))

	serveCmd.Flags().StringSlice("lookup-oidc-scopes", []string{"metadata:read:metadata", "metadata:read:userdata"}, "OIDC JWT scopes for lookup service")
	viperBindFlag("lookup.oidc.scopes", serveCmd.Flags().Lookup("lookup-oidc-scopes"))

	// Misc serve flags
	serveCmd.Flags().StringSlice("gin-trusted-proxies", []string{}, "Comma-separated list of IP addresses, like `\"192.168.1.1,10.0.0.1\"`. When running the Metadata Service behind something like a reverse proxy or load balancer, you may need to set this so that gin's `(*Context).ClientIP()` method returns a value provided by the proxy in a header like `X-Forwarded-For`.")
	viperBindFlag("gin.trustedproxies", serveCmd.Flags().Lookup("gin-trusted-proxies"))

	serveCmd.Flags().String("api-url", "", "An optional golang template string used to build a URL which instances can use as a reference to the Metadata Service API itself. This template string will be evaluated against the instance metadata, and appended as an 'api_url' field on the metadata document served to instances. If no template string is specified, the 'api_url' field will not be added to the metadata document.")
	viperBindFlag("metadata.api_url", serveCmd.Flags().Lookup("api-url"))

	serveCmd.Flags().String("phone-home-url", "", "An optional golang template string used to build a URL which instances can use as part of a 'phone home' process. This template string will be evaluated against the instance metadata, and appended as a 'phone_home_url' field on the metadata document served to instances. If no template string is specified, the 'phone_home_url' field will not be added to the metadata document.")
	viperBindFlag("metadata.phone_home_url", serveCmd.Flags().Lookup("phone-home-url"))

	serveCmd.Flags().String("user-state-url", "", "An optional golang template string used to build a URL which instances can use for sending user state events. This template string will be evaluated against the instance metadata, and appended as a 'user_state_url' field on the metadata document served to instances. If no template string is specified, the 'user_state_url' field will not be added to the metadata document.")
	viperBindFlag("metadata.user_state_url", serveCmd.Flags().Lookup("user-state-url"))

	serveCmd.Flags().Duration("shutdown-grace-period", shutdownGracePeriod, "The grace period for requests to finish before forcibly exiting.")
	viperBindFlag("shutdown_grace_period", serveCmd.Flags().Lookup("shutdown-grace-period"))
}

func serve(ctx context.Context) {
	setupTracing(logger)

	db := initDB()

	logger.Infow("starting metadata server", "address", viper.GetString("listen"))

	lookupClient, err := getLookupClient(ctx)
	if err != nil {
		logger.Fatalw("error getting lookup service client", "error", err)
	}

	hs := &httpsrv.Server{
		Logger: logger.Desugar(),
		Listen: viper.GetString("listen"),
		Debug:  viper.GetBool("logging.debug"),
		DB:     db,
		AuthConfig: ginjwt.AuthConfig{
			Enabled:       viper.GetBool("oidc.enabled"),
			Audience:      viper.GetString("oidc.audience"),
			Issuer:        viper.GetString("oidc.issuer"),
			JWKSURI:       viper.GetString("oidc.jwksuri"),
			LogFields:     viper.GetStringSlice("oidc.log"), // TODO: We don't seem to be grabbing this from config?
			RolesClaim:    viper.GetString("oidc.claims.roles"),
			UsernameClaim: viper.GetString("oidc.claims.username"),
		},
		TrustedProxies:  viper.GetStringSlice("gin.trustedproxies"),
		LookupEnabled:   viper.GetBool("lookup.enabled"),
		LookupClient:    lookupClient,
		TemplateFields:  getTemplateFields(),
		ShutdownTimeout: viper.GetDuration("shutdown_grace_period"),
	}

	if err := hs.Run(ctx); err != nil && !errors.Is(err, http.ErrServerClosed) {
		logger.Fatalw("failure running metadata server", "error", err)
	}
}

func setupTracing(logger *zap.SugaredLogger) {
	logger.Debug("Setting up otel tracing")

	err := otelx.InitTracer(config.AppConfig.Tracing, serviceName, logger)
	if err != nil {
		logger.Fatalw("failed to initialize otel tracer", "error", err)
	}
}

func initDB() *sqlx.DB {
	dbDriverName := "postgres"

	sqldb, err := crdbx.NewDB(config.AppConfig.CRDB, config.AppConfig.Tracing.Enabled)
	if err != nil {
		logger.Fatalw("failed to initialize database connection", "error", err)
	}

	db := sqlx.NewDb(sqldb, dbDriverName)

	return db
}

func getLookupClient(ctx context.Context) (*lookup.ServiceClient, error) {
	if viper.GetBool("lookup.enabled") {
		provider, err := oidc.NewProvider(ctx, viper.GetString("lookup.oidc.issuer"))
		if err != nil {
			return nil, err
		}

		oauthConfig := clientcredentials.Config{
			ClientID:       viper.GetString("lookup.oidc.clientid"),
			ClientSecret:   viper.GetString("lookup.oidc.clientsecret"),
			TokenURL:       provider.Endpoint().TokenURL,
			Scopes:         viper.GetStringSlice("lookup.oidc.scopes"),
			EndpointParams: url.Values{"audience": []string{viper.GetString("lookup.oidc.audience")}},
		}

		return lookup.NewClient(logger.Desugar(), viper.GetString("lookup.service.url"), oauthConfig.Client(ctx))
	}

	return nil, nil
}

func getTemplateFields() map[string]template.Template {
	templates := make(map[string]template.Template)

	apiURL := viper.GetString("metadata.api_url")
	phoneHomeURL := viper.GetString("metadata.phone_home_url")
	userStateURL := viper.GetString("metadata.user_state_url")

	if len(apiURL) > 0 {
		apiURLTempl, err := template.New("apiURL").Parse(apiURL)
		if err != nil {
			logger.Fatalf("failed to parse API URL template (%s)", apiURL, "error", err)
		}

		templates["api_url"] = *apiURLTempl
	}

	if len(phoneHomeURL) > 0 {
		phoneHomeTempl, err := template.New("phoneHomeURL").Parse(phoneHomeURL)
		if err != nil {
			logger.Fatalf("failed to parse phone home URL template (%s)", phoneHomeURL, "error", err)
		}

		templates["phone_home_url"] = *phoneHomeTempl
	}

	if len(userStateURL) > 0 {
		userStateTempl, err := template.New("userStateURL").Parse(userStateURL)
		if err != nil {
			logger.Fatalf("failed to parse user state URL template (%s)", userStateURL, "error", err)
		}

		templates["user_state_url"] = *userStateTempl
	}

	return templates
}
