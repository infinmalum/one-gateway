package model

import (
	"context"
	"strconv"
	"sync"
	"testing"

	"github.com/redis/go-redis/v9"
	"github.com/infinmalum/one-gateway/common"
	"github.com/infinmalum/one-gateway/common/config"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

type channelVersionRedis struct {
	redis.Cmdable
	mu     sync.Mutex
	values map[string]string
}

func (f *channelVersionRedis) Get(_ context.Context, key string) *redis.StringCmd {
	f.mu.Lock()
	defer f.mu.Unlock()
	value, ok := f.values[key]
	if !ok {
		return redis.NewStringResult("", redis.Nil)
	}
	return redis.NewStringResult(value, nil)
}

func (f *channelVersionRedis) Del(_ context.Context, keys ...string) *redis.IntCmd {
	f.mu.Lock()
	defer f.mu.Unlock()
	var removed int64
	for _, key := range keys {
		if _, ok := f.values[key]; ok {
			delete(f.values, key)
			removed++
		}
	}
	return redis.NewIntResult(removed, nil)
}

func (f *channelVersionRedis) Incr(_ context.Context, key string) *redis.IntCmd {
	f.mu.Lock()
	defer f.mu.Unlock()
	value, _ := strconv.ParseInt(f.values[key], 10, 64)
	value++
	f.values[key] = strconv.FormatInt(value, 10)
	return redis.NewIntResult(value, nil)
}

func TestChannelChangesReflectImmediatelyWithoutRedis(t *testing.T) {
	previousDB, previousCache, previousRedis, previousSQLite := DB, config.MemoryCacheEnabled, common.RedisEnabled, common.UsingSQLite
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&Channel{}, &Ability{}))
	DB, config.MemoryCacheEnabled, common.RedisEnabled, common.UsingSQLite = db, true, false, true
	t.Cleanup(func() {
		DB, config.MemoryCacheEnabled, common.RedisEnabled, common.UsingSQLite = previousDB, previousCache, previousRedis, previousSQLite
	})
	InitChannelCache()

	channel := Channel{Name: "test", Status: ChannelStatusEnabled, Group: "default", Models: "old-model"}
	require.NoError(t, channel.Insert())
	selected, err := CacheGetRandomSatisfiedChannel("default", "old-model", false)
	require.NoError(t, err)
	require.Equal(t, channel.Id, selected.Id)

	channel.Models = "new-model"
	require.NoError(t, channel.Update())
	_, err = CacheGetRandomSatisfiedChannel("default", "old-model", false)
	require.Error(t, err)
	_, err = CacheGetRandomSatisfiedChannel("default", "new-model", false)
	require.NoError(t, err)

	UpdateChannelStatusById(channel.Id, ChannelStatusManuallyDisabled)
	_, err = CacheGetRandomSatisfiedChannel("default", "new-model", false)
	require.Error(t, err)
}

func TestChannelCacheReloadsAfterAnotherInstanceChangesChannels(t *testing.T) {
	previousDB, previousCache, previousRedis, previousRDB := DB, config.MemoryCacheEnabled, common.RedisEnabled, common.RDB
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&Channel{}, &Ability{}))
	DB, config.MemoryCacheEnabled, common.RedisEnabled = db, true, true
	common.RDB = &channelVersionRedis{values: make(map[string]string)}
	t.Cleanup(func() {
		DB, config.MemoryCacheEnabled, common.RedisEnabled, common.RDB = previousDB, previousCache, previousRedis, previousRDB
	})
	InitChannelCache()

	channel := Channel{Name: "test", Status: ChannelStatusEnabled, Group: "default", Models: "first"}
	require.NoError(t, channel.Insert())
	channelSyncLock.RLock()
	staleChannels, staleVersion := group2model2channels, channelCacheVersion
	channelSyncLock.RUnlock()
	channel.Models = "second"
	require.NoError(t, channel.Update())
	channelSyncLock.Lock()
	group2model2channels, channelCacheVersion = staleChannels, staleVersion
	channelSyncLock.Unlock()

	selected, err := CacheGetRandomSatisfiedChannel("default", "second", false)
	require.NoError(t, err)
	require.Equal(t, channel.Id, selected.Id)
	_, err = CacheGetRandomSatisfiedChannel("default", "first", false)
	require.Error(t, err)
}
