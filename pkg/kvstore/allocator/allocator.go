// SPDX-License-Identifier: Apache-2.0
// Copyright Authors of Cilium

package allocator

import (
	"context"
	"fmt"
	"log/slog"
	"path"
	"strconv"
	"strings"

	"github.com/cilium/cilium/pkg/allocator"
	"github.com/cilium/cilium/pkg/idpool"
	"github.com/cilium/cilium/pkg/kvstore"
	"github.com/cilium/cilium/pkg/logging/logfields"
	"github.com/cilium/cilium/pkg/rate"
)

// kvstoreBackend is an implementation of pkg/allocator.Backend. It stores
// identities in the following format:
//
// Slave keys:
//
// Slave keys are owned by individual nodes:
//   - basePath/value/key1/node1 => 1001
//   - basePath/value/key1/node2 => 1001
//   - basePath/value/key2/node1 => 1002
//   - basePath/value/key2/node2 => 1002
//
// If at least one key exists with the prefix basePath/value/keyN then that
// key must be considered to be in use in the allocation space.
//
// Slave keys are protected by a lease and will automatically get removed
// after ~ option.Config.KVstoreLeaseTTL if the node does not renew in time.
//
// Master key:
//   - basePath/id/1001 => key1
//   - basePath/id/1002 => key2
//
// Master keys provide the mapping from ID to key. As long as a master key
// for an ID exists, the ID is still in use. However, if a master key is no
// longer backed by at least one slave key, the garbage collector will
// eventually release the master key and return it back to the pool.
type kvstoreBackend struct {
	logger *slog.Logger
	// basePrefix is the prefix in the kvstore that all keys share which
	// are being managed by this allocator. The basePrefix typically
	// consists of something like: "space/project/allocatorName"
	basePrefix string

	// idPrefix is the kvstore key prefix for all master keys. It is being
	// derived from the basePrefix.
	idPrefix string

	// valuePrefix is the kvstore key prefix for all slave keys. It is
	// being derived from the basePrefix.
	valuePrefix string

	// lockPrefix is the prefix to use for all kvstore locks. This prefix
	// is different from the idPrefix and valuePrefix to simplify watching
	// for ID and key changes.
	lockPrefix string

	// suffix is the suffix attached to keys which must be node specific,
	// this is typical set to the node's IP address
	suffix string

	backend kvstore.BackendOperations

	keyType allocator.AllocatorKey
}

func prefixMatchesKey(prefix, key string) bool {
	// cilium/state/identities/v1/value/label;foo;bar;/172.0.124.60
	lastSlash := strings.LastIndex(key, "/")
	return len(prefix) == lastSlash
}

type KVStoreBackendConfiguration struct {
	BasePath string
	Suffix   string
	Typ      allocator.AllocatorKey
	Backend  kvstore.BackendOperations
}

// NewKVStoreBackend creates a pkg/allocator.Backend compatible instance. The
// specific kvstore used is configured in pkg/kvstore.
func NewKVStoreBackend(logger *slog.Logger, c KVStoreBackendConfiguration) (allocator.Backend, error) {
	if c.Backend == nil {
		return nil, fmt.Errorf("kvstore client not configured")
	}

	return &kvstoreBackend{
		logger:      logger.With(logfields.LogSubsys, "kvstorebackend"),
		basePrefix:  c.BasePath,
		idPrefix:    path.Join(c.BasePath, "id"),
		valuePrefix: path.Join(c.BasePath, "value"),
		lockPrefix:  path.Join(c.BasePath, "locks"),
		suffix:      c.Suffix,
		keyType:     c.Typ,
		backend:     c.Backend,
	}, nil
}

// lockPath locks a key in the scope of an allocator
func (k *kvstoreBackend) lockPath(ctx context.Context, key string) (*kvstore.Lock, error) {
	suffix := strings.TrimPrefix(key, k.basePrefix)
	return kvstore.LockPath(ctx, k.logger, k.backend, path.Join(k.lockPrefix, suffix))
}

