// Package room owns the Room protocol of spec/room/v1: events, claims on work,
// and the messages that carry them. It is pure: no I/O, no clock, no goroutine.
// No value is safe for concurrent use. A Receiver or a Subscription belongs to
// one goroutine.
package room
