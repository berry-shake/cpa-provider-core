package core

import (
	"time"

	"github.com/berry-shake/cpa-provider-core/provider"
)

// Test hooks: with Options.ManualRefresh they let the snapshot runner
// (package coretest) run the background work of a call synchronously.
// *Service satisfies coretest.Plugin.

// WaitIdle blocks until every background stream pump has closed its host
// stream and written its request log entry.
func (s *Service) WaitIdle() {
	s.streams.Wait()
}

// WaitIdleFor is WaitIdle with a limit; it reports whether the pumps
// finished in time.
func (s *Service) WaitIdleFor(timeout time.Duration) bool {
	return s.streams.waitTimeout(timeout)
}

// RunQueuedRefresh runs, synchronously, the catalog refresh the background
// loop would run for a queued kick, and reports whether one was queued.
func (s *Service) RunQueuedRefresh() bool {
	select {
	case <-s.kick:
		_, _ = s.refreshCatalog(false)
		return true
	default:
		return false
	}
}

// DropQueuedRefresh discards a queued catalog refresh without running it.
func (s *Service) DropQueuedRefresh() {
	select {
	case <-s.kick:
	default:
	}
}

// RefreshTick runs, synchronously, what one iteration of the background
// catalog loop runs, whether or not a refresh is queued: a catalog refresh
// that keeps what the catalog caches (no force), then the re-registration
// of auth files built from an older catalog. It returns the catalog and the
// refresh error. Snapshot bindings serve it as a pseudo-method (Wokey's
// seam.refresh_tick).
func (s *Service) RefreshTick() (provider.Catalog, error) {
	return s.refreshCatalog(false)
}
