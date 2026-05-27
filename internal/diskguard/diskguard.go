// Package diskguard provides a minimal "do we have enough free space"
// helper. The bot calls FreeBytes(dir) before inserting a new note so
// it can refuse the write before SQLite hits a no-space-left error
// halfway through.
//
// Platform support: the real check needs Unix syscalls (Statfs). On
// Windows / unsupported platforms we return a sentinel large value so
// development builds don't refuse perfectly fine writes — the bot
// container runs Linux in production.
package diskguard
