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

	"github.com/stretchr/testify/assert"

	"go.hollow.sh/metadataservice/internal/dbtools"
	"go.hollow.sh/metadataservice/internal/models"
	v1api "go.hollow.sh/metadataservice/pkg/api/v1"
)

const (
	userdata1 string = `
#!/bin/bash
export DEBIAN_FRONTEND=noninteractive
apt-get update && apt-get upgrade -y
apt-get install nginx -y
`
	userdata2 string = `
#cloud-config
package_upgrade: true
packages:
		-  nginx
`
)

func TestGetUserDataByIP(t *testing.T) {
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
			string(dbtools.FixtureInstanceA.InstanceUserdata.Userdata.Bytes),
		}

		testCases = append(testCases, caseItem)
	}

	// Instance B tests
	// Instance B does not have any userdata, so *for now* we should expect it to return 404.
	// Once we've implemented the call-out-to-external-service bits, we'll need to update this test.
	for _, hostIP := range dbtools.FixtureInstanceB.HostIPs {
		caseItem := testCase{
			fmt.Sprintf("Instance B IP %s", hostIP),
			hostIP,
			http.StatusNotFound,
			"",
		}

		testCases = append(testCases, caseItem)
	}

	// Instance E tests
	for _, hostIP := range dbtools.FixtureInstanceE.HostIPs {
		caseItem := testCase{
			fmt.Sprintf("Instance E IP %s", hostIP),
			hostIP,
			http.StatusOK,
			string(dbtools.FixtureInstanceE.InstanceUserdata.Userdata.Bytes),
		}

		testCases = append(testCases, caseItem)
	}

	for _, testcase := range testCases {
		t.Run(testcase.testName, func(t *testing.T) {
			w := httptest.NewRecorder()

			req, _ := http.NewRequestWithContext(context.TODO(), http.MethodGet, v1api.GetUserdataPath(), nil)
			req.RemoteAddr = net.JoinHostPort(testcase.instanceIP, "0")
			router.ServeHTTP(w, req)

			assert.Equal(t, testcase.expectedStatus, w.Code)

			if testcase.expectedStatus == http.StatusOK {
				assert.Equal(t, testcase.expectedBody, w.Body.String())
			}
		})
	}
}

// TestSetUserdataRequestValidations tests the different validations performed
// on the request body
func TestSetUserdataRequestValidations(t *testing.T) {
	router := *testHTTPServer(t)

	type testCase struct {
		testName       string
		requestBody    *v1api.UpsertUserdataRequest
		expectedStatus int
		expectedBody   *regexp.Regexp
	}

	testCases := []testCase{
		{
			"empty instance ID",
			&v1api.UpsertUserdataRequest{
				ID:          "",
				Userdata:    []byte(userdata1),
				IPAddresses: []string{"1.2.3.4", "10.1.0.0/25", "fe80:aede:48ff:fe00::1122"},
			},
			http.StatusBadRequest,
			regexp.MustCompile(`.*id.*required`),
		},
		{
			"non-uuid instance ID",
			&v1api.UpsertUserdataRequest{
				ID:          "abc123",
				Userdata:    []byte(userdata2),
				IPAddresses: []string{"1.2.3.4", "10.1.0.0/25", "fe80:aede:48ff:fe00::1122"},
			},
			http.StatusBadRequest,
			regexp.MustCompile(`.*id.*uuid`),
		},
		{
			"empty instance ID and empty userdata",
			&v1api.UpsertUserdataRequest{
				ID:          "",
				Userdata:    []byte(""),
				IPAddresses: []string{"1.2.3.4", "10.1.0.0/25", "fe80:aede:48ff:fe00::1122"},
			},
			http.StatusBadRequest,
			regexp.MustCompile(`.*id.*required`),
		},
		{
			"invalid IPv4 address",
			&v1api.UpsertUserdataRequest{
				ID:          "b9b24320-304e-4bfb-b46a-db75901c2f46",
				Userdata:    []byte(userdata1),
				IPAddresses: []string{"a.b.c.d"},
			},
			http.StatusBadRequest,
			regexp.MustCompile(`.*ipAddresses\[0\].*ip_addr|cidr`),
		},
		{
			"invalid IPv6 address",
			&v1api.UpsertUserdataRequest{
				ID:          "02d91622-b1e8-41b4-9add-ce77ac619b89",
				Userdata:    []byte(userdata2),
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

			req, _ := http.NewRequestWithContext(context.TODO(), http.MethodPost, v1api.GetInternalUserdataPath(), bytes.NewReader(reqBody))
			router.ServeHTTP(w, req)

			assert.Equal(t, testcase.expectedStatus, w.Code)
			assert.Regexp(t, testcase.expectedBody, w.Body.String())
		})
	}
}

