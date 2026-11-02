package q

// NuGetSearchScanSQL is the statement of NuGetSearchScan for a caller that reads its rows as they
// arrive, in one transaction, instead of all of them at once (internal/formatdata/nuget
// SearchScan, Q-NUGET-74). Its parameters are the fields of NuGetSearchScanParams in their order.
// This file is written by hand: sqlc generates the statement and the method, not this name.
const NuGetSearchScanSQL = nuGetSearchScan
