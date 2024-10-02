package metadataservice_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"regexp"
	"testing"
	"text/template"
	"time"

	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"

	"go.hollow.sh/metadataservice/internal/dbtools"
	"go.hollow.sh/metadataservice/internal/models"
	v1api "go.hollow.sh/metadataservice/pkg/api/v1"
)

func TestGetMetadataByIP(t *testing.T) {
	router := *testHTTPServer(t)

	type testCase struct {
		testName       string
		instanceIP     string
		expectedStatus int
		expectedBody   string
	}

	testCases := []testCase{
		{
			"unknown IPv4 address",
			"1.2.3.4",
			http.StatusNotFound,
			"",
		},
		{
			"unknown IPv6 address",
			"fe80::aede:48ff:fe00:1122",
			http.StatusNotFound,
			"",
		},
	}

	// Instance A tests
	for _, hostIP := range dbtools.FixtureInstanceA.HostIPs {
		caseItem := testCase{
			fmt.Sprintf("Instance A IP %s", hostIP),
			hostIP,
			http.StatusOK,
			dbtools.FixtureInstanceA.InstanceMetadata.Metadata.String(),
		}

		testCases = append(testCases, caseItem)
	}

	// Instance B tests
	for _, hostIP := range dbtools.FixtureInstanceB.HostIPs {
		caseItem := testCase{
			fmt.Sprintf("Instance B IP %s", hostIP),
			hostIP,
			http.StatusOK,
			dbtools.FixtureInstanceB.InstanceMetadata.Metadata.String(),
		}

		testCases = append(testCases, caseItem)
	}

	// Instance E tests
	// Instance E does not have any metadata, so *for now* we should expect it to return 404.
	// Once we've implemented the call-out-to-external-service bits, we'll need to update this test.
	for _, hostIP := range dbtools.FixtureInstanceE.HostIPs {
		caseItem := testCase{
			fmt.Sprintf("Instance E IP %s", hostIP),
			hostIP,
			http.StatusNotFound,
			"",
		}

		testCases = append(testCases, caseItem)
	}

	for _, testcase := range testCases {
		t.Run(testcase.testName, func(t *testing.T) {
			w := httptest.NewRecorder()

			req, _ := http.NewRequestWithContext(context.TODO(), http.MethodGet, v1api.GetMetadataPath(), nil)
			req.RemoteAddr = net.JoinHostPort(testcase.instanceIP, "0")
			router.ServeHTTP(w, req)

			assert.Equal(t, testcase.expectedStatus, w.Code)

			if testcase.expectedStatus == http.StatusOK {
				var (
					err         error
					expectedMap map[string]interface{}
					resultMap   map[string]interface{}
				)

				err = json.Unmarshal([]byte(testcase.expectedBody), &expectedMap)
				if err != nil {
					t.Fatal(err)
				}

				err = json.Unmarshal(w.Body.Bytes(), &resultMap)
				if err != nil {
					t.Fatal(err)
				}

				assert.Equal(t, expectedMap, resultMap)
			}
		})
	}
}

