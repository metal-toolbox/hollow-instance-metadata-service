package lookup_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"go.uber.org/zap"

	"go.hollow.sh/metadataservice/internal/lookup"
)

func lookupMetadataServerMock(instance testInstance) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		resp := instance.MetadataResponse()

		_ = json.NewEncoder(w).Encode(resp)
	}))
}

func lookupUserdataServerMock(instance testInstance) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		resp := instance.UserdataResponse()

		_ = json.NewEncoder(w).Encode(resp)
	}))
}

func lookupServerWithStatusMock(status int, body string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)

		if len(body) > 0 {
			fmt.Fprint(w, body)
		}
	}))
}

func lookupServerForbiddenMock() *httptest.Server {
	return lookupServerWithStatusMock(http.StatusForbidden, `{"message": "invalid auth token"}`)
}

func TestNewClient(t *testing.T) {
	// NewClient returns an error if an empty baseURL string is provided
	_, err := lookup.NewClient(zap.NewNop(), "", http.DefaultClient)
	assert.NotNil(t, err)

	// NewClient returns an error if the provided baseURL is not pareseable
	_, err = lookup.NewClient(zap.NewNop(), "https://ba{uh...}=:user@shouldn't parse!", http.DefaultClient)
	assert.NotNil(t, err)
}

func TestGetMetadataByID(t *testing.T) {
	type testCase struct {
		testName      string
		ID            string
		expectedError *error
		expectedResp  interface{}
		srv           *httptest.Server
	}

	var testCases = []testCase{
		{
			testName:      "unknown instance ID",
			ID:            "badid",
			expectedError: &lookup.ErrNotFound,
			expectedResp:  nil,
			srv:           lookupServerWithStatusMock(404, `{"errors": ["not found"]}`),
		},
		{
			testName:      "credentials error - access forbidden",
			ID:            "badcreds",
			expectedError: &lookup.ErrUnexpectedStatus,
			expectedResp:  nil,
			srv:           lookupServerForbiddenMock(),
		},
		{
			testName:      "instance 0 test",
			ID:            testInstances[0].ID,
			expectedError: nil,
			expectedResp:  testInstances[0].MetadataResponse(),
			srv:           lookupMetadataServerMock(testInstances[0]),
		},
		{
			testName:      "instance 1 test",
			ID:            testInstances[1].ID,
			expectedError: nil,
			expectedResp:  testInstances[1].MetadataResponse(),
			srv:           lookupMetadataServerMock(testInstances[1]),
		},
	}

	for _, tc := range testCases {
		t.Run(tc.testName, func(t *testing.T) {
			defer tc.srv.Close()

			client, err := lookup.NewClient(zap.NewNop(), tc.srv.URL, http.DefaultClient)
			if err != nil {
				t.Errorf("error getting lookup service client: %v\n", err)
			}

			resp, err := client.GetMetadataByID(context.TODO(), tc.ID)

			if tc.expectedError != nil {
				assert.ErrorIs(t, err, *tc.expectedError)
			} else {
				assert.Nil(t, err)
			}

			if tc.expectedResp != nil {
				assert.NotNil(t, resp)
				assert.Equal(t, tc.expectedResp, *resp)
			} else {
				assert.Nil(t, resp)
			}
		})
	}
}

func TestGetMetadataByIP(t *testing.T) {
	type testCase struct {
		testName      string
		IPAddress     string
		expectedError *error
		expectedResp  interface{}
		srv           *httptest.Server
	}

	var testCases = []testCase{
		{
			testName:      "unknown instance IP",
			IPAddress:     "127.0.0.1",
			expectedError: &lookup.ErrNotFound,
			expectedResp:  nil,
			srv:           lookupServerWithStatusMock(404, `{"errors": ["not found"]}`),
		},
		{
			testName:      "credentials error - access forbidden",
			IPAddress:     "192.168.0.1",
			expectedError: &lookup.ErrUnexpectedStatus,
			expectedResp:  nil,
			srv:           lookupServerForbiddenMock(),
		},
		{
			testName:      "instance 0 test",
			IPAddress:     testInstances[0].IPAddresses[0],
			expectedError: nil,
			expectedResp:  testInstances[0].MetadataResponse(),
			srv:           lookupMetadataServerMock(testInstances[0]),
		},
		{
			testName:      "instance 1 test",
			IPAddress:     testInstances[1].IPAddresses[0],
			expectedError: nil,
			expectedResp:  testInstances[1].MetadataResponse(),
			srv:           lookupMetadataServerMock(testInstances[1]),
		},
	}

	for _, tc := range testCases {
		t.Run(tc.testName, func(t *testing.T) {
			defer tc.srv.Close()

			client, err := lookup.NewClient(zap.NewNop(), tc.srv.URL, http.DefaultClient)
			if err != nil {
				t.Errorf("error getting lookup service client: %v\n", err)
			}

			resp, err := client.GetMetadataByIP(context.TODO(), tc.IPAddress)

			if tc.expectedError != nil {
				assert.ErrorIs(t, err, *tc.expectedError)
			} else {
				assert.Nil(t, err)
			}

			if tc.expectedResp != nil {
				assert.NotNil(t, resp)
				assert.Equal(t, tc.expectedResp, *resp)
			} else {
				assert.Nil(t, resp)
			}
		})
	}
}

