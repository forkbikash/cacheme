package encache

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"reflect"
	"time"

	"github.com/redis/go-redis/v9"
)

type MapCacheImpl struct {
	cache map[string]cacheEntry
}

type cacheEntry struct {
	value      []reflect.Value
	expiryTime time.Time
}

// for slice
// size[0] is length and size[1] is capacity
// if size[1] not passed length and capacity are both equal to size[0]

// for map
// size[0] is the size
func NewMapCacheImpl(size ...int) *MapCacheImpl {
	if len(size) > 1 {
		panic("too many arguments")
	}
	var cache map[string]cacheEntry
	if len(size) > 0 {
		cache = make(map[string]cacheEntry, size[0])
	} else {
		cache = make(map[string]cacheEntry)
	}

	return &MapCacheImpl{
		cache: cache,
	}
}

func (cacheImpl *MapCacheImpl) Get(key string, _ reflect.Type) ([]reflect.Value, bool, error) {
	if res, ok := cacheImpl.cache[key]; ok && res.expiryTime.After(time.Now()) {
		return res.value, true, nil
	}
	return nil, false, nil
}

func (cacheImpl *MapCacheImpl) Set(key string, value []reflect.Value, expiry time.Duration) error {
	cacheImpl.cache[key] = cacheEntry{
		value:      value,
		expiryTime: time.Now().Add(expiry),
	}
	return nil
}

// just to satisfy the interface
func (cacheImpl *MapCacheImpl) Serialize(res []reflect.Value) (string, error) {
	return "", nil
}

// just to satisfy the interface
func (cacheImpl *MapCacheImpl) Deserialize(serializedResult string, fType reflect.Type) ([]reflect.Value, error) {
	return nil, nil
}

// expire after a certain duration
func (cacheImpl *MapCacheImpl) Expire(key string, expiry time.Duration) error {
	muLockImpl := NewMuLockImpl()
	lockerr := muLockImpl.lock()
	if lockerr != nil {
		// unreachable
		log.Println("error in lock: ", lockerr)
		panic("error in lock: " + lockerr.Error())
	}
	defer func() {
		unlockerr := muLockImpl.unlock()
		if unlockerr != nil {
			// unreachable
			log.Println("error in unlock: ", unlockerr)
			panic("error in unlock: " + unlockerr.Error())
		}
	}()

	cacheEntry, ok := cacheImpl.cache[key]
	if ok {
		if expiry <= 0 {
			delete(cacheImpl.cache, key)
			return nil
		} else {
			seterr := cacheImpl.Set(key, cacheEntry.value, expiry)
			return seterr
		}
	}

	return nil
}

// start a goroutine to periodically check and remove expired cache entries
func (cacheImpl *MapCacheImpl) PeriodicExpire(runOnDuration time.Duration) {
	go func() {
		for {
			time.Sleep(runOnDuration)
			for key, entry := range cacheImpl.cache {
				if entry.expiryTime.Before(time.Now()) {
					err := cacheImpl.Expire(key, 0)
					if err != nil {
						log.Println("error in periodic expire: ", err)
					}
				}
			}
		}
	}()
}

type RedisCacheImpl struct {
	client redis.UniversalClient
}

func NewRedisCacheImpl(client redis.UniversalClient) *RedisCacheImpl {
	return &RedisCacheImpl{
		client: client,
	}
}

func (cacheImpl *RedisCacheImpl) Serialize(res []reflect.Value) (string, error) {
	serializedRes, err := json.Marshal(res)
	if err != nil {
		return "", err
	}
	return string(serializedRes), nil
}

func (cacheImpl *RedisCacheImpl) Deserialize(serializedResult string, fType reflect.Type) ([]reflect.Value, error) {
	var results []interface{}
	err := json.Unmarshal([]byte(serializedResult), &results)
	if err != nil {
		return nil, err
	}

	res := make([]reflect.Value, len(results))
	for i := range results {
		res[i] = reflect.New(fType.Out(i)).Elem()
		res[i].Set(reflect.ValueOf(results[i]))
	}
	return res, nil
}

func (cacheImpl *RedisCacheImpl) Get(key string, fType reflect.Type) ([]reflect.Value, bool, error) {
	ctx := context.Background()

	cachedResult, err := cacheImpl.client.Get(ctx, key).Result()
	if err != nil && err != redis.Nil {
		return nil, false, err
	}
	if err == redis.Nil {
		return nil, false, nil
	}

	returnValue, err := cacheImpl.Deserialize(cachedResult, fType)
	if err != nil {
		return nil, false, err
	}
	return returnValue, true, nil
}

func (cacheImpl *RedisCacheImpl) Set(key string, value []reflect.Value, expiry time.Duration) error {
	ctx := context.Background()

	serializedResult, err := cacheImpl.Serialize(value)
	if err != nil {
		return err
	}

	err = cacheImpl.client.Set(ctx, key, serializedResult, expiry).Err()
	if err != nil {
		return err
	}

	return nil
}

// just to satisfy the interface as expirations happens automatically
func (cacheImpl *RedisCacheImpl) PeriodicExpire(_ time.Duration) {}

func (cacheImpl *RedisCacheImpl) Expire(key string, expiry time.Duration) error {
	ctx := context.Background()
	err := cacheImpl.client.Expire(ctx, key, expiry).Err()

	return err
}

type CacheKeyImpl struct{}

func NewDefaultCacheKeyImpl() *CacheKeyImpl {
	return &CacheKeyImpl{}
}

func (cacheImpl *CacheKeyImpl) Key(fName string, args []reflect.Value) string {
	key := fName
	for _, arg := range args {
		key += fmt.Sprintf("%v", arg.Interface())
	}
	return key
}