func TestGetMetadataByIPWithTemplateFields(t *testing.T) {
	apiURLTmpl, err := template.New("apiURL").Parse("https://metadata-service")
	if err != nil {
		t.Error(err)
	}

	phoneHomeTmpl, err := template.New("phoneHomeURL").Parse("https://{{.facility}}.phone.home/phone-home")
	if err != nil {
		t.Error(err)
	}

	userStateTmpl, err := template.New("userStateURL").Parse("https://{{.metro}}.user.state/events/{{.id}}")
	if err != nil {
		t.Error(err)
	}

	missingFieldTmpl, err := template.New("missingField").Parse("{{if .missingField}} oh look it's {{.missingField}}{{end}}")
	if err != nil {
		t.Error(err)
	}

	// Test that the template system doesn't replace a field that's already present in the metadata response
	existingFieldTmpl, err := template.New("hostname").Parse("HEY I'M A TEMPLATE FOR {{.hostname}}")
	if err != nil {
		t.Error(err)
	}

	// Test that a field can just be static text
	staticTextTmpl, err := template.New("staticText").Parse("just some static text")
	if err != nil {
		t.Error(err)
	}

	config := TestServerConfig{
		TemplateFields: map[string]template.Template{
			"api_url":        *apiURLTmpl,
			"phone_home_url": *phoneHomeTmpl,
			"user_state_url": *userStateTmpl,
			"missing_field":  *missingFieldTmpl,
			"hostname":       *existingFieldTmpl,
			"static_text":    *staticTextTmpl,
		},
	}

	router := *testHTTPServerWithConfig(t, config)

	// Get the metadata for instance A, and check that the template fields were returned as expected
	w := httptest.NewRecorder()

	req, _ := http.NewRequestWithContext(context.TODO(), http.MethodGet, v1api.GetMetadataPath(), nil)
	req.RemoteAddr = net.JoinHostPort(dbtools.FixtureInstanceA.HostIPs[0], "0")
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var resultMap map[string]interface{}

	err = json.Unmarshal(w.Body.Bytes(), &resultMap)
	if err != nil {
		t.Fatal(err)
	}

	assert.Equal(t, "https://metadata-service", resultMap["api_url"])
	assert.Equal(t, "https://da11.phone.home/phone-home", resultMap["phone_home_url"])
	assert.Equal(t, fmt.Sprintf("https://da.user.state/events/%s", dbtools.FixtureInstanceA.InstanceID), resultMap["user_state_url"])
	assert.Equal(t, "instance-a", resultMap["hostname"])
	assert.Nil(t, resultMap["missingField"])
	assert.Equal(t, "just some static text", resultMap["static_text"])
}

func TestGetMetadataByIPWithErrorTemplate(t *testing.T) {
	// Test that if an error occurs attempting to produce output for a template
	// field, we just return the original metadata.
	missingFieldTmpl, err := template.New("missingField").Option("missingkey=error").Parse("oh look it's {{.missingField}}")
	if err != nil {
		t.Error(err)
	}

	config := TestServerConfig{
		TemplateFields: map[string]template.Template{
			"missing_field": *missingFieldTmpl,
		},
	}

	router := *testHTTPServerWithConfig(t, config)

	// Get the metadata for instance A, and check that the missing template field is not returned due to error
	w := httptest.NewRecorder()

	req, _ := http.NewRequestWithContext(context.TODO(), http.MethodGet, v1api.GetMetadataPath(), nil)
	req.RemoteAddr = net.JoinHostPort(dbtools.FixtureInstanceA.HostIPs[0], "0")
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var resultMap map[string]interface{}

	err = json.Unmarshal(w.Body.Bytes(), &resultMap)
	if err != nil {
		t.Fatal(err)
	}

	v, ok := resultMap["missingField"]
	assert.False(t, ok)
	assert.Nil(t, v)
}

// TestSetMetadataRequestValidations tests the different validations performed
// on the request body
func TestSetMetadataRequestValidations(t *testing.T) {
	router := *testHTTPServer(t)

	type testCase struct {
		testName       string
		requestBody    *v1api.UpsertMetadataRequest
		expectedStatus int
		expectedBody   *regexp.Regexp
	}

	testCases := []testCase{
		{
			"empty instance ID",
			&v1api.UpsertMetadataRequest{
				ID:          "",
				Metadata:    `{"some": "json"}`,
				IPAddresses: []string{"1.2.3.4", "10.1.0.0/25", "fe80:aede:48ff:fe00::1122"},
			},
			http.StatusBadRequest,
			regexp.MustCompile(`.*id.*required`),
		},
		{
			"non-uuid instance ID",
			&v1api.UpsertMetadataRequest{
				ID:          "abc123",
				Metadata:    `{"some": "json"}`,
				IPAddresses: []string{"1.2.3.4", "10.1.0.0/25", "fe80:aede:48ff:fe00::1122"},
			},
			http.StatusBadRequest,
			regexp.MustCompile(`.*id.*uuid`),
		},
		{
			"empty instance ID and empty metadata",
			&v1api.UpsertMetadataRequest{
				ID:          "",
				Metadata:    "",
				IPAddresses: []string{"1.2.3.4", "10.1.0.0/25", "fe80:aede:48ff:fe00::1122"},
			},
			http.StatusBadRequest,
			regexp.MustCompile(`.*id.*required.*metadata.*required`),
		},
		{
			"invalid IPv4 address",
			&v1api.UpsertMetadataRequest{
				ID:          "b9b24320-304e-4bfb-b46a-db75901c2f46",
				Metadata:    `{"some": "json"}`,
				IPAddresses: []string{"a.b.c.d"},
			},
			http.StatusBadRequest,
			regexp.MustCompile(`.*ipAddresses\[0\].*ip_addr|cidr`),
		},
		{
			"invalid IPv6 address",
			&v1api.UpsertMetadataRequest{
				ID:          "02d91622-b1e8-41b4-9add-ce77ac619b89",
				Metadata:    `{"some": "json"}`,
				IPAddresses: []string{"a:b:c:d:e:f:g:h"},
			},
			http.StatusBadRequest,
			regexp.MustCompile(`.*ipAddresses\[0\].*ip_addr|cidr`),
		},
	}

	for _, testcase := range testCases {
		t.Run(testcase.testName, func(t *testing.T) {
			reqBody, err := json.Marshal(testcase.requestBody)
			if err != nil {
				t.Fatal(err)
			}

			w := httptest.NewRecorder()

			req, _ := http.NewRequestWithContext(context.TODO(), http.MethodPost, v1api.GetInternalMetadataPath(), bytes.NewReader(reqBody))
			router.ServeHTTP(w, req)

			assert.Equal(t, testcase.expectedStatus, w.Code)
			assert.Regexp(t, testcase.expectedBody, w.Body.String())
		})
	}
}

