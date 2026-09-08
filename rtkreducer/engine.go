package rtkreducer

// This file intentionally keeps the process coordinator API private. The
// transform lane owns admission and calls engineLease.reduce for each selected
// field, while tests and package code can use coordinator.reduce for one-off
// process checks.