// DeleteAllKeys will delete all keys
func (k *kvstoreBackend) DeleteAllKeys(ctx context.Context) {
	k.backend.DeletePrefix(ctx, k.basePrefix)
}

func (k *kvstoreBackend) DeleteID(ctx context.Context, id idpool.ID) error {
	return k.backend.Delete(ctx, path.Join(k.idPrefix, id.String()))
}

// AllocateID allocates a key->ID mapping in the kvstore.
func (k *kvstoreBackend) AllocateID(ctx context.Context, id idpool.ID, key allocator.AllocatorKey) (allocator.AllocatorKey, error) {
	// create /id/<ID> and fail if it already exists
	keyPath := path.Join(k.idPrefix, id.String())
	success, err := k.backend.CreateOnly(ctx, keyPath, []byte(key.GetKey()), false)
	if err != nil || !success {
		return nil, fmt.Errorf("unable to create master key '%s': %w", keyPath, err)
	}

	return key, nil
}

// AllocateID allocates a key->ID mapping in the kvstore.
func (k *kvstoreBackend) AllocateIDIfLocked(ctx context.Context, id idpool.ID, key allocator.AllocatorKey, lock kvstore.KVLocker) (allocator.AllocatorKey, error) {
	// create /id/<ID> and fail if it already exists
	keyPath := path.Join(k.idPrefix, id.String())
	success, err := k.backend.CreateOnlyIfLocked(ctx, keyPath, []byte(key.GetKey()), false, lock)
	if err != nil || !success {
		return nil, fmt.Errorf("unable to create master key '%s': %w", keyPath, err)
	}

	return key, nil
}

// AcquireReference marks that this node is using this key->ID mapping in the kvstore.
func (k *kvstoreBackend) AcquireReference(ctx context.Context, id idpool.ID, key allocator.AllocatorKey, lock kvstore.KVLocker) error {
	keyString := key.GetKey()
	if err := k.createValueNodeKey(ctx, keyString, id, lock); err != nil {
		return fmt.Errorf("unable to create slave key '%s': %w", keyString, err)
	}
	return nil
}

// createValueKey records that this "node" is using this key->ID
func (k *kvstoreBackend) createValueNodeKey(ctx context.Context, key string, newID idpool.ID, lock kvstore.KVLocker) error {
	// add a new key /value/<key>/<node> to account for the reference
	// The key is protected with a TTL/lease and will expire after LeaseTTL
	valueKey := path.Join(k.valuePrefix, key, k.suffix)
	if _, err := k.backend.UpdateIfDifferentIfLocked(ctx, valueKey, []byte(newID.String()), true, lock); err != nil {
		return fmt.Errorf("unable to create value-node key '%s': %w", valueKey, err)
	}

	return nil
}

// Lock locks a key in the scope of an allocator
func (k *kvstoreBackend) lock(ctx context.Context, key string) (*kvstore.Lock, error) {
	suffix := strings.TrimPrefix(key, k.basePrefix)
	return kvstore.LockPath(ctx, k.logger, k.backend, path.Join(k.lockPrefix, suffix))
}

// Lock locks a key in the scope of an allocator
func (k *kvstoreBackend) Lock(ctx context.Context, key allocator.AllocatorKey) (kvstore.KVLocker, error) {
	return k.lock(ctx, key.GetKey())
}

