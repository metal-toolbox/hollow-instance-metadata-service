package upserter

import (
	"context"
	"encoding/json"
	"math/rand"
	"strings"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/spf13/viper"
	"github.com/volatiletech/sqlboiler/v4/boil"
	"go.uber.org/zap"

	"go.hollow.sh/metadataservice/internal/models"
)

// RecordUpserter is a function defined in by each metadata or userdata upsert
// handler function and passed into the general handleUpsertRequest function.
// This lets us share the common functionality shared between both, like
// handling conflicting IPs, adding new instance_ip_address rows, and
// removing stale instance_ip_address rows can be handled generically while
// delegating the specific implementation for handling upserting metadata
// or userdata records back to the calling method.
type RecordUpserter func(c context.Context, exec boil.ContextExecutor) error

// The following types are used to unmarshal the metadata JSON body so we can
// extract the IP addresses from it for logging.

// NetworkAddress is a struct used to unmarshal the "network.addresses" JSON array
type NetworkAddress struct {
	Address string `json:"address"`
}

// Network is a struct used to unmarshal the "network" JSON object
type Network struct {
	Addresses []NetworkAddress `json:"addresses"`
}

// MetadataContent is a struct used to unmarshal the metadata JSON body
type MetadataContent struct {
	Network Network `json:"network"`
}

// ExtractIPAddressesFromMetadata is a helper function used to extract IP addresses
// from the metadata JSON. We only use this for logging purposes, so it can fail silently.
func ExtractIPAddressesFromMetadata(metadata *models.InstanceMetadatum) []string {
	var raw map[string]interface{}
	if err := json.Unmarshal([]byte(metadata.Metadata), &raw); err != nil {
		return nil
	}

	network, ok := raw["network"].(map[string]interface{})
	if !ok {
		return nil
	}

	addresses, ok := network["addresses"].([]interface{})
	if !ok {
		return nil
	}

	result := make([]string, 0, len(addresses))

	for _, addr := range addresses {
		if addrMap, ok := addr.(map[string]interface{}); ok {
			if ipAddr, ok := addrMap["address"].(string); ok {
				result = append(result, ipAddr)
			}
		}
	}

	return result
}

// UpsertMetadata is used to upsert (update or insert) an instance_metadata
// record, along with managing inserting new instance_ip_addresses rows and
// removing conflicting or stale instance_ip_addresses rows.
func UpsertMetadata(ctx context.Context, db *sqlx.DB, logger *zap.Logger, id string, ipAddresses []string, metadata *models.InstanceMetadatum) error {
	metadataUpserter := func(c context.Context, exec boil.ContextExecutor) error {
		return metadata.Upsert(c, exec, true, []string{"id"}, boil.Whitelist("metadata", "updated_at"), boil.Infer())
	}

	// Extract all IP addresses from the metadata body - note that this is different from
	// the ipAddresses list, which doesn't include IPv6 addresses, as it only includes
	// addresses that the metadata service would conceivably perform lookups based on.
	allIPs := ExtractIPAddressesFromMetadata(metadata)
	logger.Sugar().Info("Starting metadata upsert for uuid: ", id, " where metadata contains IPs: ", allIPs)

	return doUpsertWithRetries(ctx, db, logger, id, ipAddresses, metadataUpserter)
}

// UpsertUserdata is used to upsert (update or insert) an instance_userdata
// record, along with managing inserting new instance_ip_addresses rows and
// removing conflicting or stale instance_ip_addresses rows.
func UpsertUserdata(ctx context.Context, db *sqlx.DB, logger *zap.Logger, id string, ipAddresses []string, userdata *models.InstanceUserdatum) error {
	userdataUpserter := func(c context.Context, exec boil.ContextExecutor) error {
		return userdata.Upsert(c, exec, true, []string{"id"}, boil.Whitelist("userdata", "updated_at"), boil.Infer())
	}

	logger.Sugar().Info("Starting userdata upsert for uuid: ", id)

	return doUpsertWithRetries(ctx, db, logger, id, ipAddresses, userdataUpserter)
}

// doUpsertWithRetries is just a wrapper function that invokes doUpsert(), but handles the retry logic
func doUpsertWithRetries(ctx context.Context, db *sqlx.DB, logger *zap.Logger, id string, ipAddresses []string, upsertRecordFunc RecordUpserter) error {
	upsertSuccess := false
	maxUpsertRetries := viper.GetInt("crdb.max_retries")
	dbRetryInterval := viper.GetDuration("crdb.retry_interval")

	var err error

	for i := 0; i <= maxUpsertRetries && !upsertSuccess; i++ {
		err = doUpsert(ctx, db, logger, id, ipAddresses, upsertRecordFunc)
		if err == nil {
			upsertSuccess = true

			if i > 0 {
				logger.Sugar().Info("Upsert operation for instance: ", id, " successful on retry attempt #", i)
			} else {
				logger.Sugar().Info("Upsert operation for instance: ", id, " successful on first attempt")
			}
		} else {
			// Exponential backoff would be overkill here, but adding a bit of jitter
			// to sleep a short time is reasonable
			jitter := time.Duration(rand.Int63n(int64(dbRetryInterval)))
			time.Sleep(jitter)
		}
	}

	if !upsertSuccess {
		logger.Sugar().Error("Upsert operation failed for instance: ", id, " even after ", maxUpsertRetries, " attempts")
		return err
	}

	return nil
}

