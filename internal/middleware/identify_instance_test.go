package middleware_test

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"go.uber.org/zap"

	"go.hollow.sh/metadataservice/internal/dbtools"
	"go.hollow.sh/metadataservice/internal/middleware"
)

func TestIdentifyInstanceByIP(t *testing.T) {
	testdb := dbtools.DatabaseTest(t)

	type testCase struct {
		testName           string
		clientIP           string
		shouldFindInstance bool
		expectedInstanceID string
	}

	var testCases = []testCase{
		{
			"unknown IPv4 address",
			"1.2.3.4",
			false,
			"",
		},
		{
			"unknown IPv6 address",
			"fe80::aede:48ff:fe00:1122",
			false,
			"",
		},
	}

	// Instance A IPs
	for _, hostIP := range dbtools.FixtureInstanceA.HostIPs {
		caseItem := testCase{
			fmt.Sprintf("Instance A IP %s", hostIP),
			hostIP,
			true,
			dbtools.FixtureInstanceA.InstanceID,
		}
		testCases = append(testCases, caseItem)
	}

	// Instance B IPs
	for _, hostIP := range dbtools.FixtureInstanceB.HostIPs {
		caseItem := testCase{
			fmt.Sprintf("Instance B IP %s", hostIP),
			hostIP,
			true,
			dbtools.FixtureInstanceB.InstanceID,
		}
		testCases = append(testCases, caseItem)
	}

	// Instance E IPs
	for _, hostIP := range dbtools.FixtureInstanceE.HostIPs {
		caseItem := testCase{
			fmt.Sprintf("Instance E IP %s", hostIP),
			hostIP,
			true,
			dbtools.FixtureInstanceE.InstanceID,
		}
		testCases = append(testCases, caseItem)
	}

	for _, testcase := range testCases {
		t.Run(testcase.testName, func(t *testing.T) {
			logger := zap.NewNop()
			r := gin.New()
			r.Use(middleware.IdentifyInstanceByIP(logger, testdb))
			r.GET("/", func(c *gin.Context) {
				instanceIDValue, found := c.Get(middleware.ContextKeyInstanceID)

				if testcase.shouldFindInstance {
					assert.Equal(t, testcase.expectedInstanceID, instanceIDValue)
					assert.True(t, found)
				} else {
					assert.Equal(t, nil, instanceIDValue)
					assert.False(t, found)
				}

				c.JSON(http.StatusOK, "ok")
			})

			w := httptest.NewRecorder()
			ctx := context.TODO()
			req, _ := http.NewRequestWithContext(ctx, "GET", "http://test/", nil)
			req.RemoteAddr = net.JoinHostPort(testcase.clientIP, "0")
			r.ServeHTTP(w, req)
		})
	}
}

func TestIdentifyInstanceByIPWithTrustedProxies(t *testing.T) {
	testdb := dbtools.DatabaseTest(t)

	proxyIP := "1.2.3.4"

	logger := zap.NewNop()
	trustedProxies := []string{proxyIP}
	r := gin.New()
	err := r.SetTrustedProxies(trustedProxies)

	if err != nil {
		t.Errorf("Error setting trusted proxies: %v\n", err)
	}

	hostAIP := dbtools.FixtureInstanceA.HostIPs[0]

	r.Use(middleware.IdentifyInstanceByIP(logger, testdb))
	r.GET("/", func(c *gin.Context) {
		instanceIDValue, found := c.Get(middleware.ContextKeyInstanceID)

		assert.True(t, found)
		assert.Equal(t, dbtools.FixtureInstanceA.InstanceID, instanceIDValue)

		c.Status(http.StatusOK)
	})

	w := httptest.NewRecorder()
	req, _ := http.NewRequestWithContext(context.TODO(), "GET", "http://test/", nil)
	req.RemoteAddr = net.JoinHostPort(proxyIP, "0")
	req.Header.Add("X-Forwarded-For", hostAIP)
	r.ServeHTTP(w, req)
}