// TestSetMetadataIPAddressConflict tests the actions performed when the
// incoming request specifies an IP address (or multiple IP addresses) that are
// currently associated to another instance.
func TestSetMetadataIPAddressConflict(t *testing.T) {
	router := *testHTTPServer(t)
	testDB := dbtools.TestDB()

	viper.SetDefault("crdb.max_retries", 5)
	viper.SetDefault("crdb.retry_interval", 1*time.Second)
	viper.SetDefault("crdb.tx_timeout", 15*time.Second)

	type testCase struct {
		testName                string
		conflictInstanceIDToIPs map[string][]string
		requestBody             *v1api.UpsertMetadataRequest
	}

	testCases := []testCase{
		{
			"single IPv4 conflict",
			map[string][]string{dbtools.FixtureInstanceA.InstanceID: {dbtools.FixtureInstanceA.HostIPs[0]}},
			&v1api.UpsertMetadataRequest{
				ID:          "59e1fac8-adc5-4955-9cc3-2fa3e5f5370e",
				Metadata:    `{"some": "json"}`,
				IPAddresses: []string{dbtools.FixtureInstanceA.HostIPs[0]},
			},
		},
		{
			"single IPv6 conflict",
			map[string][]string{dbtools.FixtureInstanceA.InstanceID: {dbtools.FixtureInstanceA.HostIPs[1]}},
			&v1api.UpsertMetadataRequest{
				ID:          "b5b851a7-ea59-498d-b5c2-9ba10201ac28",
				Metadata:    `{"some": "json"}`,
				IPAddresses: []string{dbtools.FixtureInstanceA.HostIPs[1]},
			},
		},
		{
			"ipv4 and ipv6 conflict from same 'old' instance",
			map[string][]string{dbtools.FixtureInstanceA.InstanceID: {dbtools.FixtureInstanceA.HostIPs[0], dbtools.FixtureInstanceA.HostIPs[1]}},
			&v1api.UpsertMetadataRequest{
				ID:          "12256023-e708-4620-b6f0-57d39541994a",
				Metadata:    `{"some": "json"}`,
				IPAddresses: []string{dbtools.FixtureInstanceA.HostIPs[0], dbtools.FixtureInstanceA.HostIPs[1]},
			},
		},
		{
			"ipv4 conflicts from two different 'old' instances",
			map[string][]string{
				dbtools.FixtureInstanceA.InstanceID: {dbtools.FixtureInstanceA.HostIPs[0]},
				dbtools.FixtureInstanceB.InstanceID: {dbtools.FixtureInstanceB.HostIPs[0]}},
			&v1api.UpsertMetadataRequest{
				ID:          "6bd001dd-0523-4002-93e9-36a98607638a",
				Metadata:    `{"some": "json"}`,
				IPAddresses: []string{dbtools.FixtureInstanceA.HostIPs[0], dbtools.FixtureInstanceB.HostIPs[0]},
			},
		},
		{
			"ipv6 conflicts from two different 'old' instances",
			map[string][]string{
				dbtools.FixtureInstanceA.InstanceID: {dbtools.FixtureInstanceA.HostIPs[1]},
				dbtools.FixtureInstanceB.InstanceID: {dbtools.FixtureInstanceB.HostIPs[1]},
			},
			&v1api.UpsertMetadataRequest{
				ID:          "8c18b684-efb4-476b-87c3-a1dfd70a2024",
				Metadata:    `{"some": "json"}`,
				IPAddresses: []string{dbtools.FixtureInstanceA.HostIPs[1], dbtools.FixtureInstanceB.HostIPs[1]},
			},
		},
		{
			"ipv4 and ipv6 conflicts from two different 'old' instances",
			map[string][]string{
				dbtools.FixtureInstanceA.InstanceID: {dbtools.FixtureInstanceA.HostIPs[0]},
				dbtools.FixtureInstanceB.InstanceID: {dbtools.FixtureInstanceB.HostIPs[1]},
			},
			&v1api.UpsertMetadataRequest{
				ID:          "f92d1d4a-a408-42d7-b541-3bc3296c9c7d",
				Metadata:    `{"some": "json"}`,
				IPAddresses: []string{dbtools.FixtureInstanceA.HostIPs[0], dbtools.FixtureInstanceB.HostIPs[1]},
			},
		},
	}

	for _, testcase := range testCases {
		t.Run(testcase.testName, func(t *testing.T) {
			reqBody, err := json.Marshal(testcase.requestBody)
			if err != nil {
				t.Fatal(err)
			}

			w := httptest.NewRecorder()

			req, _ := http.NewRequestWithContext(context.TODO(), http.MethodPost, v1api.GetInternalMetadataPath(), bytes.NewReader(reqBody))
			router.ServeHTTP(w, req)

			assert.Equal(t, http.StatusOK, w.Code)

			// Check that the conflicting InstanceIPAddress row has been deleted
			for id, conflictIPs := range testcase.conflictInstanceIDToIPs {
				for _, conflictIP := range conflictIPs {
					count, err := models.InstanceIPAddresses(models.InstanceIPAddressWhere.InstanceID.EQ(id), models.InstanceIPAddressWhere.Address.EQ(conflictIP)).Count(context.TODO(), testDB)
					if err != nil {
						t.Fatal(err)
					}

					assert.Equal(t, int64(0), count)
				}
			}
		})
	}
}

