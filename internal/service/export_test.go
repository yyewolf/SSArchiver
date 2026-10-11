package service

// PruneEventsWith exposes pruneEvents with custom limits to external tests.
var PruneEventsWith = (*Service).pruneEvents

// MarkReplayImported exposes markReplayImported to external tests.
var MarkReplayImported = (*Service).markReplayImported