// Get returns the ID which is allocated to a key in the kvstore
func (k *kvstoreBackend) Get(ctx context.Context, key allocator.AllocatorKey) (idpool.ID, error) {
	// ListPrefix() will return all keys matching the prefix, the prefix
	// can cover multiple different keys, example:
	//
	// key1 := label1;label2;
	// key2 := label1;label2;label3;
	//
	// In order to retrieve the correct key, the position of the last '/'
	// is significant, e.g.
	//
	// prefix := cilium/state/identities/v1/value/label;foo;
	//
	// key1 := cilium/state/identities/v1/value/label;foo;/172.0.124.60
	// key2 := cilium/state/identities/v1/value/label;foo;bar;/172.0.124.60
	//
	// Only key1 should match
	prefix := path.Join(k.valuePrefix, key.GetKey())
	pairs, err := k.backend.ListPrefix(ctx, prefix)
	kvstore.Trace(k.logger, "ListPrefix",
		logfields.Error, err,
		logfields.Prefix, prefix,
		logfields.Entries, len(pairs),
	)
	if err != nil {
		return 0, err
	}

	for k, v := range pairs {
		if prefixMatchesKey(prefix, k) {
			id, err := strconv.ParseUint(string(v.Data), 10, 64)
			if err == nil {
				return idpool.ID(id), nil
			}
		}
	}

	return idpool.NoID, nil
}

// GetIfLocked returns the ID which is allocated to a key in the kvstore
// if the client is still holding the given lock.
func (k *kvstoreBackend) GetIfLocked(ctx context.Context, key allocator.AllocatorKey, lock kvstore.KVLocker) (idpool.ID, error) {
	// ListPrefixIfLocked() will return all keys matching the prefix, the prefix
	// can cover multiple different keys, example:
	//
	// key1 := label1;label2;
	// key2 := label1;label2;label3;
	//
	// In order to retrieve the correct key, the position of the last '/'
	// is significant, e.g.
	//
	// prefix := cilium/state/identities/v1/value/label;foo;
	//
	// key1 := cilium/state/identities/v1/value/label;foo;/172.0.124.60
	// key2 := cilium/state/identities/v1/value/label;foo;bar;/172.0.124.60
	//
	// Only key1 should match
	prefix := path.Join(k.valuePrefix, key.GetKey())
	pairs, err := k.backend.ListPrefixIfLocked(ctx, prefix, lock)
	kvstore.Trace(k.logger, "ListPrefixLocked",
		logfields.Prefix, prefix,
		logfields.Entries, len(pairs),
	)
	if err != nil {
		return 0, err
	}

	for k, v := range pairs {
		if prefixMatchesKey(prefix, k) {
			id, err := strconv.ParseUint(string(v.Data), 10, 64)
			if err == nil {
				return idpool.ID(id), nil
			}
		}
	}

	return idpool.NoID, nil
}

// GetByID returns the key associated with an ID. Returns nil if no key is
// associated with the ID.
func (k *kvstoreBackend) GetByID(ctx context.Context, id idpool.ID) (allocator.AllocatorKey, error) {
	v, err := k.backend.Get(ctx, path.Join(k.idPrefix, id.String()))
	if err != nil {
		return nil, err
	}

	if v == nil {
		return nil, nil
	}

	return k.keyType.PutKey(string(v)), nil
}

// UpdateKey refreshes the record that this node is using this key -> id
// mapping. When reliablyMissing is set it will also recreate missing master or
// slave keys.
func (k *kvstoreBackend) UpdateKey(ctx context.Context, id idpool.ID, key allocator.AllocatorKey, reliablyMissing bool) error {
	var (
		err       error
		recreated bool
		keyPath   = path.Join(k.idPrefix, id.String())
		valueKey  = path.Join(k.valuePrefix, key.GetKey(), k.suffix)
	)

	// Use of CreateOnly() ensures that any existing potentially
	// conflicting key is never overwritten.
	success, err := k.backend.CreateOnly(ctx, keyPath, []byte(key.GetKey()), false)
	switch {
	case err != nil:
		return fmt.Errorf("Unable to re-create missing master key \"%s\" -> \"%s\": %w", logfields.Key, valueKey, err)
	case success:
		k.logger.Warn(
			"Re-created missing master key",
			logfields.Key, keyPath,
		)
	}

	// Also re-create the slave key in case it has been deleted. This will
	// ensure that the next garbage collection cycle of any participating
	// node does not remove the master key again.
	if reliablyMissing {
		recreated, err = k.backend.CreateOnly(ctx, valueKey, []byte(id.String()), true)
	} else {
		recreated, err = k.backend.UpdateIfDifferent(ctx, valueKey, []byte(id.String()), true)
	}
	switch {
	case err != nil:
		return fmt.Errorf("Unable to re-create missing slave key \"%s\" -> \"%s\": %w", logfields.Key, valueKey, err)
	case recreated:
		k.logger.Warn(
			"Re-created missing slave key",
			logfields.Key, valueKey,
		)
	}

	return nil
}

