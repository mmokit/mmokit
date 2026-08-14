// Package logger provides thread-safe, category-based debug logging with
// hierarchical groups, regular-expression filters, and synchronous hooks.
//
// Categories conventionally use "group:subcategory", such as "mesh:transfer".
// Registering a category also registers the text before the colon as a group,
// so enabling or disabling a group applies to every category under it.
//
// Log calls on a disabled category return before formatting, which makes them
// cheap — and means a message logged before its category is enabled is dropped
// silently. Emit startup diagnostics after category configuration, not before.
//
// Hooks fire synchronously on the calling goroutine. Anything doing real work
// in a hook should hand off to a bounded channel.
package logger