// TestSetMetadataCreateMetadata tests the actions we perform when we receive a
// request that should insert the metadata for an instance ID we haven't seen
// before.
func TestSetMetadataCreateMetadata(t *testing.T) {
	router := *testHTTPServer(t)
	testDB := dbtools.TestDB()

	viper.SetDefault("crdb.max_retries", 5)
	viper.SetDefault("crdb.retry_interval", 1*time.Second)
	viper.SetDefault("crdb.tx_timeout", 15*time.Second)

	requestBody := &v1api.UpsertMetadataRequest{
		ID:          "b94fa75b-1fee-45eb-9925-83011c4834b9",
		Metadata:    `{"some": "json for instance 'b94fa75b-1fee-45eb-9925-83011c4834b9'"}`,
		IPAddresses: []string{"192.168.0.1/25"},
	}

	// Assert that we don't have an existing record for InstanceID
	exists, err := models.InstanceIPAddressExists(context.TODO(), testDB, requestBody.ID)
	if err != nil {
		t.Fatal(err)
	}

	assert.False(t, exists)

	reqBody, err := json.Marshal(requestBody)
	if err != nil {
		t.Fatal(err)
	}

	w := httptest.NewRecorder()

	req, _ := http.NewRequestWithContext(context.TODO(), http.MethodPost, v1api.GetInternalMetadataPath(), bytes.NewReader(reqBody))
	router.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)

	instanceMetadata, _ := models.InstanceMetadata(models.InstanceMetadatumWhere.ID.EQ(requestBody.ID)).One(context.TODO(), testDB)
	assert.NotNil(t, instanceMetadata)
	assert.Equal(t, requestBody.ID, instanceMetadata.ID)
	assert.Equal(t, requestBody.Metadata, instanceMetadata.Metadata.String())

	instanceIPAddresses, _ := models.InstanceIPAddresses(models.InstanceIPAddressWhere.ID.EQ(requestBody.ID)).All(context.TODO(), testDB)
	for _, instanceIPAddress := range instanceIPAddresses {
		assert.Equal(t, requestBody.ID, instanceIPAddress.InstanceID)

		found := false

		for _, ipAddress := range requestBody.IPAddresses {
			if ipAddress == instanceIPAddress.Address {
				found = true
			}
		}

		assert.True(t, found)
	}
}

