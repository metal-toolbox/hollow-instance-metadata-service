//go:build testtools
// +build testtools

package dbtools

import (
	"context"
	"os"
	"testing"

	// import the crdbpgx package for automatic retries of errors for crdb that support retry
	_ "github.com/cockroachdb/cockroach-go/v2/crdb/crdbpgx"
	"github.com/jmoiron/sqlx"
	_ "github.com/lib/pq" // Register the Postgres driver.
	"github.com/stretchr/testify/require"

	"go.hollow.sh/metadataservice/internal/models"
)

// TestDBURI is the URI for the test database
var TestDBURI = os.Getenv("METADATASERVICE_DB_URI")
var testDB *sqlx.DB

func testDatastore() error {
	// don't setup the datastore if we already have one
	if testDB != nil {
		return nil
	}

	// Uncomment when you are having database issues with your tests and need to see the db logs
	// Hidden by default because it can be noisy and make it harder to read normal failures.
	// You can also enable at the beginning of your test and then disable it again at the end
	// boil.DebugMode = true

	db, err := sqlx.Open("postgres", TestDBURI)
	if err != nil {
		return err
	}

	testDB = db

	cleanDB()

	return addFixtures()
}

// DatabaseTest allows you to run tests that interact with the database
func DatabaseTest(t *testing.T) *sqlx.DB {
	// No hooks to register yet...
	// RegisterHooks()
	if testing.Short() {
		t.Skip("skipping database test in short mode")
	}

	t.Cleanup(func() {
		cleanDB()

		err := addFixtures()
		require.NoError(t, err, "Unexpected error setting up fixture data")
	})

	err := testDatastore()
	require.NoError(t, err, "Unexpected error getting connection to test datastore")

	return testDB
}

// TestDB allows us to get a pointer to the current Test DB connection
func TestDB() *sqlx.DB {
	return testDB
}

// nolint
func cleanDB() {
	ctx := context.TODO()

	// Make sure the deletion goes in order so you don't break the databases foreign key constraints
	testDB.Exec("SET sql_safe_updates = false;")
	models.InstanceMetadata().DeleteAll(ctx, testDB)
	models.InstanceUserdata().DeleteAll(ctx, testDB)
	models.InstanceIPAddresses().DeleteAll(ctx, testDB)
	testDB.Exec("SET sql_safe_updates = true;")
}