// UpdateKeyIfLocked refreshes the record that this node is using this key -> id
// mapping. When reliablyMissing is set it will also recreate missing master or
// slave keys.
func (k *kvstoreBackend) UpdateKeyIfLocked(ctx context.Context, id idpool.ID, key allocator.AllocatorKey, reliablyMissing bool, lock kvstore.KVLocker) error {
	var (
		err       error
		recreated bool
		keyPath   = path.Join(k.idPrefix, id.String())
		valueKey  = path.Join(k.valuePrefix, key.GetKey(), k.suffix)
	)

	// Use of CreateOnly() ensures that any existing potentially
	// conflicting key is never overwritten.
	success, err := k.backend.CreateOnlyIfLocked(ctx, keyPath, []byte(key.GetKey()), false, lock)
	switch {
	case err != nil:
		return fmt.Errorf("Unable to re-create missing master key \"%s\" -> \"%s\": %w", logfields.Key, valueKey, err)
	case success:
		k.logger.Warn(
			"Re-created missing master key",
			logfields.Key, keyPath,
		)
	}

	// Also re-create the slave key in case it has been deleted. This will
	// ensure that the next garbage collection cycle of any participating
	// node does not remove the master key again.
	// lock is ignored since the key doesn't exist.
	if reliablyMissing {
		recreated, err = k.backend.CreateOnly(ctx, valueKey, []byte(id.String()), true)
	} else {
		recreated, err = k.backend.UpdateIfDifferentIfLocked(ctx, valueKey, []byte(id.String()), true, lock)
	}
	switch {
	case err != nil:
		return fmt.Errorf("Unable to re-create missing slave key \"%s\" -> \"%s\": %w", logfields.Key, valueKey, err)
	case recreated:
		k.logger.Warn(
			"Re-created missing slave key",
			logfields.Key, valueKey,
		)
	}

	return nil
}

// Release releases the use of an ID associated with the provided key.  It does
// not guard against concurrent releases. This is currently guarded by
// Allocator.slaveKeysMutex when called from pkg/allocator.Allocator.Release.
func (k *kvstoreBackend) Release(ctx context.Context, _ idpool.ID, key allocator.AllocatorKey) (err error) {
	valueKey := path.Join(k.valuePrefix, key.GetKey(), k.suffix)
	k.logger.Info(
		"Released last local use of key, invoking global release",
		logfields.Key, key,
	)

	// does not need to be deleted with a lock as its protected by the
	// Allocator.slaveKeysMutex
	if err := k.backend.Delete(ctx, valueKey); err != nil {
		k.logger.Warn(
			"Ignoring node specific ID",
			logfields.Error, err,
			logfields.Key, key,
		)
		return err
	}

	// if k.lockless {
	// FIXME: etcd 3.3 will make it possible to do a lockless
	// cleanup of the ID and release it right away. For now we rely
	// on the GC to kick in a release unused IDs.
	// }

	return nil
}