// TestSetMetadataUpsertMetadata tests the actions we perform when we receive a
// request that should update the metadata for an existing instance record.
func TestSetMetadataUpsertMetadata(t *testing.T) {
	router := *testHTTPServer(t)
	testDB := dbtools.TestDB()

	// Assert that we have an existing record for InstanceID
	exists, err := models.InstanceMetadatumExists(context.TODO(), testDB, dbtools.FixtureInstanceA.InstanceID)
	if err != nil {
		t.Fatal(err)
	}

	assert.True(t, exists)

	requestBody := &v1api.UpsertMetadataRequest{
		ID:          dbtools.FixtureInstanceA.InstanceID,
		Metadata:    `{"some": "json"}`,
		IPAddresses: dbtools.FixtureInstanceA.HostIPs,
	}

	reqBody, err := json.Marshal(requestBody)
	if err != nil {
		t.Fatal(err)
	}

	w := httptest.NewRecorder()

	req, _ := http.NewRequestWithContext(context.TODO(), http.MethodPost, v1api.GetInternalMetadataPath(), bytes.NewReader(reqBody))
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	instanceMetadata, _ := models.InstanceMetadata(models.InstanceMetadatumWhere.ID.EQ(dbtools.FixtureInstanceA.InstanceID)).One(context.TODO(), testDB)
	assert.Equal(t, requestBody.Metadata, instanceMetadata.Metadata.String())
}

func TestDeleteMetadata(t *testing.T) {
	router := *testHTTPServer(t)
	testDB := dbtools.TestDB()

	type testCase struct {
		testName       string
		instanceID     string
		expectedStatus int
		// anyIPs is used to test to see if there are any instance_ip_addresses
		// rows remaining after the call
		anyIPs bool
	}

	testCases := []testCase{
		{
			"unknown ID",
			"99c53a90-61c8-472d-95dc-9abeaeb646c9",
			http.StatusNotFound,
			false,
		},
		{
			"blank ID",
			"",
			http.StatusNotFound,
			false,
		},
		// Instance A has both metadata and userdata, so instance_ip_addresses
		// should remain
		{
			"Instance A",
			dbtools.FixtureInstanceA.InstanceID,
			http.StatusOK,
			true,
		},
		// Instance B has metadata but no userdata, so instance_ip_addresses
		// should be deleted
		{
			"Instance B",
			dbtools.FixtureInstanceB.InstanceID,
			http.StatusOK,
			false,
		},
		// Instance C has metadata and userdata, but no associated IPs, so there
		// should not be any instance_ip_addresses rows found.
		{
			"Instance C",
			dbtools.FixtureInstanceC.InstanceID,
			http.StatusOK,
			false,
		},
		// Instance D has metadata and no userdata, and no associated IPs, so there
		// should not be any instance_ip_addresses rows found.
		{
			"Instance D",
			dbtools.FixtureInstanceD.InstanceID,
			http.StatusOK,
			false,
		},
		// Instance E does not have metadata, so we'd expect a 404
		{
			"Instance E",
			dbtools.FixtureInstanceE.InstanceID,
			http.StatusNotFound,
			true,
		},
		// Instance F does not have metadata, so we'd expect a 404
		{
			"Instance F",
			dbtools.FixtureInstanceF.InstanceID,
			http.StatusNotFound,
			false,
		},
	}

	for _, testcase := range testCases {
		t.Run(testcase.testName, func(t *testing.T) {
			w := httptest.NewRecorder()

			req, _ := http.NewRequestWithContext(context.TODO(), http.MethodDelete, v1api.GetInternalMetadataByIDPath(testcase.instanceID), nil)
			router.ServeHTTP(w, req)

			assert.Equal(t, testcase.expectedStatus, w.Code)

			if testcase.expectedStatus == http.StatusOK {
				count, err := models.InstanceIPAddresses(models.InstanceIPAddressWhere.InstanceID.EQ(testcase.instanceID)).Count(context.TODO(), testDB)
				if err != nil {
					t.Fatal(err)
				}

				if testcase.anyIPs {
					assert.Greater(t, count, int64(0))
				} else {
					assert.Equal(t, int64(0), count)
				}
			}
		})
	}
}

