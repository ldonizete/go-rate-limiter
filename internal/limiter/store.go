package limiter

import (
	"context"
	"time"
)

// Store is the persistence Strategy used by the Limiter. Implementations only
// know how to count hits per key and block keys; which key and which limit to
// use is decided by the Limiter, so swapping Redis for another backend means
// writing a single new Store.
type Store interface {
	// Hit registers one request for key and reports whether it is allowed.
	//
	// Implementations must behave atomically:
	//   - while key is blocked, Hit returns false without counting;
	//   - otherwise the hit is counted in a fixed window of the given size;
	//   - when the count in the window exceeds limit, key is blocked for
	//     blockFor and Hit returns false.
	Hit(ctx context.Context, key string, limit int, window, blockFor time.Duration) (allowed bool, err error)
}