// TestSetUserdataIPAddressConflict tests the actions performed when the
// incoming request specifies an IP address (or multiple IP addresses) that are
// currently associated to another instance.
func TestSetUserdataIPAddressConflict(t *testing.T) {
	router := *testHTTPServer(t)
	testDB := dbtools.TestDB()

	type testCase struct {
		testName                string
		conflictInstanceIDToIPs map[string][]string
		requestBody             *v1api.UpsertUserdataRequest
	}

	testCases := []testCase{
		{
			"single IPv4 conflict",
			map[string][]string{dbtools.FixtureInstanceA.InstanceID: {dbtools.FixtureInstanceA.HostIPs[0]}},
			&v1api.UpsertUserdataRequest{
				ID:          "59e1fac8-adc5-4955-9cc3-2fa3e5f5370e",
				Userdata:    []byte(userdata1),
				IPAddresses: []string{dbtools.FixtureInstanceA.HostIPs[0]},
			},
		},
		{
			"single IPv6 conflict",
			map[string][]string{dbtools.FixtureInstanceA.InstanceID: {dbtools.FixtureInstanceA.HostIPs[1]}},
			&v1api.UpsertUserdataRequest{
				ID:          "b5b851a7-ea59-498d-b5c2-9ba10201ac28",
				Userdata:    []byte(userdata2),
				IPAddresses: []string{dbtools.FixtureInstanceA.HostIPs[1]},
			},
		},
		{
			"ipv4 and ipv6 conflict from same 'old' instance",
			map[string][]string{dbtools.FixtureInstanceA.InstanceID: {dbtools.FixtureInstanceA.HostIPs[0], dbtools.FixtureInstanceA.HostIPs[1]}},
			&v1api.UpsertUserdataRequest{
				ID:          "12256023-e708-4620-b6f0-57d39541994a",
				Userdata:    []byte(userdata1),
				IPAddresses: []string{dbtools.FixtureInstanceA.HostIPs[0], dbtools.FixtureInstanceA.HostIPs[1]},
			},
		},
		{
			"ipv4 conflicts from two different 'old' instances",
			map[string][]string{
				dbtools.FixtureInstanceA.InstanceID: {dbtools.FixtureInstanceA.HostIPs[0]},
				dbtools.FixtureInstanceB.InstanceID: {dbtools.FixtureInstanceB.HostIPs[0]}},
			&v1api.UpsertUserdataRequest{
				ID:          "6bd001dd-0523-4002-93e9-36a98607638a",
				Userdata:    []byte(userdata2),
				IPAddresses: []string{dbtools.FixtureInstanceA.HostIPs[0], dbtools.FixtureInstanceB.HostIPs[0]},
			},
		},
		{
			"ipv6 conflicts from two different 'old' instances",
			map[string][]string{
				dbtools.FixtureInstanceA.InstanceID: {dbtools.FixtureInstanceA.HostIPs[1]},
				dbtools.FixtureInstanceB.InstanceID: {dbtools.FixtureInstanceB.HostIPs[1]},
			},
			&v1api.UpsertUserdataRequest{
				ID:          "8c18b684-efb4-476b-87c3-a1dfd70a2024",
				Userdata:    []byte(userdata1),
				IPAddresses: []string{dbtools.FixtureInstanceA.HostIPs[1], dbtools.FixtureInstanceB.HostIPs[1]},
			},
		},
		{
			"ipv4 and ipv6 conflicts from two different 'old' instances",
			map[string][]string{
				dbtools.FixtureInstanceA.InstanceID: {dbtools.FixtureInstanceA.HostIPs[0]},
				dbtools.FixtureInstanceB.InstanceID: {dbtools.FixtureInstanceB.HostIPs[1]},
			},
			&v1api.UpsertUserdataRequest{
				ID:          "f92d1d4a-a408-42d7-b541-3bc3296c9c7d",
				Userdata:    []byte(userdata2),
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

			req, _ := http.NewRequestWithContext(context.TODO(), http.MethodPost, v1api.GetInternalUserdataPath(), bytes.NewReader(reqBody))
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

// TestSetUserdataCreateUserdata tests the actions we perform when we receive a
// request that should insert the userdata for an instance ID we haven't seen
// before.
func TestSetUserdataCreateUserdata(t *testing.T) {
	router := *testHTTPServer(t)
	testDB := dbtools.TestDB()

	requestBody := &v1api.UpsertUserdataRequest{
		ID:          "b94fa75b-1fee-45eb-9925-83011c4834b9",
		Userdata:    []byte(userdata1),
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

	req, _ := http.NewRequestWithContext(context.TODO(), http.MethodPost, v1api.GetInternalUserdataPath(), bytes.NewReader(reqBody))
	router.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)

	instanceUserdata, _ := models.InstanceUserdata(models.InstanceUserdatumWhere.ID.EQ(requestBody.ID)).One(context.TODO(), testDB)
	assert.NotNil(t, instanceUserdata)
	assert.Equal(t, requestBody.ID, instanceUserdata.ID)
	assert.Equal(t, requestBody.Userdata, instanceUserdata.Userdata.Bytes)

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

// TestSetUserdataUpsertUserdata tests the actions we perform when we receive a
// request that should update the userdata for an existing instance record.
func TestSetUserdataUpsertUserdata(t *testing.T) {
	router := *testHTTPServer(t)
	testDB := dbtools.TestDB()

	// Assert that we have an existing record for InstanceID
	exists, err := models.InstanceUserdatumExists(context.TODO(), testDB, dbtools.FixtureInstanceA.InstanceID)
	if err != nil {
		t.Fatal(err)
	}

	assert.True(t, exists)

	requestBody := &v1api.UpsertUserdataRequest{
		ID:          dbtools.FixtureInstanceA.InstanceID,
		Userdata:    []byte(userdata2),
		IPAddresses: dbtools.FixtureInstanceA.HostIPs,
	}

	reqBody, err := json.Marshal(requestBody)
	if err != nil {
		t.Fatal(err)
	}

	w := httptest.NewRecorder()

	req, _ := http.NewRequestWithContext(context.TODO(), http.MethodPost, v1api.GetInternalUserdataPath(), bytes.NewReader(reqBody))
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	instanceUserdata, _ := models.InstanceUserdata(models.InstanceUserdatumWhere.ID.EQ(dbtools.FixtureInstanceA.InstanceID)).One(context.TODO(), testDB)
	assert.Equal(t, requestBody.Userdata, instanceUserdata.Userdata.Bytes)
}

func TestGetUserdataInternal(t *testing.T) {
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
			"Instance A",
			dbtools.FixtureInstanceA.InstanceID,
			http.StatusOK,
			string(dbtools.FixtureInstanceA.InstanceUserdata.Userdata.Bytes),
		},
		// Instance B does not have userdata, so we'd expect a 404
		{
			"Instance B",
			dbtools.FixtureInstanceB.InstanceID,
			http.StatusNotFound,
			"",
		},
		{
			"Instance C",
			dbtools.FixtureInstanceC.InstanceID,
			http.StatusOK,
			string(dbtools.FixtureInstanceC.InstanceUserdata.Userdata.Bytes),
		},
		// Instance D does not have userdata, so we'd expect a 404
		{
			"Instance D",
			dbtools.FixtureInstanceD.InstanceID,
			http.StatusNotFound,
			"",
		},
		{
			"Instance E",
			dbtools.FixtureInstanceE.InstanceID,
			http.StatusOK,
			string(dbtools.FixtureInstanceE.InstanceUserdata.Userdata.Bytes),
		},
		// Instance F does not have metadata, so we'd expect a 404
		{
			"Instance F",
			dbtools.FixtureInstanceF.InstanceID,
			http.StatusOK,
			string(dbtools.FixtureInstanceF.InstanceUserdata.Userdata.Bytes),
		},
	}

	// HEAD request tests
	for _, testcase := range testCases {
		t.Run(testcase.testName, func(t *testing.T) {
			w := httptest.NewRecorder()

			req, _ := http.NewRequestWithContext(context.TODO(), http.MethodHead, v1api.GetInternalUserdataByIDPath(testcase.instanceID), nil)
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

			req, _ := http.NewRequestWithContext(context.TODO(), http.MethodGet, v1api.GetInternalUserdataByIDPath(testcase.instanceID), nil)
			router.ServeHTTP(w, req)

			assert.Equal(t, testcase.expectedStatus, w.Code)

			if testcase.expectedStatus == http.StatusOK {
				assert.Equal(t, testcase.expectedBody, w.Body.String())
			}
		})
	}
}

func TestDeleteUserdata(t *testing.T) {
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
		// Instance B has metadata but no userdata, so expect a 404
		{
			"Instance B",
			dbtools.FixtureInstanceB.InstanceID,
			http.StatusNotFound,
			true,
		},
		// Instance C has metadata and userdata, but no associated IPs, so there
		// should not be any instance_ip_addresses rows found.
		{
			"Instance C",
			dbtools.FixtureInstanceC.InstanceID,
			http.StatusOK,
			false,
		},
		// Instance D has metadata and no userdata, and no associated IPs, so
		// expect a 404
		{
			"Instance D",
			dbtools.FixtureInstanceD.InstanceID,
			http.StatusNotFound,
			false,
		},
		// Instance E does not have metadata, but has userdata and IPs, so expect
		// the userdata and IPs to be removed
		{
			"Instance E",
			dbtools.FixtureInstanceE.InstanceID,
			http.StatusOK,
			false,
		},
		// Instance F does not have metadata, has userdata, but no IPs
		{
			"Instance F",
			dbtools.FixtureInstanceF.InstanceID,
			http.StatusOK,
			false,
		},
	}

	for _, testcase := range testCases {
		t.Run(testcase.testName, func(t *testing.T) {
			w := httptest.NewRecorder()

			req, _ := http.NewRequestWithContext(context.TODO(), http.MethodDelete, v1api.GetInternalUserdataByIDPath(testcase.instanceID), nil)
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
