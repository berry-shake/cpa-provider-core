package core

// Test hooks: with Options.ManualRefresh they let the snapshot runner
// (package coretest) run the background work of a call synchronously.
// *Service satisfies coretest.Plugin.

// WaitIdle blocks until every background stream pump has closed its host
// stream and written its request log entry.
func (s *Service) WaitIdle() {
	s.streams.Wait()
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