func TestGetUserdataByID(t *testing.T) {
	type testCase struct {
		testName      string
		ID            string
		expectedError *error
		expectedResp  interface{}
		srv           *httptest.Server
	}

	var testCases = []testCase{
		{
			testName:      "unknown instance ID",
			ID:            "badid",
			expectedError: &lookup.ErrNotFound,
			expectedResp:  nil,
			srv:           lookupServerWithStatusMock(404, `{"errors": ["not found"]}`),
		},
		{
			testName:      "credentials error - access forbidden",
			ID:            "badcreds",
			expectedError: &lookup.ErrUnexpectedStatus,
			expectedResp:  nil,
			srv:           lookupServerForbiddenMock(),
		},
		{
			testName:      "instance 0 test",
			ID:            testInstances[0].ID,
			expectedError: nil,
			expectedResp:  testInstances[0].UserdataResponse(),
			srv:           lookupUserdataServerMock(testInstances[0]),
		},
		{
			testName:      "instance 1 test",
			ID:            testInstances[1].ID,
			expectedError: nil,
			expectedResp:  testInstances[1].UserdataResponse(),
			srv:           lookupUserdataServerMock(testInstances[1]),
		},
	}

	for _, tc := range testCases {
		t.Run(tc.testName, func(t *testing.T) {
			defer tc.srv.Close()

			client, err := lookup.NewClient(zap.NewNop(), tc.srv.URL, http.DefaultClient)
			if err != nil {
				t.Errorf("error getting lookup service client: %v\n", err)
			}

			resp, err := client.GetUserdataByID(context.TODO(), tc.ID)

			if tc.expectedError != nil {
				assert.ErrorIs(t, err, *tc.expectedError)
			} else {
				assert.Nil(t, err)
			}

			if tc.expectedResp != nil {
				assert.NotNil(t, resp)
				assert.Equal(t, tc.expectedResp, *resp)
			} else {
				assert.Nil(t, resp)
			}
		})
	}
}

func TestGetUserdataByIP(t *testing.T) {
	type testCase struct {
		testName      string
		IPAddress     string
		expectedError *error
		expectedResp  interface{}
		srv           *httptest.Server
	}

	var testCases = []testCase{
		{
			testName:      "unknown instance IP",
			IPAddress:     "127.0.0.1",
			expectedError: &lookup.ErrNotFound,
			expectedResp:  nil,
			srv:           lookupServerWithStatusMock(404, `{"errors": ["not found"]}`),
		},
		{
			testName:      "credentials error - access forbidden",
			IPAddress:     "192.168.0.1",
			expectedError: &lookup.ErrUnexpectedStatus,
			expectedResp:  nil,
			srv:           lookupServerForbiddenMock(),
		},
		{
			testName:      "instance 0 test",
			IPAddress:     testInstances[0].IPAddresses[0],
			expectedError: nil,
			expectedResp:  testInstances[0].UserdataResponse(),
			srv:           lookupUserdataServerMock(testInstances[0]),
		},
		{
			testName:      "instance 1 test",
			IPAddress:     testInstances[1].IPAddresses[0],
			expectedError: nil,
			expectedResp:  testInstances[1].UserdataResponse(),
			srv:           lookupUserdataServerMock(testInstances[1]),
		},
	}

	for _, tc := range testCases {
		t.Run(tc.testName, func(t *testing.T) {
			defer tc.srv.Close()

			client, err := lookup.NewClient(zap.NewNop(), tc.srv.URL, http.DefaultClient)
			if err != nil {
				t.Errorf("error getting lookup service client: %v\n", err)
			}

			resp, err := client.GetUserdataByIP(context.TODO(), tc.IPAddress)

			if tc.expectedError != nil {
				assert.ErrorIs(t, err, *tc.expectedError)
			} else {
				assert.Nil(t, err)
			}

			if tc.expectedResp != nil {
				assert.NotNil(t, resp)
				assert.Equal(t, tc.expectedResp, *resp)
			} else {
				assert.Nil(t, resp)
			}
		})
	}
}
