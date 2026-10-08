package service

// PruneEventsWith exposes pruneEvents with custom limits to external tests.
var PruneEventsWith = (*Service).pruneEvents
