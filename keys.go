package harvester

import (
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/Machiel/slugify"
	"github.com/julianshen/og"
)

// HarvestedResourceKeys tracks the keys for a single URL that was discovered in content.
// Keys allow the URL to be identified in a database, key value store, etc.
type HarvestedResourceKeys struct {
	hr       *HarvestedResource
	uniqueID uint32
	pageInfo *og.PageInfo
	piError  error
}

// HarvestedResource returns the underlying resource the keys were generated for
func (keys *HarvestedResourceKeys) HarvestedResource() *HarvestedResource {
	return keys.hr
}

// UniqueID returns the unique identifier based on key searching algorithm
func (keys *HarvestedResourceKeys) UniqueID() uint32 {
	return keys.uniqueID
}

// UniqueIDText returns a unique identity key formatted as requested
func (keys *HarvestedResourceKeys) UniqueIDText(format string) string {
	return fmt.Sprintf(format, keys.uniqueID)
}

// IsValid returns true if there are no errors
func (keys *HarvestedResourceKeys) IsValid() bool {
	return keys.piError == nil
}

// Slug returns the title of the content
func (keys *HarvestedResourceKeys) Slug() string {
	if keys.piError == nil {
		return slugify.Slugify(keys.pageInfo.Title)
	}
	return "Error getting PageInfo"
}

// KeyExists is a function passed in that checks whether a key already exists
type KeyExists func(random uint32, try int) bool

// GenerateUniqueID generates a unique identifier for this resource
func generateUniqueID(existsFn KeyExists) uint32 {
	nconflict := 0
	for i := 0; i < 10000; i++ {
		nextInt := nextRandomNumber()
		if !existsFn(nextInt, i) {
			return nextInt
		}

		if nconflict++; nconflict > 10 {
			randmu.Lock()
			rand = reseed()
			randmu.Unlock()
		}
	}

	// give up after max tries, not much we can do
	return nextRandomNumber()
}

// CreateHarvestedResourceKeys returns a new resource keys object
func CreateHarvestedResourceKeys(hr *HarvestedResource, existsFn KeyExists) *HarvestedResourceKeys {
	result := new(HarvestedResourceKeys)
	result.hr = hr
	result.uniqueID = generateUniqueID(existsFn)
	// TODO this does an extra HTTP get, instead we should re-use a downloaded HTML
	if hr.finalURL != nil {
		result.pageInfo, result.piError = og.GetPageInfoFromUrl(hr.finalURL.String())
	} else {
		result.pageInfo = nil
		result.piError = fmt.Errorf("HR %s finalURL is null", hr.OriginalURLText())
	}

	return result
}

// Random number state, approach copied from tempfile.go standard library
var rand uint32
var randmu sync.Mutex

func reseed() uint32 {
	return uint32(time.Now().UnixNano() + int64(os.Getpid()))
}

func nextRandomNumber() uint32 {
	randmu.Lock()
	r := rand
	if r == 0 {
		r = reseed()
	}
	r = r*1664525 + 1013904223 // constants from Numerical Recipes
	rand = r
	randmu.Unlock()
	return 1e9 + r%1e9
}