// doUpsert handles the functionality common to inserting or updating both
// metadata and userdata records. Namely, handling conflicting or stale
// (in the case of an update) IP address associations.
func doUpsert(ctx context.Context, db *sqlx.DB, logger *zap.Logger, id string, ipAddresses []string, upsertRecordFunc RecordUpserter) error {
	logger.Sugar().Info("doUpsert starting for id: ", id, " - upserting lookupable IPs ", ipAddresses)

	ctx = boil.WithDebug(ctx, true)

	// Start a DB transaction
	txErr := false

	ctxWithTimeout, cancel := context.WithTimeout(ctx, viper.GetDuration("crdb.tx_timeout"))
	defer cancel()

	tx, err := db.BeginTx(ctxWithTimeout, nil)
	if err != nil {
		return err
	}

	// If there's an error, we'll want to roll back the transaction.
	defer func() {
		if txErr {
			logger.Sugar().Warn("Rolling back doUpsert transaction for instance: ", id, " with ipAddresses: ", ipAddresses)

			err := tx.Rollback()
			if err != nil {
				logger.Sugar().Error("Could not roll back doUpsert transaction for instance: ", id, "Error: ", err)
			}
		}
	}()

	// Step 1
	// Select and lock the ip address rows that may be updated or deleted by this operation, to prevent race conditions
	// This includes:
	// * ip addresses that already exist for this instance id (instanceIPAddresses)
	// * ip addresses included in this update request, but are associated with a different instance id (conflictIPs)
	instanceIPAddresses, err := models.InstanceIPAddresses(models.InstanceIPAddressWhere.InstanceID.EQ(id)).All(ctxWithTimeout, db)
	if err != nil {
		logger.Sugar().Error("doUpsert DB error when selecting instanceIPAddresses for update: ", err)
		return err
	}

	conflictIPs, err := models.InstanceIPAddresses(models.InstanceIPAddressWhere.Address.IN(ipAddresses), models.InstanceIPAddressWhere.InstanceID.NEQ(id)).All(ctxWithTimeout, db)
	if err != nil {
		logger.Sugar().Error("doUpsert DB error when selecting conflictIPs for update: ", err)
		return err
	}

	// Step 2.a
	// Find "stale" InstanceIPAddress rows for this instance. That is, select
	// rows from the instanceIPAddresses result which don't have a corresponding
	// entry in the list of IP Addresses supplied in the call.
	var staleInstanceIPAddresses models.InstanceIPAddressSlice

	for _, instanceIP := range instanceIPAddresses {
		found := false

		for _, IP := range ipAddresses {
			if strings.EqualFold(instanceIP.Address, IP) {
				found = true
				break
			}
		}

		if !found {
			staleInstanceIPAddresses = append(staleInstanceIPAddresses, instanceIP)
		}
	}

	// Step 2.b
	// Find new IP Addresses that were specified in the call that aren't
	// currently associated to the instance.
	var newInstanceIPAddresses models.InstanceIPAddressSlice

	for _, IP := range ipAddresses {
		found := false

		for _, instanceIP := range instanceIPAddresses {
			if strings.EqualFold(IP, instanceIP.Address) {
				found = true
				break
			}
		}

		if !found {
			newRecord := &models.InstanceIPAddress{
				InstanceID: id,
				Address:    IP,
			}
			newInstanceIPAddresses = append(newInstanceIPAddresses, newRecord)
		}
	}

	// Step 3
	// Remove any instance_ip_address rows for the specified IP addresses that
	// are currently associated to a *different* instance ID
	for _, conflictingIP := range conflictIPs {
		// TODO: Maybe remove instance_metadata and instance_userdata records for the "old" instance ID(s)?
		// Potentially after checking to see if this IP was the *last* IP address associated to the
		// "old" instance ID?
		_, err := conflictingIP.Delete(ctxWithTimeout, tx)
		if err != nil {
			txErr = true

			logger.Sugar().Error("doUpsert DB error when deleting conflictIPs: ", err)

			return err
		}
	}

	// Step 4
	// Remove any "stale" instance_ip_addresses rows associated to the provided
	// instnace_id but were not specified in the call.
	for _, staleIP := range staleInstanceIPAddresses {
		_, err := staleIP.Delete(ctxWithTimeout, tx)
		if err != nil {
			txErr = true

			logger.Sugar().Error("doUpsert DB error when deleting staleIPs: ", err)

			return err
		}
	}

	// Step 5
	// Create instance_ip_addresses rows for any IP addresses specified in the
	// call that aren't already associated to the provided instance_id
	for _, newInstanceIP := range newInstanceIPAddresses {
		err := newInstanceIP.Insert(ctxWithTimeout, tx, boil.Infer())
		if err != nil {
			txErr = true

			logger.Sugar().Error("doUpsert DB error when inserting newInstanceIPs: ", err)

			return err
		}
	}

	// Step 6
	// Upsert the instance_metadata or instance_userdata table. This will create
	// a new row with the provided instance ID and metadata or userdata if there
	// is no current row for instance_id. If there is an existing row matching on
	// instance_id, instead this will just update the metadata or userdata column
	// value.
	if err := upsertRecordFunc(ctxWithTimeout, tx); err != nil {
		txErr = true

		logger.Sugar().Error("doUpsert DB error when upserting the instance_metadata or instance_userdata table: ", err)

		return err
	}

	// Step 7
	// Commit our transaction
	err = tx.Commit()
	if err != nil {
		txErr = true

		logger.Sugar().Warn("Unable to commit db upsert transaction for instance: ", id, "Error: ", err)

		return err
	}

	return nil
}