// RunLocksGC scans the kvstore for unused locks and removes them. Returns
// a map of locks that are currently being held, including the ones that have
// failed to be GCed.
func (k *kvstoreBackend) RunLocksGC(ctx context.Context, staleKeysPrevRound map[string]kvstore.Value) (map[string]kvstore.Value, error) {
	// fetch list of all /../locks keys
	allocated, err := k.backend.ListPrefix(ctx, k.lockPrefix)
	if err != nil {
		return nil, fmt.Errorf("list failed: %w", err)
	}

	staleKeys := map[string]kvstore.Value{}

	// iterate over /../locks
	for key, v := range allocated {
		// Only delete if this key was previously marked as to be deleted
		if modRev, ok := staleKeysPrevRound[key]; ok &&
			// comparing ModRevision ensures the same client is still holding
			// this lock since the last GC was called.
			modRev.ModRevision == v.ModRevision &&
			modRev.LeaseID == v.LeaseID {
			if err := k.backend.Delete(ctx, key); err == nil {
				k.logger.Warn("Forcefully removed distributed lock due to client staleness."+
					" Please check the connectivity between the KVStore and the client with that lease ID.",
					logfields.Key, key,
					logfields.LeaseID, strconv.FormatUint(uint64(v.LeaseID), 16),
				)
				continue
			}
			k.logger.Warn(
				"Unable to remove distributed lock due to client staleness."+
					" Please check the connectivity between the KVStore and the client with that lease ID.",
				logfields.Error, err,
				logfields.Key, key,
				logfields.LeaseID, strconv.FormatUint(uint64(v.LeaseID), 16),
			)
		}
		// If the key was not found mark it to be delete in the next RunGC
		staleKeys[key] = kvstore.Value{
			ModRevision: v.ModRevision,
			LeaseID:     v.LeaseID,
		}
	}

	return staleKeys, nil
}

// RunGC scans the kvstore for unused master keys and removes them
func (k *kvstoreBackend) RunGC(
	ctx context.Context,
	rateLimit *rate.Limiter,
	staleKeysPrevRound map[string]uint64,
	minID, maxID idpool.ID,
) (map[string]uint64, *allocator.GCStats, error) {

	// fetch list of all /id/ keys
	allocated, err := k.backend.ListPrefix(ctx, k.idPrefix)
	if err != nil {
		return nil, nil, fmt.Errorf("list failed: %w", err)
	}

	totalEntries := len(allocated)
	deletedEntries := 0

	staleKeys := map[string]uint64{}

	min := uint64(minID)
	max := uint64(maxID)
	reasonOutOfRange := "out of local cluster identity range [" + strconv.FormatUint(min, 10) + "," + strconv.FormatUint(max, 10) + "]"

	// iterate over /id/
	for key, v := range allocated {
		// if k.lockless {
		// FIXME: Add DeleteOnZeroCount support
		// }

		// Parse identity ID
		items := strings.Split(key, "/")
		if len(items) == 0 {
			k.logger.Warn(
				"Unknown identity key found, skipping",
				logfields.Error, err,
				logfields.Key, key,
			)
			continue
		}

		if identityID, err := strconv.ParseUint(items[len(items)-1], 10, 64); err != nil {
			k.logger.Warn(
				"Parse identity failed, skipping",
				logfields.Error, err,
				logfields.Key, key,
			)
			continue
		} else {
			// We should not GC those identities that are out of our scope
			if identityID < min || identityID > max {
				k.logger.Debug(
					"Skipping this key",
					logfields.Key, key,
					logfields.Reason, reasonOutOfRange,
				)
				continue
			}
		}

		lock, err := k.lockPath(ctx, key)
		if err != nil {
			k.logger.Warn(
				"allocator garbage collector was unable to lock key",
				logfields.Error, err,
				logfields.Key, key,
			)
			continue
		}

		// fetch list of all /value/<key> keys
		valueKeyPrefix := path.Join(k.valuePrefix, string(v.Data))
		pairs, err := k.backend.ListPrefixIfLocked(ctx, valueKeyPrefix, lock)
		if err != nil {
			k.logger.Warn(
				"allocator garbage collector was unable to list keys",
				logfields.Error, err,
				logfields.Prefix, valueKeyPrefix,
			)
			lock.Unlock(context.Background())
			continue
		}

		hasUsers := false
		for prefix := range pairs {
			if prefixMatchesKey(valueKeyPrefix, prefix) {
				hasUsers = true
				break
			}
		}

		var deleted bool
		// if ID has no user, delete it
		if !hasUsers {
			// Only delete if this key was previously marked as to be deleted
			if modRev, ok := staleKeysPrevRound[key]; ok {
				// if the v.ModRevision is different than the modRev (which is
				// the last seen v.ModRevision) then this key was re-used in
				// between GC calls. We should not mark it as stale keys yet,
				// but the next GC call will do it.
				if modRev == v.ModRevision {
					if err := k.backend.DeleteIfLocked(ctx, key, lock); err != nil {
						k.logger.Warn(
							"Unable to delete unused allocator master key",
							logfields.Error, err,
							logfields.Key, key,
							logfields.Identity, path.Base(key),
						)
					} else {
						deletedEntries++
						k.logger.Info(
							"Deleted unused allocator master key in KVStore",
							logfields.Key, key,
							logfields.Identity, path.Base(key),
						)
					}
					// consider the key regardless if there was an error from
					// the kvstore. We want to rate limit the number of requests
					// done to the KVStore.
					deleted = true
				}
			} else {
				// If the key was not found mark it to be delete in the next RunGC
				staleKeys[key] = v.ModRevision
			}
		}

		lock.Unlock(context.Background())
		if deleted {
			// Wait after deleted the key. This is not ideal because we have
			// done the operation that should be rate limited before checking the
			// rate limit. We have to do this here to avoid holding the global lock
			// for a long period of time.
			err = rateLimit.Wait(ctx)
			if err != nil {
				return nil, nil, err
			}
		}
	}

	gcStats := &allocator.GCStats{
		Alive:   totalEntries - deletedEntries,
		Deleted: deletedEntries,
	}
	return staleKeys, gcStats, nil
}

