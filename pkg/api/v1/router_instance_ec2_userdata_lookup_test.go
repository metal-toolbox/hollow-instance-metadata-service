package metadataservice_test

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"

	"go.hollow.sh/metadataservice/internal/lookup"
	v1api "go.hollow.sh/metadataservice/pkg/api/v1"
)

func TestGetEc2UserdataLookupByIP(t *testing.T) {
	lookupClient := newMockLookupClient()
	serverConfig := TestServerConfig{LookupEnabled: true, LookupClient: lookupClient}
	router := *testHTTPServerWithConfig(t, serverConfig)

	viper.SetDefault("crdb.max_retries", 5)
	viper.SetDefault("crdb.retry_interval", 1*time.Second)
	viper.SetDefault("crdb.tx_timeout", 15*time.Second)

	type testCase struct {
		testName       string
		instanceIP     string
		expectedStatus int
		lookupResponse lookupResponse
	}

	validResponse := lookup.UserdataLookupResponse{
		ID:          "81dc6612-c854-440e-87cb-ead5684c9559",
		IPAddresses: []string{"3.4.5.6"},
		Userdata:    []byte("some userdata..."),
	}

	testCases := []testCase{
		{
			"IPv4 address not found in lookup service",
			"1.2.3.4",
			http.StatusNotFound,
			lookupResponse{
				Error: lookup.ErrNotFound,
			},
		},
		{
			"lookup service unavailable",
			"2.3.4.5",
			http.StatusInternalServerError,
			lookupResponse{
				Error: lookup.ErrUnexpectedStatus,
			},
		},
		{
			"lookup service found instance",
			"3.4.5.6",
			http.StatusOK,
			lookupResponse{
				userdataResponse: validResponse,
			},
		},
	}

	for _, testcase := range testCases {
		t.Run(testcase.testName, func(t *testing.T) {
			lookupClient.setResponse(testcase.instanceIP, testcase.lookupResponse)

			w := httptest.NewRecorder()

			req, _ := http.NewRequestWithContext(context.TODO(), http.MethodGet, v1api.GetEc2UserdataPath(), nil)
			req.RemoteAddr = net.JoinHostPort(testcase.instanceIP, "")
			router.ServeHTTP(w, req)

			assert.Equal(t, testcase.expectedStatus, w.Code)
		})
	}
}