// metadataString is a helper function that ensures the db fixture string is marshaled
// in a way that we can properly calculate its length for Content-Length comparisons
func metadataString(metadata interface{}) string {
	b, _ := json.Marshal(metadata)
	return string(b)
}

func TestGetMetadataInternal(t *testing.T) {
	router := *testHTTPServer(t)

	type testCase struct {
		testName       string
		instanceID     string
		expectedStatus int
		expectedBody   string
	}

	testCases := []testCase{
		{
			"unknown ID",
			"99c53a90-61c8-472d-95dc-9abeaeb646c9",
			http.StatusNotFound,
			"",
		},
		{
			"blank ID",
			"",
			http.StatusNotFound,
			"",
		},
		{
			"invalid ID",
			"bad-id",
			http.StatusNotFound,
			"",
		},
		{
			"Instance A",
			dbtools.FixtureInstanceA.InstanceID,
			http.StatusOK,
			metadataString(dbtools.FixtureInstanceA.InstanceMetadata.Metadata),
		},
		{
			"Instance B",
			dbtools.FixtureInstanceB.InstanceID,
			http.StatusOK,
			metadataString(dbtools.FixtureInstanceB.InstanceMetadata.Metadata),
		},
		{
			"Instance C",
			dbtools.FixtureInstanceC.InstanceID,
			http.StatusOK,
			metadataString(dbtools.FixtureInstanceC.InstanceMetadata.Metadata),
		},
		{
			"Instance D",
			dbtools.FixtureInstanceD.InstanceID,
			http.StatusOK,
			metadataString(dbtools.FixtureInstanceD.InstanceMetadata.Metadata),
		},
		// Instance E does not have metadata, so we'd expect a 404
		{
			"Instance E",
			dbtools.FixtureInstanceE.InstanceID,
			http.StatusNotFound,
			"",
		},
		// Instance F does not have metadata, so we'd expect a 404
		{
			"Instance F",
			dbtools.FixtureInstanceF.InstanceID,
			http.StatusNotFound,
			"",
		},
	}

	// HEAD request tests
	for _, testcase := range testCases {
		t.Run(testcase.testName, func(t *testing.T) {
			w := httptest.NewRecorder()

			req, _ := http.NewRequestWithContext(context.TODO(), http.MethodHead, v1api.GetInternalMetadataByIDPath(testcase.instanceID), nil)
			router.ServeHTTP(w, req)
			response := w.Result()

			assert.Equal(t, testcase.expectedStatus, w.Code)

			if w.Code == 200 {
				// HEAD responses should have an empty body, but set the Content-Length
				// header to what the response body would otherwise be for a GET request
				assert.Zero(t, w.Body.Len())
				assert.Equal(t, int64(len(testcase.expectedBody)), response.ContentLength)
				response.Body.Close()
			}
		})
	}

	// GET request tests
	for _, testcase := range testCases {
		t.Run(testcase.testName, func(t *testing.T) {
			w := httptest.NewRecorder()

			req, _ := http.NewRequestWithContext(context.TODO(), http.MethodGet, v1api.GetInternalMetadataByIDPath(testcase.instanceID), nil)
			router.ServeHTTP(w, req)

			assert.Equal(t, testcase.expectedStatus, w.Code)

			if testcase.expectedStatus == http.StatusOK {
				var (
					err         error
					expectedMap map[string]interface{}
					resultMap   map[string]interface{}
				)

				err = json.Unmarshal([]byte(testcase.expectedBody), &expectedMap)
				if err != nil {
					t.Fatal(err)
				}

				err = json.Unmarshal(w.Body.Bytes(), &resultMap)
				if err != nil {
					t.Fatal(err)
				}

				assert.Equal(t, expectedMap, resultMap)
			}
		})
	}
}