func (k *kvstoreBackend) keyToID(key string) (id idpool.ID, err error) {
	if !strings.HasPrefix(key, k.idPrefix) {
		return idpool.NoID, fmt.Errorf("Found invalid key \"%s\" outside of prefix \"%s\"", key, k.idPrefix)
	}

	suffix := strings.TrimPrefix(key, k.idPrefix)
	if suffix[0] == '/' {
		suffix = suffix[1:]
	}

	idParsed, err := strconv.ParseUint(suffix, 10, 64)
	if err != nil {
		return idpool.NoID, fmt.Errorf("Cannot parse key suffix \"%s\"", suffix)
	}

	return idpool.ID(idParsed), nil
}

func (k *kvstoreBackend) ListIDs(ctx context.Context) (identityIDs []idpool.ID, err error) {
	identities, err := k.backend.ListPrefix(ctx, k.idPrefix)
	if err != nil {
		return nil, err
	}

	for key := range identities {
		id, err := k.keyToID(key)
		if err != nil {
			k.logger.Warn(
				"Cannot parse identity ID",
				logfields.Identity, key,
			)
			continue
		}
		identityIDs = append(identityIDs, id)
	}

	return identityIDs, nil
}

func (k *kvstoreBackend) ListAndWatch(ctx context.Context, handler allocator.CacheMutations) {
	events := k.backend.ListAndWatch(ctx, k.idPrefix)
	for event := range events {
		if event.Typ == kvstore.EventTypeListDone {
			handler.OnListDone()
			continue
		}

		id, err := k.keyToID(event.Key)
		switch {
		case err != nil:
			k.logger.Warn(
				"Invalid key",
				logfields.Error, err,
				logfields.Identity, event.Key,
			)

		case id != idpool.NoID:
			var key allocator.AllocatorKey

			if len(event.Value) > 0 {
				key = k.keyType.PutKey(string(event.Value))
			} else {
				if event.Typ != kvstore.EventTypeDelete {
					k.logger.Error(
						"Received a key with an empty value",
						logfields.Key, event.Key,
						logfields.EventType, event.Typ,
					)
					continue
				}
			}

			switch event.Typ {
			case kvstore.EventTypeCreate, kvstore.EventTypeModify:
				handler.OnUpsert(id, key)

			case kvstore.EventTypeDelete:
				handler.OnDelete(id, key)
			}
		}
	}
}
