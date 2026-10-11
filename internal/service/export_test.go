package service

// PruneEventsWith exposes pruneEvents with custom limits to external tests.
var PruneEventsWith = (*Service).pruneEvents

// MarkReplayImported exposes markReplayImported to external tests.
var MarkReplayImported = (*Service).markReplayImported

// RemoveUnreferencedImport exposes removeUnreferencedImport to external tests.
var RemoveUnreferencedImport = (*Service).removeUnreferencedImport

// FatalError exposes fatal to external tests.
var FatalError = fatal
